// Package wifi is the Wi-Fi fallback hotspot with a setup page, and the
// Wi-Fi changes behind the agent's /api/v1/wifi endpoints.
//
// It runs as root in its own service (eventscreen-wifi.service), separate
// from the unprivileged viewer agent, because it drives NetworkManager:
//
//	eventscreen wifi --config /etc/eventscreen/config.toml
//
// When the Pi hasn't been on any network for wifi_fallback_after seconds it
// starts a hotspot, puts the hotspot's name and password on the TV (through
// the agent's "show" API), and serves a setup page on http://10.42.0.1
// where a phone can pick the venue's Wi-Fi. NetworkManager's DNS for the
// hotspot points every name at the Pi (see deploy/captive-dns.conf), so
// most phones open the setup page by themselves. Every wifi_retry_interval
// seconds it drops the hotspot briefly to retry the known networks, in case
// the venue Wi-Fi was just late coming up.
//
// The agent reaches the Wi-Fi API part over a Unix socket (see Control).
package wifi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// HotspotCon is the NetworkManager connection name of the hotspot.
	HotspotCon = "eventscreen-hotspot"
	// HotspotAddr is NetworkManager's address for the Pi on a shared
	// (hotspot) connection.
	HotspotAddr = "10.42.0.1"
	// Iface is the Wi-Fi interface.
	Iface = "wlan0"
	// SetupURL is the setup page phones open.
	SetupURL = "http://" + HotspotAddr + "/"
	// ScreenURL is the instructions page shown on the TV.
	ScreenURL = "http://" + HotspotAddr + "/screen"
	// RetryWindow is how long known networks get to connect after the
	// hotspot is dropped for a retry, in seconds.
	RetryWindow = 45
	// ActivityGrace: don't drop the hotspot for a retry while someone has
	// used the setup page in the last this many seconds.
	ActivityGrace = 120
	// Tick is how often the fallback checks the network.
	Tick = 10 * time.Second
)

// SplitTerse splits one line of "nmcli -t" output, which escapes ':' and
// '\'.
func SplitTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			cur.WriteByte(line[i+1])
			i++
			continue
		}
		if c == ':' {
			fields = append(fields, cur.String())
			cur.Reset()
		} else {
			cur.WriteByte(c)
		}
	}
	return append(fields, cur.String())
}

// Network is a Wi-Fi network seen in a scan.
type Network struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
	Secure bool   `json:"secure"`
}

// Saved is a saved Wi-Fi connection.
type Saved struct {
	SSID       string `json:"ssid"`
	Connection string `json:"-"`
	Priority   int    `json:"priority"`
	Active     bool   `json:"active"`
}

// Current is the Wi-Fi network the Pi is on.
type Current struct {
	Connection string `json:"connection"`
	SSID       string `json:"ssid"`
	Signal     *int   `json:"signal"`
	IP         string `json:"ip"`
}

// Device is one row of "nmcli device".
type Device struct{ Device, Type, State, Connection string }

// Runner runs nmcli with args and returns its stdout.
type Runner func(args []string, timeout time.Duration) (string, error)

// RunNmcli is the real Runner.
func RunNmcli(args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nmcli", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprintf("nmcli %s failed: %v", args[0], err)
		}
		return "", errors.New(msg)
	}
	return string(out), nil
}

// NetworkManager is the few nmcli calls the fallback and the API need.
type NetworkManager struct{ Run Runner }

func (nm *NetworkManager) nmcli(timeout time.Duration, args ...string) (string, error) {
	run := nm.Run
	if run == nil {
		run = RunNmcli
	}
	return run(args, timeout)
}

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Devices lists network devices.
func (nm *NetworkManager) Devices() ([]Device, error) {
	out, err := nm.nmcli(30*time.Second, "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device")
	if err != nil {
		return nil, err
	}
	var rows []Device
	for _, line := range lines(out) {
		if f := SplitTerse(line); len(f) >= 4 {
			rows = append(rows, Device{f[0], f[1], f[2], f[3]})
		}
	}
	return rows, nil
}

