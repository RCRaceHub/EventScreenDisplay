package stage

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
)

var headers = map[string]map[string]string{
	"/plain":      {},
	"/deny":       {"X-Frame-Options": "DENY"},
	"/sameorigin": {"X-Frame-Options": "SAMEORIGIN"},
	"/csp-self":   {"Content-Security-Policy": "default-src 'self'; frame-ancestors 'self'"},
	"/csp-any":    {"Content-Security-Policy": "frame-ancestors *"},
	"/missing":    {},
}

func TestFramableReadsTheHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers[r.URL.Path] {
			w.Header().Set(k, v)
		}
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	expect := map[string]bool{"/plain": true, "/deny": false, "/sameorigin": false, "/csp-self": false,
		"/csp-any": true, "/missing": true}
	for path, want := range expect {
		got, ok := Framable(srv.URL+path, 4*time.Second)
		if !ok || got != want {
			t.Errorf("%s: got %v (checked %v), want %v", path, got, ok, want)
		}
	}
}

func TestFramableUnreachable(t *testing.T) {
	if _, ok := Framable("http://127.0.0.1:1/", time.Second); ok {
		t.Error("should not be checkable")
	}
}

// recorder is a kiosk browser that remembers what it was told.
type recorder struct {
	mu      sync.Mutex
	url     string
	opened  []string
	reloads int
}

func (r *recorder) Open(u string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, u)
	r.url = u
	return nil
}
func (r *recorder) Reload() error          { r.mu.Lock(); r.reloads++; r.mu.Unlock(); return nil }
func (r *recorder) EnsureRunning() error   { return nil }
func (r *recorder) Alive() bool            { return true }
func (r *recorder) Stop()                  {}
func (r *recorder) SetLastError(string)    {}
func (r *recorder) Status() browser.Status { return browser.Status{Driver: "recorder", Running: true} }
func (r *recorder) CurrentURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.url
}

const stageURL = "http://127.0.0.1:8080/stage"

type fixture struct {
	inner  *recorder
	stage  *Stage
	b      *Browser
	checks []string
}

func newFixture() *fixture {
	f := &fixture{inner: &recorder{}, stage: New(nil)}
	f.b = NewBrowser(f.inner, f.stage, stageURL, func(u string) (bool, bool) {
		f.checks = append(f.checks, u)
		return u != "http://blocked.example/", true
	})
	return f
}

func TestFramablePagesGoOnStage(t *testing.T) {
	f := newFixture()
	_ = f.b.Open("http://a.example/")
	_ = f.b.Open("http://b.example/")
	if !reflect.DeepEqual(f.inner.opened, []string{stageURL}) { // browser navigated once
		t.Errorf("opened %q", f.inner.opened)
	}
	if f.stage.Active() != "http://b.example/" || f.b.CurrentURL() != "http://b.example/" {
		t.Errorf("active %s current %s", f.stage.Active(), f.b.CurrentURL())
	}
}

func TestBlockedPageIsShownDirectly(t *testing.T) {
	f := newFixture()
	_ = f.b.Open("http://a.example/")
	_ = f.b.Preload("http://blocked.example/")
	if f.stage.Next() != "" {
		t.Error("blocked page preloaded")
	}
	_ = f.b.Open("http://blocked.example/")
	if f.inner.CurrentURL() != "http://blocked.example/" {
		t.Errorf("inner on %s", f.inner.CurrentURL())
	}
	_ = f.b.Open("http://a.example/")
	if f.inner.CurrentURL() != stageURL {
		t.Errorf("inner on %s", f.inner.CurrentURL())
	}
}

func TestPreloadAndSwitch(t *testing.T) {
	f := newFixture()
	_ = f.b.Open("http://a.example/")
	_ = f.b.Preload("http://b.example/")
	if f.stage.Next() != "http://b.example/" {
		t.Errorf("next %s", f.stage.Next())
	}
	_ = f.b.Open("http://b.example/")
	if f.stage.Active() != "http://b.example/" || f.stage.Next() != "" {
		t.Errorf("active %s next %s", f.stage.Active(), f.stage.Next())
	}
}

func TestReloadGoesToTheRightPlace(t *testing.T) {
	f := newFixture()
	_ = f.b.Open("http://a.example/")
	_ = f.b.Reload()
	if f.stage.Reloads() != 1 || f.inner.reloads != 0 {
		t.Errorf("stage %d inner %d", f.stage.Reloads(), f.inner.reloads)
	}
	_ = f.b.Open("http://blocked.example/")
	_ = f.b.Reload()
	if f.inner.reloads != 1 {
		t.Errorf("inner %d", f.inner.reloads)
	}
}

func TestChecksAreCachedAndOwnPagesSkipped(t *testing.T) {
	f := newFixture()
	for range 3 {
		_ = f.b.Open("http://a.example/")
	}
	_ = f.b.Open("http://127.0.0.1:8080/idle")
	if !reflect.DeepEqual(f.checks, []string{"http://a.example/"}) {
		t.Errorf("checks %q", f.checks)
	}
}

func TestCheckIsRepeatedAfterTTL(t *testing.T) {
	f := newFixture()
	now := time.Unix(1000, 0)
	f.b.now = func() time.Time { return now }
	_ = f.b.Open("http://a.example/")
	now = now.Add(FramableTTL + time.Second)
	_ = f.b.Open("http://a.example/")
	if len(f.checks) != 2 {
		t.Errorf("checks %q", f.checks)
	}
}

func TestStatusShowsThePageNotTheStage(t *testing.T) {
	f := newFixture()
	_ = f.b.Open("http://a.example/")
	st := f.b.Status()
	if *st.URL != "http://a.example/" || st.Preload == nil || !*st.Preload {
		t.Errorf("%+v", st)
	}
}

func TestWaitReturnsWhenChanged(t *testing.T) {
	s := New(nil)
	time.AfterFunc(100*time.Millisecond, func() { s.Show("http://a.example/") })
	start := time.Now()
	st := s.Wait(0, 5*time.Second)
	if time.Since(start) > 2*time.Second || st.Active == nil || *st.Active != "http://a.example/" {
		t.Errorf("%+v after %v", st, time.Since(start))
	}
}

func TestWaitTimesOut(t *testing.T) {
	if st := New(nil).Wait(0, 200*time.Millisecond); st.Seq != 0 {
		t.Errorf("%+v", st)
	}
}

func TestBannerChangeWakesTheStage(t *testing.T) {
	msgs := messages.New("", nil)
	s := New(msgs)
	body, _ := jsonx.Decode([]byte(`{"text":"Car 12 to the pits"}`))
	time.AfterFunc(50*time.Millisecond, func() { _, _ = msgs.SetBanner(body) })
	st := s.Wait(0, 5*time.Second)
	if st.Banner == nil || st.Banner.Text != "Car 12 to the pits" {
		t.Errorf("%+v", st)
	}
}
