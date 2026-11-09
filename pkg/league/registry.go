// Package league knows which bot may host which league's lobbies. The
// authoritative source is the game coordinator's admin league list (7633
// -> 7634), fetched for every bot when it becomes ready and again every
// league.refresh_interval. A bot's configured leagues are an unverified
// fallback used only until its GC list has been fetched once, or always
// with league.allow_unverified. See docs/research/02-league-tickets.md.
package league

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// Sources of a holding.
const (
	SourceGC     = "gc"
	SourceConfig = "config"
)

// Defaults for Options.
const (
	DefaultPollInterval  = 2 * time.Second
	DefaultRetryInterval = time.Minute
	// DefaultMinRefreshInterval is how recent a bot's last successful
	// fetch may be before Refresh and RefreshAll skip asking the GC again.
	DefaultMinRefreshInterval = 15 * time.Second
)

// persistTimeout bounds the database write after a refresh. The write
// does not use the caller's ctx: a request that times out or goes away
// after the GC answered must not leave memory and the database apart.
const persistTimeout = 5 * time.Second

var (
	// ErrUnknownLeague means no configured bot holds the league, ready or
	// not.
	ErrUnknownLeague = errors.New("league: no configured bot holds this league")
	// ErrNoReadyHolder means bots hold the league but none is ready.
	ErrNoReadyHolder = errors.New("league: no ready bot holds this league")
	// ErrGCSourceDisabled means league.sources does not include "gc", so
	// there is no GC list to refresh.
	ErrGCSourceDisabled = errors.New(`league: the "gc" source is not enabled`)
	// ErrNotAsked means ctx ended before the bot's turn came, so the GC
	// was not asked and the bot's previous list stays. It is wrapped
	// together with ctx's error.
	ErrNotAsked = errors.New("league: the game coordinator was not asked")
)

// MaxRecentChanges is how many of a bot's league changes the registry
// keeps in memory for listings.
const MaxRecentChanges = 50

// refreshAllWorkers bounds how many bots RefreshAll asks at once. Each bot
// has its own GC connection, so this only limits the burst.
const refreshAllWorkers = 4

// Holding is one bot's claim on a league.
type Holding struct {
	Bot string
	// Verified is true when the GC's admin list names the league (or the
	// league is 0, which every bot may host).
	Verified bool
	// Source is SourceGC or SourceConfig; empty for league 0.
	Source string
}

// LeagueBot is one holder of a league, for listings.
type LeagueBot struct {
	Name     string
	Source   string
	Verified bool
	// RefreshedAt is when the GC list was fetched; zero for the config
	// source.
	RefreshedAt time.Time
	// FirstSeenAt is when the league first appeared in the bot's GC list;
	// zero for the config source.
	FirstSeenAt time.Time
	// Stale means the GC list was loaded from the database and has not been
	// fetched again since this process started.
	Stale bool
}

// BotLeague is one league a bot holds.
type BotLeague struct {
	LeagueID uint32
	// Name is from the GC list; empty when unknown.
	Name string
	LeagueBot
}

// Change is a league appearing in or disappearing from a bot's GC list.
type Change struct {
	LeagueID uint32
	// Kind is store.ChangeAdded or store.ChangeRemoved.
	Kind string
	At   time.Time
}

// Inventory is everything known about one bot's leagues.
type Inventory struct {
	Bot     string
	Leagues []BotLeague
	// OKAt is the last successful GC refresh; zero if none.
	OKAt time.Time
	// Error is the last refresh failure since OKAt, if any.
	Error   string
	ErrorAt time.Time
	// Changes are the most recent changes, newest first, at most
	// MaxRecentChanges.
	Changes []Change
}

// RefreshResult is one bot's outcome in RefreshAll.
type RefreshResult struct {
	Bot string
	// NotReady means the bot was not gc_ready and was not asked.
	NotReady bool
	// Err is the refresh failure, if any.
	Err error
}

// League is one league and the bots that hold it.
type League struct {
	LeagueID uint32
	// Name is from the GC list; empty when unknown.
	Name string
	Bots []LeagueBot
}

