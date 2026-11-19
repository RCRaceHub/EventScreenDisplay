package avahi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
	"github.com/RCRaceHub/EventScreenDisplay/internal/version"
)

func TestServiceFileFollowsConfig(t *testing.T) {
	cfg := config.Default()
	cfg.ScreenID, cfg.Port = "pit<1>", 9090
	out := filepath.Join(t.TempDir(), "eventscreen.service")
	if err := Write(cfg, "/etc/eventscreen/config.toml", out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	text := string(data)
	for _, want := range []string{"<port>9090</port>", "id=pit&lt;1&gt;", "api=/api/v1", "version=" + version.Version,
		"_eventscreen._tcp", "EventScreen %h"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from\n%s", want, text)
		}
	}
	st, _ := os.Stat(out)
	if err := Write(cfg, "/etc/eventscreen/config.toml", out); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(out); !st2.ModTime().Equal(st.ModTime()) {
		t.Error("unchanged file was rewritten")
	}
}
