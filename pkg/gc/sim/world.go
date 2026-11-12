// Package sim is a simulated Steam and Dota 2 game coordinator for
// integration tests of the service and its consumers. A World holds the
// simulated lobbies; each bot gets a gc.Client from it that behaves like the
// live adapter: it logs on at once, reaches gc_ready, creates lobbies with
// the requested details, and reports every change as a lobby snapshot. The
// rest of the service runs unchanged on top of it.
//
// The World's control methods play the other side of the lobby: players
// join, take seats, chat, and the match starts and ends when a test says
// so, or on its own in auto mode. Nothing here talks to a network.
package sim

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Errors returned by the World's control methods.
var (
	// ErrNoLobby means no simulated lobby has that ID.
	ErrNoLobby = errors.New("sim: no such lobby")
	// ErrNotMember means the account is not in the lobby.
	ErrNotMember = errors.New("sim: account is not in the lobby")
	// ErrAlreadyMember means the account is in the lobby already.
	ErrAlreadyMember = errors.New("sim: account is already in the lobby")
	// ErrSeatTaken means another member holds the slot, or the team is
	// full.
	ErrSeatTaken = errors.New("sim: seat taken")
	// ErrWrongState means the lobby is not in a state that allows the
	// operation, e.g. ending a match that never started.
	ErrWrongState = errors.New("sim: lobby state does not allow this")
	// ErrNoReadyCommand means no ready command is configured, so a player
	// cannot say it.
	ErrNoReadyCommand = errors.New("sim: no ready command configured")
	// ErrBadAccount means an account ID the simulator cannot use: 0, or
	// a bot's own account.
	ErrBadAccount = errors.New("sim: not a player account")
)

// Settings are the auto-mode switches, changeable while the World runs.
type Settings struct {
	// AutoJoin makes an invited player join AutoJoinDelay after the
	// invite and take a slot on its rostered side.
	AutoJoin      bool
	AutoJoinDelay time.Duration
	// AutoReady makes an auto-joined player say the ready command in the
	// lobby chat once seated and the host is in the chat.
	AutoReady bool
	// AutoStart makes a launched lobby start its match AutoStartDelay
	// after the launch.
	AutoStart      bool
	AutoStartDelay time.Duration
}

// Options configure a World.
type Options struct {
	Settings Settings
	// ReadyCommand is the chat line a player says to mark itself ready,
	// e.g. "!ready". Empty disables Ready and auto-ready.
	ReadyCommand string
	Logger       *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Bot is one simulated Steam account.
type Bot struct {
	Name string
	// AccountID is the account's 32-bit ID; its SteamID64 follows.
	AccountID   uint32
	PersonaName string
	// Leagues are the leagues the account administers.
	Leagues []uint32
}

// RosterFunc reports the team a lobby's roster puts an account on, for
// auto-join. ok is false for an account the roster does not name.
type RosterFunc func(gcLobbyID uint64, account uint32) (team gc.Team, ok bool)

// World is the simulated Steam and game coordinator. Its methods are safe
// for concurrent use.
type World struct {
	opts Options
	log  *slog.Logger

	mu        sync.Mutex
	settings  Settings
	roster    RosterFunc
	clients   []*Client
	byAccount map[uint32]*Client
	lobbies   map[uint64]*lobby
	matches   map[uint64]*gc.MatchResult
	timers    map[*time.Timer]struct{}
	closed    bool

	nextLobby   uint64
	nextMatch   uint64
	nextChannel uint64
}

// Bases of the simulated IDs. They are far from real ones, so a simulated
// lobby or match ID is never mistaken for a live one.
const (
	lobbyIDBase   uint64 = 900_000_000_000_000_000
	matchIDBase   uint64 = 9_000_000_000
	channelIDBase uint64 = 800_000_000_000_000_000
)

// chatHistory bounds the chat lines a lobby keeps.
const chatHistory = 200

// New returns an empty World.
func New(opts Options) *World {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &World{
		opts:        opts,
		log:         opts.Logger.With("component", "sim"),
		settings:    opts.Settings,
		byAccount:   map[uint32]*Client{},
		lobbies:     map[uint64]*lobby{},
		matches:     map[uint64]*gc.MatchResult{},
		timers:      map[*time.Timer]struct{}{},
		nextLobby:   lobbyIDBase,
		nextMatch:   matchIDBase,
		nextChannel: channelIDBase,
	}
}

// Client returns a new simulated account. It panics on an account ID that
// is 0 or already taken, a wiring mistake.
func (w *World) Client(b Bot) *Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b.AccountID == 0 {
		panic("sim: bot " + b.Name + " has account ID 0")
	}
	if _, dup := w.byAccount[b.AccountID]; dup {
		panic(fmt.Sprintf("sim: account ID %d used twice", b.AccountID))
	}
	if b.PersonaName == "" {
		b.PersonaName = b.Name
	}
	b.Leagues = slices.Clone(b.Leagues)
	c := newClient(w, b)
	w.clients = append(w.clients, c)
	w.byAccount[b.AccountID] = c
	return c
}

