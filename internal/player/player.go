// Package player decides what should be on screen and when to move on.
//
// One goroutine (run) owns the browser: it works out the target page
// (message, override, playlist item or idle page), opens it, preloads the
// next one, and keeps the browser alive. API handlers change the player's
// state under its lock and kick the loop. A second goroutine
// (watchNetwork) checks the page's server can be reached and reloads the
// page when the server comes back.
package player

import (
	"errors"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
	"github.com/RCRaceHub/EventScreenDisplay/internal/playlist"
)

// ErrIndex is returned by Goto for an index outside the playlist.
var ErrIndex = errors.New("index out of range")

// Reachable reports whether a TCP connection to url's host and port works.
func Reachable(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(u.Hostname(), port), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Options are the player's settings. Zero values get the defaults.
type Options struct {
	// Page shown when the playlist is empty.
	IdleURL string
	// Page that draws the full-screen message ("" = no messages).
	MessageURL string
	Messages   *messages.Messages
	// Seconds before a switch to start loading the next page out of sight,
	// if the browser can (stage.Browser).
	PreloadAhead float64
	// Checks a page's server can be reached. Default Reachable.
	Probe func(url string) bool
	// How often the loop checks the browser is alive (default 2 s) and how
	// often the page's server is probed (default 10 s).
	HealthInterval time.Duration
	ProbeInterval  time.Duration
}

type override struct {
	item  playlist.Item
	until *float64 // nil = until cleared
	// Shown even over a full-screen event message (the Wi-Fi setup screen).
	aboveMessages bool
}

type probeResult struct {
	url string
	ok  bool
	at  float64
}

// Current is the page on screen, in Status.
type Current struct {
	URL       string   `json:"url"`
	Label     string   `json:"label"`
	ID        string   `json:"id"`
	StartedAt float64  `json:"started_at"`
	EndsAt    *float64 `json:"ends_at"`
}

// OverrideStatus is the active "show", in Status.
type OverrideStatus struct {
	URL   string   `json:"url"`
	Until *float64 `json:"until"`
}

// Network is the reachability of the page on screen, in Status.
type Network struct {
	PageReachable *bool    `json:"page_reachable"`
	CheckedAt     *float64 `json:"checked_at"`
}

// PlaylistSummary is the playlist part of Status.
type PlaylistSummary struct {
	Revision  int     `json:"revision"`
	Length    int     `json:"length"`
	UpdatedAt float64 `json:"updated_at"`
	Source    string  `json:"source"`
}

// Status is the player's part of GET /api/v1/status.
type Status struct {
	State    string          `json:"state"`
	Paused   bool            `json:"paused"`
	Index    *int            `json:"index"`
	Current  *Current        `json:"current"`
	Override *OverrideStatus `json:"override"`
	Network  Network         `json:"network"`
	Playlist PlaylistSummary `json:"playlist"`
}

// Player cycles the browser through the playlist.
type Player struct {
	browser browser.Browser
	store   playlist.Store
	opt     Options

	mu          sync.Mutex
	playlist    playlist.Playlist
	index       int
	paused      bool
	override    *override
	startedAt   float64
	endsAt      *float64
	showing     *playlist.Item
	showingKind string
	wantReload  bool
	dirty       bool
	stopped     bool
	preloadedAt *float64
	probe       *probeResult

	wake chan struct{}
	stop chan struct{}
	wg   sync.WaitGroup
}

// New makes a player and loads the saved playlist.
func New(b browser.Browser, store playlist.Store, opt Options) *Player {
	if opt.Probe == nil {
		opt.Probe = Reachable
	}
	if opt.HealthInterval == 0 {
		opt.HealthInterval = 2 * time.Second
	}
	if opt.ProbeInterval == 0 {
		opt.ProbeInterval = 10 * time.Second
	}
	p := &Player{browser: b, store: store, opt: opt, playlist: store.Load(), dirty: true,
		wake: make(chan struct{}, 1), stop: make(chan struct{})}
	if opt.Messages != nil {
		opt.Messages.OnChange(func() {
			p.mu.Lock()
			p.kick()
			p.mu.Unlock()
		})
	}
	return p
}

// Browser is the browser the player drives.
func (p *Player) Browser() browser.Browser { return p.browser }

// Messages is the player's message store (may be nil).
func (p *Player) Messages() *messages.Messages { return p.opt.Messages }

func now() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func ptr[T any](v T) *T { return &v }

// Start starts the player's goroutines.
func (p *Player) Start() {
	p.wg.Add(2)
	go p.run()
	go p.watchNetwork()
}

// Stop stops the goroutines and the browser.
func (p *Player) Stop() {
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		close(p.stop)
	}
	p.mu.Unlock()
	p.wg.Wait()
	p.browser.Stop()
}

