package wifi

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/config"
)

// Run is "eventscreen wifi": the service's main loop. It returns when
// SIGTERM or SIGINT arrives.
func Run(cfg config.Config) error {
	nm := &NetworkManager{}
	has, err := nm.HasWifi()
	if err != nil {
		return err
	}
	if !has {
		slog.Info(fmt.Sprintf("no %s on this machine; nothing to do", Iface))
		return nil
	}
	ssid, password, err := HotspotCredentials(cfg)
	if err != nil {
		return err
	}
	fb := NewFallback(nm, cfg, NewScreen(cfg), ssid, password, nil)
	setup := NewSetupServer(fb, HotspotAddr+":80")
	fb.OnHotspotUp = setup.Start
	fb.OnHotspotDown = setup.Stop
	control, err := ServeControl(NewControl(nm, fb), ControlSocket, "eventscreen")
	if err != nil {
		return err
	}
	if cfg.WifiFallback {
		slog.Info(fmt.Sprintf("watching Wi-Fi; setup hotspot %s after %ds offline", ssid, cfg.WifiFallbackAfter))
	} else {
		slog.Info("wifi_fallback is off; serving the Wi-Fi API only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	ticker := time.NewTicker(Tick)
	defer ticker.Stop()
	for {
		if cfg.WifiFallback {
			if err := fb.Tick(); err != nil {
				slog.Error("Wi-Fi check failed", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = control.Shutdown(sctx)
			cancel()
			_ = os.Remove(ControlSocket)
			fb.Shutdown()
			return nil
		case <-ticker.C:
		}
	}
}
