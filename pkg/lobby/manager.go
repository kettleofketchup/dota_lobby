package lobby

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
	"github.com/kettleofketchup/dota_lobby/pkg/pool"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// Defaults for Options.
const (
	DefaultTick          = 5 * time.Second
	DefaultSweepInterval = time.Hour
	// storeTimeout bounds one database write. Writes run on their own ctx
	// so a cancelled request or tracker never leaves half a change.
	storeTimeout = 5 * time.Second
	// seatRetry spaces host seat attempts when every strategy failed.
	seatRetry = 30 * time.Second
)

// Store is the lobby repository; *store.Lobbies implements it.
type Store interface {
	Insert(ctx context.Context, row store.LobbyRow, players []store.PlayerRow, events []store.EventRow) error
	Transition(ctx context.Context, expected int64, row store.LobbyRow, players []store.PlayerRow, events []store.EventRow) error
	Get(ctx context.Context, id string) (store.LobbyRow, error)
	GetActiveByRef(ctx context.Context, ref string) (store.LobbyRow, error)
	List(ctx context.Context, f store.LobbyFilter) ([]store.LobbyRow, error)
	Players(ctx context.Context, lobbyID string) ([]store.PlayerRow, error)
	ActiveLobbies(ctx context.Context) ([]store.LobbyRow, error)
	Sweep(ctx context.Context, cutoff time.Time) (int, error)
}

// Pool leases bots; *pool.Pool implements it.
type Pool interface {
	Reserve(ctx context.Context, league uint32, owner string) (*pool.Lease, error)
	Release(ls *pool.Lease)
	Restore(bot, owner string) (*pool.Lease, error)
}

// Holders says which bots hold a league; *league.Registry implements it.
type Holders interface {
	Holders(league uint32) []league.Holding
}

// Options configures a Manager. Store, Pool, Holders and Bots are
// required.
type Options struct {
	Store   Store
	Pool    Pool
	Holders Holders
	Bots    bot.Source
	Lobby   config.LobbyConfig
	GC      config.GCConfig
	// Retention is how long ended lobbies are kept (store.retention); 0
	// keeps them forever.
	Retention time.Duration
	Logger    *slog.Logger
	// Now defaults to time.Now.
	Now   func() time.Time
	Hooks Hooks
	// Tick is how often trackers and the pending queue re-check without
	// an SO change; 0 means DefaultTick.
	Tick time.Duration
	// LostGrace defaults to DefaultLostGrace.
	LostGrace     time.Duration
	SweepInterval time.Duration
}

// Manager runs every lobby. Its methods are safe for concurrent use.
type Manager struct {
	opts Options
	log  *slog.Logger
	ids  idGen

	mu       sync.Mutex
	active   map[string]*entry
	started  bool
	draining bool
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	wake     chan struct{}
}

// entry is one active lobby.
type entry struct {
	id string

	// mu guards lobby and lease; it is held only to read or swap them.
	mu    sync.Mutex
	lobby Lobby
	lease *pool.Lease

	// op serializes GC work on the lobby: tracker steps and API actions.
	// The fields below it are owned by whoever holds it.
	op           sync.Mutex
	readopt      bool
	readoptUntil time.Time
	missingSince time.Time
	launchFor    time.Time // StateSince of the launching state we sent for
	destroyFor   time.Time // StateSince of the closing state we destroyed for
	seatFailedAt time.Time // when every host seat strategy last failed
	enforce      map[uint32]enforceState
	members      map[uint32]gc.Member
	membersInit  bool
	seen         []uint32

	// cancel stops the tracker; guarded by mu.
	cancel context.CancelFunc
	poke   chan struct{}
}

func (e *entry) snapshot() Lobby {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lobby.clone()
}

func (e *entry) wakeUp() {
	select {
	case e.poke <- struct{}{}:
	default:
	}
}

type noHooks struct{}

func (noHooks) OnEvent(Lobby, Event) {}

// New builds a manager; nothing runs until Start.
func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Hooks == nil {
		opts.Hooks = noHooks{}
	}
	if opts.Tick <= 0 {
		opts.Tick = DefaultTick
	}
	if opts.LostGrace <= 0 {
		opts.LostGrace = DefaultLostGrace
	}
	if opts.SweepInterval <= 0 {
		opts.SweepInterval = DefaultSweepInterval
	}
	if opts.GC.RequestTimeout <= 0 {
		opts.GC.RequestTimeout = bot.DefaultRequestTimeout
	}
	if opts.GC.CreateTimeout <= 0 {
		opts.GC.CreateTimeout = 2 * time.Minute
	}
	return &Manager{
		opts:   opts,
		log:    opts.Logger,
		active: map[string]*entry{},
		wake:   make(chan struct{}, 1),
	}
}

