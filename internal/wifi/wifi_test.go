package wifi

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
)

type fakeClock struct {
	mu sync.Mutex
	t  float64
}

func (c *fakeClock) now() float64  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d float64) { c.mu.Lock(); c.t += d; c.mu.Unlock() }

type fakeScreen struct {
	mu    sync.Mutex
	shown bool
}

func (s *fakeScreen) Show()  { s.mu.Lock(); s.shown = true; s.mu.Unlock() }
func (s *fakeScreen) Clear() { s.mu.Lock(); s.shown = false; s.mu.Unlock() }
func (s *fakeScreen) on() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shown
}

type savedCall struct {
	ssid, password string
	hidden         bool
}

// fakeNM stands in for NetworkManager in the Fallback state machine.
type fakeNM struct {
	mu            sync.Mutex
	online        bool
	hotspot       bool
	joinOK        bool
	saved         []savedCall
	hotspotStarts int
}

func newFakeNM() *fakeNM { return &fakeNM{online: true, joinOK: true} }

func (n *fakeNM) Online() (bool, error) { n.mu.Lock(); defer n.mu.Unlock(); return n.online, nil }
func (n *fakeNM) HotspotActive() (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hotspot, nil
}
func (n *fakeNM) Scan() []Network {
	return []Network{{"Venue", 80, true}, {"EventScreen-test", 99, true}}
}
func (n *fakeNM) StartHotspot(string, string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hotspot = true
	n.hotspotStarts++
	return nil
}
func (n *fakeNM) StopHotspot() { n.mu.Lock(); n.hotspot = false; n.mu.Unlock() }
func (n *fakeNM) SaveNetwork(ssid, password string, hidden bool, _ int) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.saved = append(n.saved, savedCall{ssid, password, hidden})
	return "wifi-" + ssid, nil
}
func (n *fakeNM) SavedNetworks() ([]Saved, error)   { return nil, nil }
func (n *fakeNM) CurrentNetwork() (*Current, error) { return nil, nil }
func (n *fakeNM) Forget(string) (int, error)        { return 0, nil }
func (n *fakeNM) Connect(string, int) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.online = n.joinOK
	return n.joinOK
}
func (n *fakeNM) savedCalls() []savedCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.saved)
}

func makeFallback(t *testing.T, edit func(*config.Config)) (*Fallback, *fakeNM, *fakeScreen, *fakeClock) {
	t.Helper()
	cfg := config.Default()
	if edit != nil {
		edit(&cfg)
	}
	nm, screen, clock := newFakeNM(), &fakeScreen{}, &fakeClock{t: 1000}
	return NewFallback(nm, cfg, screen, "EventScreen-test", "secret123", clock.now), nm, screen, clock
}

func tick(t *testing.T, fb *Fallback) {
	t.Helper()
	if err := fb.Tick(); err != nil {
		t.Fatal(err)
	}
}

