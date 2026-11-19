package check

import (
	"bytes"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
)

// fakeProbe is a healthy Pi; tests break one thing at a time.
type fakeProbe struct {
	cfg       config.Config
	services  map[string]string
	files     map[string]string
	binaries  map[string]bool
	nmcli     string
	throttled string
	status    *APIStatus
}

func healthy() *APIStatus {
	pid, ok := 1234, true
	st := &APIStatus{State: "playing"}
	st.Browser = &struct {
		Running   bool   `json:"running"`
		PID       *int   `json:"pid"`
		Restarts  int    `json:"restarts"`
		LastError string `json:"last_error"`
	}{Running: true, PID: &pid}
	st.Network = &struct {
		PageReachable *bool `json:"page_reachable"`
	}{PageReachable: &ok}
	st.Current = &struct {
		URL string `json:"url"`
	}{URL: "http://rctc.local/board"}
	return st
}

func newProbe() *fakeProbe {
	return &fakeProbe{
		cfg:      config.Default(),
		services: map[string]string{"eventscreen": "active", "avahi-daemon": "active", "eventscreen-wifi": "active"},
		files: map[string]string{
			"/sys/class/drm/card1-HDMI-A-1/status":      "connected\n",
			"/boot/firmware/cmdline.txt":                "console=tty1 rootwait video=HDMI-A-1:1920x1080@60D consoleblank=0\n",
			"/etc/avahi/services/eventscreen.service":   "<service-group/>",
			"/etc/hostname":                             "screen-01\n",
			"/usr/lib/aarch64-linux-gnu/libGLESv2.so.2": "",
		},
		binaries:  map[string]bool{"cog": true, "cogctl": true, "nmcli": true},
		nmcli:     "wifi:connected:wifi-Venue\nloopback:connected (externally):lo",
		throttled: "throttled=0x0",
		status:    healthy(),
	}
}

func (p *fakeProbe) Config() config.Config { return p.cfg }
func (p *fakeProbe) Run(cmd ...string) (int, string) {
	switch cmd[0] {
	case "systemctl":
		if s, ok := p.services[cmd[2]]; ok {
			return 0, s
		}
		return 3, "inactive"
	case "nmcli":
		return 0, p.nmcli
	case "vcgencmd":
		return 0, p.throttled
	}
	return 127, "not found"
}
func (p *fakeProbe) Which(b string) bool { return p.binaries[b] }
func (p *fakeProbe) Read(path string) (string, bool) {
	s, ok := p.files[path]
	return s, ok
}
func (p *fakeProbe) Glob(pattern string) []string {
	var out []string
	for f := range p.files {
		if ok, _ := filepath.Match(pattern, f); ok {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
func (p *fakeProbe) APIStatus() (*APIStatus, error) {
	if p.status == nil {
		return nil, errors.New("refused")
	}
	return p.status, nil
}

func levels(p Probe) map[string]string {
	out := map[string]string{}
	for _, r := range RunChecks(p) {
		out[r.Name] = r.Level
	}
	return out
}

func TestHealthyPiPasses(t *testing.T) {
	for name, level := range levels(newProbe()) {
		if level != Pass {
			t.Errorf("%s: %s", name, level)
		}
	}
}

func TestHDMIDisconnected(t *testing.T) {
	p := newProbe()
	p.files["/sys/class/drm/card1-HDMI-A-1/status"] = "disconnected\n"
	if l := levels(p)["HDMI"]; l != Fail {
		t.Error(l)
	}
}

func TestDisplayModeNotForced(t *testing.T) {
	p := newProbe()
	p.files["/boot/firmware/cmdline.txt"] = "console=tty1 video=HDMI-A-1:1920x1080@60\n"
	if l := levels(p)["display mode"]; l != Warn {
		t.Error(l)
	}
}

func TestAgentDown(t *testing.T) {
	p := newProbe()
	p.services["eventscreen"] = "failed"
	p.status = nil
	r := levels(p)
	if r["service eventscreen"] != Fail || r["API"] != Fail {
		t.Error(r)
	}
}

func TestBrowserNotRunning(t *testing.T) {
	p := newProbe()
	p.status.Browser.Running, p.status.Browser.LastError = false, "browser exited with code 1"
	if l := levels(p)["browser running"]; l != Fail {
		t.Error(l)
	}
}

func TestCogMissing(t *testing.T) {
	p := newProbe()
	delete(p.binaries, "cog")
	if l := levels(p)["browser"]; l != Fail {
		t.Error(l)
	}
}

func TestCogWithoutGLES(t *testing.T) {
	// Pi OS Lite installs cog without it, and every page then crashes.
	p := newProbe()
	delete(p.files, "/usr/lib/aarch64-linux-gnu/libGLESv2.so.2")
	for _, r := range RunChecks(p) {
		if r.Name == "browser" && (r.Level != Fail || !strings.Contains(r.Detail, "libgles2")) {
			t.Error(r)
		}
	}
}

func TestPageServerUnreachable(t *testing.T) {
	p := newProbe()
	no := false
	p.status.Network.PageReachable = &no
	if l := levels(p)["page server"]; l != Fail {
		t.Error(l)
	}
}

func TestSetupHotspotFailsNetwork(t *testing.T) {
	p := newProbe()
	p.nmcli = "wifi:connected:eventscreen-hotspot"
	if l := levels(p)["network"]; l != Fail {
		t.Error(l)
	}
}

func TestEthernetCounts(t *testing.T) {
	p := newProbe()
	p.nmcli = "ethernet:connected:Wired connection 1\nwifi:disconnected:"
	if l := levels(p)["network"]; l != Pass {
		t.Error(l)
	}
}

func TestUnderVoltageWarns(t *testing.T) {
	p := newProbe()
	p.throttled = "throttled=0x50005"
	if l := levels(p)["power"]; l != Warn {
		t.Error(l)
	}
}

func TestWifiServiceOptionalWhenFallbackOff(t *testing.T) {
	p := newProbe()
	p.cfg.WifiFallback = false
	p.services["eventscreen-wifi"] = "failed"
	if l := levels(p)["service eventscreen-wifi"]; l != Warn {
		t.Error(l)
	}
}

func TestMainExitCode(t *testing.T) {
	var out bytes.Buffer
	if code := Main("/nonexistent.toml", false, &out, func(config.Config) Probe { return newProbe() }); code != 0 {
		t.Errorf("healthy exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "All good.") {
		t.Error(out.String())
	}
	out.Reset()
	broken := func(config.Config) Probe { p := newProbe(); p.status = nil; return p }
	if code := Main("/nonexistent.toml", true, &out, broken); code != 1 || !strings.Contains(out.String(), `"ok": false`) {
		t.Errorf("broken exit %d\n%s", code, out.String())
	}
}
