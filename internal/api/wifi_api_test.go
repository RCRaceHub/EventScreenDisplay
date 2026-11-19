package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
	"github.com/RCRaceHub/EventScreenDisplay/internal/wifi"
)

type netInfo struct {
	priority int
	password string
}

// savedNM is NetworkManager's saved networks and the current one.
type savedNM struct {
	mu       sync.Mutex
	networks map[string]netInfo
	active   string
	joinOK   bool
	hotspot  bool
}

func (n *savedNM) Online() (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active != "", nil
}
func (n *savedNM) HotspotActive() (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hotspot, nil
}
func (n *savedNM) Scan() []wifi.Network {
	return []wifi.Network{{SSID: "Venue", Signal: 70, Secure: true}, {SSID: "Home", Signal: 60, Secure: true},
		{SSID: "EventScreen-test", Signal: 99, Secure: true}}
}
func (n *savedNM) StartHotspot(string, string) error {
	n.mu.Lock()
	n.hotspot = true
	n.mu.Unlock()
	return nil
}
func (n *savedNM) StopHotspot() { n.mu.Lock(); n.hotspot = false; n.mu.Unlock() }
func (n *savedNM) SaveNetwork(ssid, password string, _ bool, priority int) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.networks[ssid] = netInfo{priority, password}
	return "wifi-" + ssid, nil
}
func (n *savedNM) Connect(name string, _ int) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.joinOK {
		n.active = strings.TrimPrefix(name, "wifi-")
	}
	return n.joinOK
}
func (n *savedNM) SavedNetworks() ([]wifi.Saved, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []wifi.Saved
	for s, i := range n.networks {
		out = append(out, wifi.Saved{SSID: s, Connection: "wifi-" + s, Priority: i.priority, Active: s == n.active})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Priority != out[b].Priority {
			return out[a].Priority > out[b].Priority
		}
		return out[a].SSID < out[b].SSID
	})
	return out, nil
}
func (n *savedNM) CurrentNetwork() (*wifi.Current, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.active == "" {
		return nil, nil
	}
	sig := 60
	return &wifi.Current{Connection: "wifi-" + n.active, SSID: n.active, Signal: &sig, IP: "192.168.1.50"}, nil
}
func (n *savedNM) Forget(ssid string) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.networks[ssid]; ok {
		delete(n.networks, ssid)
		return 1, nil
	}
	return 0, nil
}
func (n *savedNM) has(ssid string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.networks[ssid]
	return ok
}

type noScreen struct{}

func (noScreen) Show()  {}
func (noScreen) Clear() {}

type wifiEnv struct {
	*env
	nm      *savedNM
	fb      *wifi.Fallback
	control *wifi.Control
	clock   *float64
	stop    func()
}

func newWifiEnv(t *testing.T, token string) *wifiEnv {
	t.Helper()
	nm := &savedNM{networks: map[string]netInfo{"Home": {0, "homepass1"}}, active: "Home", joinOK: true}
	clock := 1000.0
	var mu sync.Mutex
	fb := wifi.NewFallback(nm, config.Default(), noScreen{}, "EventScreen-test", "secret123", func() float64 {
		mu.Lock()
		defer mu.Unlock()
		return clock
	})
	control := wifi.NewControl(nm, fb)
	control.Clock = func() float64 { return 1234.0 }
	sock := filepath.Join(t.TempDir(), "control.sock")
	srv, err := wifi.ServeControl(control, sock, "nogroup-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = srv.Shutdown(context.Background())
		}
	}
	t.Cleanup(stop)
	w := &wifiEnv{env: newEnv(t, options{token: token, wifi: NewWifiClient(sock)}), nm: nm, fb: fb,
		control: control, clock: &clock, stop: stop}
	return w
}

func TestWifiStatusIsOpenAndHidesPasswords(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	r := e.call("GET", "/api/v1/wifi", nil, "Authorization", "")
	if r.status != 200 || r.body["mode"] != "client" {
		t.Fatalf("status %d %v", r.status, r.body)
	}
	if r.body["connected"].(map[string]any)["ssid"] != "Home" {
		t.Errorf("connected %v", r.body["connected"])
	}
	saved, _ := json.Marshal(r.body["saved"])
	if string(saved) != `[{"active":true,"priority":0,"ssid":"Home"}]` {
		t.Errorf("saved %s", saved)
	}
	if r.body["hotspot"].(map[string]any)["ssid"] != "EventScreen-test" {
		t.Errorf("hotspot %v", r.body["hotspot"])
	}
	if strings.Contains(r.raw, "homepass1") || strings.Contains(r.raw, "secret123") {
		t.Error("a password leaked")
	}
}

func TestWifiScanHidesOwnHotspot(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	r := e.call("GET", "/api/v1/wifi/scan", nil)
	var ssids []string
	for _, n := range r.body["networks"].([]any) {
		ssids = append(ssids, n.(map[string]any)["ssid"].(string))
	}
	if r.status != 200 || strings.Join(ssids, ",") != "Venue,Home" {
		t.Errorf("scan %d %v", r.status, ssids)
	}
}

