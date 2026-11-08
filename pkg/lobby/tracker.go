package lobby

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// seatPoll is how often a host seat attempt re-reads the snapshot.
const seatPoll = 50 * time.Millisecond

func (m *Manager) startTracker(e *entry) {
	ctx, cancel := context.WithCancel(m.ctx)
	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()
	m.wg.Go(func() {
		defer cancel()
		m.track(ctx, e)
	})
}

// track follows one bot-bound lobby until it ends or the manager drains.
func (m *Manager) track(ctx context.Context, e *entry) {
	l := e.snapshot()
	h, ok := m.opts.Bots.Bot(l.Bot)
	if !ok {
		m.fail(e, "bot_lost")
		return
	}
	lobbies, stopLobbies := h.SubscribeLobby()
	defer stopLobbies()
	invites, stopInvites := h.SubscribeInvites()
	defer stopInvites()

	e.op.Lock()
	needCreate := !e.readopt && l.State == StateCreating && l.GCLobbyID == 0
	e.op.Unlock()
	if needCreate {
		m.create(ctx, e, h)
	}
	tick := time.NewTicker(m.opts.Tick)
	defer tick.Stop()
	for {
		if ctx.Err() != nil || e.snapshot().State.Terminal() {
			return
		}
		m.step(ctx, e, h)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.poke:
		case <-lobbies:
		case inv := <-invites:
			m.confirmInvite(e, inv)
		}
	}
}

func (m *Manager) fail(e *entry, reason string) {
	if _, err := m.commit(e, func(l *Lobby) ([]newEvent, error) {
		l.State, l.StateReason, l.StateSince = StateFailed, reason, m.now()
		return nil, nil
	}); err != nil {
		m.log.Error("could not fail lobby", "lobby", e.id, "err", err)
	}
}

// actionCtx is for GC calls that must finish even when the tracker is
// being stopped, such as the destroy after a timeout.
func (m *Manager) actionCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), m.opts.GC.RequestTimeout)
}

func (m *Manager) doGC(ctx context.Context, h bot.Handle, op string, fn func(ctx context.Context, c gc.Client) error) error {
	actx, cancel := context.WithTimeout(ctx, m.opts.GC.RequestTimeout)
	defer cancel()
	return h.Do(actx, op, fn)
}

// create has the bot create the lobby and checks the league read back.
// It does not hold the lobby's op lock: a create may take up to
// gc.create_timeout, and a DELETE meanwhile must answer at once. Every
// result is applied through commit, which keeps a closing state.
func (m *Manager) create(ctx context.Context, e *entry, h bot.Handle) {
	l := e.snapshot()
	details := l.Spec.details(m.opts.Lobby, 0)
	cctx, cancel := context.WithTimeout(ctx, m.opts.GC.CreateTimeout)
	defer cancel()
	var view *gc.LobbyView
	err := h.Do(cctx, "create lobby", func(ctx context.Context, c gc.Client) error {
		v, err := c.CreateLobby(ctx, details)
		view = v
		return err
	})
	if ctx.Err() != nil {
		// Shutting down: leave the row in creating for re-adoption.
		return
	}
	host := h.Status().SteamID
	if err != nil {
		if errors.Is(err, gc.ErrNotReady) || errors.Is(err, bot.ErrStopped) {
			m.backToPending(e, err)
			return
		}
		code := "gc_error"
		var ce *gc.CreateError
		switch {
		case errors.As(err, &ce) && ce.Reason == gc.CreateLeagueRejected:
			code = "league_rejected"
		case errors.Is(err, context.DeadlineExceeded):
			code = "create_timeout"
		}
		m.log.Warn("lobby create failed", "lobby", l.ID, "bot", l.Bot, "code", code, "err", err)
		m.cleanupCreated(ctx, h, host, 0)
		m.createFailed(e, code, err.Error())
		return
	}
	if l.Spec.LeagueID != 0 && view.LeagueID != l.Spec.LeagueID {
		msg := "the lobby came back with league " + strconv.FormatUint(uint64(view.LeagueID), 10) +
			"; the bot does not administer league " + strconv.FormatUint(uint64(l.Spec.LeagueID), 10)
		m.log.Warn("league rejected on read-back", "lobby", l.ID, "bot", l.Bot, "want", l.Spec.LeagueID, "got", view.LeagueID)
		m.cleanupCreated(ctx, h, host, view.LobbyID)
		m.createFailed(e, "league_rejected", msg)
		return
	}
	merged := details
	merged.LobbyID = view.LobbyID
	if merged.TVDelaySeconds == 0 {
		merged.TVDelaySeconds = view.TVDelaySeconds
	}
	if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		cur.GCLobbyID, cur.Details = view.LobbyID, merged
		evs := []newEvent{{Type: "lobby.created", Data: map[string]any{
			"gc_lobby_id": strconv.FormatUint(view.LobbyID, 10), "league_verified": cur.LeagueVerified,
		}}}
		if cur.State == StateCreating {
			cur.State, cur.StateReason, cur.StateSince = StateWaiting, "created", m.now()
		}
		return evs, nil
	}); err != nil {
		m.log.Error("could not record the created lobby", "lobby", l.ID, "err", err)
	}
}

