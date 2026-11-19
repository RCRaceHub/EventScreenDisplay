package wifi

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
)

const passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NM is the NetworkManager calls the fallback and the control API make.
// NetworkManager implements it; tests use a fake.
type NM interface {
	Online() (bool, error)
	HotspotActive() (bool, error)
	Scan() []Network
	StartHotspot(ssid, password string) error
	StopHotspot()
	SaveNetwork(ssid, password string, hidden bool, priority int) (string, error)
	SavedNetworks() ([]Saved, error)
	CurrentNetwork() (*Current, error)
	Forget(ssid string) (int, error)
	Connect(name string, wait int) bool
}

// TV puts the setup instructions on the screen.
type TV interface {
	Show()
	Clear()
}

// Screen puts the setup instructions on the TV through the agent's API.
type Screen struct {
	base   string
	token  string
	client http.Client
}

// NewScreen talks to the agent on cfg.Port.
func NewScreen(cfg config.Config) *Screen {
	return &Screen{base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", cfg.Port), token: cfg.APIToken,
		client: http.Client{Timeout: 5 * time.Second}}
}

func (s *Screen) call(method, path string, body any, out any) error {
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("agent answered %s", resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (s *Screen) showing() (bool, error) {
	var st struct {
		Override *struct {
			URL string `json:"url"`
		} `json:"override"`
	}
	if err := s.call("GET", "/status", nil, &st); err != nil {
		return false, err
	}
	return st.Override != nil && st.Override.URL == ScreenURL, nil
}

// Show puts the instructions up, above any event message.
func (s *Screen) Show() {
	on, err := s.showing()
	if err == nil && !on {
		err = s.call("POST", "/control/show", map[string]any{
			"url": ScreenURL, "label": "Wi-Fi setup", "above_messages": true}, nil)
	}
	if err != nil {
		slog.Warn("couldn't put Wi-Fi setup on the screen", "err", err)
	}
}

// Clear takes the instructions down.
func (s *Screen) Clear() {
	on, err := s.showing()
	if err == nil && on {
		err = s.call("DELETE", "/control/show", nil, nil)
	}
	if err != nil {
		slog.Warn("couldn't clear Wi-Fi setup from the screen", "err", err)
	}
}

// HotspotCredentials is the hotspot's SSID and password: from the config,
// or "EventScreen-<hostname>" and a random password kept in the state
// directory.
func HotspotCredentials(cfg config.Config) (string, string, error) {
	ssid := cfg.HotspotSSID
	if ssid == "" {
		host, _ := os.Hostname()
		ssid = "EventScreen-" + host
	}
	password := cfg.HotspotPassword
	if password == "" {
		path := filepath.Join(cfg.StateDir, "hotspot-password")
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		password = strings.TrimSpace(string(data))
		if utf8.RuneCountInString(password) < 8 {
			password = randomPassword(10)
			if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
				return "", "", err
			}
			if err := os.WriteFile(path, []byte(password+"\n"), 0o600); err != nil {
				return "", "", err
			}
			_ = os.Chmod(path, 0o600)
		}
	}
	if utf8.RuneCountInString(password) < 8 {
		return "", "", errors.New("hotspot_password must be at least 8 characters")
	}
	if len(ssid) > 32 {
		ssid = truncateBytes(ssid, 32)
	}
	return ssid, password, nil
}

func truncateBytes(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func randomPassword(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		k, _ := rand.Int(rand.Reader, max)
		b[i] = passwordAlphabet[k.Int64()]
	}
	return string(b)
}

// Validate checks a network chosen on the setup page or the API, and
// returns a message for the person if it's no good ("" = fine).
func Validate(ssid, password string) string {
	if ssid == "" {
		return "Pick a network or type its name."
	}
	if len(ssid) > 32 {
		return "Network names are at most 32 bytes."
	}
	if n := utf8.RuneCountInString(password); password != "" && (n < 8 || n > 63) {
		return "Wi-Fi passwords are 8 to 63 characters (leave it empty for an open network)."
	}
	return ""
}

// Clock returns monotonic seconds. Tests swap in a fake.
type Clock func() float64

var start = time.Now()

func monotonic() float64 { return time.Since(start).Seconds() }

// Fallback decides when to start and stop the hotspot. Tick runs every
// Tick seconds.
//
// mu serialises the operations that change the network (tick, choose and
// the API's changes); vmu guards the values the pages and the API read
// while an operation runs, so a slow connect doesn't hold up a page.
type Fallback struct {
	nm       NM
	cfg      config.Config
	screen   TV
	ssid     string
	password string
	clock    Clock

	mu sync.Mutex

	vmu          sync.Mutex
	state        string // "client" or "hotspot"
	lastOnline   float64
	hotspotSince float64
	lastActivity float64
	networks     []Network
	message      string
	connecting   string

	OnHotspotUp   func()
	OnHotspotDown func()
}

// NewFallback makes a Fallback in client mode.
func NewFallback(nm NM, cfg config.Config, screen TV, ssid, password string, clock Clock) *Fallback {
	if clock == nil {
		clock = monotonic
	}
	now := clock()
	return &Fallback{nm: nm, cfg: cfg, screen: screen, ssid: ssid, password: password, clock: clock,
		state: "client", lastOnline: now, lastActivity: -ActivityGrace,
		OnHotspotUp: func() {}, OnHotspotDown: func() {}}
}

// SSID is the hotspot's name.
func (f *Fallback) SSID() string { return f.ssid }

// State is "client" or "hotspot".
func (f *Fallback) State() string {
	f.vmu.Lock()
	defer f.vmu.Unlock()
	return f.state
}

// Message is the last thing to tell the person on the setup page.
func (f *Fallback) Message() string {
	f.vmu.Lock()
	defer f.vmu.Unlock()
	return f.message
}

func (f *Fallback) setMessage(m string) {
	f.vmu.Lock()
	f.message = m
	f.vmu.Unlock()
}

// Connecting is the SSID being joined now ("" = none).
func (f *Fallback) Connecting() string {
	f.vmu.Lock()
	defer f.vmu.Unlock()
	return f.connecting
}

func (f *Fallback) setConnecting(s string) {
	f.vmu.Lock()
	f.connecting = s
	f.vmu.Unlock()
}

// Networks is the scan made just before the hotspot started.
func (f *Fallback) Networks() []Network {
	f.vmu.Lock()
	defer f.vmu.Unlock()
	return f.networks
}

func (f *Fallback) markOnline(t float64) {
	f.vmu.Lock()
	f.lastOnline = t
	f.vmu.Unlock()
}

// Tick checks the network and starts, stops or retries the hotspot.
func (f *Fallback) Tick() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.clock()
	f.vmu.Lock()
	state, lastOnline, since, activity := f.state, f.lastOnline, f.hotspotSince, f.lastActivity
	f.vmu.Unlock()
	online, err := f.nm.Online()
	if err != nil {
		return err
	}
	if state == "client" {
		if online {
			f.markOnline(now)
		} else if now-lastOnline >= float64(f.cfg.WifiFallbackAfter) {
			slog.Warn(fmt.Sprintf("no network for %.0fs; starting setup hotspot %s", now-lastOnline, f.ssid))
			return f.startHotspot(now)
		}
		return nil
	}
	if online {
		// Ethernet was plugged in while in setup mode.
		slog.Info("network is back; stopping setup hotspot")
		f.stopHotspot()
		f.markOnline(now)
		return nil
	}
	active, err := f.nm.HotspotActive()
	if err != nil {
		return err
	}
	switch {
	case !active:
		slog.Warn("setup hotspot went down; restarting it")
		return f.startHotspot(now)
	case now-since >= float64(f.cfg.WifiRetryInterval) && now-activity >= ActivityGrace:
		slog.Info("retrying known networks")
		f.stopHotspot()
		// Give the known networks RetryWindow seconds before the hotspot
		// comes back.
		f.markOnline(now - float64(f.cfg.WifiFallbackAfter) + RetryWindow)
	default:
		f.screen.Show()
	}
	return nil
}

