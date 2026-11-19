package browser

import (
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}

// failing can't start, like Cog with no display connected.
type failing struct{ Dummy }

func (failing) Command(string) ([]string, error) { return []string{"false"}, nil }

func TestFailingBrowserBacksOff(t *testing.T) {
	p := New(failing{})
	_ = p.Open("http://a.example/")
	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = p.EnsureRunning()
		time.Sleep(50 * time.Millisecond)
	}
	// Retries after 2 s, then waits 4 s: one restart, not dozens.
	if p.Restarts() != 1 || !strings.Contains(p.LastError(), "exited with code 1") {
		t.Errorf("restarts %d, last error %q", p.Restarts(), p.LastError())
	}
}

// slow is alive, but too busy to answer a navigate in time.
type slow struct{ Dummy }

func (slow) Navigate(string) error { return ErrSlow }

type broken struct{ Dummy }

func (broken) Navigate(string) error { return errors.New("cogctl open: exit status 1") }

func TestSlowAnswerDoesNotRestart(t *testing.T) {
	p := New(slow{})
	defer p.Stop()
	_ = p.Open("http://a.example/")
	pid := p.PID()
	_ = p.Open("http://b.example/")
	if p.PID() != pid || p.Restarts() != 0 || p.CurrentURL() != "http://b.example/" {
		t.Errorf("pid %d->%d restarts %d url %s", pid, p.PID(), p.Restarts(), p.CurrentURL())
	}
}

func TestRealFailureRestarts(t *testing.T) {
	p := New(broken{})
	defer p.Stop()
	_ = p.Open("http://a.example/")
	_ = p.Open("http://b.example/")
	if p.Restarts() != 1 || p.CurrentURL() != "http://b.example/" || !p.Alive() {
		t.Errorf("restarts %d url %s alive %v", p.Restarts(), p.CurrentURL(), p.Alive())
	}
}

func TestCrashIsRestartedOnTheSamePage(t *testing.T) {
	p := New(Dummy{})
	defer p.Stop()
	_ = p.Open("http://a.example/")
	p.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for !(p.Alive() && p.Restarts() == 1) && time.Now().Before(deadline) {
		_ = p.EnsureRunning()
		time.Sleep(20 * time.Millisecond)
	}
	if !p.Alive() || p.Restarts() != 1 || p.CurrentURL() != "http://a.example/" {
		t.Errorf("alive %v restarts %d", p.Alive(), p.Restarts())
	}
}

func TestStopEndsTheProcess(t *testing.T) {
	p := New(Dummy{})
	_ = p.Open("http://a.example/")
	if !p.Alive() {
		t.Fatal("not started")
	}
	p.Stop()
	if p.Alive() || p.Status().Running || p.Status().PID != nil {
		t.Error("still running")
	}
}

func TestStatus(t *testing.T) {
	p := New(Dummy{})
	defer p.Stop()
	if st := p.Status(); st.URL != nil || st.Running {
		t.Errorf("before start %+v", st)
	}
	_ = p.Open("http://a.example/")
	st := p.Status()
	if st.Driver != "dummy" || !st.Running || st.PID == nil || *st.URL != "http://a.example/" || st.Preload != nil {
		t.Errorf("%+v", st)
	}
}

func TestCreateKnowsTheDrivers(t *testing.T) {
	for _, name := range []string{"cog", "chromium", "dummy"} {
		if p, err := Create(name, nil); err != nil || p.Driver().Name() != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Create("netscape", nil); err == nil || !strings.Contains(err.Error(), "cog") {
		t.Errorf("unknown: %v", err)
	}
}

func TestCogCommand(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "cog"))
	argv, err := (&Cog{Args: []string{"--scale=1.5"}}).Command("http://a/")
	if err != nil || !strings.HasSuffix(argv[0], "/cog") || strings.Join(argv[1:], " ") != "--platform=drm --scale=1.5 http://a/" {
		t.Errorf("%q %v", argv, err)
	}
}

func TestMissingBrowserIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := (&Cog{}).Command("http://a/"); err == nil || !strings.Contains(err.Error(), "cog not found") {
		t.Errorf("got %v", err)
	}
}

func fakeBin(t *testing.T, names ...string) string {
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(dir+"/"+n, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