// cleanupCreated destroys what a failed create may have left behind.
func (m *Manager) cleanupCreated(ctx context.Context, h bot.Handle, host, lobbyID uint64) {
	v, ok := h.CurrentLobby()
	if !ok || v == nil || v.LeaderID != host || (lobbyID != 0 && v.LobbyID != lobbyID) {
		return
	}
	actx, cancel := m.actionCtx(ctx)
	defer cancel()
	if err := h.Do(actx, "destroy lobby", func(ctx context.Context, c gc.Client) error {
		err := c.Destroy(ctx)
		c.Leave()
		return err
	}); err != nil {
		m.log.Warn("could not destroy the rejected lobby", "lobby_id", v.LobbyID, "err", err)
	}
}

func (m *Manager) createFailed(e *entry, code, msg string) {
	if _, err := m.commit(e, func(l *Lobby) ([]newEvent, error) {
		evs := []newEvent{{Type: "lobby.create_failed", Data: map[string]any{"code": code, "message": msg}}}
		if l.State == StateClosing {
			l.State, l.StateReason = StateClosed, "api"
		} else {
			l.State, l.StateReason = StateFailed, code
		}
		l.StateSince = m.now()
		return evs, nil
	}); err != nil {
		m.log.Error("could not fail lobby", "lobby", e.id, "err", err)
	}
}

// backToPending returns the lease of a bot that could not take the create
// (nothing was sent) and queues the lobby again.
func (m *Manager) backToPending(e *entry, cause error) {
	m.log.Info("bot not ready for the create; queueing the lobby again", "lobby", e.id, "err", cause)
	if _, err := m.commit(e, func(l *Lobby) ([]newEvent, error) {
		if l.State == StateClosing {
			l.State, l.StateReason = StateClosed, "api"
		} else {
			l.State, l.StateReason = StatePending, "bot_not_ready"
		}
		l.StateSince, l.Bot, l.LeagueVerified = m.now(), "", nil
		return nil, nil
	}); err != nil {
		m.log.Error("could not requeue lobby", "lobby", e.id, "err", err)
		return
	}
	e.mu.Lock()
	lease := e.lease
	e.lease = nil
	cancel := e.cancel
	e.mu.Unlock()
	if lease != nil {
		m.opts.Pool.Release(lease)
	}
	if cancel != nil {
		cancel()
	}
	m.kickScheduler()
}

func (m *Manager) confirmInvite(e *entry, inv gc.InvitationCreated) {
	account := gc.AccountID(inv.SteamID)
	l := e.snapshot()
	if p := l.Spec.Teams.Player(account); p == nil || p.InviteConfirmed {
		return
	}
	if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		if p := cur.Spec.Teams.Player(account); p != nil {
			p.InviteConfirmed = true
		}
		return nil, nil
	}); err != nil {
		m.log.Warn("could not record an invite confirmation", "lobby", e.id, "err", err)
	}
}

