// Package browser has the kiosk browser drivers.
//
// Each driver owns one full-screen browser process and can point it at a
// URL. The player calls EnsureRunning regularly, so a crashed browser is
// restarted on the page that should be showing.
package browser

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// A browser that dies within QuickExit of starting counts as a failed
// start, and restarts back off up to MaxBackoff so a browser that can't
// start (no display, say) doesn't spin and flood the log.
const (
	QuickExit  = 10 * time.Second
	MaxBackoff = 60 * time.Second
)

// ErrSlow means the browser is alive but didn't answer a command in time.
// The command usually still lands, so it isn't worth a restart.
var ErrSlow = errors.New("browser slow to answer")

// Status is the browser part of GET /api/v1/status.
type Status struct {
	Driver    string  `json:"driver"`
	Running   bool    `json:"running"`
	PID       *int    `json:"pid"`
	URL       *string `json:"url"`
	Restarts  int     `json:"restarts"`
	LastError string  `json:"last_error"`
	// Only set by the stage wrapper: whether the stage page is showing.
	Preload *bool `json:"preload,omitempty"`
}

// Browser is what the player drives.
type Browser interface {
	// Open shows url, starting the browser if it isn't running.
	Open(url string) error
	// Reload reloads the page that's showing.
	Reload() error
	// EnsureRunning restarts the browser if it died (with back-off).
	EnsureRunning() error
	Alive() bool
	Stop()
	Status() Status
	// CurrentURL is the page the browser was last told to show ("" = none).
	CurrentURL() string
	SetLastError(msg string)
}

// Preloader is a Browser that can load the next page out of sight.
type Preloader interface {
	Preload(url string) error
}

// Driver is what differs between browsers: how to start one and how to
// tell a running one to change page.
type Driver interface {
	Name() string
	// Command is the argv that starts the browser on url.
	Command(url string) ([]string, error)
	// Navigate changes page in the running browser. Return ErrSlow (wrapped
	// is fine) if it timed out; any other error restarts the browser.
	Navigate(url string) error
	// Reload reloads the page in the running browser.
	Reload(url string) error
	// AfterStart waits for a new browser to be ready for Navigate.
	AfterStart(alive func() bool)
}

// Process runs a Driver's browser and keeps it alive.
type Process struct {
	driver Driver

	// Written only under mu; the player goroutine is the only writer.
	mu         sync.Mutex
	cmd        *exec.Cmd
	done       chan struct{}
	exitCode   int
	currentURL string
	restarts   int
	lastError  string
	startedAt  time.Time
	backoff    time.Duration
	retryAt    time.Time
}

// New wraps a driver.
func New(d Driver) *Process { return &Process{driver: d} }

// Driver returns the wrapped driver.
func (p *Process) Driver() Driver { return p.driver }

// Restarts is how many times the browser has been restarted.
func (p *Process) Restarts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restarts
}

// LastError is the last thing that went wrong.
func (p *Process) LastError() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastError
}

// SetLastError records an error for the status endpoint.
func (p *Process) SetLastError(msg string) {
	p.mu.Lock()
	p.lastError = msg
	p.mu.Unlock()
}

// CurrentURL is the page the browser was last told to show.
func (p *Process) CurrentURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentURL
}

// PID of the running browser, or 0.
func (p *Process) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.aliveLocked() {
		return p.cmd.Process.Pid
	}
	return 0
}

