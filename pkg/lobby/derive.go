package lobby

import (
	"strconv"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Default timings that are not configurable.
const (
	// DefaultLostGrace is how long a ready bot's cache may lack the lobby
	// before it counts as lost: after a reconnect the SO arrives a moment
	// after the welcome.
	DefaultLostGrace = 10 * time.Second
	// closingTimeout is how long a destroy may take to show.
	closingTimeout = 30 * time.Second
	// maxEnforcements stops kicking one member after this many attempts,
	// to avoid a fight with a determined player.
	maxEnforcements = 5
)

// inputs is everything derive looks at besides the lobby.
type inputs struct {
	Now time.Time
	// View is the bot's lobby snapshot, nil when the cache has none or a
	// different lobby.
	View *gc.LobbyView
	// Host is the bot's SteamID64.
	Host uint64
	// BotReady is gc_ready and not lobby_stale. Without it only timeouts
	// apply and no action is taken.
	BotReady bool
	// BotLost means the bot stopped for good (needs_attention, suspended).
	BotLost bool
	// MissingFor is how long View has been nil while BotReady.
	MissingFor time.Duration
	LostGrace  time.Duration
	// Flipping means a flip was sent that the lobby has not shown yet:
	// players are moving sides with their team, so ready flags stay.
	Flipping bool
	Cfg      config.LobbyConfig
}

// newEvent is an event before it gets an ID and a sequence number.
type newEvent struct {
	Type string
	Data map[string]any
}

type actionKind int

const (
	actKickFromTeam actionKind = iota + 1
	actKick
	actDestroy
	actLeave
)

func (k actionKind) String() string {
	switch k {
	case actKickFromTeam:
		return "kick_from_team"
	case actKick:
		return "kick"
	case actDestroy:
		return "destroy"
	case actLeave:
		return "leave"
	}
	return "unknown"
}

// action is a GC operation derive or the enforcement plan asks for.
type action struct {
	Kind    actionKind
	Account uint32
	Reason  string
}

// result is derive's answer: the lobby as it should be now, the events
// that report the change, and what to send.
type result struct {
	Lobby   Lobby
	Events  []newEvent
	Actions []action
}

func (r *result) emit(typ string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	r.Events = append(r.Events, newEvent{Type: typ, Data: data})
}

func (r *result) to(s State, reason string, now time.Time) {
	r.Lobby.State, r.Lobby.StateReason, r.Lobby.StateSince = s, reason, now
}

func (r *result) do(a action) { r.Actions = append(r.Actions, a) }

// derive computes the next state of a bot-bound lobby from a snapshot. It
// is pure: the tracker feeds it the bot's current snapshot on every SO
// change and every tick, and executes what it returns. The creating state
// belongs to the create call and only pending_timeout applies to pending.
func derive(l Lobby, in inputs) result {
	r := result{Lobby: l.clone()}
	now := in.Now
	if l.State.Terminal() {
		return r
	}
	if l.State == StatePending {
		if now.Sub(l.CreatedAt) >= in.Cfg.PendingTimeout {
			r.to(StateTimedOut, "pending_timeout", now)
		}
		return r
	}
	if in.BotLost {
		r.to(StateFailed, "bot_lost", now)
		return r
	}
	if l.State == StateCreating {
		return r
	}

	// Timeouts apply even while the bot is away.
	since := now.Sub(l.StateSince)
	switch l.State {
	case StateWaiting, StateReady:
		if in.Cfg.IdleTimeout > 0 && now.Sub(l.CreatedAt) >= in.Cfg.IdleTimeout {
			r.to(StateTimedOut, "idle_timeout", now)
			if in.BotReady {
				r.do(action{Kind: actDestroy, Reason: "idle_timeout"})
			}
			return r
		}
	case StateInGame:
		if since >= in.Cfg.IngameBackstop {
			r.to(StateTimedOut, "ingame_backstop", now)
			return r
		}
	case StatePostGame:
		if since >= in.Cfg.PostgameLinger {
			r.to(StateFinished, "postgame_linger", now)
			if in.BotReady {
				r.do(action{Kind: actLeave, Reason: "postgame_linger"})
			}
			return r
		}
	case StateClosing:
		if since >= closingTimeout {
			r.to(StateClosed, "destroy_unconfirmed", now)
			if in.BotReady {
				r.do(action{Kind: actLeave, Reason: "destroy_unconfirmed"})
			}
			return r
		}
	}
	if !in.BotReady {
		return r
	}

	v := in.View
	if v == nil {
		switch l.State {
		case StatePostGame:
			r.to(StateFinished, "lobby_gone", now)
		case StateClosing:
			r.to(StateClosed, "destroyed", now)
		case StateInGame:
			if in.MissingFor >= in.LostGrace {
				r.to(StateFailed, "lost_in_game", now)
			}
		default:
			if in.MissingFor >= in.LostGrace {
				r.to(StateFailed, "lobby_lost", now)
			}
		}
		return r
	}

	switch l.State {
	case StateWaiting, StateReady:
		deriveSeating(&r, v, in)
	case StateLaunching:
		deriveLaunching(&r, v, in)
	case StateInGame:
		deriveInGame(&r, v, in)
	}
	return r
}

func serverState(s gc.LobbyState) bool {
	return s == gc.LobbyServerAssign || s == gc.LobbyServerSetup || s == gc.LobbyRun
}

func deriveSeating(r *result, v *gc.LobbyView, in inputs) {
	l := &r.Lobby
	if v.State != gc.LobbyUI {
		// Launched without us (a lost launch we did send, or an admin
		// client). Follow it.
		r.to(StateLaunching, "launched_elsewhere", in.Now)
		deriveLaunching(r, v, in)
		return
	}
	if !in.Flipping {
		resetReady(r, v, in.Host)
	}
	ready, missing := readiness(*l, v, in.Host, readyCheckOn(*l, in.Cfg))
	switch {
	case l.State == StateWaiting && ready:
		r.to(StateReady, "", in.Now)
		r.emit("lobby.ready", nil)
	case l.State == StateReady && !ready:
		r.to(StateWaiting, "", in.Now)
		r.emit("lobby.unready", map[string]any{"missing": missing})
	}
	if l.State == StateReady && l.Spec.Launch == LaunchAuto && canAutoLaunch(*l, in.Cfg) &&
		in.Now.Sub(l.StateSince) >= in.Cfg.AutoLaunchDelay && hostGate(v, in.Host, l.HostSeat) {
		startLaunch(r, TriggerAuto, 0, in.Now)
	}
}

// canAutoLaunch reports whether automatic and chat launches may still
// try; after lobby.max_launch_attempts only a forced API launch can.
// LaunchAttempts counts launches sent: the tracker counts one just before
// it sends it.
func canAutoLaunch(l Lobby, cfg config.LobbyConfig) bool {
	return l.LaunchAttempts < cfg.MaxLaunchAttempts
}

// startLaunch moves the lobby to launching. The tracker sends the launch.
func startLaunch(r *result, trigger string, by uint32, now time.Time) {
	l := &r.Lobby
	r.to(StateLaunching, trigger, now)
	l.Match.ServerAt, l.Match.RunAt = time.Time{}, time.Time{}
	data := map[string]any{
		"trigger":            trigger,
		"by_account_id":      nil,
		"attempt":            l.LaunchAttempts + 1,
		"host_seat_strategy": l.HostSeat.Strategy,
	}
	if by != 0 {
		data["by_account_id"] = by
	}
	r.emit("lobby.launching", data)
}

func deriveLaunching(r *result, v *gc.LobbyView, in inputs) {
	l := &r.Lobby
	now := in.Now
	if serverState(v.State) && l.Match.ServerAt.IsZero() {
		l.Match.ServerAt = now
	}
	if v.State == gc.LobbyRun && l.Match.RunAt.IsZero() {
		l.Match.RunAt = now
	}
	// A match_id left over from a failed launch is not this launch's.
	fresh := v.MatchID != 0 && v.MatchID != l.Match.StaleMatchID
	running := v.MatchID != 0 && (v.State == gc.LobbyRun || v.State == gc.LobbyPostGame)
	switch {
	case (serverState(v.State) || v.State == gc.LobbyPostGame) && (fresh || running):
		startMatch(r, v, now)
		if v.State == gc.LobbyPostGame || (v.State == gc.LobbyRun && ScoredOutcome(v.Outcome)) {
			endMatch(r, v, now)
		}
	case (v.State == gc.LobbyServerAssign || v.State == gc.LobbyServerSetup) && now.Sub(l.Match.ServerAt) >= in.Cfg.ServerStallTimeout:
		r.emit("lobby.launch_failed", map[string]any{"reason": "server_stall", "attempt": l.LaunchAttempts, "next_strategy": nil})
		r.to(StateTimedOut, "server_stall", now)
	case v.State == gc.LobbyUI && (!l.Match.ServerAt.IsZero() || fresh):
		launchFailed(r, "load_failed", v, in)
	case v.State == gc.LobbyUI && now.Sub(l.StateSince) >= in.Cfg.LaunchSettleTimeout:
		launchFailed(r, "ignored", v, in)
	}
}

func startMatch(r *result, v *gc.LobbyView, now time.Time) {
	l := &r.Lobby
	l.Match.MatchID = v.MatchID
	l.Match.StartedAt = now
	l.Match.RadiantTeamRef = l.Spec.Teams.Radiant.TeamRef
	l.Match.DireTeamRef = l.Spec.Teams.Dire.TeamRef
	l.Match.HostSeatStrategy = l.HostSeat.Strategy
	r.to(StateInGame, "", now)
	r.emit("match.started", map[string]any{
		"match_id":           strconv.FormatUint(v.MatchID, 10),
		"host_seat_strategy": l.HostSeat.Strategy,
		"radiant_team_ref":   nullable(l.Match.RadiantTeamRef),
		"dire_team_ref":      nullable(l.Match.DireTeamRef),
	})
}

func deriveInGame(r *result, v *gc.LobbyView, in inputs) {
	l := &r.Lobby
	now := in.Now
	if v.MatchID != 0 && l.Match.MatchID == 0 {
		l.Match.MatchID = v.MatchID
	}
	if v.State == gc.LobbyRun && l.Match.RunAt.IsZero() {
		l.Match.RunAt = now
	}
	switch {
	case v.State == gc.LobbyPostGame || ((v.State == gc.LobbyRun || v.State == gc.LobbyUI) && ScoredOutcome(v.Outcome)):
		// A lobby may go back to UI without a POSTGAME the tracker saw: a
		// scored outcome still ends the match.
		endMatch(r, v, now)
	case v.State == gc.LobbyUI:
		launchFailed(r, "load_failed", v, in)
	case (v.State == gc.LobbyServerAssign || v.State == gc.LobbyServerSetup) && !l.Match.ServerAt.IsZero() &&
		now.Sub(l.Match.ServerAt) >= in.Cfg.ServerStallTimeout:
		r.to(StateTimedOut, "server_stall", now)
	}
}

func endMatch(r *result, v *gc.LobbyView, now time.Time) {
	l := &r.Lobby
	o := v.Outcome
	l.Match.Outcome = &o
	l.Match.EndedAt = now
	r.to(StatePostGame, "", now)
	data := map[string]any{
		"match_id":         strconv.FormatUint(l.Match.MatchID, 10),
		"outcome":          o,
		"outcome_name":     OutcomeName(o),
		"winner_side":      nullable(string(l.Match.WinnerSide())),
		"winner_team_ref":  nullable(l.Match.WinnerTeamRef()),
		"scored":           nil,
		"duration_seconds": nil,
		"provisional":      true,
		"source":           "lobby",
	}
	if s := l.Match.Scored(); s != nil {
		data["scored"] = *s
	}
	if d := l.Match.DurationSeconds(); d != nil {
		data["duration_seconds"] = *d
	}
	r.emit("match.ended", data)
}

// launchFailed returns the lobby to waiting after a launch that did not
// take, and moves the host seat on to the next strategy. The match_id the
// lobby still shows is kept as stale for the next launch.
func launchFailed(r *result, reason string, v *gc.LobbyView, in inputs) {
	l := &r.Lobby
	strategies := in.Cfg.HostSeatStrategy
	next := l.HostSeat.Next
	if next+1 < len(strategies) {
		next++
	}
	l.HostSeat.Next, l.HostSeat.Confirmed = next, false
	var nextName any
	if next < len(strategies) {
		nextName = strategies[next]
	}
	r.emit("lobby.launch_failed", map[string]any{"reason": reason, "attempt": l.LaunchAttempts, "next_strategy": nextName})
	stale := l.Match.StaleMatchID
	if v.MatchID != 0 {
		stale = v.MatchID
	}
	l.Match = Match{StaleMatchID: stale}
	stateReason := reason
	if !canAutoLaunch(*l, in.Cfg) {
		stateReason = "launch_attempts_exhausted"
	}
	r.to(StateWaiting, stateReason, in.Now)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// player is a member who counts as a player: not the host, team known,
// not a coach.
func player(m gc.Member, host uint64) bool {
	return m.SteamID != host && m.TeamKnown && !m.Coach()
}

// readyCheckOn reports whether players must also say they are ready in
// lobby chat. It needs chat, and a roster: an open lobby has nobody whose
// ready flag could count.
func readyCheckOn(l Lobby, cfg config.LobbyConfig) bool {
	return cfg.Chat.Enabled && l.Spec.ReadyCheck && !l.Spec.Teams.Empty()
}

// resetReady clears the ready flag of rostered players who left or are
// no longer on their side. A member whose team the snapshot omits keeps
// the flag: that is not a move.
func resetReady(r *result, v *gc.LobbyView, host uint64) {
	byAccount := map[uint32]gc.Member{}
	for _, m := range v.Members {
		byAccount[gc.AccountID(m.SteamID)] = m
	}
	for _, e := range r.Lobby.Spec.Teams.All() {
		if !e.Ready {
			continue
		}
		reason := ""
		m, ok := byAccount[e.AccountID]
		switch {
		case !ok:
			reason = "left"
		case !m.TeamKnown:
			continue
		case !player(m, host) || m.Team != e.Side.team():
			reason = "moved"
		default:
			continue
		}
		r.Lobby.Spec.Teams.Player(e.AccountID).Ready = false
		r.emit("lobby.player_unready", map[string]any{"account_id": e.AccountID, "reason": reason})
	}
}

// readiness reports whether the lobby may launch and, if not, what is
// missing: account IDs of rostered players not seated on their side,
// "unready:<id>" for seated players who have not said they are ready
// (with readyCheck), "stranger:<id>" for others on a side, or
// "radiant"/"dire" for an open lobby's empty side.
func readiness(l Lobby, v *gc.LobbyView, host uint64, readyCheck bool) (bool, []string) {
	missing := []string{}
	roster := l.Spec.Teams
	if roster.Empty() {
		counts := map[gc.Team]int{}
		for _, m := range v.Members {
			if player(m, host) {
				counts[m.Team]++
			}
		}
		for _, s := range []Side{Radiant, Dire} {
			if counts[s.team()] == 0 {
				missing = append(missing, string(s))
			}
		}
		return len(missing) == 0, missing
	}

	byAccount := map[uint32]gc.Member{}
	for _, m := range v.Members {
		byAccount[gc.AccountID(m.SteamID)] = m
	}
	seated := map[Side]int{}
	for _, e := range roster.All() {
		m, ok := byAccount[e.AccountID]
		switch {
		case ok && player(m, host) && m.Team == e.Side.team():
			seated[e.Side]++
			if readyCheck && !e.Ready {
				missing = append(missing, "unready:"+strconv.FormatUint(uint64(e.AccountID), 10))
			}
		case !ok && l.Spec.AllowShortHanded:
			// Absent players do not block a short-handed lobby.
		default:
			missing = append(missing, strconv.FormatUint(uint64(e.AccountID), 10))
		}
	}
	for _, m := range v.Members {
		if !player(m, host) || (m.Team != gc.TeamRadiant && m.Team != gc.TeamDire) {
			continue
		}
		if _, ok := roster.Side(gc.AccountID(m.SteamID)); !ok {
			missing = append(missing, "stranger:"+strconv.FormatUint(uint64(gc.AccountID(m.SteamID)), 10))
		}
	}
	if l.Spec.AllowShortHanded {
		for _, s := range []Side{Radiant, Dire} {
			if seated[s] == 0 && len(roster.Team(s).Players) > 0 {
				missing = append(missing, string(s))
			}
		}
	}
	return len(missing) == 0, missing
}

// hostMember finds the host in a snapshot.
func hostMember(v *gc.LobbyView, host uint64) (gc.Member, bool) {
	if v == nil {
		return gc.Member{}, false
	}
	for _, m := range v.Members {
		if m.SteamID == host {
			return m, true
		}
	}
	return gc.Member{}, false
}

// hostGate is the launch precondition: the host seat is confirmed and the
// host is in the lobby with a known team that is not Radiant or Dire, where
// the strategy in effect puts it (the broadcaster slot for caster). An
// absent team decodes as Radiant, so presence is checked.
func hostGate(v *gc.LobbyView, host uint64, hs HostSeat) bool {
	m, ok := hostMember(v, host)
	if !hs.Confirmed || !ok || !m.TeamKnown || m.Team == gc.TeamRadiant || m.Team == gc.TeamDire {
		return false
	}
	return seatSatisfied(hs.Strategy, m)
}

// seatSatisfied reports whether the host sits where a strategy puts it.
// self_kick takes the host out of the broadcaster slot too, so a launch
// that failed with caster really moves the host when it falls through.
func seatSatisfied(strategy string, m gc.Member) bool {
	if !m.TeamKnown {
		return false
	}
	switch strategy {
	case SeatCaster:
		return m.Team == gc.TeamBroadcaster
	case SeatSelfKick:
		return m.Team != gc.TeamRadiant && m.Team != gc.TeamDire && m.Team != gc.TeamBroadcaster
	case SeatPlayerPool:
		return m.Team == gc.TeamPlayerPool
	case SeatSpectator:
		return m.Team == gc.TeamSpectator
	case SeatNoTeam:
		return m.Team == gc.TeamNoTeam
	}
	return false
}

// enforceState is the per-member history the enforcement plan honours.
type enforceState struct {
	Last  time.Time
	Count int
}

// planEnforcement lists the seating kicks for a lobby in waiting or ready:
// rostered players on the other side and strangers on a side are kicked to
// the pool. With kick_strangers, strangers on a side or in the pool are
// kicked out of the lobby; the caster and spectator slots are left alone.
// The host, members without a known team and coaches are never touched, and
// each member gets at most one action per lobby.enforce_cooldown and
// maxEnforcements in all.
func planEnforcement(l Lobby, v *gc.LobbyView, host uint64, now time.Time, history map[uint32]enforceState, cfg config.LobbyConfig) []action {
	roster := l.Spec.Teams
	if v == nil || v.State != gc.LobbyUI {
		return nil
	}
	var out []action
	for _, m := range v.Members {
		if !player(m, host) {
			continue
		}
		account := gc.AccountID(m.SteamID)
		onSide := m.Team == gc.TeamRadiant || m.Team == gc.TeamDire
		var a action
		if side, ok := roster.Side(account); ok {
			if !onSide || m.Team == side.team() {
				continue
			}
			a = action{Kind: actKickFromTeam, Account: account, Reason: "wrong_side"}
		} else {
			switch {
			case roster.Empty():
				continue
			case l.Spec.KickStrangers && (onSide || m.Team == gc.TeamPlayerPool):
				a = action{Kind: actKick, Account: account, Reason: "stranger"}
			case onSide:
				a = action{Kind: actKickFromTeam, Account: account, Reason: "stranger"}
			default:
				continue
			}
		}
		h := history[account]
		if h.Count >= maxEnforcements || (!h.Last.IsZero() && now.Sub(h.Last) < cfg.EnforceCooldown) {
			continue
		}
		out = append(out, a)
	}
	return out
}