// step runs derive on the current snapshot, stores the result and sends
// what it asks for, then does the per-state work: host seat, invites and
// seating in waiting and ready, the launch in launching, the destroy in
// closing.
func (m *Manager) step(ctx context.Context, e *entry, h bot.Handle) {
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if l.State.Terminal() || l.State == StatePending || l.State == StateCreating && !e.readopt {
		return
	}
	now := m.now()
	st := h.Status()
	botReady := st.Ready && !st.LobbyStale
	botLost := st.State == bot.StateNeedsAttention || st.State == bot.StateSuspended
	host := st.SteamID
	view := lobbyView(h, l.GCLobbyID)

	if e.readopt {
		if !m.readopt(ctx, e, h, st, l) {
			return
		}
		l = e.snapshot()
		view = lobbyView(h, l.GCLobbyID)
	}

	if botReady && view == nil {
		if e.missingSince.IsZero() {
			e.missingSince = now
		}
	} else {
		e.missingSince = time.Time{}
	}
	var memberEvs []newEvent
	if view != nil {
		memberEvs = m.observeMembers(e, l, view, host)
	}
	in := inputs{
		Now: now, View: view, Host: host, BotReady: botReady, BotLost: botLost,
		LostGrace: m.opts.LostGrace, Cfg: m.opts.Lobby,
	}
	if !e.missingSince.IsZero() {
		in.MissingFor = now.Sub(e.missingSince)
	}
	res := derive(l, in)
	if len(memberEvs) > 0 || len(res.Events) > 0 || changed(l, res.Lobby) {
		next, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
			if cur.Version != l.Version {
				return nil, errors.New("lobby changed during the step")
			}
			live := cur.Live
			*cur = res.Lobby
			cur.Live = live
			return append(memberEvs, res.Events...), nil
		})
		if err != nil {
			m.log.Warn("lobby step not saved", "lobby", l.ID, "err", err)
			return
		}
		l = next
	}
	for _, a := range res.Actions {
		m.act(ctx, e, h, a)
	}
	if l.State.Terminal() || !botReady || view == nil {
		return
	}
	switch l.State {
	case StateWaiting, StateReady:
		m.prepare(ctx, e, h, l, view, host)
	case StateLaunching:
		if e.launchFor.Equal(l.StateSince) || view.State != gc.LobbyUI {
			return
		}
		m.sendLaunch(ctx, e, h, l, view, host)
	case StateClosing:
		if e.destroyFor.Equal(l.StateSince) {
			return
		}
		e.destroyFor = l.StateSince
		if err := m.doGC(ctx, h, "destroy lobby", func(ctx context.Context, c gc.Client) error { return c.Destroy(ctx) }); err != nil {
			m.log.Warn("destroy not confirmed", "lobby", l.ID, "err", err)
		}
	}
}

// changed reports whether derive changed anything worth storing.
func changed(a, b Lobby) bool {
	return a.State != b.State || a.StateReason != b.StateReason || a.LaunchAttempts != b.LaunchAttempts ||
		a.HostSeat != b.HostSeat || !reflect.DeepEqual(a.Match, b.Match)
}

func (m *Manager) act(ctx context.Context, e *entry, h bot.Handle, a action) {
	actx, cancel := m.actionCtx(ctx)
	defer cancel()
	err := h.Do(actx, a.Kind.String(), func(ctx context.Context, c gc.Client) error {
		switch a.Kind {
		case actDestroy:
			err := c.Destroy(ctx)
			c.Leave()
			return err
		case actLeave:
			c.Leave()
		case actKick:
			return c.Kick(a.Account)
		case actKickFromTeam:
			return c.KickFromTeam(a.Account)
		}
		return nil
	})
	if err != nil {
		m.log.Warn("lobby action failed", "lobby", e.id, "action", a.Kind.String(), "reason", a.Reason, "err", err)
	}
}