func TestWifiAddWithoutConnecting(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	r := e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Venue", "password": "venuepass", "priority": 10})
	if r.status != 201 || r.raw != `{"connecting":false,"saved":true,"ssid":"Venue"}` {
		t.Fatalf("add %d %s", r.status, r.raw)
	}
	if e.nm.networks["Venue"].priority != 10 || e.nm.active != "Home" {
		t.Errorf("nm %+v", e.nm.networks)
	}
	saved := e.call("GET", "/api/v1/wifi", nil).body["saved"].([]any)
	if saved[0].(map[string]any)["ssid"] != "Venue" || saved[1].(map[string]any)["ssid"] != "Home" {
		t.Errorf("order %v", saved)
	}
}

func TestWifiAddAndConnect(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	r := e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Venue", "password": "venuepass", "connect": true})
	if r.status != 202 || r.body["connecting"] != true {
		t.Fatalf("add %d %v", r.status, r.body)
	}
	eventually(t, "switch", func() bool { return e.control.LastChange() != nil })
	last, _ := json.Marshal(e.call("GET", "/api/v1/wifi", nil).body["last_change"])
	if e.nm.active != "Venue" || string(last) != `{"at":1234,"message":"","ok":true,"ssid":"Venue"}` {
		t.Errorf("active %s last %s", e.nm.active, last)
	}
}

func TestWifiConnectFailureIsReported(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	e.nm.joinOK = false
	e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Venue", "password": "wrongpass", "connect": true})
	eventually(t, "switch", func() bool { return e.control.LastChange() != nil })
	last := e.call("GET", "/api/v1/wifi", nil).body["last_change"].(map[string]any)
	if last["ok"] != false || !strings.Contains(last["message"].(string), "Venue") {
		t.Errorf("last %v", last)
	}
}

func TestWifiConnectFromSetupMode(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	e.nm.active = ""
	*e.clock += 120
	if err := e.fb.Tick(); err != nil || e.fb.State() != "hotspot" {
		t.Fatalf("tick %v state %s", err, e.fb.State())
	}
	if m := e.call("GET", "/api/v1/wifi", nil).body["mode"]; m != "hotspot" {
		t.Errorf("mode %v", m)
	}
	e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Venue", "password": "venuepass", "connect": true})
	eventually(t, "switch", func() bool { return e.control.LastChange() != nil })
	if e.fb.State() != "client" || e.nm.hotspot {
		t.Errorf("state %s hotspot %v", e.fb.State(), e.nm.hotspot)
	}
}

func TestWifiAddValidation(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	for _, body := range []map[string]any{
		{"password": "venuepass"},
		{"ssid": "Venue", "password": "short"},
		{"ssid": strings.Repeat("x", 33)},
		{"ssid": "Venue", "priority": "high"},
		{"ssid": "Venue", "priority": 1000},
		{"ssid": "Venue", "connect": "yes"},
		{"ssid": "EventScreen-test", "password": "whatever1"},
	} {
		if r := e.call("POST", "/api/v1/wifi/networks", body); r.status != 400 || r.body["error"] == nil {
			t.Errorf("%v: %d %v", body, r.status, r.body)
		}
	}
	if len(e.nm.networks) != 1 {
		t.Errorf("networks %v", e.nm.networks)
	}
}

func TestWifiForget(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Old Venue/2", "password": "venuepass"})
	r := e.call("DELETE", "/api/v1/wifi/networks/Old%20Venue%2F2", nil)
	if r.status != 200 || r.raw != `{"forgotten":true,"ssid":"Old Venue/2"}` {
		t.Errorf("forget %d %s", r.status, r.raw)
	}
	if e.nm.has("Old Venue/2") {
		t.Error("still saved")
	}
	if r := e.call("DELETE", "/api/v1/wifi/networks/Nowhere", nil); r.status != 404 {
		t.Errorf("unknown: %d", r.status)
	}
}

func TestWifiCantForgetCurrentNetwork(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	if r := e.call("DELETE", "/api/v1/wifi/networks/Home", nil); r.status != 409 || !e.nm.has("Home") {
		t.Errorf("forget current: %d", r.status)
	}
}

func TestWifiWritesNeedTheToken(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	if r := e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Venue", "password": "venuepass"}, "Authorization", ""); r.status != 401 {
		t.Errorf("no token: %d", r.status)
	}
	if r := e.call("DELETE", "/api/v1/wifi/networks/Home", nil, "Authorization", "Bearer wrong"); r.status != 401 {
		t.Errorf("wrong token: %d", r.status)
	}
}

func TestWifiServiceDown(t *testing.T) {
	e := newWifiEnv(t, "s3cret")
	e.stop()
	r := e.call("GET", "/api/v1/wifi", nil)
	if r.status != 503 || !strings.Contains(r.body["error"].(string), "eventscreen-wifi") {
		t.Errorf("service down: %d %v", r.status, r.body)
	}
}

func TestWifiWithoutTokenReadsWorkWritesRefused(t *testing.T) {
	e := newWifiEnv(t, "")
	if r := e.call("GET", "/api/v1/wifi", nil); r.status != 200 {
		t.Errorf("read: %d", r.status)
	}
	r := e.call("POST", "/api/v1/wifi/networks", map[string]any{"ssid": "Venue", "password": "venuepass"})
	if r.status != 403 || !strings.Contains(r.body["error"].(string), "api_token") || e.nm.has("Venue") {
		t.Errorf("write: %d %v", r.status, r.body)
	}
}