// Choose saves a network from the setup page and tries to join it. On
// failure the hotspot comes back with a message for the person.
func (f *Fallback) Choose(ssid, password string, hidden bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setConnecting(ssid)
	ok, msg := func() (bool, string) {
		defer f.setConnecting("")
		name, err := f.nm.SaveNetwork(ssid, password, hidden, 0)
		if err != nil {
			slog.Error("saving network failed", "ssid", ssid, "err", err)
			return false, fmt.Sprintf("Couldn't save %s: %v", ssid, err)
		}
		f.stopHotspot()
		if f.nm.Connect(name, RetryWindow) {
			return true, ""
		}
		return false, fmt.Sprintf("Couldn't join %s. Check the password and that the network is in range, then try again.", ssid)
	}()
	f.setMessage(msg)
	if ok {
		slog.Info("joined " + ssid)
		f.markOnline(f.clock())
		return true
	}
	if err := f.startHotspot(f.clock()); err != nil {
		slog.Error("couldn't restart the setup hotspot", "err", err)
	}
	return false
}

// Activity records that someone is using the setup page.
func (f *Fallback) Activity() {
	t := f.clock()
	f.vmu.Lock()
	f.lastActivity = t
	f.vmu.Unlock()
}

// Shutdown takes the hotspot down if it's up.
func (f *Fallback) Shutdown() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.State() == "hotspot" {
		f.stopHotspot()
	}
}

func (f *Fallback) startHotspot(now float64) error {
	var nets []Network
	for _, n := range f.nm.Scan() {
		if n.SSID != f.ssid {
			nets = append(nets, n)
		}
	}
	f.vmu.Lock()
	f.networks = nets
	f.vmu.Unlock()
	if err := f.nm.StartHotspot(f.ssid, f.password); err != nil {
		return err
	}
	f.vmu.Lock()
	f.state = "hotspot"
	f.hotspotSince = now
	f.vmu.Unlock()
	f.OnHotspotUp()
	f.screen.Show()
	return nil
}

func (f *Fallback) stopHotspot() {
	f.OnHotspotDown()
	f.screen.Clear()
	f.nm.StopHotspot()
	f.vmu.Lock()
	f.state = "client"
	f.vmu.Unlock()
}