// Online reports a connection to a real network: Wi-Fi (not our hotspot)
// or Ethernet.
func (nm *NetworkManager) Online() (bool, error) {
	devs, err := nm.Devices()
	for _, d := range devs {
		if (d.Type == "wifi" || d.Type == "ethernet") && d.State == "connected" && d.Connection != HotspotCon {
			return true, nil
		}
	}
	return false, err
}

// HotspotActive reports whether the setup hotspot is up.
func (nm *NetworkManager) HotspotActive() (bool, error) {
	devs, err := nm.Devices()
	for _, d := range devs {
		if d.Connection == HotspotCon && d.State == "connected" {
			return true, nil
		}
	}
	return false, err
}

// HasWifi reports whether there is a wlan0.
func (nm *NetworkManager) HasWifi() (bool, error) {
	devs, err := nm.Devices()
	for _, d := range devs {
		if d.Device == Iface {
			return true, nil
		}
	}
	return false, err
}

// Scan lists networks in range, strongest first, one entry per SSID.
func (nm *NetworkManager) Scan() []Network {
	out, err := nm.nmcli(40*time.Second, "-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		slog.Warn("Wi-Fi scan failed", "err", err)
		return []Network{}
	}
	best := map[string]Network{}
	var order []string
	for _, line := range lines(out) {
		f := SplitTerse(line)
		if len(f) < 3 || f[0] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[1])
		n := Network{f[0], sig, f[2] != "" && f[2] != "--"}
		old, seen := best[n.SSID]
		if !seen {
			order = append(order, n.SSID)
		}
		if !seen || sig > old.Signal {
			best[n.SSID] = n
		}
	}
	nets := make([]Network, 0, len(order))
	for _, s := range order {
		nets = append(nets, best[s])
	}
	sort.SliceStable(nets, func(i, j int) bool { return nets[i].Signal > nets[j].Signal })
	return nets
}

// StartHotspot (re)creates and starts the setup hotspot.
func (nm *NetworkManager) StartHotspot(ssid, password string) error {
	nm.delete(HotspotCon)
	if _, err := nm.nmcli(60*time.Second, "device", "wifi", "hotspot", "ifname", Iface, "con-name", HotspotCon,
		"ssid", ssid, "password", password); err != nil {
		return err
	}
	// Only ever started by us, never at boot.
	_, err := nm.nmcli(30*time.Second, "connection", "modify", HotspotCon, "connection.autoconnect", "no")
	return err
}

// StopHotspot takes the hotspot down (no error if it wasn't up).
func (nm *NetworkManager) StopHotspot() {
	_, _ = nm.nmcli(30*time.Second, "connection", "down", HotspotCon)
}

// SaveNetwork stores a Wi-Fi network for autoconnect and returns the
// connection name. It replaces any saved connection for the same SSID, so
// saving a network again is how its password is changed.
func (nm *NetworkManager) SaveNetwork(ssid, password string, hidden bool, priority int) (string, error) {
	name := "wifi-" + ssid
	old, err := nm.ConnectionsFor(ssid)
	if err != nil {
		return "", err
	}
	nm.delete(name)
	for _, o := range old {
		if o != name {
			nm.delete(o)
		}
	}
	args := []string{"connection", "add", "type", "wifi", "con-name", name, "ifname", Iface,
		"ssid", ssid, "connection.autoconnect", "yes",
		"connection.autoconnect-priority", strconv.Itoa(priority)}
	if password != "" {
		args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password)
	}
	if hidden {
		args = append(args, "802-11-wireless.hidden", "yes")
	}
	_, err = nm.nmcli(30*time.Second, args...)
	return name, err
}