func (m *Manager) now() time.Time { return m.opts.Now().UTC() }

// Start re-adopts every lobby the database has not seen end, then starts
// the pending queue. Bot-bound lobbies get their bot's lease back and
// wait up to lobby.readopt_timeout for it to be ready.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("lobby manager already started")
	}
	m.started = true
	m.ctx, m.cancel = context.WithCancel(context.WithoutCancel(ctx))
	m.mu.Unlock()

	rows, err := m.opts.Store.ActiveLobbies(ctx)
	if err != nil {
		return fmt.Errorf("start lobbies: %w", err)
	}
	for _, row := range rows {
		players, err := m.opts.Store.Players(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("start lobbies: %w", err)
		}
		l, err := fromRow(row, players)
		if err != nil {
			return fmt.Errorf("start lobbies: %w", err)
		}
		e := m.add(l)
		if l.State == StatePending {
			continue
		}
		lease, err := m.opts.Pool.Restore(l.Bot, l.ID)
		if err != nil {
			m.log.Error("lobby bot is gone; failing the lobby", "lobby", l.ID, "bot", l.Bot, "err", err)
			if _, err := m.commit(e, func(l *Lobby) ([]newEvent, error) {
				l.State, l.StateReason, l.StateSince = StateFailed, "bot_lost", m.now()
				return nil, nil
			}); err != nil {
				m.log.Error("could not fail lobby", "lobby", l.ID, "err", err)
			}
			continue
		}
		e.mu.Lock()
		e.lease = lease
		e.mu.Unlock()
		e.readopt, e.readoptUntil = true, m.now().Add(m.opts.Lobby.ReadoptTimeout)
		m.log.Info("re-adopting lobby", "lobby", l.ID, "state", l.State, "bot", l.Bot)
		m.startTracker(e)
	}
	m.wg.Go(m.schedule)
	return nil
}

func (m *Manager) add(l Lobby) *entry {
	e := &entry{
		id:      l.ID,
		lobby:   l,
		enforce: map[uint32]enforceState{},
		members: map[uint32]gc.Member{},
		poke:    make(chan struct{}, 1),
	}
	m.mu.Lock()
	m.active[l.ID] = e
	m.mu.Unlock()
	return e
}

func (m *Manager) entry(id string) (*entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.active[id]
	return e, ok
}

func (m *Manager) kickScheduler() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// commit applies change to a copy of the lobby, stores it with its events
// in one transaction and only then makes it current. A state change adds
// lobby.state_changed, and an ending state its lobby.<state> event and
// ended_at; the lease is released when the lobby ends. Hooks hear about
// the events after the write.
func (m *Manager) commit(e *entry, change func(l *Lobby) ([]newEvent, error)) (Lobby, error) {
	e.mu.Lock()
	old := e.lobby
	l := old.clone()
	evs, err := change(&l)
	if err != nil {
		e.mu.Unlock()
		return old, err
	}
	now := m.now()
	if l.State != old.State {
		from := any(string(old.State))
		if old.State == "" {
			from = nil
		}
		evs = append(evs, newEvent{Type: "lobby.state_changed", Data: map[string]any{
			"from": from, "to": string(l.State), "reason": nullable(l.StateReason),
		}})
		if l.State.Terminal() {
			l.EndedAt = now
			evs = append(evs, newEvent{Type: "lobby." + string(l.State), Data: map[string]any{"reason": nullable(l.StateReason)}})
		}
	}
	l.UpdatedAt = now
	l.Version = old.Version + 1
	events := make([]Event, len(evs))
	rows := make([]store.EventRow, len(evs))
	for i, ev := range evs {
		l.Seq++
		events[i] = Event{ID: "evt_" + m.ids.next(now), LobbyID: l.ID, Seq: l.Seq, Type: ev.Type, OccurredAt: now, Data: ev.Data}
		if rows[i], err = eventRow(events[i]); err != nil {
			e.mu.Unlock()
			return old, err
		}
	}
	row, players, err := toRow(l)
	if err != nil {
		e.mu.Unlock()
		return old, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	err = m.opts.Store.Transition(ctx, old.Version, row, players, rows)
	cancel()
	if err != nil {
		e.mu.Unlock()
		return old, fmt.Errorf("save lobby %s: %w", l.ID, err)
	}
	e.lobby = l
	ended := l.State.Terminal() && !old.State.Terminal()
	var (
		lease         *pool.Lease
		cancelTracker context.CancelFunc
	)
	if ended {
		lease, e.lease = e.lease, nil
		cancelTracker = e.cancel
	}
	e.mu.Unlock()

	if l.State != old.State {
		m.log.Info("lobby state", "lobby", l.ID, "from", old.State, "to", l.State, "reason", l.StateReason, "bot", l.Bot)
	}
	if ended {
		m.mu.Lock()
		delete(m.active, l.ID)
		m.mu.Unlock()
		if cancelTracker != nil {
			cancelTracker()
		}
		if lease != nil {
			m.opts.Pool.Release(lease)
			m.kickScheduler()
		}
	}
	for _, ev := range events {
		m.opts.Hooks.OnEvent(l, ev)
	}
	return l.clone(), nil
}

// --- pending queue ---

func (m *Manager) schedule() {
	tick := time.NewTicker(m.opts.Tick)
	defer tick.Stop()
	sweep := time.NewTicker(m.opts.SweepInterval)
	defer sweep.Stop()
	m.sweep()
	for {
		m.schedulePending()
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
		case <-m.wake:
		case <-sweep.C:
			m.sweep()
		}
	}
}

