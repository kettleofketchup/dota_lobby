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
	"github.com/kettleofketchup/dota_lobby/pkg/league"
	"github.com/kettleofketchup/dota_lobby/pkg/pool"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// httpShutdownTimeout bounds in-flight requests at shutdown; the
// bots then get their own budget (bot.DefaultShutdownBudget).
const httpShutdownTimeout = 5 * time.Second

// serve runs the service until ctx ends (SIGINT/SIGTERM) or the listener
// fails, then shuts down in this order: fail /readyz, stop HTTP, stop the
// league registry, stop the bots (releasing the state-dir lock), close the
// database.
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
	// The database opens under the state-dir lock that Start took, so only
	// one process ever migrates or writes it.
	db, err := store.Open(ctx, cfg.Store.Path)
	if err != nil {
		_ = ln.Close()
		return errors.Join(err, stopBots(mgr, log))
	}
	defer closeStore(db, log)
	reg := league.New(db.Leagues(), mgr, cfg.League, cfg.Bots, league.Options{Logger: log})
	if err := reg.Load(ctx); err != nil {
		log.Warn("could not load stored league lists; starting empty", "err", err)
	}
	regCtx, stopReg := context.WithCancel(context.WithoutCancel(ctx))
	regDone := make(chan struct{})
	go func() { defer close(regDone); reg.Run(regCtx) }()
	bots := pool.New(mgr, reg, pool.Options{})

	srv := api.New(api.Options{
		Addr:             addr,
		APIKey:           cfg.Secrets.APIKey,
		Bots:             mgr,
		Leagues:          reg,
		Leases:           bots,
		MinReadyBots:     cfg.Readiness.MinReadyBots,
		GCRequestTimeout: cfg.GC.RequestTimeout,
		MaxBodyBytes:     cfg.Server.MaxBodyBytes,
		Logger:           log,
		Version:          version,
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
	stopReg()
	<-regDone
	if err := stopBots(mgr, log); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr == nil {
		log.Info("stopped cleanly")
	}
	return runErr
}

// closeStore closes the database last, after everything that writes to it
// has stopped.
func closeStore(db *store.Store, log *slog.Logger) {
	if err := db.Close(); err != nil {
		log.Error("database did not close cleanly", "err", err)
	}
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
