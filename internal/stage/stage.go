// Package stage makes page switches smooth, with no blank gap.
//
// Instead of navigating the kiosk browser to each page, the browser stays
// on one local "stage" page (served by the agent at /stage) holding two
// full-screen iframes. The page that's showing lives in one; shortly before
// a switch the player asks for the next page to be loaded in the other,
// hidden one, and at switch time the stage just swaps which frame is
// visible. Only two pages are ever loaded, so memory stays bounded on a Pi
// Zero 2 W, and the old frame is blanked once the swap is done.
//
// Pages that refuse to be framed (X-Frame-Options, or a CSP
// frame-ancestors rule) are shown the old way, by navigating the browser to
// them directly.
//
// The stage page learns what to show by long-polling /stage/state.
package stage

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
)

// FramableTTL is how long a framing check result is trusted.
const FramableTTL = 600 * time.Second

// Framable reports whether url can be shown in an iframe. ok is false if
// it couldn't be checked (the server didn't answer).
func Framable(url string, timeout time.Duration) (framable, ok bool) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("User-Agent", "eventscreen-framecheck")
	client := http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, false
	}
	resp.Body.Close()
	return FramableHeaders(resp.Header), true
}

// FramableHeaders applies the framing rules to a response's headers.
func FramableHeaders(h http.Header) bool {
	xfo := strings.ToLower(strings.TrimSpace(h.Get("X-Frame-Options")))
	if xfo == "deny" || xfo == "sameorigin" || strings.HasPrefix(xfo, "allow-from") {
		return false
	}
	for _, csp := range h.Values("Content-Security-Policy") {
		for _, directive := range strings.Split(csp, ";") {
			parts := strings.Fields(directive)
			if len(parts) == 0 || strings.ToLower(parts[0]) != "frame-ancestors" {
				continue
			}
			any := false
			for _, p := range parts[1:] {
				if strings.ToLower(strings.Trim(p, "'")) == "*" {
					any = true
				}
			}
			if !any {
				return false
			}
		}
	}
	return true
}

// State is what GET /stage/state returns. Now is Unix seconds, so the page
// can time a banner's end against the agent's clock.
type State struct {
	Seq     int              `json:"seq"`
	Active  *string          `json:"active"`
	Next    *string          `json:"next"`
	Reloads int              `json:"reloads"`
	Banner  *messages.Banner `json:"banner"`
	Now     float64          `json:"now"`
}

// Stage is what the stage page should show. Safe for concurrent use.
type Stage struct {
	mu       sync.Mutex
	changed  chan struct{} // closed and replaced on every change
	seq      int
	active   string
	next     string
	reloads  int
	messages *messages.Messages
}

// New makes a stage. msgs (may be nil) supplies the banner drawn over the
// frames.
func New(msgs *messages.Messages) *Stage {
	s := &Stage{changed: make(chan struct{}), messages: msgs}
	if msgs != nil {
		msgs.OnChange(func() {
			s.mu.Lock()
			s.bump()
			s.mu.Unlock()
		})
	}
	return s
}

func (s *Stage) bump() {
	s.seq++
	close(s.changed)
	s.changed = make(chan struct{})
}

// Show makes url the visible page.
func (s *Stage) Show(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = url
	if s.next == url {
		s.next = ""
	}
	s.bump()
}

// Preload asks for url to be loaded in the hidden frame ("" = nothing).
func (s *Stage) Preload(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if url == s.active {
		url = ""
	}
	if url != s.next {
		s.next = url
		s.bump()
	}
}

// Reload asks the stage page to reload the visible frame.
func (s *Stage) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloads++
	s.bump()
}

// Active is the visible page ("" = none).
func (s *Stage) Active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Next is the page being preloaded ("" = none).
func (s *Stage) Next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Reloads is how many reloads have been asked for.
func (s *Stage) Reloads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloads
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// State is the current state.
func (s *Stage) State() State {
	s.mu.Lock()
	st := State{Seq: s.seq, Active: opt(s.active), Next: opt(s.next), Reloads: s.reloads}
	s.mu.Unlock()
	if s.messages != nil {
		st.Banner = s.messages.Banner()
	}
	st.Now = messages.Now()
	return st
}