// observeMembers updates the live view and reports joins, leaves and
// seat changes. The first snapshot after a (re)start only sets the
// baseline.
func (m *Manager) observeMembers(e *entry, l Lobby, v *gc.LobbyView, host uint64) []newEvent {
	cur := map[uint32]gc.Member{}
	live := &Live{Pending: slices.Clone(v.Pending)}
	for _, mem := range v.Members {
		a := gc.AccountID(mem.SteamID)
		seat := "unknown"
		if mem.TeamKnown {
			seat = mem.Team.String()
		}
		live.Members = append(live.Members, Member{AccountID: a, Seat: seat, Coach: mem.Coach()})
		if mem.SteamID != host {
			cur[a] = mem
			if !slices.Contains(e.seen, a) {
				e.seen = append(e.seen, a)
			}
		}
	}
	live.Seen = slices.Clone(e.seen)
	e.mu.Lock()
	e.lobby.Live = live
	e.mu.Unlock()

	prev := e.members
	e.members = cur
	if !e.membersInit {
		e.membersInit = true
		return nil
	}
	accounts := make([]uint32, 0, len(cur)+len(prev))
	for a := range cur {
		accounts = append(accounts, a)
	}
	for a := range prev {
		if _, ok := cur[a]; !ok {
			accounts = append(accounts, a)
		}
	}
	slices.Sort(accounts)
	var evs []newEvent
	for _, a := range accounts {
		side, rostered := l.Spec.Teams.Side(a)
		ref := ""
		if rostered {
			ref = l.Spec.Teams.Team(side).TeamRef
		}
		now, inNow := cur[a]
		was, inBefore := prev[a]
		switch {
		case inNow && !inBefore:
			evs = append(evs, newEvent{Type: "lobby.player_joined", Data: map[string]any{"account_id": a, "rostered": rostered, "team_ref": nullable(ref)}})
		case !inNow && inBefore:
			evs = append(evs, newEvent{Type: "lobby.player_left", Data: map[string]any{"account_id": a, "rostered": rostered, "team_ref": nullable(ref)}})
			continue
		}
		onSide := now.TeamKnown && !now.Coach() && (now.Team == gc.TeamRadiant || now.Team == gc.TeamDire)
		moved := !inBefore || was.Team != now.Team || was.TeamKnown != now.TeamKnown
		if onSide && moved {
			correct := l.Spec.Teams.Empty() || (rostered && side.team() == now.Team)
			evs = append(evs, newEvent{Type: "lobby.player_seated", Data: map[string]any{"account_id": a, "seat": now.Team.String(), "correct": correct}})
		}
	}
	return evs
}

// prepare is the work of waiting and ready: get the host out of the
// player slots, invite the roster once, and enforce seating.
func (m *Manager) prepare(ctx context.Context, e *entry, h bot.Handle, l Lobby, v *gc.LobbyView, host uint64) {
	if !l.HostSeat.Confirmed && (e.seatFailedAt.IsZero() || m.now().Sub(e.seatFailedAt) >= seatRetry) {
		hs := m.seatHost(ctx, h, l, host)
		e.seatFailedAt = time.Time{}
		if !hs.Confirmed {
			e.seatFailedAt = m.now()
		}
		if hs != l.HostSeat {
			var err error
			if l, err = m.commit(e, func(cur *Lobby) ([]newEvent, error) {
				cur.HostSeat = hs
				return nil, nil
			}); err != nil {
				m.log.Warn("host seat not saved", "lobby", l.ID, "err", err)
			}
		}
		if v = lobbyView(h, l.GCLobbyID); v == nil {
			return
		}
	}
	if err := m.autoInvite(ctx, e, h, v); err != nil {
		m.log.Warn("invites incomplete", "lobby", l.ID, "err", err)
	}
	m.enforceSeating(ctx, e, h, v, host)
}

// autoInvite sends the one automatic invite to rostered players who never
// had one and are neither in the lobby nor already invited.
func (m *Manager) autoInvite(ctx context.Context, e *entry, h bot.Handle, v *gc.LobbyView) error {
	l := e.snapshot()
	if l.State != StateWaiting && l.State != StateReady {
		return nil
	}
	var want []uint32
	for _, p := range l.Spec.Teams.All() {
		if p.InvitesSent > 0 || inLobby(v, p.AccountID) {
			continue
		}
		if v != nil && slices.Contains(v.Pending, gc.SteamID64(p.AccountID)) {
			continue
		}
		want = append(want, p.AccountID)
	}
	return m.sendInvites(ctx, e, h, want)
}

func (m *Manager) enforceSeating(ctx context.Context, e *entry, h bot.Handle, v *gc.LobbyView, host uint64) {
	l := e.snapshot()
	now := m.now()
	acts := planEnforcement(l, v, host, now, e.enforce, m.opts.Lobby)
	if len(acts) == 0 {
		return
	}
	var evs []newEvent
	for _, a := range acts {
		m.act(ctx, e, h, a)
		hist := e.enforce[a.Account]
		hist.Last, hist.Count = now, hist.Count+1
		e.enforce[a.Account] = hist
		from := "team"
		if a.Kind == actKick {
			from = "lobby"
		}
		evs = append(evs, newEvent{Type: "lobby.player_kicked", Data: map[string]any{"account_id": a.Account, "from": from, "reason": a.Reason}})
		if hist.Count == maxEnforcements {
			m.log.Warn("stopped enforcing a seat", "lobby", l.ID, "account_id", a.Account, "kicks", hist.Count)
			evs = append(evs, newEvent{Type: "lobby.enforcement_stopped", Data: map[string]any{"account_id": a.Account, "kicks": hist.Count}})
		}
	}
	if _, err := m.commit(e, func(*Lobby) ([]newEvent, error) { return evs, nil }); err != nil {
		m.log.Warn("kicks not recorded", "lobby", l.ID, "err", err)
	}
}

