package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// DefaultShutdownBudget is the per-process time Stop gives the bots to
// disconnect.
const DefaultShutdownBudget = 15 * time.Second

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Logger *slog.Logger
	// NewClient builds a bot's gc.Client; nil means gc.New.
	NewClient func(b config.BotConfig) gc.Client
	// Clock defaults to SystemClock.
	Clock Clock
	// ShutdownBudget defaults to DefaultShutdownBudget.
	ShutdownBudget time.Duration
	// Seed returns a jitter seed per bot; nil picks random seeds.
	Seed func(b config.BotConfig) uint64
}

// Manager runs a Supervisor for every enabled bot and holds the state-dir
// lock while they run.
type Manager struct {
	cfg    *config.Config
	opts   ManagerOptions
	log    *slog.Logger
	gate   *Gate
	sups   []*Supervisor
	byName map[string]*Supervisor

	mu      sync.Mutex
	lock    *auth.Lock
	started bool
	stopped bool
}

// NewManager builds one supervisor per enabled bot. Nothing connects until
// Start.
func NewManager(cfg *config.Config, opts ManagerOptions) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.ShutdownBudget <= 0 {
		opts.ShutdownBudget = DefaultShutdownBudget
	}
	if opts.NewClient == nil {
		log := opts.Logger
		opts.NewClient = func(b config.BotConfig) gc.Client {
			return gc.New(gc.Options{Logger: log.With("bot", b.Name)})
		}
	}
	m := &Manager{
		cfg:    cfg,
		opts:   opts,
		log:    opts.Logger,
		gate:   NewGate(cfg.Steam.LoginStagger),
		byName: map[string]*Supervisor{},
	}
	params := ParamsFromConfig(cfg.Steam, cfg.GC)
	for _, b := range cfg.Bots {
		if !b.Enabled {
			continue
		}
		so := SupervisorOptions{
			Params:         params,
			Store:          auth.NewStore(cfg.BotStateDir(b)),
			Gate:           m.gate,
			Clock:          opts.Clock,
			Logger:         opts.Logger,
			RequestTimeout: cfg.GC.RequestTimeout,
			TOTPSkew:       cfg.Steam.TOTPSkew,
		}
		if opts.Seed != nil {
			so.Seed = opts.Seed(b)
		}
		s := NewSupervisor(b, opts.NewClient(b), so)
		m.sups = append(m.sups, s)
		m.byName[strings.ToLower(b.Name)] = s
	}
	return m
}

// Start takes the state-dir lock and starts every bot. Logons are spaced
// steam.login_stagger apart by the shared gate. It fails with an error
// wrapping auth.ErrLocked when another process holds the state dir.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("bot manager already started")
	}
	lock, err := auth.AcquireLock(m.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("start bots: %w", err)
	}
	m.lock, m.started = lock, true
	// The supervisors outlive the caller's ctx; Stop ends them.
	runCtx := context.WithoutCancel(ctx)
	for _, s := range m.sups {
		s.Start(runCtx)
	}
	m.log.Info("bots started", "count", len(m.sups), "login_stagger", m.cfg.Steam.LoginStagger)
	return nil
}

// Stop disconnects every bot in parallel within the shutdown budget (or
// ctx, if sooner), then releases the lock. Bots that miss the budget are cut
// hard and reported in the error.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil
	}
	m.stopped = true
	ctx, cancel := context.WithTimeout(ctx, m.opts.ShutdownBudget)
	defer cancel()

	errs := make([]error, len(m.sups))
	var wg sync.WaitGroup
	for i, s := range m.sups {
		wg.Go(func() { errs[i] = s.Stop(ctx) })
	}
	wg.Wait()
	errs = append(errs, m.lock.Release())
	return errors.Join(errs...)
}

// Statuses returns every bot's status in config order.
func (m *Manager) Statuses() []Status {
	out := make([]Status, len(m.sups))
	for i, s := range m.sups {
		out[i] = s.Status()
	}
	return out
}

// Supervisor returns the bot with the given name, compared
// case-insensitively.
func (m *Manager) Supervisor(name string) (*Supervisor, bool) {
	s, ok := m.byName[strings.ToLower(name)]
	return s, ok
}

// ErrUnknownBot is returned for a bot name that is not configured.
var ErrUnknownBot = errors.New("bot: unknown bot")

// Retry asks the named bot to try again now (see Supervisor.Retry). It
// returns ErrUnknownBot when there is no such bot and ErrBusy when the
// bot's run loop does not take the request before ctx ends.
func (m *Manager) Retry(ctx context.Context, name string) error {
	s, ok := m.Supervisor(name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownBot, name)
	}
	return s.Retry(ctx)
}

// Stalled returns the names of bots whose run loop has not made progress
// for longer than maxAge, for the /livez watchdog.
func (m *Manager) Stalled(maxAge time.Duration) []string {
	var out []string
	for _, s := range m.sups {
		if s.Stalled(maxAge) {
			out = append(out, s.Name())
		}
	}
	return out
}

// ReadyCount is how many bots are in gc_ready, for /readyz.
func (m *Manager) ReadyCount() int {
	n := 0
	for _, s := range m.sups {
		if s.Ready() {
			n++
		}
	}
	return n
}
