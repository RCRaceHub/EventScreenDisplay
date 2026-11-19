// Package deploy holds the files installed on the Pi. This test runs
// eventscreen-setup against a scratch root with fake system tools.
package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
)

// fakes record every call so tests can see what would have happened.
var fakes = map[string]string{
	"systemctl": `echo "systemctl $*" >> "$EVENTSCREEN_ROOT/calls"
[ "$1" = is-enabled ] && [ -f "$EVENTSCREEN_ROOT/getty-enabled" ] && exit 0
[ "$1" = is-enabled ] && exit 1
exit 0`,
	"hostnamectl": `echo "hostnamectl $*" >> "$EVENTSCREEN_ROOT/calls"`,
	"hostname":    "echo raspberrypi",
	"apt-get":     `echo "apt-get $*" >> "$EVENTSCREEN_ROOT/calls"`,
}

type root struct {
	t    *testing.T
	dir  string
	env  []string
	conf string
	cmd  string
}

func newRoot(t *testing.T) *root {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	r := &root{t: t, dir: t.TempDir()}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(r.dir, "etc/eventscreen"), 0o755))
	example, err := os.ReadFile("config.example.toml")
	must(err)
	r.conf = filepath.Join(r.dir, "etc/eventscreen/config.toml")
	must(os.WriteFile(r.conf, example, 0o644))
	must(os.WriteFile(filepath.Join(r.dir, "etc/hosts"), []byte("127.0.0.1\tlocalhost\n127.0.1.1\traspberrypi\n"), 0o644))
	must(os.MkdirAll(filepath.Join(r.dir, "boot/firmware"), 0o755))
	r.cmd = filepath.Join(r.dir, "boot/firmware/cmdline.txt")
	r.writeCmdline("console=tty1 root=PARTUUID=1234-02 rootwait\n")
	must(os.WriteFile(filepath.Join(r.dir, "getty-enabled"), nil, 0o644))
	bin := filepath.Join(r.dir, "bin")
	must(os.MkdirAll(bin, 0o755))
	for name, body := range fakes {
		must(os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	r.env = append(os.Environ(), "EVENTSCREEN_ROOT="+r.dir, "PATH="+bin+":"+os.Getenv("PATH"))
	return r
}

func (r *root) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("bash", append([]string{"eventscreen-setup"}, args...)...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("eventscreen-setup %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (r *root) read(rel string) string {
	data, _ := os.ReadFile(filepath.Join(r.dir, rel))
	return string(data)
}

func (r *root) cmdline() string { return r.read("boot/firmware/cmdline.txt") }

func (r *root) writeCmdline(s string) {
	if err := os.WriteFile(r.cmd, []byte(s), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func contains(t *testing.T, what, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: %q missing from\n%s", what, sub, s)
	}
}

func TestSetupDefaults(t *testing.T) {
	r := newRoot(t)
	out := r.run()
	cl := r.cmdline()
	contains(t, "cmdline", cl, "video=HDMI-A-1:1920x1080@60D")
	contains(t, "cmdline", cl, "consoleblank=0")
	if n := strings.Count(strings.TrimRight(cl, "\n"), "\n"); n != 0 {
		t.Error("cmdline.txt must stay one line")
	}
	contains(t, "calls", r.read("calls"), "systemctl disable getty@tty1.service")
	contains(t, "calls", r.read("calls"), "systemctl restart eventscreen")
	contains(t, "output", out, "Reboot")
}

func TestSetupIsIdempotent(t *testing.T) {
	r := newRoot(t)
	r.run()
	first := r.cmdline()
	_ = os.Remove(filepath.Join(r.dir, "getty-enabled"))
	out := r.run()
	if r.cmdline() != first || strings.Contains(out, "Reboot") {
		t.Errorf("second run changed things:\n%s", out)
	}
}

func TestSetupOptionsWriteConfig(t *testing.T) {
	r := newRoot(t)
	r.run("--hostname", "screen-07", "--name", "Pit lane", "--port", "9090", "--token", "s3cret", "--browser", "dummy")
	conf := r.read("etc/eventscreen/config.toml")
	for _, want := range []string{`name = "Pit lane"`, "port = 9090", `api_token = "s3cret"`, `browser = "dummy"`} {
		contains(t, "config", conf, want)
	}
	contains(t, "calls", r.read("calls"), "hostnamectl set-hostname screen-07")
	contains(t, "hosts", r.read("etc/hosts"), "127.0.1.1\tscreen-07")
	// The result must still be valid config.
	cfg, err := config.Load(r.conf)
	if err != nil || cfg.Port != 9090 || cfg.APIToken != "s3cret" {
		t.Errorf("config %+v %v", cfg, err)
	}
}

func TestSetupResolution(t *testing.T) {
	r := newRoot(t)
	r.run("--resolution", "1920x1080@50")
	contains(t, "cmdline", r.cmdline(), "video=HDMI-A-1:1920x1080@50")
	r.run("--resolution", "auto")
	if strings.Contains(r.cmdline(), "video=") {
		t.Error(r.cmdline())
	}
}

func TestSetupUpgradesOldDefault(t *testing.T) {
	// 0.2.0 wrote @60 without the D that forces HDMI on.
	r := newRoot(t)
	r.writeCmdline("console=tty1 rootwait video=HDMI-A-1:1920x1080@60 consoleblank=0\n")
	r.run()
	contains(t, "cmdline", r.cmdline(), "video=HDMI-A-1:1920x1080@60D consoleblank=0")
}

func TestSetupKeepsCustomMode(t *testing.T) {
	r := newRoot(t)
	r.writeCmdline("console=tty1 video=HDMI-A-1:1920x1080@60D,rotate=90\n")
	r.run()
	contains(t, "cmdline", r.cmdline(), "video=HDMI-A-1:1920x1080@60D,rotate=90")
}