// --- commands (called from API goroutines) ----------------------------

// Playlist is the current playlist.
func (p *Player) Playlist() playlist.Playlist {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playlist
}

// SetPlaylist saves pl and starts it at startIndex (0 if out of range).
func (p *Player) SetPlaylist(pl playlist.Playlist, startIndex int) error {
	if err := p.store.Save(pl); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.playlist = pl
	p.index = 0
	if startIndex >= 0 && startIndex < len(pl.Items) {
		p.index = startIndex
	}
	p.kick()
	return nil
}

// Next moves to the next page (and ends any "show").
func (p *Player) Next() { p.stepCmd(1) }

// Previous moves to the previous page (and ends any "show").
func (p *Player) Previous() { p.stepCmd(-1) }

func (p *Player) stepCmd(delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.override = nil
	if n := len(p.playlist.Items); n > 0 {
		p.index = ((p.index+delta)%n + n) % n
	}
	p.kick()
}

// Goto jumps to a playlist index.
func (p *Player) Goto(index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.playlist.Items) {
		return ErrIndex
	}
	p.override = nil
	p.index = index
	p.kick()
	return nil
}

// Pause stays on the current page.
func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
	p.endsAt = nil
	p.notify()
}

// Resume carries on cycling, giving the current page a fresh dwell.
func (p *Player) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused {
		p.paused = false
		p.kick()
	}
}

// Show temporarily shows item for duration seconds (0 = until cleared),
// then returns to the playlist.
func (p *Player) Show(item playlist.Item, duration int, aboveMessages bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := &override{item: item, aboveMessages: aboveMessages}
	if duration > 0 {
		o.until = ptr(now() + float64(duration))
	}
	p.override = o
	p.kick()
}

// ClearOverride ends a "show" early.
func (p *Player) ClearOverride() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.override != nil {
		p.override = nil
		p.kick()
	}
}

// Reload reloads the page on screen.
func (p *Player) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wantReload = true
	p.notify()
}

// Status reports what's on screen.
func (p *Player) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := Status{Paused: p.paused, Playlist: PlaylistSummary{
		Revision: p.playlist.Revision, Length: len(p.playlist.Items),
		UpdatedAt: p.playlist.UpdatedAt, Source: p.playlist.Source}}
	switch {
	case p.showingKind == "message":
		st.State = "message"
	case p.override != nil:
		st.State = "override"
	case len(p.playlist.Items) == 0:
		st.State = "idle"
	case p.paused:
		st.State = "paused"
	default:
		st.State = "playing"
	}
	if len(p.playlist.Items) > 0 {
		st.Index = ptr(p.index)
	}
	if s := p.showing; s != nil {
		st.Current = &Current{URL: s.URL, Label: s.Label, ID: s.ID, StartedAt: p.startedAt, EndsAt: p.endsAt}
		if p.probe != nil && p.probe.url == s.URL {
			st.Network = Network{PageReachable: ptr(p.probe.ok), CheckedAt: ptr(p.probe.at)}
		}
	}
	if p.override != nil {
		st.Override = &OverrideStatus{URL: p.override.item.URL, Until: p.override.until}
	}
	return st
}

// --- internals ---------------------------------------------------------

// kick marks the target stale and wakes the loop. Call with mu held.
func (p *Player) kick() {
	p.dirty = true
	p.notify()
}

func (p *Player) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// target is what should be on screen now and when it should change (nil =
// not by itself). Call with mu held.
func (p *Player) target() (playlist.Item, *float64) {
	t := now()
	if p.override != nil && p.override.until != nil && *p.override.until <= t {
		p.override = nil // ran out while a message was covering it
	}
	if p.override != nil && p.override.aboveMessages {
		p.showingKind = "override"
		return p.override.item, p.override.until
	}
	if p.opt.Messages != nil {
		if msg := p.opt.Messages.Message(); msg != nil {
			p.showingKind = "message"
			return playlist.Item{URL: p.opt.MessageURL, Label: "message"}, msg.Until
		}
	}
	if p.override != nil {
		p.showingKind = "override"
		return p.override.item, p.override.until
	}
	p.showingKind = "playlist"
	items := p.playlist.Items
	if len(items) == 0 {
		return playlist.Item{URL: p.opt.IdleURL, Label: "idle"}, nil
	}
	item := items[p.index%len(items)]
	// A single page never needs to move on; keep its live connection open.
	if p.paused || len(items) == 1 {
		return item, nil
	}
	return item, ptr(t + float64(item.Duration))
}