// seatHost tries the host seat strategies from HostSeat.Next on, each
// confirmed by the snapshot within lobby.host_seat_confirm_timeout.
func (m *Manager) seatHost(ctx context.Context, h bot.Handle, l Lobby, host uint64) HostSeat {
	strategies := m.opts.Lobby.HostSeatStrategy
	start := min(max(l.HostSeat.Next, 0), len(strategies)-1)
	out := HostSeat{Next: start}
	if len(strategies) == 0 {
		return out
	}
	for i := start; i < len(strategies); i++ {
		s := strategies[i]
		out.Strategy = s
		if mem, ok := hostMember(lobbyView(h, l.GCLobbyID), host); ok && seatSatisfied(s, mem) {
			return HostSeat{Strategy: s, Confirmed: true, Team: mem.Team.String(), Next: i}
		}
		err := m.doGC(ctx, h, "host seat "+s, func(_ context.Context, c gc.Client) error {
			return seatOp(c, s, gc.AccountID(host))
		})
		if err != nil {
			m.log.Warn("host seat not sent", "lobby", l.ID, "strategy", s, "err", err)
			if errors.Is(err, gc.ErrNotReady) || ctx.Err() != nil {
				break
			}
			continue
		}
		if mem, ok := m.waitSeat(ctx, h, l.GCLobbyID, host, s); ok {
			m.log.Info("host seat confirmed", "lobby", l.ID, "strategy", s, "team", mem.Team.String())
			return HostSeat{Strategy: s, Confirmed: true, Team: mem.Team.String(), Next: i}
		}
		m.log.Warn("host seat not confirmed; trying the next strategy", "lobby", l.ID, "strategy", s,
			"timeout", m.opts.Lobby.HostSeatConfirmTimeout)
	}
	if mem, ok := hostMember(lobbyView(h, l.GCLobbyID), host); ok && mem.TeamKnown {
		out.Team = mem.Team.String()
	}
	return out
}

func seatOp(c gc.Client, strategy string, account uint32) error {
	switch strategy {
	case SeatCaster:
		return c.JoinBroadcastChannel(0)
	case SeatSelfKick:
		return c.KickFromTeam(account)
	case SeatPlayerPool:
		return c.JoinTeam(gc.TeamPlayerPool, 0)
	case SeatSpectator:
		return c.JoinTeam(gc.TeamSpectator, 0)
	case SeatNoTeam:
		return c.JoinTeam(gc.TeamNoTeam, 0)
	}
	return errors.New("unknown host seat strategy " + strconv.Quote(strategy))
}