// SetRoster sets the roster lookup auto-join uses. It is called without
// the World's lock held, so it may call into the lobby layer.
func (w *World) SetRoster(f RosterFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.roster = f
}

// Settings returns the current auto-mode settings.
func (w *World) Settings() Settings {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.settings
}

// SetSettings replaces the auto-mode settings. Joins and starts already
// scheduled keep the delay they were scheduled with.
func (w *World) SetSettings(s Settings) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settings = s
	w.log.Info("simulation settings changed", "auto_join", s.AutoJoin, "auto_join_delay", s.AutoJoinDelay,
		"auto_ready", s.AutoReady, "auto_start", s.AutoStart, "auto_start_delay", s.AutoStartDelay)
}

// ReadyCommand is the chat line Ready says, "" when none is configured.
func (w *World) ReadyCommand() string { return w.opts.ReadyCommand }

// Close stops every scheduled auto-mode action. The clients keep working.
func (w *World) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	for t := range w.timers {
		t.Stop()
	}
	clear(w.timers)
}

// after runs fn with the World's lock held, d from now, unless the World
// is closed by then.
func (w *World) after(d time.Duration, fn func()) {
	var t *time.Timer
	t = time.AfterFunc(max(d, 0), func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.timers, t)
		if w.closed {
			return
		}
		fn()
	})
	w.timers[t] = struct{}{}
}

// --- control ---

// MemberInfo is one member of a simulated lobby.
type MemberInfo struct {
	AccountID   uint32
	Team        gc.Team
	Slot        uint32
	PersonaName string
	// Host marks the bot that hosts the lobby.
	Host bool
}

// ChatLine is one message said in a simulated lobby's chat.
type ChatLine struct {
	AccountID   uint32
	PersonaName string
	Text        string
	At          time.Time
	// Delivered is false for a player's message said while the host was
	// not in the chat, which the host never sees.
	Delivered bool
}

// LobbyInfo is a simulated lobby as the control API shows it.
type LobbyInfo struct {
	LobbyID       uint64
	Bot           string
	HostAccountID uint32
	State         gc.LobbyState
	Details       gc.LobbyDetails
	MatchID       uint64
	Outcome       int32
	Members       []MemberInfo
	// Pending are the invited account IDs that have not joined.
	Pending    []uint32
	ChatJoined bool
	Chat       []ChatLine
	CreatedAt  time.Time
}

// Lobbies lists every simulated lobby, oldest first.
func (w *World) Lobbies() []LobbyInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]LobbyInfo, 0, len(w.lobbies))
	for _, l := range w.lobbies {
		out = append(out, l.info())
	}
	slices.SortFunc(out, func(a, b LobbyInfo) int {
		switch {
		case a.LobbyID < b.LobbyID:
			return -1
		case a.LobbyID > b.LobbyID:
			return 1
		}
		return 0
	})
	return out
}

// Lobby returns one simulated lobby.
func (w *World) Lobby(id uint64) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	return l.info(), nil
}

func (w *World) lobbyLocked(id uint64) (*lobby, error) {
	l, ok := w.lobbies[id]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrNoLobby, id)
	}
	return l, nil
}

// Join puts an account in the lobby's unassigned pool, as a player
// accepting an invite (or typing the password) would. An invite is not
// required. personaName may be empty.
func (w *World) Join(id uint64, account uint32, personaName string) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	if err := w.playerAccount(account); err != nil {
		return LobbyInfo{}, err
	}
	if l.member(account) != nil {
		return LobbyInfo{}, fmt.Errorf("%w: %d", ErrAlreadyMember, account)
	}
	w.joinLocked(l, account, personaName)
	return l.info(), nil
}

func (w *World) playerAccount(account uint32) error {
	if account == 0 {
		return fmt.Errorf("%w: 0", ErrBadAccount)
	}
	if _, bot := w.byAccount[account]; bot {
		return fmt.Errorf("%w: %d is a simulated bot", ErrBadAccount, account)
	}
	return nil
}

func (w *World) joinLocked(l *lobby, account uint32, personaName string) {
	if personaName == "" {
		personaName = fmt.Sprintf("Player %d", account)
	}
	l.pending = slices.DeleteFunc(l.pending, func(a uint32) bool { return a == account })
	l.members = append(l.members, &member{account: account, team: gc.TeamPlayerPool, persona: personaName})
	w.log.Info("sim: player joined", "lobby_id", l.id, "account_id", account)
	l.publish(gc.LobbyUpdated)
}

