package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/api"
	"github.com/kettleofketchup/dota_lobby/pkg/bot"
)

// httpShutdownTimeout bounds in-flight requests at shutdown; the
// bots then get their own budget (bot.DefaultShutdownBudget).
const httpShutdownTimeout = 5 * time.Second

// serve runs the service until ctx ends (SIGINT/SIGTERM) or the listener
// fails, then shuts down in this order: fail /readyz, stop HTTP, stop
// the bots, release the state-dir lock.
func (c cli) serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var cf configFlags
	cf.register(fs)
	if err := c.parse(fs, args); err != nil {
		return err
	}

	boot := newLogger(c.stderr, defaultLogConfig)
	cfg, err := cf.load(boot)
	if err != nil {
		return err
	}
	log := newLogger(c.stderr, cfg.Log)
	log.Info("starting dota_lobby", "version", version, "commit", commit, "built", buildDate,
		"bots", len(cfg.Bots), "state_dir", cfg.StateDir)

	// Bind first, so a busy port fails startup before any bot logs on.
	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	mgr := bot.NewManager(cfg, bot.ManagerOptions{Logger: log})
	if err := mgr.Start(ctx); err != nil {
		_ = ln.Close()
		return err
	}
	srv := api.New(api.Options{
		Addr:         addr,
		APIKey:       cfg.Secrets.APIKey,
		Bots:         mgr,
		MinReadyBots: cfg.Readiness.MinReadyBots,
		MaxBodyBytes: cfg.Server.MaxBodyBytes,
		Logger:       log,
		Version:      version,
	})
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		if err == nil {
			err = errors.New("stopped unexpectedly")
		}
		runErr = fmt.Errorf("api server: %w", err)
		log.Error("api server failed; shutting down", "err", err)
	}

	srv.SetDraining()
	hctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(hctx); err != nil {
		log.Warn("http shutdown incomplete", "err", err)
	}
	if err := stopBots(mgr, log); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr == nil {
		log.Info("stopped cleanly")
	}
	return runErr
}

// stopBots stops every bot within the manager's own budget; the signal ctx
// has already ended, so it is not used here.
func stopBots(mgr *bot.Manager, log *slog.Logger) error {
	if err := mgr.Stop(context.Background()); err != nil {
		log.Error("bots did not stop cleanly", "err", err)
		return fmt.Errorf("stop bots: %w", err)
	}
	return nil
}
