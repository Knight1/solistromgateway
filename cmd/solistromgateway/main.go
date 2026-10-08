// Command solistromgateway reads solar hardware on the local network and
// pushes its readings to Solistrom.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/push"
	"github.com/Knight1/solistromgateway/internal/runner"
	"github.com/Knight1/solistromgateway/internal/source"
	"github.com/Knight1/solistromgateway/internal/source/ahoydtu"
	"github.com/Knight1/solistromgateway/internal/source/growatt"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the configuration file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(log)

	devices, err := buildDevices(cfg)
	if err != nil {
		return err
	}

	for _, d := range cfg.Devices {
		for _, setting := range d.InapplicableSettings() {
			log.Warn("setting has no effect for this device type and is being ignored",
				"device", d.Name, "type", d.Type, "setting", setting)
		}
		if d.IntervalIsSlow() {
			log.Warn("interval is long; readings will look stale in the app",
				"device", d.Name, "interval", d.Interval.Duration())
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "devices", len(devices))
	runner.Run(ctx, devices, push.New(), log)
	log.Info("stopped")
	return nil
}

// buildDevices turns validated config into runnable devices.
// buildDevices turns validated config into runnable devices.
func buildDevices(cfg *config.Config) ([]runner.Device, error) {
	devices := make([]runner.Device, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		var src source.Source
		switch d.Type {
		case config.TypeGrowatt:
			src = growatt.New(d)
		case config.TypeAhoyDTU:
			src = ahoydtu.New(d)
		default:
			return nil, fmt.Errorf("device %q: type %q cannot be built", d.Name, d.Type)
		}

		devices = append(devices, runner.Device{
			Name:     d.Name,
			Source:   src,
			PushURL:  d.PushURL,
			Interval: d.Interval.Duration(),
			Timeout:  d.Timeout.Duration(),
			Attempts: d.Retry.AttemptCount(),
			Backoff:  d.Retry.Backoff.Duration(),
		})
	}
	return devices, nil
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
