package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, text string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil || cfg.Port != 8080 || cfg.Browser != "cog" || !cfg.WifiFallback || !cfg.Preload {
		t.Errorf("got %+v, %v", cfg, err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	// The file the package installs as /etc/eventscreen/config.toml.
	cfg, err := Load("../../deploy/config.example.toml")
	if err != nil || cfg.Port != 8080 || cfg.StateDir != "/var/lib/eventscreen" {
		t.Errorf("got %+v, %v", cfg, err)
	}
}

func TestValuesOverrideDefaults(t *testing.T) {
	cfg, err := load(t, "port = 9000\nname = \"Pit\"\nstate_dir = \"/tmp/x\"\nbrowser_args = [\"--scale=1.5\"]\npreload = false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 || cfg.Name != "Pit" || cfg.StateDir != "/tmp/x" || cfg.Preload {
		t.Errorf("got %+v", cfg)
	}
	if cfg.PlaylistPath() != "/tmp/x/playlist.json" || cfg.MessagesPath() != "/tmp/x/messages.json" {
		t.Errorf("paths %s %s", cfg.PlaylistPath(), cfg.MessagesPath())
	}
	if !reflect.DeepEqual(cfg.BrowserArgs, []string{"--scale=1.5"}) {
		t.Errorf("browser_args %q", cfg.BrowserArgs)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	// Catches typos instead of silently ignoring them.
	if _, err := load(t, "prot = 9000\n"); err == nil || !strings.Contains(err.Error(), "prot") {
		t.Errorf("got %v", err)
	}
}

func TestWrongTypeIsAnError(t *testing.T) {
	if _, err := load(t, "port = \"8080\"\n"); err == nil {
		t.Error("string port accepted")
	}
}