// Store is the persistence the registry uses; *store.Leagues implements
// it.
type Store interface {
	ReplaceBotLeagues(ctx context.Context, bot, source string, ids []uint32, at time.Time) error
	RecordRefreshError(ctx context.Context, bot string, err error, at time.Time) error
	BotLeagues(ctx context.Context) (map[string]store.BotLeagueSet, error)
	LeagueChanges(ctx context.Context, perBot int) (map[string][]store.LeagueChange, error)
}

// Options configures a Registry.
type Options struct {
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// PollInterval is how often bot readiness is checked; 0 means
	// DefaultPollInterval.
	PollInterval time.Duration
	// RetryInterval is the wait after a failed refresh, capped at the
	// refresh interval; 0 means DefaultRetryInterval.
	RetryInterval time.Duration
	// MinRefreshInterval is how recent a bot's last successful fetch, in
	// the same GC session, may be before Refresh and RefreshAll return it
	// instead of asking the GC again. Run does not use it. 0 means
	// DefaultMinRefreshInterval; negative disables it.
	MinRefreshInterval time.Duration
}

// Registry tracks every bot's leagues. Its methods are safe for concurrent
// use.
type Registry struct {
	store  Store
	bots   bot.Source
	cfg    config.LeagueConfig
	opts   Options
	log    *slog.Logger
	useGC  bool
	useCfg bool
	// configured is each bot's static list, keyed by lowercased name.
	configured map[string][]uint32

	loadOnce sync.Once

	mu    sync.Mutex
	state map[string]*botState // keyed by lowercased name
	names map[uint32]string
	// refreshing holds one token per bot, so one bot's refreshes (Run's
	// and an operator's) never overlap. Keyed by lowercased name.
	refreshing map[string]chan struct{}
}

type botState struct {
	gcKnown     bool
	gc          []uint32 // sorted
	firstSeen   map[uint32]time.Time
	changes     []Change // newest first
	refreshedAt time.Time
	stale       bool
	lastErr     string
	lastErrAt   time.Time

	// Owned by Run.
	seenReadySince time.Time
	nextAttempt    time.Time
}

// New builds a registry. st may be nil, which keeps everything in memory.
// botCfgs supplies the configured fallback lists.
func New(st Store, bots bot.Source, cfg config.LeagueConfig, botCfgs []config.BotConfig, opts Options) *Registry {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = DefaultRetryInterval
	}
	if opts.MinRefreshInterval == 0 {
		opts.MinRefreshInterval = DefaultMinRefreshInterval
	}
	if cfg.RefreshInterval > 0 && opts.RetryInterval > cfg.RefreshInterval {
		opts.RetryInterval = cfg.RefreshInterval
	}
	r := &Registry{
		store:      st,
		bots:       bots,
		cfg:        cfg,
		opts:       opts,
		log:        opts.Logger,
		useGC:      slices.Contains(cfg.Sources, SourceGC),
		useCfg:     slices.Contains(cfg.Sources, SourceConfig),
		configured: map[string][]uint32{},
		state:      map[string]*botState{},
		names:      map[uint32]string{},
		refreshing: map[string]chan struct{}{},
	}
	for _, b := range botCfgs {
		ids := slices.Clone(b.Leagues)
		slices.Sort(ids)
		r.configured[key(b.Name)] = slices.Compact(ids)
	}
	return r
}

func key(name string) string { return strings.ToLower(name) }

// Load reads the lists persisted by an earlier run, marked stale until
// they are fetched again. Run calls it; calling it first makes the lists
// available before Run starts. Only the first call reads the store.
func (r *Registry) Load(ctx context.Context) error {
	var err error
	r.loadOnce.Do(func() { err = r.load(ctx) })
	return err
}

