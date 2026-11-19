// Package packaging builds the .deb. This test builds one with an explicit
// version, the way a release tag does.
package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAndArchAreStampedIntoThePackage(t *testing.T) {
	for _, tool := range []string{"bash", "dpkg-deb", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs " + tool)
		}
	}
	if testing.Short() {
		t.Skip("builds three binaries")
	}
	repo, _ := filepath.Abs("..")
	version := "9.8.7~rc1"
	for _, arch := range []string{"arm64", "armhf", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			// Build from a copy so the test never touches the repo's dist/.
			src := filepath.Join(t.TempDir(), "src")
			cp := exec.Command("cp", "-r", repo, src)
			if out, err := cp.CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			_ = os.RemoveAll(filepath.Join(src, "dist"))
			cmd := exec.Command("bash", filepath.Join(src, "packaging/build-deb.sh"), version)
			cmd.Env = append(os.Environ(), "ARCH="+arch)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			deb := filepath.Join(src, "dist", "eventscreen_"+version+"_"+arch+".deb")
			for field, want := range map[string]string{"Version": version, "Architecture": arch} {
				out, err := exec.Command("dpkg-deb", "-f", deb, field).Output()
				if err != nil || strings.TrimSpace(string(out)) != want {
					t.Errorf("%s = %q (%v), want %q", field, out, err, want)
				}
			}
			x := filepath.Join(t.TempDir(), "x")
			if out, err := exec.Command("dpkg-deb", "-x", deb, x).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			for _, f := range []string{"usr/bin/eventscreen", "usr/sbin/eventscreen-setup", "usr/sbin/eventscreen-check",
				"usr/lib/systemd/system/eventscreen.service", "etc/eventscreen/config.toml"} {
				if _, err := os.Stat(filepath.Join(x, f)); err != nil {
					t.Errorf("%s missing", f)
				}
			}
			bin, _ := os.ReadFile(filepath.Join(x, "usr/bin/eventscreen"))
			if !strings.Contains(string(bin), version) {
				t.Error("version not stamped into the binary")
			}
			if arch == "amd64" {
				out, err := exec.Command(filepath.Join(x, "usr/bin/eventscreen"), "version").Output()
				if err != nil || strings.TrimSpace(string(out)) != version {
					t.Errorf("eventscreen version = %q %v", out, err)
				}
			}
		})
	}
	// The repo itself is left alone.
	src, _ := os.ReadFile(filepath.Join(repo, "internal/version/version.go"))
	if strings.Contains(string(src), version) {
		t.Error("repo version.go was changed")
	}
}