// Seat moves a member to a team. For Radiant and Dire, slot picks the
// slot (0-4); nil takes the first free one.
func (w *World) Seat(id uint64, account uint32, team gc.Team, slot *uint32) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	m := l.member(account)
	if m == nil {
		return LobbyInfo{}, fmt.Errorf("%w: %d", ErrNotMember, account)
	}
	if err := l.seat(m, team, slot); err != nil {
		return LobbyInfo{}, err
	}
	w.log.Info("sim: player seated", "lobby_id", l.id, "account_id", account, "team", team.String(), "slot", m.slot)
	l.publish(gc.LobbyUpdated)
	return l.info(), nil
}

// Leave takes an account out of the lobby, and out of its pending
// invites.
func (w *World) Leave(id uint64, account uint32) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	if l.host.bot.AccountID == account {
		return LobbyInfo{}, fmt.Errorf("%w: the host leaves through the service", ErrBadAccount)
	}
	if l.member(account) == nil {
		return LobbyInfo{}, fmt.Errorf("%w: %d", ErrNotMember, account)
	}
	l.remove(account)
	w.log.Info("sim: player left", "lobby_id", l.id, "account_id", account)
	l.publish(gc.LobbyUpdated)
	return l.info(), nil
}

// Say posts a chat message as a member. It reports whether the host saw
// it: a host that has not joined the lobby chat does not.
func (w *World) Say(id uint64, account uint32, text string) (delivered bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return false, err
	}
	m := l.member(account)
	if m == nil {
		return false, fmt.Errorf("%w: %d", ErrNotMember, account)
	}
	return l.say(m, text), nil
}

// Ready says the ready command as a member.
func (w *World) Ready(id uint64, account uint32) (delivered bool, err error) {
	if w.opts.ReadyCommand == "" {
		return false, ErrNoReadyCommand
	}
	return w.Say(id, account, w.opts.ReadyCommand)
}

// StartMatch starts the match of a launched lobby: the lobby gets a match
// ID and goes to the run state. matchID 0 picks the next simulated one.
func (w *World) StartMatch(id, matchID uint64) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	if err := w.startLocked(l, matchID); err != nil {
		return LobbyInfo{}, err
	}
	return l.info(), nil
}

func (w *World) startLocked(l *lobby, matchID uint64) error {
	if l.state != gc.LobbyServerAssign && l.state != gc.LobbyServerSetup {
		return fmt.Errorf("%w: the lobby is %s, not launched", ErrWrongState, l.state)
	}
	if matchID == 0 {
		w.nextMatch++
		matchID = w.nextMatch
	}
	l.matchID, l.outcome, l.state, l.startedAt = matchID, 0, gc.LobbyRun, w.opts.Now()
	w.log.Info("sim: match started", "lobby_id", l.id, "match_id", matchID)
	l.publish(gc.LobbyUpdated)
	return nil
}

// Outcomes EndMatch takes, as EMatchOutcome values.
const (
	OutcomeRadiantVictory int32 = 2
	OutcomeDireVictory    int32 = 3
	// OutcomeNotScored is not_scored_canceled.
	OutcomeNotScored int32 = 68
)

// EndMatch ends a running match with an EMatchOutcome, and the lobby goes
// to the postgame state. The result is what MatchDetails answers later.
func (w *World) EndMatch(id uint64, outcome int32) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	if l.state != gc.LobbyRun {
		return LobbyInfo{}, fmt.Errorf("%w: the lobby is %s, no match is running", ErrWrongState, l.state)
	}
	l.state, l.outcome = gc.LobbyPostGame, outcome
	w.matches[l.matchID] = l.result(w.opts.Now())
	w.log.Info("sim: match ended", "lobby_id", l.id, "match_id", l.matchID, "outcome", outcome)
	l.publish(gc.LobbyUpdated)
	return l.info(), nil
}

// Failure modes for Fail.
const (
	// FailLost drops the lobby from the host's cache, as when the GC
	// loses it.
	FailLost = "lost"
	// FailLoadFailed sends a launched lobby back to the UI state without
	// a game, as when the server never loads.
	FailLoadFailed = "load_failed"
)

// Fail makes a lobby fail the way mode says.
func (w *World) Fail(id uint64, mode string) (LobbyInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, err := w.lobbyLocked(id)
	if err != nil {
		return LobbyInfo{}, err
	}
	switch mode {
	case FailLost:
		info := l.info()
		w.dropLocked(l)
		w.log.Info("sim: lobby lost", "lobby_id", l.id)
		return info, nil
	case FailLoadFailed:
		if l.state != gc.LobbyServerAssign && l.state != gc.LobbyServerSetup && l.state != gc.LobbyRun {
			return LobbyInfo{}, fmt.Errorf("%w: the lobby is %s, not launched", ErrWrongState, l.state)
		}
		l.state = gc.LobbyUI
		l.launches++ // cancels a scheduled auto start
		w.log.Info("sim: launch failed", "lobby_id", l.id)
		l.publish(gc.LobbyUpdated)
		return l.info(), nil
	}
	return LobbyInfo{}, fmt.Errorf("sim: unknown failure mode %q", mode)
}

