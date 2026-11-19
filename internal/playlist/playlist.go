// Package playlist holds the playlist model, its validation and its
// persistence.
package playlist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
)

// Error is a validation error; the API answers it with 400.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Item is one page of the playlist.
type Item struct {
	URL      string `json:"url"`
	Duration int    `json:"duration"`
	Label    string `json:"label"`
	ID       string `json:"id"`
}

// Playlist is the whole playlist as saved and as returned by the API.
type Playlist struct {
	Items []Item `json:"items"`
	// Bumped on every change so a manager can detect drift cheaply.
	Revision int `json:"revision"`
	// Unix seconds.
	UpdatedAt float64 `json:"updated_at"`
	// Opaque value the manager can set to tag where this playlist came from.
	Source string `json:"source"`
}

// Replaced is old with new items, the next revision and a fresh timestamp.
func Replaced(old Playlist, items []Item, source string) Playlist {
	if items == nil {
		items = []Item{}
	}
	return Playlist{Items: items, Revision: old.Revision + 1, UpdatedAt: now(), Source: source}
}

func now() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

// ValidateURL accepts only absolute http(s) URLs.
func ValidateURL(v any) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", errorf("url must be a non-empty string")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errorf("url must be an absolute http(s) URL: %s", pyRepr(s))
	}
	return s, nil
}

// ParseDuration reads a dwell time in seconds; nil gives def.
func ParseDuration(v any, def, minimum int) (int, error) {
	if v == nil {
		return def, nil
	}
	f, ok := jsonx.Float(v)
	if !ok {
		return 0, errorf("duration must be a number of seconds")
	}
	if f < float64(minimum) {
		return 0, errorf("duration must be at least %d seconds", minimum)
	}
	return int(f), nil
}

// ParseItems turns the "items" array of a PUT body into validated Items.
// Each entry may be a plain URL string or an object with url, duration,
// label and id.
func ParseItems(v any, def, minimum int) ([]Item, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errorf("items must be an array")
	}
	items := make([]Item, 0, len(list))
	for n, raw := range list {
		item, err := parseItem(raw, def, minimum)
		if err != nil {
			return nil, errorf("items[%d]: %s", n, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func parseItem(raw any, def, minimum int) (Item, error) {
	if s, ok := raw.(string); ok {
		raw = map[string]any{"url": s}
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return Item{}, errorf("item must be a URL string or an object")
	}
	u, err := ValidateURL(obj["url"])
	if err != nil {
		return Item{}, err
	}
	d, err := ParseDuration(obj["duration"], def, minimum)
	if err != nil {
		return Item{}, err
	}
	item := Item{URL: u, Duration: d}
	if v, ok := obj["label"]; ok {
		item.Label = jsonx.Str(v)
	}
	if v, ok := obj["id"]; ok {
		item.ID = jsonx.Str(v)
	}
	return item, nil
}

// Store loads and atomically saves the playlist to disk.
type Store struct{ Path string }

// Load returns the saved playlist, or an empty one if there is none. A
// damaged file (say, from a power cut) mustn't stop the screen starting: it
// is moved aside to playlist.bad and the screen starts empty.
func (s Store) Load() Playlist {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Playlist{Items: []Item{}}
	}
	var pl Playlist
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err = decodeStored(dec, &pl)
	}
	if err != nil {
		bad := strings.TrimSuffix(s.Path, filepath.Ext(s.Path)) + ".bad"
		slog.Error("can't read playlist; moved it aside and starting empty", "path", s.Path, "err", err, "moved_to", bad)
		_ = os.Rename(s.Path, bad)
		return Playlist{Items: []Item{}}
	}
	if pl.Items == nil {
		pl.Items = []Item{}
	}
	return pl
}

func decodeStored(dec *json.Decoder, pl *Playlist) error {
	var raw struct {
		Items     []json.RawMessage `json:"items"`
		Revision  int               `json:"revision"`
		UpdatedAt float64           `json:"updated_at"`
		Source    string            `json:"source"`
	}
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	pl.Revision, pl.UpdatedAt, pl.Source = raw.Revision, raw.UpdatedAt, raw.Source
	for _, r := range raw.Items {
		d := json.NewDecoder(bytes.NewReader(r))
		d.DisallowUnknownFields()
		var item Item
		if err := d.Decode(&item); err != nil {
			return err
		}
		if item.URL == "" {
			return errors.New("item without a url")
		}
		pl.Items = append(pl.Items, item)
	}
	return nil
}

// Save writes the playlist atomically (temp file and rename).
func (s Store) Save(pl Playlist) error {
	if pl.Items == nil {
		pl.Items = []Item{}
	}
	data, err := json.MarshalIndent(pl, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(s.Path, data, 0o644)
}

// WriteAtomic writes data to path through a temp file and a rename, so a
// power cut leaves either the old file or the new one.
func WriteAtomic(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pyRepr quotes s the way the Python agent's error messages did ('...').
func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}
