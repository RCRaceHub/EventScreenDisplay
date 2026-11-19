package player

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
	"github.com/RCRaceHub/EventScreenDisplay/internal/playlist"
	"github.com/RCRaceHub/EventScreenDisplay/internal/stage"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}

// fake is a browser with no process.
type fake struct {
	mu     sync.Mutex
	url    string
	opened []string
}

func (f *fake) Open(u string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.url = u
	f.opened = append(f.opened, u)
	return nil
}
func (f *fake) Reload() error          { return nil }
func (f *fake) EnsureRunning() error   { return nil }
func (f *fake) Alive() bool            { return true }
func (f *fake) Stop()                  {}
func (f *fake) SetLastError(string)    {}
func (f *fake) Status() browser.Status { return browser.Status{Driver: "fake"} }
func (f *fake) CurrentURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.url
}
func (f *fake) openedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opened)
}

func start(t *testing.T, b browser.Browser, opt Options) *Player {
	t.Helper()
	opt.IdleURL = "http://127.0.0.1:1/idle"
	opt.Probe = func(string) bool { return true }
	opt.HealthInterval = 50 * time.Millisecond
	p := New(b, playlist.Store{Path: filepath.Join(t.TempDir(), "p.json")}, opt)
	p.Start()
	t.Cleanup(p.Stop)
	return p
}

func waitURL(t *testing.T, b browser.Browser, u string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.CurrentURL() == u {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("browser on %s, wanted %s", b.CurrentURL(), u)
}

func items(urls ...string) playlist.Playlist {
	var it []playlist.Item
	for _, u := range urls {
		it = append(it, playlist.Item{URL: u, Duration: 30})
	}
	return playlist.Replaced(playlist.Playlist{}, it, "")
}

func set(t *testing.T, p *Player, pl playlist.Playlist) {
	t.Helper()
	if err := p.SetPlaylist(pl, 0); err != nil {
		t.Fatal(err)
	}
}

func msg(t *testing.T, m *messages.Messages, s string) {
	t.Helper()
	body, err := jsonx.Decode([]byte(s))
	if err == nil {
		_, err = m.SetMessage(body)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestNextPageIsPreloadedBeforeTheSwitch(t *testing.T) {
	s := stage.New(nil)
	inner := &fake{}
	b := stage.NewBrowser(inner, s, "http://127.0.0.1:8080/stage", func(string) (bool, bool) { return true, true })
	p := start(t, b, Options{PreloadAhead: 0.6})
	set(t, p, playlist.Replaced(playlist.Playlist{}, []playlist.Item{
		{URL: "http://a.example/", Duration: 1}, {URL: "http://b.example/", Duration: 1}}, ""))
	type pair struct{ active, next string }
	var seen []pair
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		seen = append(seen, pair{s.Active(), s.Next()})
		time.Sleep(20 * time.Millisecond)
	}
	// b was preloaded while a was showing, then shown, then a preloaded.
	firstPre := slices.Index(seen, pair{"http://a.example/", "http://b.example/"})
	firstB := slices.IndexFunc(seen, func(x pair) bool { return x.active == "http://b.example/" })
	if firstPre < 0 || firstB < 0 || firstPre >= firstB || !slices.Contains(seen, pair{"http://b.example/", "http://a.example/"}) {
		t.Errorf("sequence %v", seen)
	}
	if got := inner.openedURLs(); !slices.Equal(got, []string{"http://127.0.0.1:8080/stage"}) {
		t.Errorf("inner opened %q", got)
	}
}

func TestFinalsFlow(t *testing.T) {
	m := messages.New(filepath.Join(t.TempDir(), "m.json"), nil)
	b := &fake{}
	p := start(t, b, Options{Messages: m, MessageURL: "http://127.0.0.1:1/message"})
	// Qualifying playlist running...
	set(t, p, items("http://rctc/quali"))
	waitURL(t, b, "http://rctc/quali")
	// ...hold while finals are calculated...
	msg(t, m, `{"title":"Finals being calculated"}`)
	waitURL(t, b, "http://127.0.0.1:1/message")
	if st := p.Status().State; st != "message" {
		t.Errorf("state %s", st)
	}
	// ...announce for a moment, with the finals playlist pushed underneath...
	msg(t, m, `{"title":"Finals lists are up","style":"announce","duration":1}`)
	set(t, p, playlist.Replaced(p.Playlist(), items("http://rctc/finals").Items, ""))
	time.Sleep(300 * time.Millisecond)
	if b.CurrentURL() != "http://127.0.0.1:1/message" {
		t.Errorf("message replaced early by %s", b.CurrentURL())
	}
	// ...then the finals boards, by themselves.
	waitURL(t, b, "http://rctc/finals")
	if st := p.Status().State; st != "playing" {
		t.Errorf("state %s", st)
	}
}

func TestMessageCoversShowAndWifiSetupCoversMessage(t *testing.T) {
	m := messages.New("", nil)
	b := &fake{}
	p := start(t, b, Options{Messages: m, MessageURL: "http://127.0.0.1:1/message"})
	p.Show(playlist.Item{URL: "http://podium"}, 0, false)
	waitURL(t, b, "http://podium")
	msg(t, m, `{"title":"Hold"}`)
	waitURL(t, b, "http://127.0.0.1:1/message")
	p.Show(playlist.Item{URL: "http://10.42.0.1/screen"}, 0, true)
	waitURL(t, b, "http://10.42.0.1/screen")
	p.ClearOverride()
	waitURL(t, b, "http://127.0.0.1:1/message")
	m.ClearMessage()
	waitURL(t, b, "http://127.0.0.1:1/idle")
}

func TestStartIndexAndWrapAround(t *testing.T) {
	b := &fake{}
	p := start(t, b, Options{})
	if err := p.SetPlaylist(items("http://a/", "http://b/", "http://c/"), 2); err != nil {
		t.Fatal(err)
	}
	waitURL(t, b, "http://c/")
	p.Next()
	waitURL(t, b, "http://a/")
	p.Previous()
	waitURL(t, b, "http://c/")
	if err := p.Goto(3); err != ErrIndex {
		t.Errorf("Goto(3) = %v", err)
	}
	if err := p.SetPlaylist(items("http://a/"), 7); err != nil {
		t.Fatal(err)
	}
	waitURL(t, b, "http://a/")
}

func TestSinglePageNeverMovesOn(t *testing.T) {
	b := &fake{}
	p := start(t, b, Options{})
	set(t, p, items("http://a/"))
	waitURL(t, b, "http://a/")
	if st := p.Status(); st.Current == nil || st.Current.EndsAt != nil || *st.Index != 0 {
		t.Errorf("%+v", st)
	}
}

func TestStatusWithoutPlaylist(t *testing.T) {
	b := &fake{}
	p := start(t, b, Options{})
	waitURL(t, b, "http://127.0.0.1:1/idle")
	st := p.Status()
	if st.State != "idle" || st.Index != nil || st.Override != nil || st.Network.PageReachable != nil {
		t.Errorf("%+v", st)
	}
}

func TestReachable(t *testing.T) {
	if Reachable("http://127.0.0.1:1/") {
		t.Error("port 1 reachable?")
	}
	if Reachable("not a url") {
		t.Error("bad url reachable")
	}
}
