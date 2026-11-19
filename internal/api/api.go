// Package api is the agent's local HTTP API. See docs/API.md for the
// contract.
package api

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
	"github.com/RCRaceHub/EventScreenDisplay/internal/player"
	"github.com/RCRaceHub/EventScreenDisplay/internal/playlist"
	"github.com/RCRaceHub/EventScreenDisplay/internal/stage"
	"github.com/RCRaceHub/EventScreenDisplay/internal/version"
)

// Prefix is where the versioned API lives.
const Prefix = "/api/v" + version.API

// MaxBody is the largest request body accepted.
const MaxBody = 256 * 1024

var (
	//go:embed pages/idle.html
	idlePage string
	//go:embed pages/stage.html
	stagePage string
	//go:embed pages/message.html
	messagePage string
)

// Error is an API error with its HTTP status.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func apiErr(status int, format string, args ...any) *Error {
	return &Error{status, fmt.Sprintf(format, args...)}
}

// WifiCaller forwards /api/v1/wifi calls to the Wi-Fi service. It returns
// the service's status code and JSON body.
type WifiCaller interface {
	Call(method, path string, body jsonx.Object) (int, json.RawMessage, error)
}

// Agent is the glue between the HTTP handlers and the player.
type Agent struct {
	Cfg      config.Config
	Player   *player.Player
	Stage    *stage.Stage // nil when preload is off
	Messages *messages.Messages
	Wifi     WifiCaller
	bootedAt time.Time
}

// NewAgent makes an Agent. stg may be nil; wifi nil means the real
// eventscreen-wifi control socket.
func NewAgent(cfg config.Config, p *player.Player, stg *stage.Stage, wifi WifiCaller) *Agent {
	msgs := p.Messages()
	if msgs == nil {
		msgs = messages.New("", nil)
	}
	if wifi == nil {
		wifi = NewWifiClient(DefaultControlSocket)
	}
	return &Agent{Cfg: cfg, Player: p, Stage: stg, Messages: msgs, Wifi: wifi, bootedAt: time.Now()}
}

// Limits is part of Info.
type Limits struct {
	DefaultDuration int `json:"default_duration"`
	MinDuration     int `json:"min_duration"`
}

// Info is GET /api/v1/info.
type Info struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	Version      string   `json:"version"`
	APIVersion   string   `json:"api_version"`
	Browser      string   `json:"browser"`
	AuthRequired bool     `json:"auth_required"`
	Features     []string `json:"features"`
	Limits       Limits   `json:"limits"`
}

// Info describes the screen.
func (a *Agent) Info() Info {
	host, _ := os.Hostname()
	return Info{
		ID: a.Cfg.ScreenID, Name: a.Cfg.Name, Hostname: host,
		Version: version.Version, APIVersion: version.API, Browser: a.Cfg.Browser,
		AuthRequired: a.Cfg.APIToken != "",
		// Lets a manager tell which endpoint groups a screen has.
		Features: []string{"playlist", "control", "wifi", "message", "banner"},
		Limits:   Limits{a.Cfg.DefaultDuration, a.Cfg.MinDuration},
	}
}

// BannerStatus is the banner with whether it can be seen.
type BannerStatus struct {
	messages.Banner
	Visible bool `json:"visible"`
}

// Status is GET /api/v1/status.
type Status struct {
	ID     string  `json:"id"`
	Uptime float64 `json:"uptime"`
	Time   float64 `json:"time"`
	player.Status
	Browser browser.Status    `json:"browser"`
	Message *messages.Message `json:"message"`
	Banner  *BannerStatus     `json:"banner"`
}

// Status reports what the screen is doing.
func (a *Agent) Status() Status {
	return Status{
		ID:      a.Cfg.ScreenID,
		Uptime:  float64(time.Since(a.bootedAt).Round(100*time.Millisecond)) / float64(time.Second),
		Time:    messages.Now(),
		Status:  a.Player.Status(),
		Browser: a.Player.Browser().Status(),
		Message: a.Messages.Message(),
		Banner:  a.BannerStatus(),
	}
}