func TestSplitTerseUnescapes(t *testing.T) {
	cases := map[string][]string{
		`My\:Net:72:WPA2`: {"My:Net", "72", "WPA2"},
		`a\\b::c`:         {`a\b`, "", "c"},
	}
	for in, want := range cases {
		if got := SplitTerse(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitTerse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNetworkManagerParsesDevicesAndScan(t *testing.T) {
	nm := &NetworkManager{Run: func(args []string, _ time.Duration) (string, error) {
		if slices.Contains(args, "list") {
			return "Venue:40:WPA2\nVenue:75:WPA2\nCafe\\:Free:30:\n:90:WPA2\n", nil
		}
		return "wlan0:wifi:connected:eventscreen-hotspot\neth0:ethernet:unavailable:\nlo:loopback:unmanaged:\n", nil
	}}
	if on, _ := nm.HotspotActive(); !on {
		t.Error("hotspot should be active")
	}
	if on, _ := nm.Online(); on {
		t.Error("the hotspot doesn't count as online")
	}
	if has, _ := nm.HasWifi(); !has {
		t.Error("should have wlan0")
	}
	want := []Network{{"Venue", 75, true}, {"Cafe:Free", 30, false}}
	if got := nm.Scan(); !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %+v, want %+v", got, want)
	}
}

func TestNetworkManagerOnlineOnWifi(t *testing.T) {
	nm := &NetworkManager{Run: func([]string, time.Duration) (string, error) {
		return "wlan0:wifi:connected:wifi-Venue\n", nil
	}}
	if on, _ := nm.Online(); !on {
		t.Error("should be online")
	}
	if on, _ := nm.HotspotActive(); on {
		t.Error("no hotspot")
	}
}

func TestSaveNetworkAddsAConnection(t *testing.T) {
	var calls [][]string
	nm := &NetworkManager{Run: func(args []string, _ time.Duration) (string, error) {
		calls = append(calls, args)
		return "", nil
	}}
	if _, err := nm.SaveNetwork("Venue", "password1", true, 0); err != nil {
		t.Fatal(err)
	}
	add := calls[len(calls)-1]
	if add[0] != "connection" || add[1] != "add" {
		t.Fatalf("last call %q", add)
	}
	for _, want := range []string{"wifi-Venue", "password1", "802-11-wireless.hidden"} {
		if !slices.Contains(add, want) {
			t.Errorf("%q missing from %q", want, add)
		}
	}
}

type savedNM struct {
	calls   [][]string
	deleted []string
	nm      *NetworkManager
}

func newSavedNM() *savedNM {
	s := &savedNM{}
	s.nm = &NetworkManager{Run: func(args []string, _ time.Duration) (string, error) {
		s.calls = append(s.calls, args)
		switch {
		case len(args) >= 4 && args[2] == "NAME,TYPE,AUTOCONNECT-PRIORITY,ACTIVE":
			return "wifi-Home:802-11-wireless:0:yes\n" +
				"Venue Net:802-11-wireless:10:no\n" +
				"eventscreen-hotspot:802-11-wireless:0:no\n" +
				"Wired connection 1:802-3-ethernet:-999:no\n", nil
		case slices.Contains(args, "802-11-wireless.ssid"):
			return map[string]string{"wifi-Home": "Home\n", "Venue Net": "Venue\n"}[args[len(args)-1]], nil
		case args[0] == "connection" && args[1] == "delete":
			s.deleted = append(s.deleted, args[2])
			return "", nil
		case len(args) >= 3 && args[2] == "DEVICE,TYPE,STATE,CONNECTION":
			return "wlan0:wifi:connected:wifi-Home\n", nil
		case slices.Contains(args, "ACTIVE,SSID,SIGNAL"):
			return "no:Venue:40\nyes:Home:67\n", nil
		case slices.Contains(args, "IP4.ADDRESS"):
			return "192.168.1.50/24\n", nil
		}
		return "", nil
	}}
	return s
}

func TestSavedNetworksSkipHotspotAndEthernet(t *testing.T) {
	got, err := newSavedNM().nm.SavedNetworks()
	if err != nil {
		t.Fatal(err)
	}
	want := []Saved{{"Venue", "Venue Net", 10, false}, {"Home", "wifi-Home", 0, true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SavedNetworks = %+v, want %+v", got, want)
	}
}

func TestCurrentNetworkReadsSsidSignalAndIP(t *testing.T) {
	got, err := newSavedNM().nm.CurrentNetwork()
	if err != nil {
		t.Fatal(err)
	}
	sig := 67
	want := &Current{"wifi-Home", "Home", &sig, "192.168.1.50"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CurrentNetwork = %+v, want %+v", got, want)
	}
}

func TestForgetDeletesBySsid(t *testing.T) {
	s := newSavedNM()
	n, err := s.nm.Forget("Venue")
	if err != nil || n != 1 || !reflect.DeepEqual(s.deleted, []string{"Venue Net"}) {
		t.Errorf("Forget = %d, %v; deleted %q", n, err, s.deleted)
	}
}

func TestSaveReplacesAConnectionSavedUnderAnotherName(t *testing.T) {
	// A network saved by hand (or by the Pi Imager) under another name is
	// replaced rather than duplicated.
	s := newSavedNM()
	if _, err := s.nm.SaveNetwork("Venue", "newpass12", false, 5); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.deleted, "Venue Net") {
		t.Errorf("deleted %q", s.deleted)
	}
	add := s.calls[len(s.calls)-1]
	if i := slices.Index(add, "connection.autoconnect-priority"); add[i+1] != "5" {
		t.Errorf("priority %q", add[i+1])
	}
}

func TestFallbackStaysQuietWhileOnline(t *testing.T) {
	fb, nm, _, clock := makeFallback(t, nil)
	for range 50 {
		clock.add(10)
		tick(t, fb)
	}
	if fb.State() != "client" || nm.hotspot {
		t.Error("hotspot started while online")
	}
}

func TestFallbackStartsHotspotAfterTimeout(t *testing.T) {
	fb, nm, screen, clock := makeFallback(t, func(c *config.Config) { c.WifiFallbackAfter = 120 })
	nm.online = false
	clock.add(110)
	tick(t, fb)
	if nm.hotspot {
		t.Fatal("too early")
	}
	clock.add(10)
	tick(t, fb)
	if fb.State() != "hotspot" || !nm.hotspot || !screen.on() {
		t.Fatal("hotspot should be up and on the screen")
	}
	// Its own hotspot isn't offered as a network to join.
	if got := fb.Networks(); len(got) != 1 || got[0].SSID != "Venue" {
		t.Errorf("networks %+v", got)
	}
}

func TestFallbackRetriesKnownNetworks(t *testing.T) {
	fb, nm, screen, clock := makeFallback(t, func(c *config.Config) {
		c.WifiFallbackAfter, c.WifiRetryInterval = 120, 300
	})
	nm.online = false
	clock.add(120)
	tick(t, fb)
	clock.add(300)
	tick(t, fb)
	if fb.State() != "client" || nm.hotspot || screen.on() {
		t.Fatal("should have dropped the hotspot to retry")
	}
	// Back up after the retry window if still offline.
	clock.add(RetryWindow - 1)
	tick(t, fb)
	if nm.hotspot {
		t.Fatal("back too early")
	}
	clock.add(1)
	tick(t, fb)
	if !nm.hotspot {
		t.Fatal("hotspot should be back")
	}
}

func TestFallbackDoesNotRetryWhileSomeoneIsSettingUp(t *testing.T) {
	fb, nm, _, clock := makeFallback(t, func(c *config.Config) {
		c.WifiFallbackAfter, c.WifiRetryInterval = 120, 300
	})
	nm.online = false
	clock.add(120)
	tick(t, fb)
	clock.add(290)
	fb.Activity()
	clock.add(10)
	tick(t, fb)
	if fb.State() != "hotspot" {
		t.Error("dropped the hotspot while in use")
	}
}

func TestFallbackRestartsDeadHotspot(t *testing.T) {
	fb, nm, _, clock := makeFallback(t, nil)
	nm.online = false
	clock.add(120)
	tick(t, fb)
	nm.hotspot = false
	clock.add(10)
	tick(t, fb)
	if nm.hotspotStarts != 2 {
		t.Errorf("starts = %d", nm.hotspotStarts)
	}
}

func TestEthernetEndsSetupMode(t *testing.T) {
	fb, nm, _, clock := makeFallback(t, nil)
	nm.online = false
	clock.add(120)
	tick(t, fb)
	nm.online = true
	clock.add(10)
	tick(t, fb)
	if fb.State() != "client" || nm.hotspot {
		t.Error("should be back in client mode")
	}
}

func TestChooseJoinsTheNetwork(t *testing.T) {
	fb, nm, screen, clock := makeFallback(t, nil)
	nm.online = false
	clock.add(120)
	tick(t, fb)
	if !fb.Choose("Venue", "password1", false) {
		t.Fatal("Choose failed")
	}
	if got := nm.savedCalls(); !reflect.DeepEqual(got, []savedCall{{"Venue", "password1", false}}) {
		t.Errorf("saved %+v", got)
	}
	if fb.State() != "client" || nm.hotspot || screen.on() {
		t.Error("should be in client mode")
	}
	tick(t, fb)
	if fb.State() != "client" {
		t.Error("should stay in client mode")
	}
}

func TestChooseFailureBringsHotspotBack(t *testing.T) {
	fb, nm, _, clock := makeFallback(t, nil)
	nm.online, nm.joinOK = false, false
	clock.add(120)
	tick(t, fb)
	if fb.Choose("Venue", "wrongpass", false) {
		t.Fatal("Choose should fail")
	}
	if fb.State() != "hotspot" || !nm.hotspot || !strings.Contains(fb.Message(), "Couldn't join Venue") {
		t.Errorf("state %s hotspot %v message %q", fb.State(), nm.hotspot, fb.Message())
	}
}

func TestValidate(t *testing.T) {
	for _, ok := range [][2]string{{"Venue", "password1"}, {"Open", ""}} {
		if msg := Validate(ok[0], ok[1]); msg != "" {
			t.Errorf("Validate(%q) = %q", ok, msg)
		}
	}
	for _, bad := range [][2]string{{"", "password1"}, {strings.Repeat("x", 33), ""}, {"Venue", "short"}} {
		if Validate(bad[0], bad[1]) == "" {
			t.Errorf("Validate(%q) accepted", bad)
		}
	}
}

func TestGeneratedHotspotPasswordIsKept(t *testing.T) {
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	ssid, pw, err := HotspotCredentials(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ssid, "EventScreen-") || len(pw) < 8 {
		t.Errorf("got %q %q", ssid, pw)
	}
	if _, again, _ := HotspotCredentials(cfg); again != pw {
		t.Error("password changed")
	}
	st, err := os.Stat(filepath.Join(cfg.StateDir, "hotspot-password"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", st.Mode(), err)
	}
}

func TestConfiguredHotspotCredentials(t *testing.T) {
	cfg := config.Default()
	cfg.HotspotSSID, cfg.HotspotPassword = "Pit", "letmein99"
	ssid, pw, err := HotspotCredentials(cfg)
	if err != nil || ssid != "Pit" || pw != "letmein99" {
		t.Errorf("got %q %q %v", ssid, pw, err)
	}
	cfg.HotspotPassword = "short"
	if _, _, err := HotspotCredentials(cfg); err == nil {
		t.Error("short password accepted")
	}
}

func setupServer(t *testing.T) (*Fallback, *fakeNM, string) {
	t.Helper()
	fb, nm, _, clock := makeFallback(t, nil)
	nm.online = false
	clock.add(120)
	tick(t, fb)
	srv := NewSetupServer(fb, "127.0.0.1:0")
	srv.Start()
	t.Cleanup(srv.Stop)
	return fb, nm, "http://" + srv.Addr()
}

func get(t *testing.T, u string) (*http.Response, string) {
	t.Helper()
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestSetupPagesAndCaptivePortalRedirect(t *testing.T) {
	_, _, base := setupServer(t)
	resp, body := get(t, base+"/")
	if resp.StatusCode != 200 || !strings.Contains(body, "Venue") {
		t.Errorf("setup page %d %q", resp.StatusCode, body)
	}
	_, body = get(t, base+"/screen")
	if !strings.Contains(body, "secret123") || !strings.Contains(body, "EventScreen-test") {
		t.Errorf("screen page %q", body)
	}
	resp, _ = get(t, base+"/generate_204")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != SetupURL {
		t.Errorf("redirect %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func post(t *testing.T, base string, form url.Values) string {
	t.Helper()
	resp, err := http.PostForm(base+"/", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestSetupPostRejectsShortPassword(t *testing.T) {
	_, nm, base := setupServer(t)
	body := post(t, base, url.Values{"ssid": {"Venue"}, "password": {"short"}})
	if !strings.Contains(body, "8 to 63") || len(nm.savedCalls()) != 0 {
		t.Errorf("body %q saved %+v", body, nm.savedCalls())
	}
}

func TestSetupPostJoinsTypedNetwork(t *testing.T) {
	_, nm, base := setupServer(t)
	body := post(t, base, url.Values{"ssid": {"Venue"}, "other": {"Typed Net"}, "password": {"password1"}})
	if !strings.Contains(body, "Connecting to Typed Net") {
		t.Errorf("body %q", body)
	}
	eventually(t, func() bool {
		return reflect.DeepEqual(nm.savedCalls(), []savedCall{{"Typed Net", "password1", false}})
	})
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
