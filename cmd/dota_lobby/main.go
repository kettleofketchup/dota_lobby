package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/api"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// offlineBots reports the configured bots as not ready. It stands in for the
// bot supervisor, which is not wired up yet.
type offlineBots []config.BotConfig

func (b offlineBots) ListBots() map[string]bool {
	out := make(map[string]bool, len(b))
	for _, bot := range b {
		out[bot.Name] = false
	}
	return out
}

func (offlineBots) AvailableBot() (string, error) {
	return "", errors.New("bot runtime not implemented yet")
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	log.Info("starting dota_lobby", "version", version, "commit", commit, "built", buildDate)

	cfg, err := config.Load(config.Options{Logger: log})
	if err != nil {
		return err
	}
	log.Info("configuration loaded", "host", cfg.Server.Host, "port", cfg.Server.Port, "bots", len(cfg.Bots))

	srv := api.NewServer(cfg.Server.Host, cfg.Server.Port, cfg.Secrets.APIKey, offlineBots(cfg.Bots))
	errc := make(chan error, 1)
	go func() { errc <- srv.Start() }()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errc:
		return err
	case <-quit:
	}

	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