func (m *Manager) waitSeat(ctx context.Context, h bot.Handle, lobbyID, host uint64, strategy string) (gc.Member, bool) {
	deadline := time.NewTimer(m.opts.Lobby.HostSeatConfirmTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(seatPoll)
	defer poll.Stop()
	for {
		if mem, ok := hostMember(lobbyView(h, lobbyID), host); ok && seatSatisfied(strategy, mem) {
			return mem, true
		}
		select {
		case <-ctx.Done():
			return gc.Member{}, false
		case <-deadline.C:
			return gc.Member{}, false
		case <-poll.C:
		}
	}
}

// sendLaunch re-checks the host seat right before the launch and sends it.
// The attempt is counted before the send.
func (m *Manager) sendLaunch(ctx context.Context, e *entry, h bot.Handle, l Lobby, v *gc.LobbyView, host uint64) {
	if !hostGate(v, host, l.HostSeat) {
		hs := m.seatHost(ctx, h, l, host)
		v = lobbyView(h, l.GCLobbyID)
		if !hs.Confirmed || !hostGate(v, host, hs) {
			m.log.Warn("launch refused: host still on a team", "lobby", l.ID, "strategy", hs.Strategy)
			if _, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
				cur.HostSeat = hs
				cur.State, cur.StateReason, cur.StateSince = StateWaiting, "host_not_seated", m.now()
				return []newEvent{{Type: "lobby.launch_failed", Data: map[string]any{
					"reason": "host_not_seated", "attempt": cur.LaunchAttempts, "next_strategy": nullable(hs.Strategy),
				}}}, nil
			}); err != nil {
				m.log.Warn("launch refusal not saved", "lobby", l.ID, "err", err)
			}
			return
		}
		l.HostSeat = hs
	}
	next, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		if cur.State != StateLaunching || !cur.StateSince.Equal(l.StateSince) {
			return nil, ErrInvalidState
		}
		cur.HostSeat = l.HostSeat
		cur.LaunchAttempts++
		return nil, nil
	})
	if err != nil {
		return
	}
	e.launchFor = next.StateSince
	if err := m.doGC(ctx, h, "launch lobby", func(_ context.Context, c gc.Client) error { return c.Launch() }); err != nil {
		m.log.Warn("launch not sent", "lobby", l.ID, "err", err)
		return
	}
	m.log.Info("launch sent", "lobby", l.ID, "attempt", next.LaunchAttempts, "host_seat_strategy", l.HostSeat.Strategy)
}

// readopt settles a lobby found in the database at startup. It returns
// true once the tracker can carry on normally; until then it keeps
// waiting for the bot, up to lobby.readopt_timeout.
func (m *Manager) readopt(ctx context.Context, e *entry, h bot.Handle, st bot.Status, l Lobby) bool {
	now := m.now()
	ready := st.Ready && !st.LobbyStale
	expired := !now.Before(e.readoptUntil)
	if !ready {
		if expired {
			m.lost(e, l)
		}
		return false
	}
	cur, _ := h.CurrentLobby()
	// The SO arrives a moment after the welcome.
	if cur == nil && now.Sub(st.ReadySince) < m.opts.LostGrace && !expired {
		return false
	}
	switch {
	case cur != nil && l.GCLobbyID != 0 && cur.LobbyID == l.GCLobbyID:
		e.readopt = false
		m.log.Info("lobby re-adopted", "lobby", l.ID, "state", l.State, "gc_lobby_id", l.GCLobbyID)
		return true
	case cur != nil && l.State == StateCreating && l.GCLobbyID == 0 && cur.LeaderID == st.SteamID && cur.GameName == l.Spec.Name:
		// The create went through just before the restart.
		details := l.Spec.details(m.opts.Lobby, cur.LobbyID)
		if details.TVDelaySeconds == 0 {
			details.TVDelaySeconds = cur.TVDelaySeconds
		}
		if _, err := m.commit(e, func(c *Lobby) ([]newEvent, error) {
			c.GCLobbyID, c.Details = cur.LobbyID, details
			c.State, c.StateReason, c.StateSince = StateWaiting, "readopted", now
			return []newEvent{{Type: "lobby.created", Data: map[string]any{
				"gc_lobby_id": strconv.FormatUint(cur.LobbyID, 10), "league_verified": c.LeagueVerified,
			}}}, nil
		}); err != nil {
			m.log.Error("could not adopt the created lobby", "lobby", l.ID, "err", err)
			return false
		}
		e.readopt = false
		return true
	}
	m.lost(e, l)
	return false
}

// lost ends a re-adopted lobby whose GC lobby is gone.
func (m *Manager) lost(e *entry, l Lobby) {
	state, reason := StateFailed, "lobby_lost"
	switch l.State {
	case StateCreating:
		reason = "create_interrupted"
	case StateClosing:
		state, reason = StateClosed, "destroyed"
	case StateInGame, StatePostGame:
		reason = "lost_in_game"
	}
	m.log.Warn("re-adopted lobby is gone", "lobby", l.ID, "state", l.State, "bot", l.Bot, "reason", reason)
	e.readopt = false
	if _, err := m.commit(e, func(c *Lobby) ([]newEvent, error) {
		c.State, c.StateReason, c.StateSince = state, reason, m.now()
		return nil, nil
	}); err != nil {
		m.log.Error("could not end the lost lobby", "lobby", l.ID, "err", err)
	}
}
