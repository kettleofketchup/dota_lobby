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
	"github.com/kettleofketchup/dota_lobby/pkg/events"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
	"github.com/kettleofketchup/dota_lobby/pkg/pool"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
	"github.com/kettleofketchup/dota_lobby/pkg/webhook"
)

// httpShutdownTimeout bounds in-flight requests at shutdown; the
// bots then get their own budget (bot.DefaultShutdownBudget).
const httpShutdownTimeout = 5 * time.Second

// serve runs the service until ctx ends (SIGINT/SIGTERM) or the listener
// fails, then shuts down in this order: fail /readyz, stop HTTP, drain the
// lobbies, stop webhook delivery, stop the league registry, stop the bots
// (releasing the state-dir lock), close the database.
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
	storeOpts := []store.Option{store.WithLogger(log)}
	if b := cfg.Store.Backup; b.Enabled {
		storeOpts = append(storeOpts, store.WithPreMigrateBackup(b.Dir, b.Keep))
	} else {
		log.Warn("pre-migration backups are disabled (store.backup.enabled: false)")
	}
	db, err := store.Open(ctx, cfg.Store.Path, storeOpts...)
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
	bus := events.New(log)
	lobbies := lobby.New(lobby.Options{
		// Each event is queued for its webhooks in the transaction that
		// stores it, so a crash cannot lose a delivery.
		Store: db.Lobbies().WithWebhooks(webhook.Route(cfg.Webhooks)), Pool: bots, Holders: reg, Bots: mgr,
		Lobby: cfg.Lobby, GC: cfg.GC, Retention: cfg.Store.Retention, Logger: log, Hooks: bus,
	})
	// Re-adopts the lobbies a previous run left; their bots are leased
	// again before any new lobby can take them.
	if err := lobbies.Start(ctx); err != nil {
		_ = ln.Close()
		drainLobbies(lobbies, cfg.Lobby.DrainTimeout, log)
		stopReg()
		<-regDone
		return errors.Join(err, stopBots(mgr, log))
	}
	wake, unsubscribe := bus.Subscribe(nil)
	deliverer := webhook.New(webhook.Options{
		Queue: db.Deliveries(), Webhooks: cfg.Webhooks, Secrets: cfg.Secrets, Delivery: cfg.WebhookDelivery,
		Wake: wake, Version: version, Logger: log,
	})
	hookCtx, stopHooks := context.WithCancel(context.WithoutCancel(ctx))
	hooksDone := make(chan struct{})
	go func() { defer close(hooksDone); deliverer.Run(hookCtx) }()
	if len(cfg.Webhooks) > 0 {
		log.Info("webhooks enabled", "webhooks", len(cfg.Webhooks))
	}

	srv := api.New(api.Options{
		Addr:             addr,
		APIKey:           cfg.Secrets.APIKey,
		Bots:             mgr,
		Leagues:          reg,
		Leases:           bots,
		Lobbies:          lobbies,
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
	drainLobbies(lobbies, cfg.Lobby.DrainTimeout, log)
	// After the drain, so the events it writes are queued first; whatever
	// is still pending goes out on the next start.
	stopHooks()
	<-hooksDone
	unsubscribe()
	log.Info("webhook delivery stopped")
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

// drainLobbies stops the lobby trackers before the bots go, so none of
// them sees its bot disconnect; see lobby.Manager.Drain.
func drainLobbies(m *lobby.Manager, budget time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := m.Drain(ctx); err != nil {
		log.Warn("lobby drain incomplete", "err", err)
		return
	}
	log.Info("lobbies drained")
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
