package wifi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
)

// ControlSocket is where the agent reaches the Wi-Fi API. Only root and the
// eventscreen group can open it.
const ControlSocket = "/run/eventscreen-wifi/control.sock"

// ControlError is an error with the HTTP status the API should answer.
type ControlError struct {
	Status  int
	Message string
}

func (e *ControlError) Error() string { return e.Message }

// Change is the outcome of the last API Wi-Fi switch.
type Change struct {
	SSID    string  `json:"ssid"`
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	At      float64 `json:"at"`
}

// Control is what the API can ask for: status, scan, add and forget
// networks.
type Control struct {
	nm       NM
	fallback *Fallback
	// Clock gives Unix seconds for LastChange. Tests swap it.
	Clock func() float64

	mu         sync.Mutex
	lastChange *Change
}

// LastChange is the outcome of the last background switch (nil = none yet).
func (c *Control) LastChange() *Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastChange
}

// NewControl wraps the network manager and the fallback.
func NewControl(nm NM, fb *Fallback) *Control {
	return &Control{nm: nm, fallback: fb, Clock: func() float64 { return float64(time.Now().UnixMicro()) / 1e6 }}
}

type savedOut struct {
	SSID     string `json:"ssid"`
	Priority int    `json:"priority"`
	Active   bool   `json:"active"`
}

// Status is GET /api/v1/wifi.
func (c *Control) Status() (any, error) {
	fb := c.fallback
	state := fb.State()
	var current *Current
	if state != "hotspot" {
		var err error
		if current, err = c.nm.CurrentNetwork(); err != nil {
			return nil, err
		}
	}
	saved, err := c.nm.SavedNetworks()
	if err != nil {
		return nil, err
	}
	out := make([]savedOut, 0, len(saved))
	for _, n := range saved {
		out = append(out, savedOut{n.SSID, n.Priority, n.Active})
	}
	var connecting *string
	if s := fb.Connecting(); s != "" {
		connecting = &s
	}
	c.mu.Lock()
	last := c.lastChange
	c.mu.Unlock()
	return map[string]any{
		"mode":      state,
		"connected": current,
		"saved":     out,
		"hotspot": map[string]any{
			"ssid":    fb.ssid,
			"enabled": fb.cfg.WifiFallback,
			"after":   fb.cfg.WifiFallbackAfter,
		},
		"connecting":  connecting,
		"last_change": last,
	}, nil
}

// Scan is GET /api/v1/wifi/scan. In setup mode the radio is busy being a
// hotspot, so it gives the scan made just before the hotspot started.
func (c *Control) Scan() any {
	var nets []Network
	if c.fallback.State() == "hotspot" {
		nets = c.fallback.Networks()
	} else {
		nets = c.nm.Scan()
	}
	out := []Network{}
	for _, n := range nets {
		if n.SSID != c.fallback.ssid {
			out = append(out, n)
		}
	}
	return map[string]any{"networks": out}
}

func cerr(status int, format string, args ...any) error {
	return &ControlError{status, fmt.Sprintf(format, args...)}
}

// Add is POST /api/v1/wifi/networks. With connect it answers 202 straight
// away and switches in the background, because the caller may lose the
// screen if the new network is a different LAN.
func (c *Control) Add(body jsonx.Object) (int, any, error) {
	ssid, ok1 := body["ssid"].(string)
	password := ""
	ok2 := true
	if v := body["password"]; v != nil {
		if s, ok := v.(string); ok {
			password = s
		} else if b, isBool := v.(bool); isBool && !b {
			password = "" // Python's falsy "or ''"
		} else {
			ok2 = false
		}
	}
	if !ok1 || !ok2 {
		return 0, nil, cerr(400, "ssid and password must be strings")
	}
	hidden, connect := false, false
	for key, dst := range map[string]*bool{"hidden": &hidden, "connect": &connect} {
		if v, ok := body[key]; ok {
			b, isBool := v.(bool)
			if !isBool {
				return 0, nil, cerr(400, "hidden and connect must be true or false")
			}
			*dst = b
		}
	}
	priority := int64(0)
	if v, ok := body["priority"]; ok {
		p, isInt := jsonx.Int(v)
		if !isInt || p < -999 || p > 999 {
			return 0, nil, cerr(400, "priority must be an integer from -999 to 999")
		}
		priority = p
	}
	if msg := Validate(ssid, password); msg != "" {
		return 0, nil, cerr(400, "%s", msg)
	}
	if ssid == c.fallback.ssid {
		return 0, nil, cerr(400, "that's this screen's own setup hotspot")
	}
	if connect {
		go c.switchTo(ssid, password, hidden, int(priority))
		return 202, map[string]any{"ssid": ssid, "saved": true, "connecting": true}, nil
	}
	c.fallback.mu.Lock()
	_, err := c.nm.SaveNetwork(ssid, password, hidden, int(priority))
	c.fallback.mu.Unlock()
	if err != nil {
		return 0, nil, err
	}
	return 201, map[string]any{"ssid": ssid, "saved": true, "connecting": false}, nil
}