func (r *Registry) load(ctx context.Context) error {
	if r.store == nil || !r.useGC {
		return nil
	}
	sets, err := r.store.BotLeagues(ctx)
	if err != nil {
		return fmt.Errorf("load bot leagues: %w", err)
	}
	changes, err := r.store.LeagueChanges(ctx, MaxRecentChanges)
	if err != nil {
		return fmt.Errorf("load bot league changes: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, set := range sets {
		bs := r.botLocked(name)
		if bs.gcKnown && !bs.stale {
			continue // already fetched in this process
		}
		if !set.OKAt.IsZero() {
			src := set.Sources[SourceGC]
			bs.gcKnown = true
			bs.gc = slices.Clone(src.IDs)
			bs.firstSeen = maps.Clone(src.FirstSeen)
			bs.refreshedAt = set.OKAt
			bs.stale = true
		}
		bs.lastErr, bs.lastErrAt = set.Error, set.ErrorAt
	}
	for name, list := range changes {
		bs := r.botLocked(name)
		if len(bs.changes) > 0 {
			continue // already refreshed in this process
		}
		for _, c := range list {
			if c.Source == SourceGC {
				bs.changes = append(bs.changes, Change{LeagueID: c.LeagueID, Kind: c.Change, At: c.At})
			}
		}
	}
	return nil
}

func (r *Registry) botLocked(name string) *botState {
	k := key(name)
	bs, ok := r.state[k]
	if !ok {
		bs = &botState{}
		r.state[k] = bs
	}
	return bs
}

// Run loads the persisted lists, then refreshes each bot's GC list when it
// becomes ready (a new Status.ReadySince) and every
// league.refresh_interval while it stays ready, until ctx ends. Without
// "gc" in league.sources it only loads.
func (r *Registry) Run(ctx context.Context) {
	if err := r.Load(ctx); err != nil {
		r.log.Warn("could not load stored league lists; starting empty", "err", err)
	}
	if !r.useGC {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(r.opts.PollInterval)
	defer t.Stop()
	for {
		r.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// poll refreshes every bot that is due, one at a time; each refresh is a
// single GC request on that bot's worker.
func (r *Registry) poll(ctx context.Context) {
	for _, name := range r.bots.Names() {
		if ctx.Err() != nil {
			return
		}
		h, ok := r.bots.Bot(name)
		if !ok {
			continue
		}
		st := h.Status()
		if !st.Ready || st.ReadySince.IsZero() {
			continue
		}
		now := r.opts.Now()
		r.mu.Lock()
		bs := r.botLocked(name)
		due := !st.ReadySince.Equal(bs.seenReadySince) || !now.Before(bs.nextAttempt)
		r.mu.Unlock()
		if due {
			// refresh logs and records its own failures.
			_ = r.refresh(ctx, h, st.ReadySince, 0)
		}
	}
}

// Refresh fetches one bot's GC list now, as Run does for a due bot,
// unless the last successful fetch in the bot's current GC session is
// newer than Options.MinRefreshInterval; then it keeps that list and asks
// nothing. Concurrent calls for one bot run one after another, so the
// later ones find the fresh list. It fails with bot.ErrUnknownBot, with
// ErrGCSourceDisabled, or with bot.ErrNotReady when the bot is not
// gc_ready; those are not recorded as refresh failures. Neither is ctx
// being cancelled; ctx's deadline passing during the GC request is.
func (r *Registry) Refresh(ctx context.Context, name string) error {
	h, ok := r.bots.Bot(name)
	if !ok {
		return fmt.Errorf("refresh leagues: %w: %q", bot.ErrUnknownBot, name)
	}
	if !r.useGC {
		return fmt.Errorf("refresh leagues of %s: %w", h.Name(), ErrGCSourceDisabled)
	}
	st := h.Status()
	if !st.Ready {
		return fmt.Errorf("refresh leagues of %s: %w", h.Name(), bot.ErrNotReady)
	}
	return r.refresh(ctx, h, st.ReadySince, r.opts.MinRefreshInterval)
}

// RefreshAll refreshes every gc_ready bot as Refresh does, a few at a
// time, and reports each enabled bot's outcome in config order. A bot
// that is not ready is reported as NotReady and not asked. A bot whose
// turn had not come when ctx ended fails with ErrNotAsked.
func (r *Registry) RefreshAll(ctx context.Context) ([]RefreshResult, error) {
	if !r.useGC {
		return nil, fmt.Errorf("refresh leagues: %w", ErrGCSourceDisabled)
	}
	names := r.bots.Names()
	out := make([]RefreshResult, len(names))
	sem := make(chan struct{}, refreshAllWorkers)
	var wg sync.WaitGroup
	for i, name := range names {
		out[i].Bot = name
		h, ok := r.bots.Bot(name)
		if !ok {
			out[i].Err = fmt.Errorf("refresh leagues: %w: %q", bot.ErrUnknownBot, name)
			continue
		}
		st := h.Status()
		if !st.Ready {
			out[i].NotReady = true
			continue
		}
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				out[i].Err = r.refresh(ctx, h, st.ReadySince, r.opts.MinRefreshInterval)
			case <-ctx.Done():
				out[i].Err = notAsked(name, ctx.Err())
			}
		})
	}
	wg.Wait()
	return out, nil
}

// lockRefresh waits for name's refresh token; the returned func gives it
// back.
func (r *Registry) lockRefresh(ctx context.Context, name string) (func(), error) {
	k := key(name)
	r.mu.Lock()
	tok, ok := r.refreshing[k]
	if !ok {
		tok = make(chan struct{}, 1)
		r.refreshing[k] = tok
	}
	r.mu.Unlock()
	select {
	case tok <- struct{}{}:
		return func() { <-tok }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func notAsked(name string, err error) error {
	return fmt.Errorf("refresh leagues of %s: %w: %w", name, ErrNotAsked, err)
}

// refresh fetches name's GC list and records the outcome. With minAge > 0
// it keeps a list fetched successfully less than minAge ago in the same
// GC session instead of asking the GC.
func (r *Registry) refresh(ctx context.Context, h bot.Handle, readySince time.Time, minAge time.Duration) error {
	name := h.Name()
	unlock, err := r.lockRefresh(ctx, name)
	if err != nil {
		return notAsked(name, err)
	}
	defer unlock()
	if minAge > 0 && r.fresh(name, readySince, minAge) {
		r.log.Debug("league list is fresh; not asking the game coordinator again", "bot", name)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return notAsked(name, err)
	}
	var infos []gc.LeagueInfo
	err = h.Do(ctx, "list admin leagues", func(ctx context.Context, c gc.Client) error {
		var err error
		infos, err = c.ListAdminLeagues(ctx)
		return err
	})
	now := r.opts.Now()
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		// Shutting down, or the caller went away; not the bot's fault. A
		// passed deadline is recorded below: the GC did not answer in time.
		return fmt.Errorf("refresh leagues of %s: %w", name, err)
	}

	r.mu.Lock()
	bs := r.botLocked(name)
	bs.seenReadySince = readySince
	if err != nil {
		bs.nextAttempt = now.Add(r.opts.RetryInterval)
		bs.lastErr, bs.lastErrAt = err.Error(), now
		r.mu.Unlock()
		r.log.Warn("league list refresh failed", "bot", name, "err", err, "retry_in", r.opts.RetryInterval,
			"fallback", r.fallbackNote(name))
		if r.store != nil {
			pctx, cancel := persistContext(ctx)
			defer cancel()
			if serr := r.store.RecordRefreshError(pctx, name, err, now); serr != nil {
				r.log.Warn("could not store league refresh error", "bot", name, "err", serr)
			}
		}
		return fmt.Errorf("refresh leagues of %s: %w", name, err)
	}
	ids := make([]uint32, 0, len(infos))
	for _, li := range infos {
		ids = append(ids, li.LeagueID)
		if li.Name != "" {
			r.names[li.LeagueID] = li.Name
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	added, removed := diff(bs.gc, ids)
	if bs.firstSeen == nil {
		bs.firstSeen = map[uint32]time.Time{}
	}
	for _, id := range removed {
		delete(bs.firstSeen, id)
	}
	for _, id := range ids {
		if _, ok := bs.firstSeen[id]; !ok {
			bs.firstSeen[id] = now
		}
	}
	var changes []Change
	for _, id := range removed {
		changes = append(changes, Change{LeagueID: id, Kind: store.ChangeRemoved, At: now})
	}
	for _, id := range added {
		changes = append(changes, Change{LeagueID: id, Kind: store.ChangeAdded, At: now})
	}
	slices.Reverse(changes) // newest first, added before removed as the store lists them
	bs.changes = append(changes, bs.changes...)
	if len(bs.changes) > MaxRecentChanges {
		bs.changes = bs.changes[:MaxRecentChanges]
	}
	bs.gcKnown, bs.gc, bs.refreshedAt, bs.stale = true, ids, now, false
	bs.lastErr, bs.lastErrAt = "", time.Time{}
	bs.nextAttempt = now.Add(r.cfg.RefreshInterval)
	r.mu.Unlock()

	r.log.Info("league list refreshed", "bot", name, "leagues", ids)
	if len(added) > 0 || len(removed) > 0 {
		r.log.Info("bot league tickets changed", "bot", name, "added", added, "removed", removed)
	}
	if r.useCfg {
		var missing []uint32
		for _, id := range r.configured[key(name)] {
			if _, found := slices.BinarySearch(ids, id); !found {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			r.log.Warn("configured leagues are not in the bot's GC admin list", "bot", name, "leagues", missing,
				"used_anyway", r.cfg.AllowUnverified)
		}
	}
	if r.store != nil {
		pctx, cancel := persistContext(ctx)
		defer cancel()
		if serr := r.store.ReplaceBotLeagues(pctx, name, SourceGC, ids, now); serr != nil {
			r.log.Warn("could not store league list", "bot", name, "err", serr)
		}
	}
	return nil
}

// fresh reports whether name's GC list was fetched successfully less
// than minAge ago in the GC session that became ready at readySince.
func (r *Registry) fresh(name string, readySince time.Time, minAge time.Duration) bool {
	now := r.opts.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	bs := r.botLocked(name)
	return bs.gcKnown && !bs.stale && bs.lastErr == "" && bs.seenReadySince.Equal(readySince) &&
		now.Sub(bs.refreshedAt) < minAge
}

// persistContext is ctx's values without its cancellation or deadline,
// bounded by persistTimeout.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

func (r *Registry) fallbackNote(name string) string {
	switch {
	case !r.useCfg:
		return "none"
	case len(r.configured[key(name)]) == 0:
		return "none (no leagues configured)"
	default:
		return "configured leagues, unverified"
	}
}

// Holds reports whether bot may host league L and whether the GC confirmed
// it. League 0 (no league) is held by every bot.
func (r *Registry) Holds(name string, l uint32) (verified, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.holdsLocked(name, l)
	return h.Verified, ok
}

func (r *Registry) holdsLocked(name string, l uint32) (Holding, bool) {
	if l == 0 {
		return Holding{Bot: name, Verified: true}, true
	}
	var bs botState
	if p, ok := r.state[key(name)]; ok {
		bs = *p
	}
	if bs.gcKnown {
		if _, found := slices.BinarySearch(bs.gc, l); found {
			return Holding{Bot: name, Verified: true, Source: SourceGC}, true
		}
	}
	if r.useCfg && (!bs.gcKnown || r.cfg.AllowUnverified) {
		if _, found := slices.BinarySearch(r.configured[key(name)], l); found {
			return Holding{Bot: name, Source: SourceConfig}, true
		}
	}
	return Holding{}, false
}

// Holders returns every enabled bot that holds league L, ready or not, in
// config order.
func (r *Registry) Holders(l uint32) []Holding {
	names := r.bots.Names()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Holding
	for _, n := range names {
		if h, ok := r.holdsLocked(n, l); ok {
			out = append(out, h)
		}
	}
	return out
}

// diff returns the IDs only in b (added) and only in a (removed); both
// inputs are sorted.
func diff(a, b []uint32) (added, removed []uint32) {
	for _, id := range b {
		if _, found := slices.BinarySearch(a, id); !found {
			added = append(added, id)
		}
	}
	for _, id := range a {
		if _, found := slices.BinarySearch(b, id); !found {
			removed = append(removed, id)
		}
	}
	return added, removed
}

// heldLocked lists the leagues bot name holds, by league ID.
func (r *Registry) heldLocked(name string) []BotLeague {
	var ids []uint32
	bs, known := r.state[key(name)]
	if known && bs.gcKnown {
		ids = append(ids, bs.gc...)
	}
	ids = append(ids, r.configured[key(name)]...)
	slices.Sort(ids)
	var out []BotLeague
	for _, id := range slices.Compact(ids) {
		h, ok := r.holdsLocked(name, id)
		if !ok {
			continue
		}
		lb := LeagueBot{Name: name, Source: h.Source, Verified: h.Verified}
		if h.Source == SourceGC {
			lb.RefreshedAt, lb.Stale, lb.FirstSeenAt = bs.refreshedAt, bs.stale, bs.firstSeen[id]
		}
		out = append(out, BotLeague{LeagueID: id, Name: r.names[id], LeagueBot: lb})
	}
	return out
}

// BotLeagues returns the leagues bot holds, sorted.
func (r *Registry) BotLeagues(name string) []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []uint32
	for _, bl := range r.heldLocked(name) {
		out = append(out, bl.LeagueID)
	}
	return out
}

// Inventory returns what is known about one enabled bot's leagues, or
// false for no such bot.
func (r *Registry) Inventory(name string) (Inventory, bool) {
	h, ok := r.bots.Bot(name)
	if !ok {
		return Inventory{}, false
	}
	name = h.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	inv := Inventory{Bot: name, Leagues: r.heldLocked(name)}
	if bs, ok := r.state[key(name)]; ok {
		if bs.gcKnown {
			inv.OKAt = bs.refreshedAt
		}
		inv.Error, inv.ErrorAt = bs.lastErr, bs.lastErrAt
		inv.Changes = slices.Clone(bs.changes)
	}
	return inv, true
}

// All lists every league some enabled bot holds, by league ID, with its
// holders in config order.
func (r *Registry) All() []League {
	names := r.bots.Names()
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := map[uint32]*League{}
	for _, n := range names {
		for _, bl := range r.heldLocked(n) {
			l, ok := byID[bl.LeagueID]
			if !ok {
				l = &League{LeagueID: bl.LeagueID, Name: bl.Name}
				byID[bl.LeagueID] = l
			}
			l.Bots = append(l.Bots, bl.LeagueBot)
		}
	}
	out := make([]League, 0, len(byID))
	for _, l := range byID {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b League) int { return cmp.Compare(a.LeagueID, b.LeagueID) })
	return out
}

// League returns one league with its holders, or false when no enabled
// bot holds it.
func (r *Registry) League(id uint32) (League, bool) {
	names := r.bots.Names()
	r.mu.Lock()
	defer r.mu.Unlock()
	l := League{LeagueID: id, Name: r.names[id]}
	for _, n := range names {
		for _, bl := range r.heldLocked(n) {
			if bl.LeagueID == id {
				l.Bots = append(l.Bots, bl.LeagueBot)
			}
		}
	}
	return l, len(l.Bots) > 0
}

// Nodes fetches league L's lobby nodes through a ready holder, verified
// holders first. It returns the bot that answered. A holder that fails for
// any reason but a timeout or the caller's ctx ending is skipped for the
// next one, so one dropped or failing bot does not fail the request; a
// timeout stays terminal so the caller's deadline holds. When every holder
// failed, the last error is returned, wrapped in ErrNoReadyHolder if that
// holder was no longer ready.
func (r *Registry) Nodes(ctx context.Context, l uint32) (string, []gc.LeagueNode, error) {
	holders := r.Holders(l)
	if l == 0 || len(holders) == 0 {
		return "", nil, fmt.Errorf("league %d: %w", l, ErrUnknownLeague)
	}
	slices.SortStableFunc(holders, func(a, b Holding) int {
		switch {
		case a.Verified == b.Verified:
			return 0
		case a.Verified:
			return -1
		default:
			return 1
		}
	})
	var lastBot string
	var lastErr error
	for _, hd := range holders {
		h, ok := r.bots.Bot(hd.Bot)
		if !ok {
			continue
		}
		if st := h.Status(); !st.Ready {
			continue
		}
		var nodes []gc.LeagueNode
		err := h.Do(ctx, "league nodes", func(ctx context.Context, c gc.Client) error {
			var err error
			nodes, err = c.LeagueNodes(ctx, l)
			return err
		})
		if err == nil {
			return hd.Bot, nodes, nil
		}
		lastBot, lastErr = hd.Bot, fmt.Errorf("league %d nodes via %s: %w", l, hd.Bot, err)
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return lastBot, nil, lastErr
		}
	}
	switch {
	case lastErr == nil:
		return "", nil, fmt.Errorf("league %d: %w", l, ErrNoReadyHolder)
	case notReady(lastErr):
		return lastBot, nil, fmt.Errorf("%w: %w", ErrNoReadyHolder, lastErr)
	default:
		return lastBot, nil, lastErr
	}
}

// notReady reports whether err means the bot dropped out of gc_ready
// between the Status check and the call.
func notReady(err error) bool {
	return errors.Is(err, bot.ErrNotReady) || errors.Is(err, bot.ErrStopped) ||
		errors.Is(err, gc.ErrNotReady) || errors.Is(err, gc.ErrNotConnected)
}