// SavedNetworks lists saved Wi-Fi networks (not our hotspot), highest
// priority first.
func (nm *NetworkManager) SavedNetworks() ([]Saved, error) {
	out, err := nm.nmcli(30*time.Second, "-t", "-f", "NAME,TYPE,AUTOCONNECT-PRIORITY,ACTIVE", "connection", "show")
	if err != nil {
		return nil, err
	}
	nets := []Saved{}
	for _, line := range lines(out) {
		f := SplitTerse(line)
		if len(f) < 4 || (f[1] != "802-11-wireless" && f[1] != "wifi") || f[0] == HotspotCon {
			continue
		}
		ssidOut, err := nm.nmcli(30*time.Second, "-t", "-g", "802-11-wireless.ssid", "connection", "show", f[0])
		if err != nil {
			return nil, err
		}
		ssid := strings.TrimSpace(ssidOut)
		if ssid == "" {
			ssid = f[0]
		}
		prio, _ := strconv.Atoi(f[2])
		nets = append(nets, Saved{SSID: ssid, Connection: f[0], Priority: prio, Active: f[3] == "yes"})
	}
	sort.SliceStable(nets, func(i, j int) bool {
		if nets[i].Priority != nets[j].Priority {
			return nets[i].Priority > nets[j].Priority
		}
		return nets[i].SSID < nets[j].SSID
	})
	return nets, nil
}

// ConnectionsFor lists the saved connection names for ssid.
func (nm *NetworkManager) ConnectionsFor(ssid string) ([]string, error) {
	saved, err := nm.SavedNetworks()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range saved {
		if n.SSID == ssid {
			names = append(names, n.Connection)
		}
	}
	return names, nil
}

// CurrentNetwork is the Wi-Fi network the Pi is on now (nil if none, or
// only our hotspot).
func (nm *NetworkManager) CurrentNetwork() (*Current, error) {
	devs, err := nm.Devices()
	if err != nil {
		return nil, err
	}
	var dev *Device
	for i := range devs {
		if devs[i].Device == Iface {
			dev = &devs[i]
			break
		}
	}
	if dev == nil || dev.State != "connected" || dev.Connection == HotspotCon {
		return nil, nil
	}
	info := &Current{Connection: dev.Connection}
	out, err := nm.nmcli(30*time.Second, "-t", "-f", "ACTIVE,SSID,SIGNAL", "device", "wifi", "list", "--rescan", "no")
	if err == nil {
		for _, line := range lines(out) {
			if f := SplitTerse(line); len(f) >= 3 && f[0] == "yes" {
				info.SSID = f[1]
				if s, err := strconv.Atoi(f[2]); err == nil {
					info.Signal = &s
				}
				break
			}
		}
		if addr, err := nm.nmcli(30*time.Second, "-t", "-g", "IP4.ADDRESS", "device", "show", Iface); err == nil {
			first := strings.SplitN(strings.TrimSpace(addr), "|", 2)[0]
			info.IP = strings.SplitN(first, "/", 2)[0]
		}
	} else {
		slog.Debug("couldn't read Wi-Fi details", "err", err)
	}
	return info, nil
}

// Forget deletes every saved connection for ssid and returns how many.
func (nm *NetworkManager) Forget(ssid string) (int, error) {
	names, err := nm.ConnectionsFor(ssid)
	if err != nil {
		return 0, err
	}
	for _, n := range names {
		if _, err := nm.nmcli(30*time.Second, "connection", "delete", n); err != nil {
			return 0, err
		}
	}
	return len(names), nil
}

// Connect brings a saved connection up, waiting up to wait seconds.
func (nm *NetworkManager) Connect(name string, wait int) bool {
	_, err := nm.nmcli(time.Duration(wait+15)*time.Second, "--wait", strconv.Itoa(wait), "connection", "up", name)
	if err != nil {
		slog.Warn("couldn't connect", "connection", name, "err", err)
		return false
	}
	return true
}

func (nm *NetworkManager) delete(name string) {
	_, _ = nm.nmcli(30*time.Second, "connection", "delete", name)
}