// Wait returns the state once seq has moved past since, or after timeout.
func (s *Stage) Wait(since int, timeout time.Duration) State {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		seq, ch := s.seq, s.changed
		s.mu.Unlock()
		if seq > since {
			break
		}
		select {
		case <-ch:
			continue
		case <-deadline.C:
		}
		break
	}
	return s.State()
}

// Checker reports whether a page can be framed (ok=false: couldn't tell).
type Checker func(url string) (framable, ok bool)

// DefaultChecker checks with a GET and a 4 second timeout.
func DefaultChecker(url string) (bool, bool) { return Framable(url, 4*time.Second) }

type cached struct {
	ok bool
	at time.Time
}

// Browser wraps a kiosk browser so pages are shown on the stage when they
// can be. The player uses it like any browser.Browser, plus Preload.
type Browser struct {
	inner    browser.Browser
	stage    *Stage
	stageURL string
	check    Checker
	now      func() time.Time

	mu         sync.Mutex
	currentURL string
	cache      map[string]cached
}

// NewBrowser wraps inner. stageURL is the agent's /stage page.
func NewBrowser(inner browser.Browser, s *Stage, stageURL string, check Checker) *Browser {
	if check == nil {
		check = DefaultChecker
	}
	return &Browser{inner: inner, stage: s, stageURL: stageURL, check: check, now: time.Now,
		cache: map[string]cached{}}
}

// Inner is the wrapped browser.
func (b *Browser) Inner() browser.Browser { return b.inner }

func (b *Browser) framable(url string) bool {
	if own := b.stageURL[:strings.LastIndex(b.stageURL, "/")+1]; strings.HasPrefix(url, own) {
		return true // our own pages (idle, message)
	}
	b.mu.Lock()
	c, hit := b.cache[url]
	b.mu.Unlock()
	now := b.now()
	if hit && now.Sub(c.at) < FramableTTL {
		return c.ok
	}
	ok, checked := b.check(url)
	if !checked {
		// Unreachable right now: frame it, and check again next time.
		return true
	}
	if !ok {
		slog.Info("page refuses to be framed; showing it directly", "url", url)
	}
	b.mu.Lock()
	b.cache[url] = cached{ok, now}
	b.mu.Unlock()
	return ok
}

// OnStage reports whether the browser is showing the stage page.
func (b *Browser) OnStage() bool { return b.inner.CurrentURL() == b.stageURL }

// Open implements browser.Browser.
func (b *Browser) Open(url string) error {
	var err error
	if b.framable(url) {
		b.stage.Show(url)
		if !b.OnStage() || !b.inner.Alive() {
			err = b.inner.Open(b.stageURL)
		}
	} else {
		b.stage.Preload("")
		err = b.inner.Open(url)
	}
	b.mu.Lock()
	b.currentURL = url
	b.mu.Unlock()
	return err
}

// Preload implements browser.Preloader.
func (b *Browser) Preload(url string) error {
	if url != "" && b.OnStage() && b.framable(url) {
		b.stage.Preload(url)
	}
	return nil
}

// Reload implements browser.Browser.
func (b *Browser) Reload() error {
	if b.OnStage() {
		b.stage.Reload()
		return nil
	}
	return b.inner.Reload()
}

func (b *Browser) EnsureRunning() error  { return b.inner.EnsureRunning() }
func (b *Browser) Alive() bool           { return b.inner.Alive() }
func (b *Browser) Stop()                 { b.inner.Stop() }
func (b *Browser) SetLastError(m string) { b.inner.SetLastError(m) }

// CurrentURL is the page that's showing, on the stage or not.
func (b *Browser) CurrentURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentURL
}

// Status is the inner browser's status with the page that's really showing.
func (b *Browser) Status() browser.Status {
	st := b.inner.Status()
	st.URL = opt(b.CurrentURL())
	on := b.OnStage()
	st.Preload = &on
	return st
}