// Kill kills the browser without the agent asking, as a crash would. For tests.
func (p *Process) Kill() {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (p *Process) aliveLocked() bool {
	if p.cmd == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Alive reports whether the browser process is running.
func (p *Process) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aliveLocked()
}

func (p *Process) setURL(url string) {
	p.mu.Lock()
	p.currentURL = url
	p.mu.Unlock()
}

// Open implements Browser.
func (p *Process) Open(url string) error {
	if !p.Alive() {
		return p.start(url)
	}
	err := p.driver.Navigate(url)
	switch {
	case err == nil:
		p.setURL(url)
		return nil
	case errors.Is(err, ErrSlow):
		// Busy with a heavy page on a Pi. Restarting would flash the text
		// console, so leave it be.
		p.mu.Lock()
		p.currentURL = url
		p.lastError = "browser slow to answer navigate; not restarting"
		p.mu.Unlock()
		slog.Warn("browser slow to answer navigate; not restarting", "url", url)
		return nil
	default:
		return p.restartAfter(fmt.Sprintf("navigate failed: %v", err), url)
	}
}

// Reload implements Browser.
func (p *Process) Reload() error {
	url := p.CurrentURL()
	if url == "" {
		return nil
	}
	if !p.Alive() {
		return p.start(url)
	}
	if err := p.driver.Reload(url); err != nil {
		return p.restartAfter(fmt.Sprintf("reload failed: %v", err), url)
	}
	return nil
}

func (p *Process) restartAfter(msg, url string) error {
	slog.Warn(msg + "; restarting browser")
	p.Stop()
	p.mu.Lock()
	p.lastError = msg
	p.restarts++
	p.mu.Unlock()
	return p.start(url)
}

// EnsureRunning implements Browser: a browser that died is restarted, with
// back-off if it keeps dying straight after starting.
func (p *Process) EnsureRunning() error {
	p.mu.Lock()
	if p.currentURL == "" || p.aliveLocked() {
		p.mu.Unlock()
		return nil
	}
	now := time.Now()
	if p.retryAt.IsZero() {
		code := "None"
		if p.cmd != nil {
			code = fmt.Sprint(p.exitCode)
		}
		p.lastError = "browser exited with code " + code
		if now.Sub(p.startedAt) < QuickExit {
			p.backoff = min(max(p.backoff*2, 2*time.Second), MaxBackoff)
		} else {
			p.backoff = 0
		}
		p.retryAt = now.Add(p.backoff)
		slog.Warn(p.lastError+"; restarting", "in", p.backoff)
	}
	url := p.currentURL
	due := !now.Before(p.retryAt)
	if due {
		p.retryAt = time.Time{}
	}
	p.mu.Unlock()
	if due {
		return p.start(url)
	}
	return nil
}

// Stop stops the browser and everything it started.
func (p *Process) Stop() {
	p.mu.Lock()
	cmd, done, alive := p.cmd, p.done, p.aliveLocked()
	if !alive {
		p.cmd = nil
	}
	p.mu.Unlock()
	if !alive {
		return
	}
	terminate(cmd)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		kill(cmd)
		<-done
	}
	p.mu.Lock()
	p.cmd = nil
	p.mu.Unlock()
}

// Status implements Browser.
func (p *Process) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := Status{Driver: p.driver.Name(), Running: p.aliveLocked(), Restarts: p.restarts, LastError: p.lastError}
	if st.Running {
		pid := p.cmd.Process.Pid
		st.PID = &pid
	}
	if p.currentURL != "" {
		u := p.currentURL
		st.URL = &u
	}
	return st
}

func (p *Process) start(url string) error {
	argv, err := p.driver.Command(url)
	if err != nil {
		p.mu.Lock()
		p.currentURL = url
		p.mu.Unlock()
		return err
	}
	slog.Info("starting browser: " + strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	newSession(cmd)
	done := make(chan struct{})
	p.mu.Lock()
	if p.cmd != nil {
		p.restarts++
	}
	p.currentURL = url
	p.startedAt = time.Now()
	if err := cmd.Start(); err != nil {
		// Counts as an immediate exit, so EnsureRunning backs off.
		p.cmd = cmd
		p.exitCode = 127
		p.done = done
		close(done)
		p.mu.Unlock()
		return err
	}
	p.cmd, p.done = cmd, done
	p.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		if p.done == done {
			p.exitCode = cmd.ProcessState.ExitCode()
		}
		p.mu.Unlock()
		close(done)
	}()
	p.driver.AfterStart(p.Alive)
	return nil
}

// Create makes the named driver's browser.
func Create(name string, extraArgs []string) (*Process, error) {
	mk, ok := drivers[name]
	if !ok {
		names := make([]string, 0, len(drivers))
		for n := range drivers {
			names = append(names, "'"+n+"'")
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown browser '%s', expected one of [%s]", name, strings.Join(names, ", "))
	}
	return New(mk(extraArgs)), nil
}

var drivers = map[string]func([]string) Driver{
	"cog":      func(a []string) Driver { return &Cog{Args: a} },
	"chromium": func(a []string) Driver { return &Chromium{Args: a, DebugPort: 9222} },
	"dummy":    func(a []string) Driver { return Dummy{} },
}

func which(binary string) (string, error) {
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("%s not found; run sudo eventscreen-setup --browser <name>", binary)
	}
	return path, nil
}
