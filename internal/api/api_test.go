package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
	"github.com/RCRaceHub/EventScreenDisplay/internal/player"
	"github.com/RCRaceHub/EventScreenDisplay/internal/playlist"
	"github.com/RCRaceHub/EventScreenDisplay/internal/stage"
)

// countingDriver is the dummy browser, counting reloads.
type countingDriver struct {
	browser.Dummy
	reloads atomic.Int32
}

func (d *countingDriver) Reload(string) error { d.reloads.Add(1); return nil }

type env struct {
	t        *testing.T
	cfg      config.Config
	proc     *browser.Process
	driver   *countingDriver
	player   *player.Player
	msgs     *messages.Messages
	stage    *stage.Stage
	base     string
	token    string
	serverUp atomic.Bool
}

type options struct {
	token string
	stage bool
	wifi  WifiCaller
}

func newEnv(t *testing.T, o options) *env {
	t.Helper()
	e := &env{t: t, token: o.token}
	e.serverUp.Store(true)
	e.cfg = config.Default()
	e.cfg.ScreenID, e.cfg.StateDir, e.cfg.Browser = "screen-test", t.TempDir(), "dummy"
	e.cfg.MinDuration, e.cfg.DefaultDuration, e.cfg.APIToken = 1, 1, o.token
	e.driver = &countingDriver{}
	e.proc = browser.New(e.driver)
	var b browser.Browser = e.proc
	e.msgs = messages.New(e.cfg.MessagesPath(), nil)
	if o.stage {
		e.stage = stage.New(e.msgs)
		b = stage.NewBrowser(e.proc, e.stage, "http://127.0.0.1:1/stage", func(string) (bool, bool) { return true, true })
	}
	e.player = player.New(b, playlist.Store{Path: e.cfg.PlaylistPath()}, player.Options{
		IdleURL: "http://127.0.0.1:1/idle", MessageURL: "http://127.0.0.1:1/message", Messages: e.msgs,
		Probe:          func(string) bool { return e.serverUp.Load() },
		HealthInterval: 50 * time.Millisecond, ProbeInterval: 50 * time.Millisecond,
	})
	wifi := o.wifi
	if wifi == nil {
		wifi = NewWifiClient(t.TempDir() + "/none.sock")
	}
	srv := httptest.NewServer(NewAgent(e.cfg, e.player, e.stage, wifi).Handler())
	e.base = srv.URL
	e.player.Start()
	t.Cleanup(func() {
		srv.Close()
		e.player.Stop()
	})
	return e
}

type reply struct {
	status int
	body   map[string]any
	raw    string
	header http.Header
}

func (e *env) call(method, path string, body any, headers ...string) reply {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	r := reply{status: resp.StatusCode, raw: string(data), header: resp.Header}
	_ = json.Unmarshal(data, &r.body)
	return r
}

func (e *env) status() map[string]any { return e.call("GET", "/api/v1/status", nil).body }

func TestMain(m *testing.M) {
	flag.Parse()
	quiet()
	os.Exit(m.Run())
}

func quiet() {
	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) waitURL(url string) {
	e.t.Helper()
	eventually(e.t, "browser on "+url, func() bool { return e.proc.CurrentURL() == url })
}

func TestInfo(t *testing.T) {
	e := newEnv(t, options{})
	r := e.call("GET", "/api/v1/info", nil)
	if r.status != 200 || r.body["id"] != "screen-test" || r.body["api_version"] != "1" || r.body["auth_required"] != false {
		t.Errorf("info %d %v", r.status, r.body)
	}
}

func TestIdleWithoutPlaylist(t *testing.T) {
	e := newEnv(t, options{})
	e.waitURL("http://127.0.0.1:1/idle")
	st := e.status()
	if st["state"] != "idle" || st["browser"].(map[string]any)["running"] != true {
		t.Errorf("status %v", st)
	}
}

func TestPlaylistCycles(t *testing.T) {
	e := newEnv(t, options{})
	r := e.call("PUT", "/api/v1/playlist", map[string]any{
		"items": []any{"http://a.example/", map[string]any{"url": "http://b.example/", "duration": 1, "label": "B"}},
	})
	if r.status != 200 || r.body["revision"] != 1.0 || r.header.Get("ETag") != `"1"` {
		t.Fatalf("put %d %v %v", r.status, r.body, r.header)
	}
	e.waitURL("http://a.example/")
	e.waitURL("http://b.example/")
	e.waitURL("http://a.example/")
}