// upcoming is what will be shown when the current page's time is up.
func (p *Player) upcoming() *playlist.Item {
	items := p.playlist.Items
	if p.showingKind == "message" {
		return nil
	}
	if p.override != nil {
		if p.override.until == nil || len(items) == 0 {
			return nil
		}
		return &items[p.index%len(items)]
	}
	if len(items) < 2 || p.paused {
		return nil
	}
	return &items[(p.index+1)%len(items)]
}

func (p *Player) run() {
	defer p.wg.Done()
	preloader, canPreload := p.browser.(browser.Preloader)
	ahead := p.opt.PreloadAhead
	for {
		var target *playlist.Item
		reload := false
		preload := ""
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return
		}
		t := now()
		if p.endsAt != nil && t >= *p.endsAt {
			switch p.showingKind {
			case "message": // the message has expired; show what's underneath
			case "override":
				p.override = nil
			default:
				p.index = (p.index + 1) % max(len(p.playlist.Items), 1)
			}
			p.dirty = true
		}
		if p.dirty {
			p.dirty = false
			item, ends := p.target()
			target = &item
			p.endsAt = ends
			p.startedAt = t
			p.showing = target
		}
		if p.wantReload {
			p.wantReload = false
			reload = true
		}
		if canPreload && p.endsAt != nil && (p.preloadedAt == nil || *p.preloadedAt != *p.endsAt) &&
			t >= *p.endsAt-ahead {
			p.preloadedAt = ptr(*p.endsAt)
			if up := p.upcoming(); up != nil {
				preload = up.URL
			}
		}
		p.mu.Unlock()

		if err := p.drive(target, reload, preload, preloader); err != nil {
			p.browser.SetLastError(err.Error())
			slog.Error("browser error", "err", err)
			// Back off, then try to show the same page again.
			select {
			case <-p.stop:
			case <-time.After(p.opt.HealthInterval):
			}
			p.mu.Lock()
			if target != nil && !p.dirty {
				p.dirty = true
				p.endsAt = nil
			}
			p.mu.Unlock()
		}

		p.mu.Lock()
		if p.stopped || p.dirty || p.wantReload {
			p.mu.Unlock()
			continue
		}
		timeout := p.opt.HealthInterval
		if p.endsAt != nil {
			wake := *p.endsAt
			if canPreload && (p.preloadedAt == nil || *p.preloadedAt != *p.endsAt) {
				wake -= ahead
			}
			left := time.Duration((wake - now()) * float64(time.Second))
			timeout = max(0, min(timeout, left))
		}
		p.mu.Unlock()
		timer := time.NewTimer(timeout)
		select {
		case <-p.wake:
		case <-p.stop:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (p *Player) drive(target *playlist.Item, reload bool, preload string, pre browser.Preloader) error {
	if target != nil && target.URL != p.browser.CurrentURL() {
		slog.Info("showing " + target.URL)
		if err := p.browser.Open(target.URL); err != nil {
			return err
		}
	} else if reload {
		if err := p.browser.Reload(); err != nil {
			return err
		}
	}
	if preload != "" {
		slog.Debug("preloading " + preload)
		if err := pre.Preload(preload); err != nil {
			return err
		}
	}
	return p.browser.EnsureRunning()
}

// watchNetwork reloads the page when its server comes back after being
// unreachable. At boot the browser often loads the first page before Wi-Fi
// is up, and a timing server can restart mid-event. Either way the browser
// is left on an error page; a single-page playlist would never recover on
// its own, so watch for the server to come back and reload.
func (p *Player) watchNetwork() {
	defer p.wg.Done()
	for {
		u := p.browser.CurrentURL()
		if u != "" && u != p.opt.IdleURL {
			ok := p.opt.Probe(u)
			p.mu.Lock()
			prev := p.probe
			p.probe = &probeResult{u, ok, now()}
			p.mu.Unlock()
			if ok && prev != nil && prev.url == u && !prev.ok {
				slog.Info(u + " reachable again; reloading")
				p.Reload()
			} else if !ok && (prev == nil || prev.url != u || prev.ok) {
				slog.Warn(u + " is not reachable")
			}
		}
		select {
		case <-p.stop:
			return
		case <-time.After(p.opt.ProbeInterval):
		}
	}
}