func (m *Manager) sweep() {
	if m.opts.Retention <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, storeTimeout)
	defer cancel()
	n, err := m.opts.Store.Sweep(ctx, m.now().Add(-m.opts.Retention))
	switch {
	case err != nil:
		m.log.Warn("lobby retention sweep failed", "err", err)
	case n > 0:
		m.log.Info("deleted old lobbies", "count", n, "retention", m.opts.Retention)
	}
}

// schedulePending gives free bots to pending lobbies, oldest first, and
// times out the ones that waited too long.
func (m *Manager) schedulePending() {
	m.mu.Lock()
	if m.draining {
		m.mu.Unlock()
		return
	}
	var pending []*entry
	for _, e := range m.active {
		pending = append(pending, e)
	}
	m.mu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i].id < pending[j].id })

	for _, e := range pending {
		e.op.Lock()
		m.schedulePendingOne(e)
		e.op.Unlock()
	}
}

func (m *Manager) schedulePendingOne(e *entry) {
	l := e.snapshot()
	if l.State != StatePending {
		return
	}
	res := derive(l, inputs{Now: m.now(), Cfg: m.opts.Lobby})
	if res.Lobby.State != l.State {
		if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
			*cur = res.Lobby
			return res.Events, nil
		}); err != nil {
			m.log.Error("could not time out pending lobby", "lobby", l.ID, "err", err)
		}
		return
	}
	lease, err := m.opts.Pool.Reserve(m.ctx, l.Spec.LeagueID, l.ID)
	if err != nil {
		if !errors.Is(err, pool.ErrNoneFree) && !errors.Is(err, pool.ErrNoHolder) {
			m.log.Warn("could not reserve a bot", "lobby", l.ID, "err", err)
		}
		return
	}
	updated, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		if cur.State != StatePending {
			return nil, ErrInvalidState
		}
		verified := lease.Verified
		cur.State, cur.StateReason, cur.StateSince = StateCreating, "", m.now()
		cur.Bot, cur.LeagueVerified = lease.Bot, &verified
		return nil, nil
	})
	if err != nil {
		m.opts.Pool.Release(lease)
		if !errors.Is(err, ErrInvalidState) {
			m.log.Error("could not lease a bot to a lobby", "lobby", l.ID, "err", err)
		}
		return
	}
	e.mu.Lock()
	e.lease = lease
	e.mu.Unlock()
	m.log.Info("bot leased", "lobby", updated.ID, "bot", lease.Bot, "league", l.Spec.LeagueID, "verified", lease.Verified)
	m.startTracker(e)
}

// --- API ---

// Create accepts a lobby. created is false when an active lobby with the
// same external_ref and the same request already exists: that lobby is
// returned. A different request for an active ref fails with ErrConflict.
func (m *Manager) Create(ctx context.Context, spec Spec) (Lobby, bool, error) {
	spec = spec.clone()
	spec.normalize()
	if err := spec.validate(m.opts.Lobby); err != nil {
		return Lobby{}, false, err
	}
	m.mu.Lock()
	draining, started := m.draining, m.started
	m.mu.Unlock()
	if draining || !started {
		return Lobby{}, false, fmt.Errorf("%w: the service is shutting down", ErrUnavailable)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if l, ok, err := m.existing(ctx, spec); err != nil || ok {
			return l, false, err
		}
		if spec.LeagueID != 0 && len(m.opts.Holders.Holders(spec.LeagueID)) == 0 {
			return Lobby{}, false, fmt.Errorf("%w: no configured bot administers league %d", ErrLeagueNotHeld, spec.LeagueID)
		}
		l, err := m.insert(ctx, spec)
		if errors.Is(err, store.ErrDuplicateRef) {
			continue // lost a race with the same ref; compare with the winner
		}
		if err != nil {
			return Lobby{}, false, err
		}
		return l, true, nil
	}
	return Lobby{}, false, fmt.Errorf("%w: external_ref %q", ErrConflict, spec.ExternalRef)
}

