// Package messages holds event messages: full-screen hold/announce screens
// and a banner.
//
// There are two independent slots, each holding at most one message:
//
//   - message: takes over the whole screen, above the playlist and any
//     "show". "hold" is for an open-ended wait ("Finals being calculated"),
//     with a gentle animation so the screen doesn't look frozen; "announce"
//     is for news ("Finals lists are up"). Drawn by the screen itself at
//     /message, so it stays up even if the server that sent it goes away.
//   - banner: a strip across the bottom of whatever is showing. Drawn by the
//     stage page, so it needs preload on and a page that allows framing.
//
// Both are saved to the state directory, so they survive a reboot, and
// expire by themselves if given a duration.
package messages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
	"github.com/RCRaceHub/EventScreenDisplay/internal/playlist"
)

var (
	styles = []string{"hold", "announce"}
	levels = []string{"info", "warning", "urgent"}
)

const (
	maxTitle = 120
	maxText  = 500
)

// Error is a validation error; the API answers it with 400.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Message is a full-screen message. Times are Unix seconds; Until is nil
// for a message that stays up until cleared.
type Message struct {
	Style string   `json:"style"`
	Title string   `json:"title"`
	Text  string   `json:"text"`
	Level string   `json:"level"`
	Since float64  `json:"since"`
	Until *float64 `json:"until"`
}

// Banner is the strip along the bottom of the screen.
type Banner struct {
	Text  string   `json:"text"`
	Level string   `json:"level"`
	Since float64  `json:"since"`
	Until *float64 `json:"until"`
}

func text(body jsonx.Object, key string, limit int, required bool) (string, error) {
	v := body[key]
	if v == nil {
		v = ""
	}
	s, ok := v.(string)
	if !ok {
		return "", errorf("%s must be a string", key)
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", errorf("%s is required", key)
	}
	if utf8.RuneCountInString(s) > limit {
		return "", errorf("%s is at most %d characters", key, limit)
	}
	return s, nil
}

func choice(body jsonx.Object, key string, options []string, def string) (string, error) {
	v, ok := body[key]
	if !ok {
		return def, nil
	}
	s, isStr := v.(string)
	if !isStr || !slices.Contains(options, s) {
		return "", errorf("%s must be one of %s", key, strings.Join(options, ", "))
	}
	return s, nil
}

func until(body jsonx.Object, now float64) (*float64, error) {
	v := body["duration"]
	if v == nil {
		return nil, nil
	}
	f, ok := jsonx.Float(v)
	if !ok || f < 1 {
		return nil, errorf("duration must be a number of seconds, at least 1 (leave it out to keep it up)")
	}
	u := now + float64(int(f))
	return &u, nil
}

// ParseMessage validates a PUT/POST /api/v1/message body.
func ParseMessage(body jsonx.Object, now float64) (*Message, error) {
	style, err := choice(body, "style", styles, "hold")
	if err != nil {
		return nil, err
	}
	title, err := text(body, "title", maxTitle, true)
	if err != nil {
		return nil, err
	}
	txt, err := text(body, "text", maxText, false)
	if err != nil {
		return nil, err
	}
	level, err := choice(body, "level", levels, "info")
	if err != nil {
		return nil, err
	}
	u, err := until(body, now)
	if err != nil {
		return nil, err
	}
	return &Message{Style: style, Title: title, Text: txt, Level: level, Since: now, Until: u}, nil
}

// ParseBanner validates a PUT/POST /api/v1/banner body.
func ParseBanner(body jsonx.Object, now float64) (*Banner, error) {
	txt, err := text(body, "text", maxText, true)
	if err != nil {
		return nil, err
	}
	level, err := choice(body, "level", levels, "info")
	if err != nil {
		return nil, err
	}
	u, err := until(body, now)
	if err != nil {
		return nil, err
	}
	return &Banner{Text: txt, Level: level, Since: now, Until: u}, nil
}

// Clock returns the time as Unix seconds. Tests swap in a fake.
type Clock func() float64

// Now is the real clock.
func Now() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

// Messages holds the two slots. Safe for concurrent use.
type Messages struct {
	path      string
	clock     Clock
	mu        sync.Mutex
	message   *Message
	banner    *Banner
	listeners []func()
}

// New loads any saved message and banner from path ("" = don't persist).
func New(path string, clock Clock) *Messages {
	if clock == nil {
		clock = Now
	}
	m := &Messages{path: path, clock: clock}
	m.load()
	return m
}

// OnChange registers fn to run (on the writer's goroutine) after a change.
func (m *Messages) OnChange(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func live(u *float64, now float64) bool { return u == nil || *u > now }

// Message is the current message, or nil (none, or expired).
func (m *Messages) Message() *Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveMessage()
}

func (m *Messages) liveMessage() *Message {
	if m.message == nil || !live(m.message.Until, m.clock()) {
		return nil
	}
	c := *m.message
	return &c
}

// Banner is the current banner, or nil.
func (m *Messages) Banner() *Banner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveBanner()
}

func (m *Messages) liveBanner() *Banner {
	if m.banner == nil || !live(m.banner.Until, m.clock()) {
		return nil
	}
	c := *m.banner
	return &c
}

// SetMessage validates body and puts it up.
func (m *Messages) SetMessage(body jsonx.Object) (*Message, error) {
	msg, err := ParseMessage(body, m.clock())
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.message = msg
	m.mu.Unlock()
	m.changed()
	return msg, nil
}

// ClearMessage takes the message down.
func (m *Messages) ClearMessage() {
	m.mu.Lock()
	had := m.liveMessage() != nil
	m.message = nil
	m.mu.Unlock()
	if had {
		m.changed()
	}
}

// SetBanner validates body and puts it up.
func (m *Messages) SetBanner(body jsonx.Object) (*Banner, error) {
	b, err := ParseBanner(body, m.clock())
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.banner = b
	m.mu.Unlock()
	m.changed()
	return b, nil
}

// ClearBanner takes the banner down.
func (m *Messages) ClearBanner() {
	m.mu.Lock()
	had := m.liveBanner() != nil
	m.banner = nil
	m.mu.Unlock()
	if had {
		m.changed()
	}
}

func (m *Messages) changed() {
	m.save()
	m.mu.Lock()
	listeners := slices.Clone(m.listeners)
	m.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

type saved struct {
	Message *Message `json:"message"`
	Banner  *Banner  `json:"banner"`
}

func (m *Messages) save() {
	if m.path == "" {
		return
	}
	m.mu.Lock()
	data, _ := json.MarshalIndent(saved{m.liveMessage(), m.liveBanner()}, "", "  ")
	m.mu.Unlock()
	if err := playlist.WriteAtomic(m.path, data, 0o644); err != nil {
		slog.Error("couldn't save messages", "path", m.path, "err", err)
	}
}

func (m *Messages) load() {
	if m.path == "" {
		return
	}
	data, err := os.ReadFile(m.path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var s saved
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err != nil {
		slog.Error("ignoring unreadable messages file", "path", m.path, "err", err)
		return
	}
	m.message, m.banner = s.Message, s.Banner
	m.message, m.banner = m.liveMessage(), m.liveBanner()
}