// Forget is DELETE /api/v1/wifi/networks/{ssid}. It refuses the network
// the screen is on, which would drop it off the network.
func (c *Control) Forget(ssid string) (any, error) {
	c.fallback.mu.Lock()
	defer c.fallback.mu.Unlock()
	saved, err := c.nm.SavedNetworks()
	if err != nil {
		return nil, err
	}
	found, active := false, false
	for _, n := range saved {
		if n.SSID == ssid {
			found = true
			active = active || n.Active
		}
	}
	if !found {
		return nil, cerr(404, "no saved network %s", pyRepr(ssid))
	}
	if active {
		return nil, cerr(409, "the screen is on %s now; connect it to another network first, or it would drop off the network", ssid)
	}
	if _, err := c.nm.Forget(ssid); err != nil {
		return nil, err
	}
	return map[string]any{"ssid": ssid, "forgotten": true}, nil
}

func (c *Control) switchTo(ssid, password string, hidden bool, priority int) {
	fb := c.fallback
	var ok bool
	var message string
	if fb.State() == "hotspot" {
		ok = fb.Choose(ssid, password, hidden)
		if !ok {
			message = fb.Message()
		}
	} else {
		fb.mu.Lock()
		fb.setConnecting(ssid)
		name, err := c.nm.SaveNetwork(ssid, password, hidden, priority)
		if err != nil {
			message = fmt.Sprintf("couldn't save %s: %v", ssid, err)
		} else if ok = c.nm.Connect(name, RetryWindow); !ok {
			// NetworkManager falls back to the other saved networks by
			// itself; the hotspot covers the rest.
			message = "couldn't join " + ssid
		}
		fb.setConnecting("")
		if ok {
			fb.markOnline(fb.clock())
		}
		fb.mu.Unlock()
	}
	c.mu.Lock()
	c.lastChange = &Change{SSID: ssid, OK: ok, Message: message, At: c.Clock()}
	c.mu.Unlock()
	if ok {
		slog.Info("API Wi-Fi change: joined " + ssid)
	} else {
		slog.Info("API Wi-Fi change to "+ssid+" failed", "message", message)
	}
}

// Handler serves the control API (paths without the /api/v1 prefix).
func (c *Control) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, data, err := c.route(r)
		if err != nil {
			if ce, ok := err.(*ControlError); ok {
				status, data = ce.Status, map[string]string{"error": ce.Message}
			} else {
				slog.Error("Wi-Fi control request failed", "err", err)
				status, data = 500, map[string]string{"error": "Wi-Fi change failed: " + err.Error()}
			}
		}
		body, _ := json.Marshal(data)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

func (c *Control) route(r *http.Request) (int, any, error) {
	path := r.URL.EscapedPath()
	notFound := cerr(404, "not found")
	switch r.Method {
	case "GET":
		switch path {
		case "/wifi":
			v, err := c.Status()
			return 200, v, err
		case "/wifi/scan":
			return 200, c.Scan(), nil
		}
		return 0, nil, notFound
	case "POST":
		if path != "/wifi/networks" {
			return 0, nil, notFound
		}
		data, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		body, err := jsonx.Decode(data)
		if err != nil {
			return 0, nil, cerr(400, "%s", err)
		}
		return c.Add(body)
	case "DELETE":
		const prefix = "/wifi/networks/"
		if !strings.HasPrefix(path, prefix) || len(path) == len(prefix) {
			return 0, nil, notFound
		}
		ssid, err := url.PathUnescape(path[len(prefix):])
		if err != nil {
			ssid = path[len(prefix):]
		}
		v, err := c.Forget(ssid)
		return 200, v, err
	}
	return 0, nil, cerr(501, "unsupported method")
}

// ServeControl listens on the Unix socket at path, which only root and
// group may open. Close the returned server to stop.
func ServeControl(c *Control, path, group string) (*http.Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if g, err := user.LookupGroup(group); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = os.Chown(path, -1, gid)
		}
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}
