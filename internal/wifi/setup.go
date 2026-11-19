package wifi

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SetupHandler serves the setup page for phones, and the instructions
// page for the TV, while the hotspot is up.
func SetupHandler(fb *Fallback) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			fb.Activity()
			data, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			form, _ := url.ParseQuery(string(data))
			ssid := strings.TrimSpace(form.Get("other"))
			if ssid == "" {
				ssid = strings.TrimSpace(form.Get("ssid"))
			}
			password := form.Get("password")
			_, hidden := form["hidden"]
			if msg := Validate(ssid, password); msg != "" {
				fb.setMessage(msg)
				page(w, SetupPage(fb))
				return
			}
			// Joining drops the hotspot (and this phone), so answer first.
			go fb.Choose(ssid, password, hidden)
			page(w, JoiningPage(ssid, fb.ssid))
		default:
			switch r.URL.Path {
			case "/screen":
				page(w, ScreenPage(fb))
				return
			case "/":
				fb.Activity()
				page(w, SetupPage(fb))
				return
			}
			fb.Activity()
			// Anything else (phones' captive portal checks included) goes to
			// setup.
			w.Header().Set("Location", SetupURL)
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusFound)
		}
	})
}

func page(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, body)
}

// SetupServer runs the setup page on the hotspot address while it's up.
type SetupServer struct {
	fb   *Fallback
	addr string
	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
}

// NewSetupServer serves fb's pages on addr (host:port).
func NewSetupServer(fb *Fallback, addr string) *SetupServer {
	return &SetupServer{fb: fb, addr: addr}
}

// Start listens, retrying while the hotspot address appears.
func (s *SetupServer) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return
	}
	var ln net.Listener
	var err error
	for range 20 {
		if ln, err = net.Listen("tcp", s.addr); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		slog.Error("couldn't listen for the setup page", "addr", s.addr, "err", err)
		return
	}
	s.ln = ln
	s.srv = &http.Server{Handler: SetupHandler(s.fb), ReadHeaderTimeout: 10 * time.Second}
	go func(srv *http.Server) { _ = srv.Serve(ln) }(s.srv)
}

// Addr is the address being served ("" when stopped).
func (s *SetupServer) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Stop stops serving.
func (s *SetupServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.srv.Shutdown(ctx)
		cancel()
		s.srv = nil
	}
}

const style = `<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body{font-family:system-ui,sans-serif;margin:0;padding:16px;max-width:32rem;margin:auto;line-height:1.4;color:#111;background:#fff}
h1{font-size:1.4rem} label{display:block;margin:.6rem 0 .2rem}
input[type=text],input[type=password],select{width:100%;font-size:1rem;padding:.6rem;box-sizing:border-box}
button{font-size:1.1rem;padding:.7rem 1.2rem;margin-top:1rem;width:100%}
.err{background:#fde8e8;color:#7a1010;padding:.6rem;border-radius:4px}
.muted{color:#555;font-size:.9rem}
@media (prefers-color-scheme: dark){body{color:#eee;background:#151515}.err{background:#4a1515;color:#fdd}.muted{color:#aaa}}
</style>`

var e = html.EscapeString

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// SetupPage is the form a phone fills in.
func SetupPage(fb *Fallback) string {
	var options strings.Builder
	for _, n := range fb.Networks() {
		open := ""
		if !n.Secure {
			open = ", open"
		}
		fmt.Fprintf(&options, `<option value="%s">%s (%d%%%s)</option>`, e(n.SSID), e(n.SSID), n.Signal, open)
	}
	opts := options.String()
	if opts == "" {
		opts = `<option value="">(none found, type it below)</option>`
	}
	errHTML := ""
	if m := fb.Message(); m != "" {
		errHTML = `<p class="err">` + e(m) + `</p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Screen Wi-Fi setup</title>` + style + `</head>
<body><h1>Wi-Fi for ` + e(hostname()) + `</h1>` + errHTML + `
<form method="post">
<label for="ssid">Network</label>
<select id="ssid" name="ssid">` + opts + `</select>
<label for="other">Or type a network name</label>
<input id="other" name="other" type="text" autocomplete="off" autocapitalize="none">
<label><input type="checkbox" name="hidden"> Hidden network</label>
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="off">
<button type="submit">Connect</button>
</form>
<p class="muted">The screen leaves this setup hotspot to try the network. If it can't connect,
the hotspot comes back in about a minute so you can try again.</p></body></html>`
}

// JoiningPage is the answer to the form.
func JoiningPage(ssid, hotspot string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Connecting</title>` + style + `</head>
<body><h1>Connecting to ` + e(ssid) + `</h1>
<p>Your phone will drop off this hotspot now. If the screen goes back to its pages, it worked.</p>
<p>If it can't connect, <b>` + e(hotspot) + `</b> comes back within about a minute: join it again to retry.</p>
</body></html>`
}

// ScreenPage is shown full-screen on the TV while in setup mode.
func ScreenPage(fb *Fallback) string {
	status := "No known Wi-Fi network found."
	if c := fb.Connecting(); c != "" {
		status = "Connecting to " + e(c) + "…"
	} else if m := fb.Message(); m != "" {
		status = e(m)
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta http-equiv="refresh" content="5">
<title>Wi-Fi setup</title><style>
html,body{margin:0;height:100%;background:#111;color:#eee;font-family:sans-serif}
body{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:1.2vw;text-align:center}
h1{font-size:4.5vw;margin:0 0 1vw} p{font-size:2.4vw;margin:0;color:#bbb} b{color:#fff;font-family:monospace}
</style></head><body>
<h1>Wi-Fi setup for ` + e(hostname()) + `</h1>
<p>` + status + `</p>
<p>1. On a phone, join Wi-Fi <b>` + e(fb.ssid) + `</b> password <b>` + e(fb.password) + `</b></p>
<p>2. The setup page opens by itself, or go to <b>` + SetupURL + `</b></p>
<p style="font-size:1.6vw">Retrying known networks every few minutes.</p>
</body></html>`
}