// existing returns the active lobby for spec's ref if the request matches.
func (m *Manager) existing(ctx context.Context, spec Spec) (Lobby, bool, error) {
	row, err := m.opts.Store.GetActiveByRef(ctx, spec.ExternalRef)
	if errors.Is(err, store.ErrNotFound) {
		return Lobby{}, false, nil
	}
	if err != nil {
		return Lobby{}, false, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	cur, err := m.Get(ctx, row.ID)
	if err != nil {
		return Lobby{}, false, err
	}
	samePass := subtle.ConstantTimeCompare([]byte(cur.Spec.Password.Reveal()), []byte(spec.Password.Reveal())) == 1
	if !samePass || cur.Spec.requestKey() != spec.requestKey() {
		return Lobby{}, false, fmt.Errorf("%w: external_ref %q", ErrConflict, spec.ExternalRef)
	}
	return cur, true, nil
}

func (m *Manager) insert(ctx context.Context, spec Spec) (Lobby, error) {
	now := m.now()
	for _, side := range []Side{Radiant, Dire} {
		t := spec.Teams.Team(side)
		for i := range t.Players {
			p := t.Players[i]
			t.Players[i] = Player{AccountID: p.AccountID, Captain: p.Captain}
		}
	}
	l := Lobby{
		ID: "lob_" + m.ids.next(now), State: StatePending, StateSince: now, Spec: spec,
		HostSeat: HostSeat{}, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(m.opts.Lobby.IdleTimeout),
		Seq: 1, Version: 0,
	}
	ev := Event{ID: "evt_" + m.ids.next(now), LobbyID: l.ID, Seq: 1, Type: "lobby.state_changed", OccurredAt: now,
		Data: map[string]any{"from": nil, "to": string(StatePending), "reason": "accepted"}}
	row, players, err := toRow(l)
	if err != nil {
		return Lobby{}, err
	}
	evRow, err := eventRow(ev)
	if err != nil {
		return Lobby{}, err
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer cancel()
	if err := m.opts.Store.Insert(sctx, row, players, []store.EventRow{evRow}); err != nil {
		if errors.Is(err, store.ErrDuplicateRef) {
			return Lobby{}, err
		}
		return Lobby{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	m.add(l)
	m.log.Info("lobby accepted", "lobby", l.ID, "external_ref", spec.ExternalRef, "league", spec.LeagueID)
	m.opts.Hooks.OnEvent(l.clone(), ev)
	m.kickScheduler()
	return l.clone(), nil
}

// Get returns one lobby, active or ended.
func (m *Manager) Get(ctx context.Context, id string) (Lobby, error) {
	if e, ok := m.entry(id); ok {
		return e.snapshot(), nil
	}
	row, err := m.opts.Store.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Lobby{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Lobby{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	players, err := m.opts.Store.Players(ctx, id)
	if err != nil {
		return Lobby{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return fromRow(row, players)
}

// Filter selects lobbies for List.
type Filter struct {
	States      []State
	ExternalRef string
	Active      bool
	// Cursor is the next_cursor of the previous page.
	Cursor string
	// Limit is 1-200; 0 means 50.
	Limit int
}

// List returns lobbies newest first and the cursor of the next page, ""
// at the end.
func (m *Manager) List(ctx context.Context, f Filter) ([]Lobby, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	sf := store.LobbyFilter{ExternalRef: f.ExternalRef, Active: f.Active, Before: f.Cursor, Limit: limit + 1}
	for _, s := range f.States {
		sf.States = append(sf.States, string(s))
	}
	rows, err := m.opts.Store.List(ctx, sf)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID
	}
	out := make([]Lobby, 0, len(rows))
	for _, row := range rows {
		if e, ok := m.entry(row.ID); ok {
			out = append(out, e.snapshot())
			continue
		}
		players, err := m.opts.Store.Players(ctx, row.ID)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		l, err := fromRow(row, players)
		if err != nil {
			return nil, "", err
		}
		out = append(out, l)
	}
	return out, next, nil
}

// active returns the entry of a lobby that has not ended. For an ended
// lobby it fails with ErrInvalidState, for an unknown one ErrNotFound.
func (m *Manager) activeEntry(ctx context.Context, id string) (*entry, error) {
	if e, ok := m.entry(id); ok {
		return e, nil
	}
	l, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: lobby is %s", ErrInvalidState, l.State)
}

func stateErr(l Lobby, allowed ...State) error {
	if slices.Contains(allowed, l.State) {
		return nil
	}
	return fmt.Errorf("%w: lobby is %s", ErrInvalidState, l.State)
}

// handle returns the lobby's bot and its current snapshot of this lobby.
func (m *Manager) handle(l Lobby) (bot.Handle, *gc.LobbyView, error) {
	h, ok := m.opts.Bots.Bot(l.Bot)
	if !ok {
		return nil, nil, fmt.Errorf("%w: bot %s is not configured", ErrUnavailable, l.Bot)
	}
	return h, lobbyView(h, l.GCLobbyID), nil
}

// lobbyView is the bot's snapshot if it is of lobby id.
func lobbyView(h bot.Handle, id uint64) *gc.LobbyView {
	v, ok := h.CurrentLobby()
	if !ok || v == nil || id == 0 || v.LobbyID != id {
		return nil
	}
	return v
}

// gcErr maps a failed GC call for the API.
func gcErr(op string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %s: %w", ErrGCTimeout, op, err)
	default:
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, op, err)
	}
}

// do runs one GC call for an API action, bounded by gc.request_timeout.
func (m *Manager) do(ctx context.Context, h bot.Handle, op string, fn func(c gc.Client) error) error {
	ctx, cancel := context.WithTimeout(ctx, m.opts.GC.RequestTimeout)
	defer cancel()
	if err := h.Do(ctx, op, func(_ context.Context, c gc.Client) error { return fn(c) }); err != nil {
		return gcErr(op, err)
	}
	return nil
}

// Update changes the lobby's settings. In waiting and ready the merged
// detail set is sent to the GC whole, because the GC replaces every detail
// with what it gets.
func (m *Manager) Update(ctx context.Context, id string, p Patch) (Lobby, error) {
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return Lobby{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if err := stateErr(l, StatePending, StateWaiting, StateReady); err != nil {
		return Lobby{}, err
	}
	spec := p.apply(l.Spec)
	if err := spec.validate(m.opts.Lobby); err != nil {
		return Lobby{}, err
	}
	details := l.Details
	if l.State != StatePending {
		details = m.mergedDetails(spec, l)
		h, _, err := m.handle(l)
		if err != nil {
			return Lobby{}, err
		}
		if err := m.do(ctx, h, "set lobby details", func(c gc.Client) error { return c.SetLobbyDetails(details) }); err != nil {
			return Lobby{}, err
		}
	}
	return m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		cur.Spec.Name, cur.Spec.Password, cur.Spec.GameMode, cur.Spec.Region = spec.Name, spec.Password, spec.GameMode, spec.Region
		cur.Spec.TVDelaySeconds, cur.Spec.Visibility, cur.Spec.AllowCheats, cur.Spec.Launch = spec.TVDelaySeconds, spec.Visibility, spec.AllowCheats, spec.Launch
		cur.Spec.ReadyCheck, cur.Spec.AllowShortHanded, cur.Spec.KickStrangers = spec.ReadyCheck, spec.AllowShortHanded, spec.KickStrangers
		cur.Spec.Admins, cur.Spec.Metadata = spec.Admins, spec.Metadata
		cur.Details = details
		return []newEvent{{Type: "lobby.updated", Data: map[string]any{"fields": p.fields()}}}, nil
	})
}

// mergedDetails is the detail set for spec on lobby l, keeping what the GC
// read back for values the spec leaves to the GC.
func (m *Manager) mergedDetails(spec Spec, l Lobby) gc.LobbyDetails {
	d := spec.details(m.opts.Lobby, l.GCLobbyID)
	if spec.TVDelaySeconds == 0 {
		d.TVDelaySeconds = l.Details.TVDelaySeconds
	}
	if spec.Region == "" {
		d.ServerRegion = l.Details.ServerRegion
	}
	return d
}

// fields names the fields a patch sets, for lobby.updated.
func (p Patch) fields() []string {
	var out []string
	add := func(name string, set bool) {
		if set {
			out = append(out, name)
		}
	}
	add("name", p.Name != nil)
	add("password", p.Password != nil)
	add("game_mode", p.GameMode != nil)
	add("region", p.Region != nil)
	add("tv_delay_seconds", p.TVDelaySeconds != nil)
	add("visibility", p.Visibility != nil)
	add("allow_cheats", p.AllowCheats != nil)
	add("launch", p.Launch != nil)
	add("ready_check", p.ReadyCheck != nil)
	add("allow_short_handed", p.AllowShortHanded != nil)
	add("kick_strangers", p.KickStrangers != nil)
	add("admins", p.Admins != nil)
	add("metadata", p.Metadata != nil)
	return out
}

// Close ends a lobby: a pending one at once, one with a bot through
// closing, where the tracker destroys it. changed is false when the lobby
// had already ended or is already closing.
func (m *Manager) Close(ctx context.Context, id string) (Lobby, bool, error) {
	e, ok := m.entry(id)
	if !ok {
		l, err := m.Get(ctx, id)
		return l, false, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	switch l.State {
	case StateClosing:
		return l, false, nil
	case StateInGame, StatePostGame:
		return Lobby{}, false, fmt.Errorf("%w: lobby is %s; it ends with the match", ErrInvalidState, l.State)
	case StateLaunching:
		if _, v, err := m.handle(l); err == nil && v != nil && v.State != gc.LobbyUI {
			return Lobby{}, false, fmt.Errorf("%w: the match is starting", ErrInvalidState)
		}
	}
	if l.State.Terminal() {
		return l, false, nil
	}
	next := StateClosing
	if l.State == StatePending {
		next = StateClosed
	}
	out, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		cur.State, cur.StateReason, cur.StateSince = next, "api", m.now()
		return nil, nil
	})
	if err != nil {
		return Lobby{}, false, err
	}
	e.wakeUp()
	return out, true, nil
}

// InviteRequest names who to invite: Players, or every rostered player not
// in the lobby with Missing.
type InviteRequest struct {
	Players []uint32
	Missing bool
}

// Skip reasons for invites.
const (
	SkipInLobby     = "in_lobby"
	SkipCooldown    = "pending_cooldown"
	SkipCap         = "cap"
	SkipNotRostered = "not_rostered"
)

// InviteSkip is a player Invite did not invite.
type InviteSkip struct {
	AccountID uint32
	Reason    string
}

// InviteResult is what Invite did.
type InviteResult struct {
	Invited []uint32
	Skipped []InviteSkip
}

// Invite resends invites, within lobby.invite.min_resend and
// lobby.invite.max_per_player per player. Only rostered players are
// invited. When every requested player is rate-limited it fails with
// *InviteLimitedError.
func (m *Manager) Invite(ctx context.Context, id string, req InviteRequest) (InviteResult, error) {
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return InviteResult{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if err := stateErr(l, StateWaiting, StateReady); err != nil {
		return InviteResult{}, err
	}
	h, v, err := m.handle(l)
	if err != nil {
		return InviteResult{}, err
	}
	now := m.now()
	var targets []uint32
	if req.Missing {
		for _, p := range l.Spec.Teams.All() {
			targets = append(targets, p.AccountID)
		}
	} else {
		// Each copy would pass the same per-player limits, so a repeated
		// account counts once.
		for _, a := range req.Players {
			if !slices.Contains(targets, a) {
				targets = append(targets, a)
			}
		}
	}
	res := InviteResult{Invited: []uint32{}, Skipped: []InviteSkip{}}
	var retry time.Duration
	limited := 0
	cfg := m.opts.Lobby.Invite
	for _, account := range targets {
		p := l.Spec.Teams.Player(account)
		switch {
		case p == nil:
			res.Skipped = append(res.Skipped, InviteSkip{account, SkipNotRostered})
		case inLobby(v, account):
			if !req.Missing {
				res.Skipped = append(res.Skipped, InviteSkip{account, SkipInLobby})
			}
		case p.InvitesSent >= cfg.MaxPerPlayer:
			res.Skipped = append(res.Skipped, InviteSkip{account, SkipCap})
			limited++
		case !p.LastInviteAt.IsZero() && now.Sub(p.LastInviteAt) < cfg.MinResend:
			res.Skipped = append(res.Skipped, InviteSkip{account, SkipCooldown})
			limited++
			wait := cfg.MinResend - now.Sub(p.LastInviteAt)
			if retry == 0 || wait < retry {
				retry = wait
			}
		default:
			res.Invited = append(res.Invited, account)
		}
	}
	if len(res.Invited) == 0 && limited > 0 && limited == len(res.Skipped) {
		return res, &InviteLimitedError{RetryAfter: retry}
	}
	if err := m.sendInvites(ctx, e, h, res.Invited); err != nil {
		return res, err
	}
	return res, nil
}

func inLobby(v *gc.LobbyView, account uint32) bool {
	if v == nil {
		return false
	}
	for _, mem := range v.Members {
		if gc.AccountID(mem.SteamID) == account {
			return true
		}
	}
	return false
}

// sendInvites records the invites, then sends them: a crash between the
// two never sends one twice.
func (m *Manager) sendInvites(ctx context.Context, e *entry, h bot.Handle, accounts []uint32) error {
	if len(accounts) == 0 {
		return nil
	}
	now := m.now()
	if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		var evs []newEvent
		for _, a := range accounts {
			p := cur.Spec.Teams.Player(a)
			if p == nil {
				continue
			}
			p.InvitesSent++
			p.LastInviteAt = now
			evs = append(evs, newEvent{Type: "lobby.player_invited", Data: map[string]any{
				"account_id": a, "steam_id": strconv.FormatUint(gc.SteamID64(a), 10),
				"invites_sent": p.InvitesSent, "confirmed": false,
			}})
		}
		return evs, nil
	}); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var errs []error
	for _, a := range accounts {
		if err := m.do(ctx, h, "invite", func(c gc.Client) error { return c.Invite(gc.SteamID64(a)) }); err != nil {
			m.log.Warn("invite not sent", "lobby", e.id, "account_id", a, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Kick moves a member to the pool (from "team") or out of the lobby
// ("lobby"). The lobby snapshot, not a reply, shows whether it worked.
func (m *Manager) Kick(ctx context.Context, id string, account uint32, from string) (Lobby, error) {
	if from != "team" && from != "lobby" {
		return Lobby{}, invalid(`from must be "team" or "lobby"`)
	}
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return Lobby{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if err := stateErr(l, StateWaiting, StateReady); err != nil {
		return Lobby{}, err
	}
	h, _, err := m.handle(l)
	if err != nil {
		return Lobby{}, err
	}
	if account == gc.AccountID(h.Status().SteamID) {
		return Lobby{}, invalid("account_id is the host")
	}
	if err := m.do(ctx, h, "kick", func(c gc.Client) error {
		if from == "lobby" {
			return c.Kick(account)
		}
		return c.KickFromTeam(account)
	}); err != nil {
		return Lobby{}, err
	}
	return m.commit(e, func(*Lobby) ([]newEvent, error) {
		return []newEvent{{Type: "lobby.player_kicked", Data: map[string]any{"account_id": account, "from": from, "reason": "api"}}}, nil
	})
}

// SetRoster replaces both rosters. Players already rostered keep their
// invite history; their ready flag resets if they changed side. New
// players are invited once; dropped players sitting on a side are kicked
// to the pool.
func (m *Manager) SetRoster(ctx context.Context, id string, r Roster) (Lobby, error) {
	if err := r.validate(); err != nil {
		return Lobby{}, err
	}
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return Lobby{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if err := stateErr(l, StateWaiting, StateReady); err != nil {
		return Lobby{}, err
	}
	h, v, err := m.handle(l)
	if err != nil {
		return Lobby{}, err
	}
	next := r.clone()
	for _, side := range []Side{Radiant, Dire} {
		t := next.Team(side)
		for i, p := range t.Players {
			np := Player{AccountID: p.AccountID, Captain: p.Captain}
			if old := l.Spec.Teams.Player(p.AccountID); old != nil {
				np.InvitesSent, np.LastInviteAt, np.InviteConfirmed = old.InvitesSent, old.LastInviteAt, old.InviteConfirmed
				if oldSide, _ := l.Spec.Teams.Side(p.AccountID); oldSide == side {
					np.Ready = old.Ready
				}
			}
			t.Players[i] = np
		}
	}
	var dropped []uint32
	if v != nil {
		host := h.Status().SteamID
		for _, mem := range v.Members {
			a := gc.AccountID(mem.SteamID)
			_, was := l.Spec.Teams.Side(a)
			_, still := next.Side(a)
			if was && !still && player(mem, host) && (mem.Team == gc.TeamRadiant || mem.Team == gc.TeamDire) {
				dropped = append(dropped, a)
			}
		}
	}
	if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		cur.Spec.Teams = next
		return []newEvent{{Type: "lobby.roster_changed", Data: map[string]any{
			"radiant_team_ref": nullable(next.Radiant.TeamRef), "dire_team_ref": nullable(next.Dire.TeamRef),
		}}}, nil
	}); err != nil {
		return Lobby{}, err
	}
	for _, a := range dropped {
		if err := m.do(ctx, h, "kick from team", func(c gc.Client) error { return c.KickFromTeam(a) }); err != nil {
			m.log.Warn("could not unseat a dropped player", "lobby", id, "account_id", a, "err", err)
			continue
		}
		if _, err := m.commit(e, func(*Lobby) ([]newEvent, error) {
			return []newEvent{{Type: "lobby.player_kicked", Data: map[string]any{"account_id": a, "from": "team", "reason": "roster"}}}, nil
		}); err != nil {
			m.log.Warn("could not record a kick", "lobby", id, "err", err)
		}
	}
	if err := m.autoInvite(ctx, e, h, v); err != nil {
		m.log.Warn("roster invites incomplete", "lobby", id, "err", err)
	}
	e.wakeUp()
	return e.snapshot(), nil
}

// LaunchRequest is a launch from the API or chat.
type LaunchRequest struct {
	// Force launches from waiting, skipping the roster and ready checks,
	// and after lobby.max_launch_attempts. It never skips the host seat.
	Force   bool
	Trigger string
	By      uint32
}

// Launch moves a lobby to launching; the tracker sends the launch. It
// fails with ErrNotReady from waiting without Force and ErrHostNotSeated
// when the host still sits on a team.
func (m *Manager) Launch(ctx context.Context, id string, req LaunchRequest) (Lobby, error) {
	if req.Trigger == "" {
		req.Trigger = TriggerAPI
	}
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return Lobby{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	h, v, err := m.handle(l)
	switch {
	case l.State == StateWaiting && !req.Force:
		missing := []string{}
		if err == nil && v != nil {
			_, missing = readiness(l, v, h.Status().SteamID)
		}
		return Lobby{}, fmt.Errorf("%w: missing %v", ErrNotReady, missing)
	case l.State != StateReady && l.State != StateWaiting:
		return Lobby{}, fmt.Errorf("%w: lobby is %s", ErrInvalidState, l.State)
	case !req.Force && !canAutoLaunch(l, m.opts.Lobby):
		return Lobby{}, fmt.Errorf("%w: %d launch attempts used; launch with force to try again", ErrInvalidState, l.LaunchAttempts)
	case err != nil:
		return Lobby{}, err
	case v == nil:
		return Lobby{}, fmt.Errorf("%w: the bot does not see the lobby right now", ErrUnavailable)
	case !hostGate(v, h.Status().SteamID, l.HostSeat):
		return Lobby{}, fmt.Errorf("%w: host is not out of the player slots", ErrHostNotSeated)
	}
	out, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		r := result{Lobby: *cur}
		startLaunch(&r, req.Trigger, req.By, m.now())
		*cur = r.Lobby
		return r.Events, nil
	})
	if err != nil {
		return Lobby{}, err
	}
	e.wakeUp()
	return out, nil
}

// FlipTeams swaps the sides in the lobby and in the roster, so each team
// keeps its players and its team_ref.
func (m *Manager) FlipTeams(ctx context.Context, id string) (Lobby, error) {
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return Lobby{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if err := stateErr(l, StateWaiting, StateReady); err != nil {
		return Lobby{}, err
	}
	h, _, err := m.handle(l)
	if err != nil {
		return Lobby{}, err
	}
	if err := m.do(ctx, h, "flip teams", func(c gc.Client) error { return c.FlipTeams() }); err != nil {
		return Lobby{}, err
	}
	out, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		cur.Spec.Teams.Radiant, cur.Spec.Teams.Dire = cur.Spec.Teams.Dire, cur.Spec.Teams.Radiant
		return []newEvent{{Type: "lobby.teams_flipped", Data: map[string]any{
			"radiant_team_ref": nullable(cur.Spec.Teams.Radiant.TeamRef), "dire_team_ref": nullable(cur.Spec.Teams.Dire.TeamRef),
		}}}, nil
	})
	if err != nil {
		return Lobby{}, err
	}
	e.wakeUp()
	return out, nil
}

// Shuffle asks the GC for a balanced shuffle. Only an open lobby (both
// rosters empty) may shuffle: a shuffle would undo a roster.
func (m *Manager) Shuffle(ctx context.Context, id string) (Lobby, error) {
	e, err := m.activeEntry(ctx, id)
	if err != nil {
		return Lobby{}, err
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if err := stateErr(l, StateWaiting, StateReady); err != nil {
		return Lobby{}, err
	}
	if !l.Spec.Teams.Empty() {
		return Lobby{}, fmt.Errorf("%w: the lobby has a roster; shuffle only works for an open lobby", ErrInvalidState)
	}
	h, _, err := m.handle(l)
	if err != nil {
		return Lobby{}, err
	}
	if err := m.do(ctx, h, "balanced shuffle", func(c gc.Client) error { return c.BalancedShuffle() }); err != nil {
		return Lobby{}, err
	}
	e.wakeUp()
	return e.snapshot(), nil
}

// Drain is the lobby step of shutdown. It stops the pending queue and
// every tracker, leaving lobbies to be re-adopted at the next start, and
// with lobby.destroy_on_shutdown destroys those that have not launched.
// Lobbies that launched are never touched.
func (m *Manager) Drain(ctx context.Context) error {
	m.mu.Lock()
	if !m.started || m.draining {
		m.mu.Unlock()
		return nil
	}
	m.draining = true
	entries := make([]*entry, 0, len(m.active))
	for _, e := range m.active {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	m.cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("drain lobbies: trackers still running: %w", ctx.Err())
	}
	if !m.opts.Lobby.DestroyOnShutdown {
		return nil
	}
	var errs []error
	for _, e := range entries {
		l := e.snapshot()
		if l.GCLobbyID == 0 || (l.State != StateWaiting && l.State != StateReady && l.State != StateCreating) {
			continue
		}
		h, v, err := m.handle(l)
		if err != nil || v == nil || v.State != gc.LobbyUI {
			continue
		}
		if err := m.do(ctx, h, "destroy lobby", func(c gc.Client) error { return c.Destroy(ctx) }); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", l.ID, err))
			continue
		}
		if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
			cur.State, cur.StateReason, cur.StateSince = StateClosed, "shutdown", m.now()
			return nil, nil
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