// BannerStatus is the banner, if any, and whether it's on screen. A banner
// is drawn by the stage page, so it only shows while the screen is on the
// stage (preload on, page allows framing).
func (a *Agent) BannerStatus() *BannerStatus {
	b := a.Messages.Banner()
	if b == nil {
		return nil
	}
	visible := false
	if sb, ok := a.Player.Browser().(*stage.Browser); ok && a.Stage != nil {
		visible = sb.OnStage()
	}
	return &BannerStatus{*b, visible}
}

// Handler returns the API's http.Handler.
func (a *Agent) Handler() http.Handler { return &handler{a} }

type handler struct{ a *Agent }

type route func(w http.ResponseWriter, r *http.Request) error

func (h *handler) routes() map[string]map[string]route {
	a := h.a
	ctl := func(action func(body jsonx.Object) error) route {
		return func(w http.ResponseWriter, r *http.Request) error {
			body := jsonx.Object{}
			if r.ContentLength > 0 {
				var err error
				if body, err = readBody(r); err != nil {
					return err
				}
			}
			if err := action(body); err != nil {
				return err
			}
			return writeJSON(w, http.StatusOK, a.Status(), nil)
		}
	}
	simple := func(fn func()) func(jsonx.Object) error {
		return func(jsonx.Object) error { fn(); return nil }
	}
	message := func(w http.ResponseWriter) error {
		return writeJSON(w, 200, map[string]any{"message": a.Messages.Message()}, nil)
	}
	banner := func(w http.ResponseWriter) error {
		return writeJSON(w, 200, map[string]any{"banner": a.BannerStatus()}, nil)
	}
	setMessage := func(w http.ResponseWriter, r *http.Request) error {
		body, err := readBody(r)
		if err != nil {
			return err
		}
		if _, err := a.Messages.SetMessage(body); err != nil {
			return apiErr(400, "%s", err)
		}
		return message(w)
	}
	setBanner := func(w http.ResponseWriter, r *http.Request) error {
		body, err := readBody(r)
		if err != nil {
			return err
		}
		if _, err := a.Messages.SetBanner(body); err != nil {
			return apiErr(400, "%s", err)
		}
		return banner(w)
	}
	wifi := func(method, path string, withBody bool) route {
		return func(w http.ResponseWriter, r *http.Request) error {
			var body jsonx.Object
			if withBody {
				var err error
				if body, err = readBody(r); err != nil {
					return err
				}
			}
			return h.wifi(w, method, path, body)
		}
	}
	return map[string]map[string]route{
		"/":                {"GET": h.idlePage},
		"/idle":            {"GET": h.idlePage},
		"/stage":           {"GET": h.stagePage},
		"/stage/state":     {"GET": h.stageState},
		"/message":         {"GET": func(w http.ResponseWriter, r *http.Request) error { return writeHTML(w, messagePage, true) }},
		Prefix + "/info":   {"GET": func(w http.ResponseWriter, r *http.Request) error { return writeJSON(w, 200, a.Info(), nil) }},
		Prefix + "/status": {"GET": func(w http.ResponseWriter, r *http.Request) error { return writeJSON(w, 200, a.Status(), nil) }},
		Prefix + "/playlist": {
			"GET":    h.getPlaylist,
			"PUT":    h.putPlaylist,
			"DELETE": h.deletePlaylist,
		},
		Prefix + "/control/next":     {"POST": ctl(simple(a.Player.Next))},
		Prefix + "/control/previous": {"POST": ctl(simple(a.Player.Previous))},
		Prefix + "/control/pause":    {"POST": ctl(simple(a.Player.Pause))},
		Prefix + "/control/resume":   {"POST": ctl(simple(a.Player.Resume))},
		Prefix + "/control/reload":   {"POST": ctl(simple(a.Player.Reload))},
		Prefix + "/control/goto":     {"POST": ctl(h.gotoIndex)},
		Prefix + "/control/show": {
			"POST":   ctl(h.show),
			"DELETE": ctl(simple(a.Player.ClearOverride)),
		},
		Prefix + "/message": {
			"GET":    func(w http.ResponseWriter, r *http.Request) error { return message(w) },
			"PUT":    setMessage,
			"POST":   setMessage,
			"DELETE": func(w http.ResponseWriter, r *http.Request) error { a.Messages.ClearMessage(); return message(w) },
		},
		Prefix + "/banner": {
			"GET":    func(w http.ResponseWriter, r *http.Request) error { return banner(w) },
			"PUT":    setBanner,
			"POST":   setBanner,
			"DELETE": func(w http.ResponseWriter, r *http.Request) error { a.Messages.ClearBanner(); return banner(w) },
		},
		Prefix + "/wifi":          {"GET": wifi("GET", "/wifi", false)},
		Prefix + "/wifi/scan":     {"GET": wifi("GET", "/wifi/scan", false)},
		Prefix + "/wifi/networks": {"POST": wifi("POST", "/wifi/networks", true)},
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "eventscreen/"+version.Version)
	if r.Method == http.MethodOptions {
		cors(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw := r.URL.EscapedPath()
	path := strings.TrimRight(raw, "/")
	if path == "" {
		path = "/"
	}
	routes := h.routes()
	networks := Prefix + "/wifi/networks/"
	if r.Method == "DELETE" && strings.HasPrefix(path, networks) && len(path) > len(networks) {
		// The SSID stays percent-encoded on its way to the Wi-Fi service.
		ssid := path[len(networks):]
		routes[path] = map[string]route{"DELETE": func(w http.ResponseWriter, r *http.Request) error {
			return h.wifi(w, "DELETE", "/wifi/networks/"+ssid, nil)
		}}
	}
	err := h.dispatch(w, r, routes, path)
	var ae *Error
	switch {
	case err == nil:
	case errors.As(err, &ae):
		_ = writeJSON(w, ae.Status, map[string]string{"error": ae.Message}, nil)
	default:
		slog.Error("error handling request", "method", r.Method, "path", r.URL.Path, "err", err)
		_ = writeJSON(w, 500, map[string]string{"error": "internal error"}, nil)
	}
}

func (h *handler) dispatch(w http.ResponseWriter, r *http.Request, routes map[string]map[string]route, path string) error {
	methods, ok := routes[path]
	if !ok {
		return apiErr(404, "not found")
	}
	fn, ok := methods[r.Method]
	if !ok {
		return apiErr(405, "method not allowed")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if err := h.checkAuth(r); err != nil {
			return err
		}
		if strings.HasPrefix(path, Prefix+"/wifi") && h.a.Cfg.APIToken == "" {
			// Changing the Wi-Fi can take a screen off the network for
			// good, so it's never open to anyone on the LAN.
			return apiErr(403, "set api_token on the screen to change Wi-Fi over the API "+
				"(sudo eventscreen-setup --token ...)")
		}
	}
	return fn(w, r)
}

func (h *handler) checkAuth(r *http.Request) error {
	token := h.a.Cfg.APIToken
	if token == "" {
		return nil
	}
	given := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(given), []byte("Bearer "+token)) != 1 {
		return apiErr(401, "missing or invalid bearer token")
	}
	return nil
}

// --- endpoints -----------------------------------------------------------

func etag(pl playlist.Playlist) map[string]string {
	return map[string]string{"ETag": fmt.Sprintf(`"%d"`, pl.Revision)}
}

func (h *handler) getPlaylist(w http.ResponseWriter, r *http.Request) error {
	pl := h.a.Player.Playlist()
	return writeJSON(w, 200, pl, etag(pl))
}

func (h *handler) putPlaylist(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	cfg := h.a.Cfg
	current := h.a.Player.Playlist()
	// Optimistic concurrency: a manager can send If-Match with the revision
	// it last saw so two managers don't silently clobber each other.
	if m := r.Header.Get("If-Match"); m != "" && strings.Trim(m, `"`) != strconv.Itoa(current.Revision) {
		return apiErr(412, "playlist revision changed")
	}
	def, err := playlist.ParseDuration(body["default_duration"], cfg.DefaultDuration, cfg.MinDuration)
	if err != nil {
		return apiErr(400, "%s", err)
	}
	items, err := playlist.ParseItems(body["items"], def, cfg.MinDuration)
	if err != nil {
		return apiErr(400, "%s", err)
	}
	start := int64(0)
	if v, ok := body["start_index"]; ok {
		if start, ok = jsonx.Int(v); !ok {
			return apiErr(400, "start_index must be an integer")
		}
	}
	source := ""
	if v, ok := body["source"]; ok {
		source = jsonx.Str(v)
	}
	pl := playlist.Replaced(current, items, source)
	if err := h.a.Player.SetPlaylist(pl, int(start)); err != nil {
		return err
	}
	return writeJSON(w, 200, pl, etag(pl))
}

func (h *handler) deletePlaylist(w http.ResponseWriter, r *http.Request) error {
	pl := playlist.Replaced(h.a.Player.Playlist(), nil, "")
	if err := h.a.Player.SetPlaylist(pl, 0); err != nil {
		return err
	}
	return writeJSON(w, 200, pl, etag(pl))
}

func (h *handler) gotoIndex(body jsonx.Object) error {
	i, ok := jsonx.Int(body["index"])
	if !ok {
		return apiErr(400, "index must be an integer")
	}
	if err := h.a.Player.Goto(int(i)); err != nil {
		return apiErr(400, "index out of range")
	}
	return nil
}

func (h *handler) show(body jsonx.Object) error {
	u, err := playlist.ValidateURL(body["url"])
	if err != nil {
		return apiErr(400, "%s", err)
	}
	duration := 0
	if body["duration"] != nil {
		if duration, err = playlist.ParseDuration(body["duration"], 0, h.a.Cfg.MinDuration); err != nil {
			return apiErr(400, "%s", err)
		}
	}
	above := false
	if v, ok := body["above_messages"]; ok {
		if above, ok = v.(bool); !ok {
			return apiErr(400, "above_messages must be true or false")
		}
	}
	label := ""
	if v, ok := body["label"]; ok {
		label = jsonx.Str(v)
	}
	h.a.Player.Show(playlist.Item{URL: u, Duration: duration, Label: label}, duration, above)
	return nil
}

func (h *handler) wifi(w http.ResponseWriter, method, path string, body jsonx.Object) error {
	status, data, err := h.a.Wifi.Call(method, path, body)
	if err != nil {
		return err
	}
	return writeJSON(w, status, data, nil)
}

func (h *handler) stagePage(w http.ResponseWriter, r *http.Request) error {
	if h.a.Stage == nil {
		return apiErr(404, "preload is off")
	}
	return writeHTML(w, stagePage, true)
}

func (h *handler) stageState(w http.ResponseWriter, r *http.Request) error {
	if h.a.Stage == nil {
		return apiErr(404, "preload is off")
	}
	since, err := strconv.Atoi(r.URL.Query().Get("since"))
	if err != nil {
		since = -1
	}
	return writeJSON(w, 200, h.a.Stage.Wait(since, 25*time.Second), nil)
}

// idlePage is shown when there is no playlist, so whoever is setting up the
// screen can see which one it is and where to reach it.
func (h *handler) idlePage(w http.ResponseWriter, r *http.Request) error {
	info := h.a.Info()
	name := info.Name
	if name == "" {
		name = info.ID
	}
	page := strings.NewReplacer(
		"{{name}}", html.EscapeString(name),
		"{{host}}", html.EscapeString(info.Hostname),
		"{{port}}", strconv.Itoa(h.a.Cfg.Port),
		"{{version}}", version.Version,
	).Replace(idlePage)
	return writeHTML(w, page, false)
}

// --- helpers -------------------------------------------------------------

func readBody(r *http.Request) (jsonx.Object, error) {
	if r.ContentLength > MaxBody {
		return nil, apiErr(413, "body too large")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, apiErr(400, "bad body")
	}
	if len(data) > MaxBody {
		return nil, apiErr(413, "body too large")
	}
	obj, err := jsonx.Decode(data)
	if err != nil {
		return nil, apiErr(400, "%s", err)
	}
	return obj, nil
}

// cors lets a browser-based manager UI talk to screens directly.
func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match")
	h.Set("Access-Control-Expose-Headers", "ETag")
}

func writeJSON(w http.ResponseWriter, status int, v any, headers map[string]string) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	for k, v := range headers {
		h.Set(k, v)
	}
	cors(w)
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

func writeHTML(w http.ResponseWriter, page string, noStore bool) error {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	if noStore {
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(200)
	_, err := io.WriteString(w, page)
	return err
}
