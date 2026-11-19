package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Cog is WPE WebKit via Cog, rendering straight to the display with
// DRM/KMS. The lightest option: no X11 or Wayland compositor is needed.
// Navigation goes over Cog's D-Bus interface using cogctl, so the agent
// must run with a session bus (the systemd unit wraps it in
// dbus-run-session).
type Cog struct{ Args []string }

func (c *Cog) Name() string { return "cog" }

func (c *Cog) Command(u string) ([]string, error) {
	cog, err := which("cog")
	if err != nil {
		return nil, err
	}
	return append(append([]string{cog, "--platform=drm"}, c.Args...), u), nil
}

func (c *Cog) Navigate(u string) error { return cogctl(15*time.Second, "open", u) }

func (c *Cog) Reload(string) error { return cogctl(15*time.Second, "reload") }

// AfterStart waits briefly for Cog to claim its D-Bus name so the next
// cogctl works.
func (c *Cog) AfterStart(alive func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && alive() {
		if cogctl(5*time.Second, "ping") == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func cogctl(timeout time.Duration, args ...string) error {
	bin, err := which("cogctl")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: cogctl %s", ErrSlow, args[0])
	}
	if err != nil {
		return fmt.Errorf("cogctl %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Chromium is Chromium in kiosk mode inside the cage Wayland compositor.
// Heavier than Cog (marginal on a Pi Zero 2 W) but useful as a fallback
// for pages WebKit renders badly. Controlled with the DevTools HTTP
// endpoints.
type Chromium struct {
	Args      []string
	DebugPort int
}

func (c *Chromium) Name() string { return "chromium" }

func (c *Chromium) Command(u string) ([]string, error) {
	cage, err := which("cage")
	if err != nil {
		return nil, err
	}
	chromium, err := which("chromium-browser")
	if err != nil {
		if chromium, err = which("chromium"); err != nil {
			return nil, err
		}
	}
	argv := []string{
		cage, "--", chromium,
		"--kiosk", "--noerrdialogs", "--disable-infobars",
		"--no-first-run", "--disable-translate", "--disable-features=Translate",
		"--check-for-update-interval=31536000", "--overscroll-history-navigation=0",
		fmt.Sprintf("--remote-debugging-port=%d", c.DebugPort),
		"--remote-allow-origins=http://127.0.0.1",
	}
	return append(append(argv, c.Args...), u), nil
}

func (c *Chromium) devtools(method, path string, out any) error {
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", c.DebugPort, path), nil)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("devtools %s: %s", path, resp.Status)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

type target struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Navigate opens url in a new tab and closes the old ones, which is
// smoother than navigating the existing tab.
func (c *Chromium) Navigate(u string) error {
	var all []target
	if err := c.devtools("GET", "/json/list", &all); err != nil {
		return err
	}
	var created target
	if err := c.devtools("PUT", "/json/new?"+url.QueryEscape(u), &created); err != nil {
		return err
	}
	if created.ID == "" {
		return errors.New("devtools didn't open a tab")
	}
	if err := c.devtools("GET", "/json/activate/"+created.ID, nil); err != nil {
		return err
	}
	for _, t := range all {
		if t.Type == "page" {
			_ = c.devtools("GET", "/json/close/"+t.ID, nil)
		}
	}
	return nil
}

func (c *Chromium) Reload(u string) error { return c.Navigate(u) }

func (c *Chromium) AfterStart(alive func() bool) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && alive() {
		if c.devtools("GET", "/json/version", nil) == nil {
			return
		}
		time.Sleep(time.Second)
	}
}

// Dummy has no display at all; it just logs. For development, tests and
// the Docker image.
type Dummy struct{}

func (Dummy) Name() string { return "dummy" }

// Command starts a stand-in process, so restarts and crashes behave like a
// real browser's.
func (Dummy) Command(string) ([]string, error) { return idleCommand() }

func (Dummy) Navigate(u string) error {
	slog.Info("dummy browser -> " + u)
	return nil
}

func (Dummy) Reload(string) error {
	slog.Info("dummy browser reload")
	return nil
}

func (Dummy) AfterStart(func() bool) {}