func TestPlaylistPersists(t *testing.T) {
	e := newEnv(t, options{})
	e.call("PUT", "/api/v1/playlist", map[string]any{"items": []string{"http://a.example/"}, "source": "rctc"})
	saved := playlist.Store{Path: e.cfg.PlaylistPath()}.Load()
	if saved.Items[0].URL != "http://a.example/" || saved.Source != "rctc" {
		t.Errorf("saved %+v", saved)
	}
}

func TestPlaylistValidation(t *testing.T) {
	e := newEnv(t, options{})
	for _, items := range []any{[]string{"ftp://x"}, []any{map[string]any{"url": "http://a/", "duration": 0}}, "nope", []int{42}} {
		r := e.call("PUT", "/api/v1/playlist", map[string]any{"items": items})
		if r.status != 400 || r.body["error"] == nil {
			t.Errorf("%v: %d %v", items, r.status, r.body)
		}
	}
	for _, body := range []map[string]any{
		{"items": []string{}, "start_index": 1.5},
		{"items": []string{}, "start_index": true},
		{"items": []string{}, "default_duration": "x"},
	} {
		if r := e.call("PUT", "/api/v1/playlist", body); r.status != 400 {
			t.Errorf("%v: %d", body, r.status)
		}
	}
	req, _ := http.NewRequest("PUT", e.base+"/api/v1/playlist", strings.NewReader("[1]"))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 400 {
		t.Errorf("array body: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestIfMatchGuardsTheRevision(t *testing.T) {
	e := newEnv(t, options{})
	e.call("PUT", "/api/v1/playlist", map[string]any{"items": []string{"http://a.example/"}})
	if r := e.call("PUT", "/api/v1/playlist", map[string]any{"items": []string{"http://b.example/"}}, "If-Match", `"0"`); r.status != 412 {
		t.Errorf("stale If-Match: %d", r.status)
	}
	if r := e.call("PUT", "/api/v1/playlist", map[string]any{"items": []string{"http://b.example/"}}, "If-Match", `"1"`); r.status != 200 {
		t.Errorf("current If-Match: %d", r.status)
	}
}

func TestPauseNextGoto(t *testing.T) {
	e := newEnv(t, options{})
	e.call("PUT", "/api/v1/playlist", map[string]any{
		"items": []string{"http://a.example/", "http://b.example/", "http://c.example/"}, "default_duration": 60})
	e.waitURL("http://a.example/")
	if r := e.call("POST", "/api/v1/control/pause", nil); r.body["state"] != "paused" {
		t.Errorf("pause: %v", r.body)
	}
	e.call("POST", "/api/v1/control/next", nil)
	e.waitURL("http://b.example/")
	e.call("POST", "/api/v1/control/goto", map[string]any{"index": 2})
	e.waitURL("http://c.example/")
	st := e.status()
	if st["index"] != 2.0 || st["current"].(map[string]any)["ends_at"] != nil {
		t.Errorf("status %v", st)
	}
	if r := e.call("POST", "/api/v1/control/goto", map[string]any{"index": 9}); r.status != 400 {
		t.Errorf("goto 9: %d", r.status)
	}
	e.call("POST", "/api/v1/control/previous", nil)
	e.waitURL("http://b.example/")
	if r := e.call("POST", "/api/v1/control/resume", nil); r.body["state"] != "playing" {
		t.Errorf("resume: %v", r.body)
	}
}

func TestShowOverrideReturnsToPlaylist(t *testing.T) {
	e := newEnv(t, options{})
	e.call("PUT", "/api/v1/playlist", map[string]any{"items": []string{"http://a.example/"}})
	e.waitURL("http://a.example/")
	e.call("POST", "/api/v1/control/show", map[string]any{"url": "http://alert.example/", "duration": 1})
	e.waitURL("http://alert.example/")
	e.waitURL("http://a.example/")
	if r := e.call("POST", "/api/v1/control/show", map[string]any{"url": "http://x/", "above_messages": "yes"}); r.status != 400 {
		t.Errorf("above_messages string: %d", r.status)
	}
}

func TestBrowserRestartedAfterCrash(t *testing.T) {
	e := newEnv(t, options{})
	eventually(t, "browser up", e.proc.Alive)
	e.proc.Kill()
	eventually(t, "restart", func() bool { return e.proc.Restarts() == 1 && e.proc.Alive() })
}

func TestReloadsWhenServerComesBack(t *testing.T) {
	e := newEnv(t, options{})
	// Boot before Wi-Fi is up: the page's server can't be reached yet.
	e.serverUp.Store(false)
	e.call("PUT", "/api/v1/playlist", map[string]any{"items": []string{"http://a.example/"}})
	eventually(t, "unreachable", func() bool {
		return e.status()["network"].(map[string]any)["page_reachable"] == false
	})
	if n := e.driver.reloads.Load(); n != 0 {
		t.Fatalf("reloads %d", n)
	}
	e.serverUp.Store(true)
	eventually(t, "reload", func() bool { return e.driver.reloads.Load() == 1 })
	if e.status()["network"].(map[string]any)["page_reachable"] != true {
		t.Error("should be reachable")
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	e := newEnv(t, options{})
	if r := e.call("GET", "/api/v1/nope", nil); r.status != 404 {
		t.Errorf("unknown route: %d", r.status)
	}
	if r := e.call("POST", "/api/v1/status", nil); r.status != 405 {
		t.Errorf("wrong method: %d", r.status)
	}
	if r := e.call("OPTIONS", "/api/v1/playlist", nil); r.status != 204 || r.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight: %d %v", r.status, r.header)
	}
}

func TestIdlePageNamesTheScreen(t *testing.T) {
	e := newEnv(t, options{})
	r := e.call("GET", "/idle", nil)
	if r.status != 200 || !strings.Contains(r.raw, "screen-test") || !strings.Contains(r.raw, "/api/v1") {
		t.Errorf("idle %d %q", r.status, r.raw)
	}
}

func TestTooLargeBody(t *testing.T) {
	e := newEnv(t, options{})
	big := strings.Repeat("x", MaxBody+1)
	if r := e.call("PUT", "/api/v1/playlist", map[string]any{"source": big}); r.status != 413 {
		t.Errorf("big body: %d", r.status)
	}
}

func TestWritesNeedTheToken(t *testing.T) {
	e := newEnv(t, options{token: "s3cret"})
	body := map[string]any{"items": []string{}}
	if r := e.call("PUT", "/api/v1/playlist", body, "Authorization", ""); r.status != 401 {
		t.Errorf("no token: %d", r.status)
	}
	if r := e.call("PUT", "/api/v1/playlist", body, "Authorization", "Bearer wrong"); r.status != 401 {
		t.Errorf("wrong token: %d", r.status)
	}
	if r := e.call("PUT", "/api/v1/playlist", body); r.status != 200 {
		t.Errorf("right token: %d", r.status)
	}
	if r := e.call("PUT", "/api/v1/message", map[string]any{"title": "x"}, "Authorization", "Bearer wrong"); r.status != 401 {
		t.Errorf("message, wrong token: %d", r.status)
	}
}

func TestReadsAreOpenWithAToken(t *testing.T) {
	e := newEnv(t, options{token: "s3cret"})
	r := e.call("GET", "/api/v1/info", nil, "Authorization", "")
	if r.status != 200 || r.body["auth_required"] != true {
		t.Errorf("info %d %v", r.status, r.body)
	}
}

func TestMessageEndpoints(t *testing.T) {
	e := newEnv(t, options{token: "s3cret", stage: true})
	r := e.call("PUT", "/api/v1/message", map[string]any{"title": "Finals being calculated", "text": "Please wait"})
	if r.status != 200 || r.body["message"].(map[string]any)["style"] != "hold" {
		t.Fatalf("put %d %v", r.status, r.body)
	}
	if m := e.status()["message"].(map[string]any); m["title"] != "Finals being calculated" {
		t.Errorf("status message %v", m)
	}
	if r := e.call("DELETE", "/api/v1/message", nil); r.status != 200 {
		t.Errorf("delete %d", r.status)
	}
	if r := e.call("GET", "/api/v1/message", nil); r.body["message"] != nil {
		t.Errorf("after delete %v", r.body)
	}
	r = e.call("PUT", "/api/v1/message", map[string]any{"title": "x", "level": "red"})
	if r.status != 400 || !strings.Contains(r.raw, "level") {
		t.Errorf("bad level %d %q", r.status, r.raw)
	}
}

func TestBannerEndpointsAndStageState(t *testing.T) {
	e := newEnv(t, options{token: "s3cret", stage: true})
	if err := e.player.SetPlaylist(playlist.Replaced(playlist.Playlist{}, []playlist.Item{{URL: "http://rctc/board", Duration: 30}}, ""), 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "on stage", func() bool {
		return e.stage.Active() == "http://rctc/board" && e.proc.CurrentURL() == "http://127.0.0.1:1/stage"
	})
	r := e.call("POST", "/api/v1/banner", map[string]any{"text": "Car 12 to the pits", "level": "warning", "duration": 30})
	if r.status != 200 || r.body["banner"].(map[string]any)["visible"] != true {
		t.Fatalf("post %d %v", r.status, r.body)
	}
	if b := e.stage.State().Banner; b == nil || b.Text != "Car 12 to the pits" {
		t.Errorf("stage banner %+v", b)
	}
	e.call("DELETE", "/api/v1/banner", nil)
	if b := e.stage.State().Banner; b != nil {
		t.Errorf("banner still up %+v", b)
	}
}

func TestMessagePageAndFeatures(t *testing.T) {
	e := newEnv(t, options{stage: true})
	r := e.call("GET", "/message", nil)
	if r.status != 200 || !strings.Contains(r.raw, "/api/v1/message") {
		t.Errorf("message page %d", r.status)
	}
	if fs := e.call("GET", "/api/v1/info", nil).body["features"].([]any); !containsAny(fs, "banner") {
		t.Errorf("features %v", fs)
	}
}

func containsAny(list []any, v any) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestStageEndpoints(t *testing.T) {
	e := newEnv(t, options{stage: true})
	e.waitURL("http://127.0.0.1:1/stage")
	e.stage.Show("http://a.example/")
	if r := e.call("GET", "/stage", nil); r.status != 200 || !strings.Contains(r.raw, "iframe") {
		t.Errorf("stage page %d", r.status)
	}
	r := e.call("GET", "/stage/state?since=-1", nil)
	if r.body["active"] != "http://a.example/" {
		t.Errorf("state %v", r.body)
	}
}

func TestStageEndpointsNeedPreload(t *testing.T) {
	e := newEnv(t, options{})
	for _, p := range []string{"/stage", "/stage/state"} {
		if r := e.call("GET", p, nil); r.status != 404 {
			t.Errorf("%s: %d", p, r.status)
		}
	}
}

func TestStageStateLongPollWakesOnChange(t *testing.T) {
	e := newEnv(t, options{stage: true})
	seq := int(e.call("GET", "/stage/state?since=-1", nil).body["seq"].(float64))
	var wg sync.WaitGroup
	wg.Add(1)
	var got reply
	start := time.Now()
	go func() {
		defer wg.Done()
		got = e.call("GET", "/stage/state?since="+itoa(seq), nil)
	}()
	time.Sleep(100 * time.Millisecond)
	e.stage.Show("http://later.example/")
	wg.Wait()
	if time.Since(start) > 3*time.Second || got.body["active"] != "http://later.example/" {
		t.Errorf("long poll %v after %v", got.body, time.Since(start))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// fakeWifi lets the Wi-Fi API tests below run without the socket.
type fakeWifi struct{ calls []string }

func (f *fakeWifi) Call(method, path string, body map[string]any) (int, json.RawMessage, error) {
	f.calls = append(f.calls, method+" "+path)
	return 200, json.RawMessage(`{"ok":true}`), nil
}

func TestWifiDeleteKeepsTheSsidEncoded(t *testing.T) {
	w := &fakeWifi{}
	e := newEnv(t, options{token: "s3cret", wifi: w})
	if r := e.call("DELETE", "/api/v1/wifi/networks/Old%20Venue%2F2", nil); r.status != 200 {
		t.Fatalf("delete %d", r.status)
	}
	if len(w.calls) != 1 || w.calls[0] != "DELETE /wifi/networks/Old%20Venue%2F2" {
		t.Errorf("calls %q", w.calls)
	}
}
