// Package check is the on-device health check: is this screen set up and
// working?
//
//	sudo eventscreen-check
//	eventscreen-check --json
//
// It prints one PASS / WARN / FAIL line per check and exits 1 if anything
// failed, so it also works in scripts. Each check is a small function that
// takes a Probe, which wraps everything that touches the system so the
// checks can be tested without a Pi.
package check

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/avahi"
	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
	"github.com/RCRaceHub/EventScreenDisplay/internal/version"
)

// Levels.
const (
	Pass = "PASS"
	Warn = "WARN"
	Fail = "FAIL"
)

// Result is one line of the report.
type Result struct {
	Name   string `json:"name"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// APIStatus is the part of the agent's status the checks read.
type APIStatus struct {
	State   string `json:"state"`
	Browser *struct {
		Running   bool   `json:"running"`
		PID       *int   `json:"pid"`
		Restarts  int    `json:"restarts"`
		LastError string `json:"last_error"`
	} `json:"browser"`
	Network *struct {
		PageReachable *bool `json:"page_reachable"`
	} `json:"network"`
	Current *struct {
		URL string `json:"url"`
	} `json:"current"`
}

// Probe reads the system. Tests use a fake.
type Probe interface {
	Config() config.Config
	// Run returns the exit code and stdout (or stderr if stdout is empty).
	Run(cmd ...string) (int, string)
	Which(binary string) bool
	// Read returns a file's contents, ok=false if it can't be read.
	Read(path string) (string, bool)
	Glob(pattern string) []string
	APIStatus() (*APIStatus, error)
}

// System is the real Probe.
type System struct{ Cfg config.Config }

func (s System) Config() config.Config { return s.Cfg }

func (s System) Run(cmd ...string) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	out := stdout.String()
	if out == "" {
		out = stderr.String()
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), strings.TrimSpace(out)
		}
		return 127, err.Error()
	}
	return 0, strings.TrimSpace(out)
}

func (s System) Which(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

func (s System) Read(path string) (string, bool) {
	data, err := os.ReadFile(path)
	return string(data), err == nil
}

func (s System) Glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Strings(m)
	return m
}

func (s System) APIStatus() (*APIStatus, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/status", s.Cfg.Port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st APIStatus
	return &st, json.NewDecoder(resp.Body).Decode(&st)
}

// Services checks the systemd units.
func Services(p Probe) []Result {
	var out []Result
	units := []struct {
		name     string
		required bool
	}{{"eventscreen", true}, {"avahi-daemon", true}, {"eventscreen-wifi", p.Config().WifiFallback}}
	for _, u := range units {
		_, state := p.Run("systemctl", "is-active", u.name)
		state = strings.SplitN(state, "\n", 2)[0]
		shown := state
		if shown == "" {
			shown = "unknown"
		}
		name := "service " + u.name
		switch {
		case state == "active":
			out = append(out, Result{name, Pass, "running", ""})
		case u.name == "eventscreen-wifi" && (state == "inactive" || state == ""):
			// It exits cleanly when there's no Wi-Fi to look after.
			out = append(out, Result{name, Warn, "not running (" + shown + ")",
				"fine on Ethernet-only Pis; else journalctl -u eventscreen-wifi"})
		default:
			level := Warn
			if u.required {
				level = Fail
			}
			out = append(out, Result{name, level, "not running (" + shown + ")", "journalctl -u " + u.name + " -e"})
		}
	}
	return out
}

// Browser checks the configured browser is installed.
func Browser(p Probe) []Result {
	b := p.Config().Browser
	need, ok := map[string][]string{"cog": {"cog", "cogctl"}, "chromium": {"cage"}, "dummy": {}}[b]
	if !ok {
		return []Result{{"browser", Fail, fmt.Sprintf("unknown browser '%s' in config", b), ""}}
	}
	var missing []string
	for _, bin := range need {
		if !p.Which(bin) {
			missing = append(missing, bin)
		}
	}
	if b == "chromium" && !p.Which("chromium-browser") && !p.Which("chromium") {
		missing = append(missing, "chromium-browser")
	}
	// WebKit's page renderer needs OpenGL ES even though nothing links cog
	// to it directly; without it every page crashes ("renderer process
	// crashed").
	if b == "cog" && len(p.Glob("/usr/lib/*/libGLESv2.so.2*")) == 0 && len(p.Glob("/usr/lib/libGLESv2.so.2*")) == 0 {
		missing = append(missing, "libgles2")
	}
	if len(missing) > 0 {
		return []Result{{"browser", Fail, b + ": missing " + strings.Join(missing, ", "),
			"sudo eventscreen-setup --browser " + b}}
	}
	return []Result{{"browser", Pass, b + " installed", ""}}
}

// HDMI checks a display is connected and the mode is pinned.
func HDMI(p Probe) []Result {
	var out []Result
	ports := p.Glob("/sys/class/drm/card*-HDMI-A-*/status")
	if len(ports) == 0 {
		out = append(out, Result{"HDMI", Warn, "no HDMI output found (not a Pi, or no KMS driver)", ""})
	} else {
		var connected, all []string
		for _, s := range ports {
			name := filepath.Base(filepath.Dir(s))
			if i := strings.Index(name, "-"); i >= 0 {
				name = name[i+1:]
			}
			v, _ := p.Read(s)
			v = strings.TrimSpace(v)
			all = append(all, name+" "+v)
			if v == "connected" {
				connected = append(connected, name)
			}
		}
		if len(connected) > 0 {
			out = append(out, Result{"HDMI", Pass, strings.Join(connected, ", ") + " connected", ""})
		} else {
			out = append(out, Result{"HDMI", Fail, "nothing connected: " + strings.Join(all, ", "),
				"check the cable, adapter and TV input; force it on with " +
					"sudo eventscreen-setup --resolution 1920x1080@60D"})
		}
	}
	cmdline, ok := p.Read("/boot/firmware/cmdline.txt")
	if !ok {
		cmdline, ok = p.Read("/boot/cmdline.txt")
	}
	if !ok {
		return append(out, Result{"display mode", Warn, "no cmdline.txt (not a Raspberry Pi?)", ""})
	}
	mode := ""
	for _, w := range strings.Fields(cmdline) {
		if strings.HasPrefix(w, "video=HDMI-A-1:") {
			mode = strings.SplitN(w, ":", 2)[1]
			break
		}
	}
	switch {
	case mode == "":
		out = append(out, Result{"display mode", Warn, "HDMI mode not pinned; the TV picks it",
			"sudo eventscreen-setup && sudo reboot"})
	case !strings.Contains(strings.SplitN(mode, ",", 2)[0], "D"):
		out = append(out, Result{"display mode", Warn, mode + ", not forced on",
			"sudo eventscreen-setup --resolution 1920x1080@60D && sudo reboot"})
	default:
		out = append(out, Result{"display mode", Pass, mode, ""})
	}
	return out
}

// API checks the agent answers, its browser runs and its page's server
// can be reached.
func API(p Probe) []Result {
	port := p.Config().Port
	st, err := p.APIStatus()
	if err != nil {
		return []Result{{"API", Fail, fmt.Sprintf("no answer on port %d: %v", port, err), "journalctl -u eventscreen -e"}}
	}
	out := []Result{{"API", Pass, fmt.Sprintf("answering on port %d, state %s", port, st.State), ""}}
	if b := st.Browser; b != nil && b.Running {
		pid := "None"
		if b.PID != nil {
			pid = fmt.Sprint(*b.PID)
		}
		out = append(out, Result{"browser running", Pass, fmt.Sprintf("pid %s, %d restarts", pid, b.Restarts), ""})
	} else {
		msg := "not running"
		if b != nil && b.LastError != "" {
			msg = b.LastError
		}
		out = append(out, Result{"browser running", Fail, msg, "journalctl -u eventscreen -e"})
	}
	current := ""
	if st.Current != nil {
		current = st.Current.URL
	}
	if st.Network != nil && st.Network.PageReachable != nil {
		if *st.Network.PageReachable {
			out = append(out, Result{"page server", Pass, current + " reachable", ""})
		} else {
			out = append(out, Result{"page server", Fail, "can't reach " + current,
				"check the timing server is up and on this network"})
		}
	}
	return out
}

// Network checks the screen is on a network and advertised over mDNS.
func Network(p Probe) []Result {
	code, out := p.Run("nmcli", "-t", "-f", "TYPE,STATE,CONNECTION", "device")
	if code != 0 {
		return []Result{{"network", Warn, "can't ask NetworkManager: " + out, ""}}
	}
	hotspot := false
	var up []string
	for _, line := range strings.Split(out, "\n") {
		r := strings.SplitN(line, ":", 3)
		if len(r) != 3 {
			continue
		}
		if r[2] == "eventscreen-hotspot" && r[1] == "connected" {
			hotspot = true
		}
		if (r[0] == "wifi" || r[0] == "ethernet") && r[1] == "connected" && r[2] != "eventscreen-hotspot" {
			up = append(up, r[2])
		}
	}
	var res []Result
	switch {
	case len(up) > 0:
		res = append(res, Result{"network", Pass, "connected (" + strings.Join(up, ", ") + ")", ""})
	case hotspot:
		res = append(res, Result{"network", Fail, "in Wi-Fi setup mode (hotspot up)",
			"join the hotspot shown on the TV and pick a network"})
	default:
		res = append(res, Result{"network", Fail, "not connected", "nmcli device wifi list"})
	}
	if _, ok := p.Read(avahi.ServicePath); ok {
		res = append(res, Result{"mDNS", Pass, "advertising as " + hostname(p) + ".local", ""})
	} else {
		res = append(res, Result{"mDNS", Fail, avahi.ServicePath + " missing", "sudo systemctl restart eventscreen"})
	}
	return res
}

// Power checks for under-voltage and throttling.
func Power(p Probe) []Result {
	code, out := p.Run("vcgencmd", "get_throttled")
	if code != 0 {
		return nil
	}
	value := out[strings.LastIndex(out, "=")+1:]
	if value == "0x0" {
		return []Result{{"power", Pass, "no under-voltage or throttling", ""}}
	}
	return []Result{{"power", Warn, "throttled=" + value + ": under-voltage or overheating since boot",
		"use the official power supply and a short cable"}}
}

func hostname(p Probe) string {
	h, _ := p.Read("/etc/hostname")
	if h = strings.TrimSpace(h); h == "" {
		return "?"
	}
	return h
}

type named struct {
	name string
	fn   func(Probe) []Result
}

var checks = []named{{"services", Services}, {"browser", Browser}, {"hdmi", HDMI}, {"api", API},
	{"network", Network}, {"power", Power}}

// RunChecks runs every check. A broken check doesn't hide the others.
func RunChecks(p Probe) (results []Result) {
	for _, c := range checks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					results = append(results, Result{c.name, Warn, fmt.Sprintf("check failed: %v", r), ""})
				}
			}()
			results = append(results, c.fn(p)...)
		}()
	}
	return results
}

// Main is "eventscreen check". It returns the exit code.
func Main(configPath string, asJSON bool, stdout io.Writer, probe func(config.Config) Probe) int {
	cfg, err := config.Load(configPath)
	cfgResult := Result{"config", Pass, configPath, ""}
	if err != nil {
		cfg = config.Default()
		cfgResult = Result{"config", Fail, err.Error(), "fix " + configPath}
	}
	if probe == nil {
		probe = func(c config.Config) Probe { return System{c} }
	}
	p := probe(cfg)
	results := append([]Result{cfgResult}, RunChecks(p)...)
	failed := false
	for _, r := range results {
		failed = failed || r.Level == Fail
	}
	if asJSON {
		data, _ := json.MarshalIndent(map[string]any{"version": version.Version, "ok": !failed, "results": results}, "", "  ")
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintf(stdout, "eventscreen %s on %s\n", version.Version, hostname(p))
		for _, r := range results {
			fmt.Fprintf(stdout, "  %s  %s: %s\n", r.Level, r.Name, r.Detail)
			if r.Fix != "" && r.Level != Pass {
				fmt.Fprintf(stdout, "        -> %s\n", r.Fix)
			}
		}
		if failed {
			fmt.Fprintln(stdout, "Some checks failed.")
		} else {
			fmt.Fprintln(stdout, "All good.")
		}
	}
	if failed {
		return 1
	}
	return 0
}
