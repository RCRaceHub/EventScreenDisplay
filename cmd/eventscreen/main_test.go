package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownCommand(t *testing.T) {
	if code := run([]string{"bogus"}); code != 2 {
		t.Errorf("exit %d", code)
	}
}

func TestVersionAndHelp(t *testing.T) {
	if run([]string{"version"}) != 0 || run([]string{"help"}) != 0 {
		t.Error("non-zero exit")
	}
}

func TestAvahiWritesTheServiceFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	out := filepath.Join(dir, "eventscreen.service")
	_ = os.WriteFile(cfg, []byte("port = 9123\n"), 0o644)
	if code := run([]string{"avahi", "--config", cfg, "--output", out}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "<port>9123</port>") {
		t.Errorf("service file:\n%s", data)
	}
}

func TestBadConfigStopsTheAgent(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfg, []byte("prot = 1\n"), 0o644)
	for _, cmd := range [][]string{{"run", "--config", cfg}, {"wifi", "--config", cfg}, {"avahi", "--config", cfg}} {
		if code := run(cmd); code != 1 {
			t.Errorf("%v: exit %d", cmd, code)
		}
	}
}

func TestUnknownBrowserStopsTheAgent(t *testing.T) {
	if code := run([]string{"--config", "/nonexistent", "--browser", "netscape"}); code != 1 {
		t.Errorf("exit %d", code)
	}
}
