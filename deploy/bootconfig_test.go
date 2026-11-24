package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
)

// runBootconfig writes eventscreen.txt to the scratch root's boot partition
// and runs eventscreen-bootconfig, which runs the real eventscreen-setup.
func (r *root) runBootconfig(file string) string {
	r.t.Helper()
	if file != "" {
		if err := os.WriteFile(filepath.Join(r.dir, "boot/firmware/eventscreen.txt"), []byte(file), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	setup, err := filepath.Abs("eventscreen-setup")
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command("bash", "eventscreen-bootconfig")
	cmd.Env = append(r.env, "EVENTSCREEN_SETUP="+setup)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("eventscreen-bootconfig: %v\n%s", err, out)
	}
	return string(out)
}

func (r *root) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(r.dir, rel))
	return err == nil
}

func TestBootconfigAppliesSettingsAndRemovesFile(t *testing.T) {
	r := newRoot(t)
	// Written on Windows: a byte order mark, CRLF line ends, quotes and spaces.
	r.runBootconfig("\xef\xbb\xbf# my screen\r\nname = \"Pit lane board\"\r\n\r\ntoken=abc123-secret\r\nport=9090\r\nresolution=1920x1080@50")
	cfg, err := config.Load(r.conf)
	if err != nil || cfg.Name != "Pit lane board" || cfg.APIToken != "abc123-secret" || cfg.Port != 9090 {
		t.Errorf("config %+v %v", cfg, err)
	}
	contains(t, "cmdline", r.cmdline(), "video=HDMI-A-1:1920x1080@50")
	if r.exists("boot/firmware/eventscreen.txt") {
		t.Error("eventscreen.txt (with the token) was left on the boot partition")
	}
	applied := filepath.Join(r.dir, "var/lib/eventscreen/eventscreen.txt.applied")
	st, err := os.Stat(applied)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("applied copy: %v %v", st, err)
	}
	calls := r.read("calls")
	if strings.Contains(calls, "restart eventscreen") {
		t.Error("restarted the viewer from a unit ordered before it (would deadlock):\n" + calls)
	}
	contains(t, "calls", calls, "systemctl --no-block reboot")
}

func TestBootconfigWithoutDisplayChangeDoesNotReboot(t *testing.T) {
	r := newRoot(t)
	r.run() // already set up, as on the image
	_ = os.Remove(filepath.Join(r.dir, "getty-enabled"))
	_ = os.Remove(filepath.Join(r.dir, "calls"))
	r.runBootconfig("name=Start line\n")
	if strings.Contains(r.read("calls"), "reboot") {
		t.Error("rebooted for a name change")
	}
	contains(t, "config", r.read("etc/eventscreen/config.toml"), `name = "Start line"`)
}

func TestBootconfigWithNoFileDoesNothing(t *testing.T) {
	r := newRoot(t)
	before := r.cmdline()
	r.runBootconfig("")
	if r.cmdline() != before || r.exists("calls") {
		t.Error("changed something with no eventscreen.txt")
	}
}

func TestBootconfigRejectsMistakesWithoutApplyingAnything(t *testing.T) {
	for _, bad := range []string{
		"name=ok\nport=99999\n",
		"name=Pit \"lane\"\n",
		"name=a & b\n",
		"token=short\n",
		"hostname=screen-01\n",
		"just some text\n",
		"name=\n",
		"resolution=big\n",
	} {
		r := newRoot(t)
		before := r.read("etc/eventscreen/config.toml")
		r.runBootconfig(bad)
		if got := r.read("etc/eventscreen/config.toml"); got != before {
			t.Errorf("%q changed the config", bad)
		}
		if r.exists("boot/firmware/eventscreen.txt") {
			t.Errorf("%q: eventscreen.txt left in place, would be retried every boot", bad)
		}
		errFile := r.read("boot/firmware/eventscreen.txt.error")
		if !strings.HasPrefix(errFile, "# Not applied: ") || !strings.Contains(errFile, bad) {
			t.Errorf("%q: error file\n%s", bad, errFile)
		}
	}
}

func TestSetupOfflineDoesNotStartAnything(t *testing.T) {
	r := newRoot(t)
	out := r.run("--offline", "--name", "Finish")
	calls := r.read("calls")
	if strings.Contains(calls, "restart") || strings.Contains(calls, "hostnamectl") {
		t.Errorf("offline run started things:\n%s", calls)
	}
	contains(t, "calls", calls, "systemctl disable getty@tty1.service")
	contains(t, "output", out, "Reboot needed")
	contains(t, "cmdline", r.cmdline(), "video=HDMI-A-1:1920x1080@60D")
}

func TestSetupOfflineRefusesHostname(t *testing.T) {
	r := newRoot(t)
	cmd := exec.Command("bash", "eventscreen-setup", "--offline", "--hostname", "x")
	cmd.Env = r.env
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("accepted --hostname with --offline:\n%s", out)
	}
}

// The template on the image's boot partition must be accepted as it is,
// and with every setting uncommented.
func TestBootconfigAcceptsTheTemplate(t *testing.T) {
	tmpl, err := os.ReadFile("../image/eventscreen.txt.example")
	if err != nil {
		t.Fatal(err)
	}
	var uncommented []string
	for _, line := range strings.Split(string(tmpl), "\n") {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "=") && !strings.Contains(line, " ") {
			line = strings.TrimPrefix(line, "#")
		}
		uncommented = append(uncommented, line)
	}
	for _, file := range []string{string(tmpl), strings.Join(uncommented, "\n")} {
		r := newRoot(t)
		r.runBootconfig(file)
		if r.exists("boot/firmware/eventscreen.txt.error") {
			t.Errorf("template rejected:\n%s\n%s", r.read("boot/firmware/eventscreen.txt.error"), file)
		}
	}
}