// dropLocked removes a lobby from the World and its host's cache.
func (w *World) dropLocked(l *lobby) {
	delete(w.lobbies, l.id)
	l.publish(gc.LobbyDestroyed)
}

// --- auto mode ---

// invited schedules the auto-join of an invited account.
func (w *World) invited(l *lobby, account uint32) {
	s := w.settings
	if !s.AutoJoin {
		return
	}
	w.after(s.AutoJoinDelay, func() {
		if w.lobbies[l.id] != l || l.member(account) != nil || !slices.Contains(l.pending, account) {
			return
		}
		w.joinLocked(l, account, "")
		roster := w.roster
		// The roster lookup may call into the lobby layer, which may be
		// waiting on this World: look it up without the lock.
		w.mu.Unlock()
		team, rostered := gc.TeamPlayerPool, false
		if roster != nil {
			team, rostered = rosterTeam(roster, l.id, account)
		}
		w.mu.Lock()
		if !rostered || w.closed || w.lobbies[l.id] != l {
			return
		}
		m := l.member(account)
		if m == nil || m.team != gc.TeamPlayerPool {
			return
		}
		if err := l.seat(m, team, nil); err != nil {
			w.log.Info("sim: auto-join could not take a seat", "lobby_id", l.id, "account_id", account, "err", err)
			return
		}
		w.log.Info("sim: player seated", "lobby_id", l.id, "account_id", account, "team", team.String(), "slot", m.slot)
		l.publish(gc.LobbyUpdated)
		if w.settings.AutoReady && w.opts.ReadyCommand != "" {
			w.readyLater(l, account, w.settings.AutoJoinDelay)
		}
	})
}

// rosterTeam asks the roster for a player side; anything else counts as
// not rostered.
func rosterTeam(f RosterFunc, lobbyID uint64, account uint32) (gc.Team, bool) {
	team, ok := f(lobbyID, account)
	if !ok || (team != gc.TeamRadiant && team != gc.TeamDire) {
		return gc.TeamPlayerPool, false
	}
	return team, true
}

// minChatDelay keeps an auto-ready line from reaching the host in the
// same instant as its chat join, before the join's result is applied.
const minChatDelay = 50 * time.Millisecond

// readyLater says the ready command for a seated member after d, or once
// the host is in the chat.
func (w *World) readyLater(l *lobby, account uint32, d time.Duration) {
	w.after(d, func() {
		if w.lobbies[l.id] != l {
			return
		}
		m := l.member(account)
		if m == nil || (m.team != gc.TeamRadiant && m.team != gc.TeamDire) {
			return
		}
		if !l.chatJoined {
			l.readyWaiting = append(l.readyWaiting, account)
			return
		}
		l.say(m, w.opts.ReadyCommand)
	})
}

// launched moves a lobby to server assignment and schedules the auto
// start.
func (w *World) launched(l *lobby) {
	if l.state != gc.LobbyUI {
		return
	}
	l.state = gc.LobbyServerAssign
	l.launches++
	gen := l.launches
	w.log.Info("sim: lobby launched", "lobby_id", l.id)
	l.publish(gc.LobbyUpdated)
	if !w.settings.AutoStart {
		return
	}
	w.after(w.settings.AutoStartDelay, func() {
		if w.lobbies[l.id] != l || l.launches != gen {
			return
		}
		if err := w.startLocked(l, 0); err != nil {
			w.log.Info("sim: auto start skipped", "lobby_id", l.id, "err", err)
		}
	})
}

// chatJoinedLocked flushes the ready lines that waited for the host.
func (w *World) chatJoinedLocked(l *lobby) {
	waiting := l.readyWaiting
	l.readyWaiting = nil
	delay := max(w.settings.AutoJoinDelay, minChatDelay)
	for _, a := range waiting {
		w.readyLater(l, a, delay)
	}
}

// TeamFromString parses a team name as the API spells it.
func TeamFromString(s string) (gc.Team, bool) {
	for _, t := range []gc.Team{gc.TeamRadiant, gc.TeamDire, gc.TeamBroadcaster, gc.TeamSpectator, gc.TeamPlayerPool, gc.TeamNoTeam} {
		if strings.EqualFold(t.String(), s) {
			return t, true
		}
	}
	return 0, false
}
