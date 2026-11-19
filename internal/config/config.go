// Package config loads the agent's TOML configuration with defaults.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultPath is where the package installs the config.
const DefaultPath = "/etc/eventscreen/config.toml"

// Config is every setting in config.toml. See deploy/config.example.toml.
type Config struct {
	// Stable identifier reported to the manager. Defaults to the hostname.
	ScreenID string `toml:"screen_id"`
	// Human-friendly name, e.g. "Pit lane board".
	Name string `toml:"name"`
	// HTTP API bind address and port.
	Listen string `toml:"listen"`
	Port   int    `toml:"port"`
	// If set, write requests must send "Authorization: Bearer <token>".
	APIToken string `toml:"api_token"`
	// Where the playlist and messages are persisted so they survive reboots.
	StateDir string `toml:"state_dir"`
	// Browser driver: "cog" (WPE WebKit), "chromium" or
	// "dummy" (no display).
	Browser string `toml:"browser"`
	// Extra arguments passed to the browser binary.
	BrowserArgs []string `toml:"browser_args"`
	// Dwell time used when a playlist item doesn't give one.
	DefaultDuration int `toml:"default_duration"`
	// Hard floor so a bad playlist can't thrash the Pi.
	MinDuration int `toml:"min_duration"`
	// Load the next page out of sight before switching (see package stage).
	Preload bool `toml:"preload"`
	// How many seconds before a switch to start loading the next page.
	PreloadAhead int `toml:"preload_ahead"`
	// Wi-Fi fallback hotspot (eventscreen wifi): when no known network has
	// been connected for WifiFallbackAfter seconds, start a setup hotspot.
	WifiFallback      bool `toml:"wifi_fallback"`
	WifiFallbackAfter int  `toml:"wifi_fallback_after"`
	// While the hotspot is up, drop it this often to retry known networks.
	WifiRetryInterval int `toml:"wifi_retry_interval"`
	// Hotspot name and WPA2 password. Empty = "EventScreen-<hostname>" and
	// a random password kept in StateDir (both are shown on the TV).
	HotspotSSID     string `toml:"hotspot_ssid"`
	HotspotPassword string `toml:"hotspot_password"`
}

// Default returns the settings used when config.toml doesn't say.
func Default() Config {
	host, _ := os.Hostname()
	return Config{
		ScreenID:          host,
		Listen:            "0.0.0.0",
		Port:              8080,
		StateDir:          "/var/lib/eventscreen",
		Browser:           "cog",
		BrowserArgs:       []string{},
		DefaultDuration:   30,
		MinDuration:       5,
		Preload:           true,
		PreloadAhead:      10,
		WifiFallback:      true,
		WifiFallbackAfter: 120,
		WifiRetryInterval: 300,
	}
}

// PlaylistPath is where the current playlist is saved.
func (c Config) PlaylistPath() string { return filepath.Join(c.StateDir, "playlist.json") }

// MessagesPath is where the current message and banner are saved.
func (c Config) MessagesPath() string { return filepath.Join(c.StateDir, "messages.json") }

// Load reads path over the defaults. A missing file gives the defaults; an
// unknown key is an error, so a typo doesn't go unnoticed.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Default(), fmt.Errorf("unknown config key %q in %s", undecoded[0].String(), path)
	}
	return cfg, nil
}
