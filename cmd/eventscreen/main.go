// Command eventscreen is the EventScreenDisplay viewer agent: it drives a
// kiosk browser through a playlist of web pages and exposes a small HTTP
// API so a central manager can control it.
//
//	eventscreen [run] [--config PATH] [--browser NAME] [--port N] [--state-dir DIR] [-v]
//	eventscreen wifi  [--config PATH] [-v]     Wi-Fi setup hotspot and Wi-Fi API (root)
//	eventscreen avahi [--config PATH] [--output FILE]   write the mDNS service file (root)
//	eventscreen check [--config PATH] [--json]  on-device health check
//	eventscreen version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/api"
	"github.com/RCRaceHub/EventScreenDisplay/internal/avahi"
	"github.com/RCRaceHub/EventScreenDisplay/internal/browser"
	"github.com/RCRaceHub/EventScreenDisplay/internal/check"
	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
	"github.com/RCRaceHub/EventScreenDisplay/internal/messages"
	"github.com/RCRaceHub/EventScreenDisplay/internal/player"
	"github.com/RCRaceHub/EventScreenDisplay/internal/playlist"
	"github.com/RCRaceHub/EventScreenDisplay/internal/stage"
	"github.com/RCRaceHub/EventScreenDisplay/internal/version"
	"github.com/RCRaceHub/EventScreenDisplay/internal/wifi"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	cmd := "run"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "run":
		return agent(args)
	case "wifi":
		return wifiCmd(args)
	case "avahi":
		return avahiCmd(args)
	case "check":
		return checkCmd(args)
	case "version":
		fmt.Println(version.Version)
		return 0
	case browser.IdleArg:
		browser.Idle()
		return 0
	case "help":
		fmt.Println("usage: eventscreen [run|wifi|avahi|check|version] [options]; eventscreen <command> -h for options")
		return 0
	}
	fmt.Fprintf(os.Stderr, "eventscreen: unknown command %q (run, wifi, avahi, check, version)\n", cmd)
	return 2
}

func setupLog(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	// systemd's journal adds the time.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{}
			}
			return a
		},
	})))
}

func loadConfig(path string) (config.Config, bool) {
	cfg, err := config.Load(path)
	if err != nil {
		slog.Error("bad config", "err", err)
		return cfg, false
	}
	return cfg, true
}

func agent(args []string) int {
	fs := flag.NewFlagSet("eventscreen", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath, "config file")
	browserName := fs.String("browser", "", "override the browser driver from the config")
	port := fs.Int("port", 0, "override the API port from the config")
	stateDir := fs.String("state-dir", "", "override the state directory from the config")
	verbose := fs.Bool("v", false, "debug logging")
	_ = fs.Parse(args)
	setupLog(*verbose)

	cfg, ok := loadConfig(*cfgPath)
	if !ok {
		return 1
	}
	if *browserName != "" {
		cfg.Browser = *browserName
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *stateDir != "" {
		cfg.StateDir = *stateDir
	}

	proc, err := browser.Create(cfg.Browser, cfg.BrowserArgs)
	if err != nil {
		slog.Error(err.Error())
		return 1
	}
	var b browser.Browser = proc
	base := "http://127.0.0.1:" + strconv.Itoa(cfg.Port)
	msgs := messages.New(cfg.MessagesPath(), nil)
	var stg *stage.Stage
	if cfg.Preload {
		stg = stage.New(msgs)
		b = stage.NewBrowser(proc, stg, base+"/stage", nil)
	}
	p := player.New(b, playlist.Store{Path: cfg.PlaylistPath()}, player.Options{
		IdleURL:      base + "/idle",
		MessageURL:   base + "/message",
		Messages:     msgs,
		PreloadAhead: float64(cfg.PreloadAhead),
	})
	a := api.NewAgent(cfg, p, stg, nil)

	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Listen, strconv.Itoa(cfg.Port)))
	if err != nil {
		slog.Error("can't listen", "err", err)
		return 1
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	slog.Info(fmt.Sprintf("eventscreen %s (%s) listening on %s, browser=%s", version.Version, cfg.ScreenID, ln.Addr(), cfg.Browser))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	p.Start()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	code := 0
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "err", err)
			code = 1
		}
	}
	// Long-polls from the stage page are cut short rather than waited for.
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Shutdown(sctx)
	cancel()
	p.Stop()
	return code
}

func wifiCmd(args []string) int {
	fs := flag.NewFlagSet("eventscreen wifi", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath, "config file")
	verbose := fs.Bool("v", false, "debug logging")
	_ = fs.Parse(args)
	setupLog(*verbose)
	cfg, ok := loadConfig(*cfgPath)
	if !ok {
		return 1
	}
	if err := wifi.Run(cfg); err != nil {
		slog.Error(err.Error())
		return 1
	}
	return 0
}

func avahiCmd(args []string) int {
	fs := flag.NewFlagSet("eventscreen avahi", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath, "config file")
	output := fs.String("output", avahi.ServicePath, "service file to write")
	_ = fs.Parse(args)
	setupLog(false)
	cfg, ok := loadConfig(*cfgPath)
	if !ok {
		return 1
	}
	if err := avahi.Write(cfg, *cfgPath, *output); err != nil {
		slog.Error(err.Error())
		return 1
	}
	return 0
}

func checkCmd(args []string) int {
	fs := flag.NewFlagSet("eventscreen-check", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath, "config file")
	asJSON := fs.Bool("json", false, "print results as JSON")
	_ = fs.Parse(args)
	return check.Main(*cfgPath, *asJSON, os.Stdout, nil)
}
