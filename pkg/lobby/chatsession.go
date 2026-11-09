package lobby

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Chat join timing. A join that fails waits chatRetry before the next
// one, doubled after every further failure up to chatRetryMax. A join gets
// at most chatJoinTimeout: it holds the bot's GC worker while it waits, and
// the GC does not answer every join.
const (
	chatRetry       = 30 * time.Second
	chatRetryMax    = 5 * time.Minute
	chatJoinTimeout = 5 * time.Second
)

// chatSession is one lobby's chat state. It belongs to the tracker
// goroutine that made it: every method that touches it runs there, and
// that tracker passes its own session around rather than reading e.chat,
// which the next tracker of a requeued lobby replaces.
type chatSession struct {
	// channel is the joined channel, 0 while not joined. session is the
	// bot's ReadySince when it was joined: a new GC session has left every
	// channel, so a new ReadySince means joining again.
	channel uint64
	session time.Time
	retryAt time.Time
	// failures counts the joins that failed in a row.
	failures int
	// joining is set while a join runs; its result arrives on joined.
	joining bool
	joined  chan chatJoin
	greeted bool

	names     map[uint32]string
	cooldowns map[cooldownKey]time.Time
	countdown *countdown
	queue     *sendQueue
}

type cooldownKey struct {
	account uint32
	cmd     string
}

// chatJoin is the result of one join.
type chatJoin struct {
	session time.Time
	channel uint64
	err     error
}

// countdown is a start announced in chat that launches at at, by the
// manager's clock. timer only wakes the tracker then; the tracker also
// checks at on every tick.
type countdown struct {
	by    uint32
	force bool
	at    time.Time
	timer *time.Timer
}

func newChatSession() *chatSession {
	return &chatSession{
		names: map[uint32]string{}, cooldowns: map[cooldownKey]time.Time{},
		queue: newSendQueue(), joined: make(chan chatJoin, 1),
	}
}

// countdownC is the running countdown's timer channel, nil without one.
func (cs *chatSession) countdownC() <-chan time.Time {
	if cs == nil || cs.countdown == nil {
		return nil
	}
	return cs.countdown.timer.C
}

// joinedC delivers the result of the running join, nil with chat off.
func (cs *chatSession) joinedC() <-chan chatJoin {
	if cs == nil {
		return nil
	}
	return cs.joined
}

func (cs *chatSession) stopCountdown() *countdown {
	cd := cs.countdown
	if cd != nil {
		cd.timer.Stop()
		cs.countdown = nil
	}
	return cd
}

// chatNames are the persona names seen in the lobby's chat, nil with chat
// off.
func (e *entry) chatNames() map[uint32]string {
	if e.chat == nil {
		return nil
	}
	return e.chat.names
}

// say queues a message for the lobby's chat; without a joined channel it is
// dropped.
func (e *entry) say(text string) {
	cs := e.chat
	if cs == nil || cs.channel == 0 || text == "" {
		return
	}
	cs.queue.push(chatLine{Channel: cs.channel, Text: truncateChat(text)})
}

// startChat subscribes to the bot's chat and starts the lobby's send queue.
// It returns the message channel and the session (both nil with chat off)
// and the func that stops both.
func (m *Manager) startChat(ctx context.Context, e *entry, h bot.Handle) (<-chan gc.ChatMessage, *chatSession, func()) {
	if !m.opts.Lobby.Chat.Enabled {
		return nil, nil, func() {}
	}
	msgs, stop := h.SubscribeChat()
	cs := newChatSession()
	// A lobby re-adopted after a restart was greeted by the last process.
	cs.greeted = e.snapshot().GCLobbyID != 0
	e.chat = cs
	q := cs.queue
	qctx, cancel := context.WithCancel(ctx)
	m.wg.Go(func() {
		q.run(qctx, m.opts.Lobby.Chat.SendInterval, func(l chatLine) error {
			return m.doGC(qctx, h, "send chat", func(_ context.Context, c gc.Client) error { return c.SendChat(l.Channel, l.Text) })
		}, func(err error) {
			m.log.Debug("chat message not sent", "lobby", e.id, "err", err)
		})
	})
	return msgs, cs, func() {
		cancel()
		stop()
	}
}

// leaveChat leaves the channel of a lobby that ended. A lobby left running
// at shutdown keeps it: the next start joins again anyway.
func (m *Manager) leaveChat(ctx context.Context, e *entry, h bot.Handle, cs *chatSession) {
	if cs == nil {
		return
	}
	cs.stopCountdown()
	if cs.channel == 0 || !e.snapshot().State.Terminal() {
		return
	}
	actx, cancel := m.actionCtx(ctx)
	defer cancel()
	ch := cs.channel
	cs.channel = 0
	if err := h.Do(actx, "leave chat", func(_ context.Context, c gc.Client) error { return c.LeaveChat(ch) }); err != nil {
		m.log.Debug("lobby chat not left", "lobby", e.id, "err", err)
	}
}

// chatStep is the chat part of a tracker step, run with op held while the
// bot is ready and sees the lobby: start a join (again after a reconnect),
// drop a countdown the lobby no longer allows, and announce what events
// mean for the players. The join runs on its own goroutine, so a GC that
// does not answer it never holds op; chatJoined applies its result.
func (m *Manager) chatStep(ctx context.Context, e *entry, h bot.Handle, l Lobby, st bot.Status, evs []newEvent) {
	cs := e.chat
	if cs == nil || l.GCLobbyID == 0 {
		return
	}
	now := m.now()
	if cs.channel != 0 && !cs.session.Equal(st.ReadySince) {
		cs.channel = 0
	}
	// Chat is for the players getting ready: a lobby past that does not
	// join, or join again after a reconnect.
	prep := l.State == StateWaiting || l.State == StateReady
	if cs.channel == 0 && !cs.joining && prep && !now.Before(cs.retryAt) {
		cs.joining = true
		m.joinChat(ctx, e, h, cs, l.GCLobbyID, st.ReadySince)
	}
	if cd := cs.countdown; cd != nil {
		if !prep || (!cd.force && l.State != StateReady) {
			cs.stopCountdown()
			if prep {
				e.say("Start cancelled: the lobby is not ready any more.")
			}
		}
	}
	p := m.opts.Lobby.Chat.Prefix
	for _, ev := range evs {
		switch ev.Type {
		case "lobby.ready":
			if l.Spec.Launch == LaunchAuto {
				e.say(fmt.Sprintf("Everyone is set. Launching in %d s.", seconds(m.opts.Lobby.AutoLaunchDelay)))
			} else {
				e.say("Everyone is set. Type " + p + "start to launch.")
			}
		case "lobby.launch_failed":
			if l.State == StateWaiting {
				e.say("The launch did not reach a game; the lobby is open again.")
			}
		}
	}
}

// joinChat starts a join of the lobby's chat. Its result goes to
// cs.joined, which holds one: only one join runs at a time.
func (m *Manager) joinChat(ctx context.Context, e *entry, h bot.Handle, cs *chatSession, gcLobbyID uint64, session time.Time) {
	timeout := min(chatJoinTimeout, m.opts.GC.RequestTimeout)
	joined := cs.joined
	m.wg.Go(func() {
		jctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var id uint64
		err := h.Do(jctx, "join lobby chat", func(ctx context.Context, c gc.Client) error {
			var err error
			id, err = c.JoinLobbyChat(ctx, gcLobbyID)
			return err
		})
		joined <- chatJoin{session: session, channel: id, err: err}
	})
}

// chatJoined applies the result of a join on the tracker goroutine. A join
// from an older GC session is kept: the next step sees the new session and
// joins again.
func (m *Manager) chatJoined(e *entry, cs *chatSession, j chatJoin) {
	cs.joining = false
	if j.err != nil {
		wait := chatRetry << min(cs.failures, 4)
		wait = min(wait, chatRetryMax)
		cs.failures++
		cs.retryAt = m.now().Add(wait)
		m.log.Warn("could not join the lobby chat; retrying", "lobby", e.id, "retry_in", wait, "failures", cs.failures, "err", j.err)
		return
	}
	cs.failures = 0
	cs.channel, cs.session = j.channel, j.session
	m.log.Info("joined the lobby chat", "lobby", e.id, "channel_id", j.channel)
	if !cs.greeted {
		cs.greeted = true
		e.say("Hi, I host this lobby. Type " + m.opts.Lobby.Chat.Prefix + "help for commands.")
	}
}

func seconds(d time.Duration) int { return int(d.Round(time.Second) / time.Second) }

// onChat handles one chat message. Only current members of the lobby can
// run commands, identified by the account ID the GC put on the message;
// the bot's own messages are ignored.
func (m *Manager) onChat(e *entry, cs *chatSession, h bot.Handle, msg gc.ChatMessage) {
	if cs == nil || cs.channel == 0 || msg.ChannelID != cs.channel {
		return
	}
	st := h.Status()
	if msg.AccountID == 0 || msg.AccountID == gc.AccountID(st.SteamID) {
		return
	}
	cfg := m.opts.Lobby
	cmd, ok := parseCommand(cfg.Chat.Prefix, msg.Text)
	if !ok {
		return
	}
	e.op.Lock()
	defer e.op.Unlock()
	l := e.snapshot()
	if l.State.Terminal() {
		return
	}
	view := lobbyView(h, l.GCLobbyID)
	if !inLobby(view, msg.AccountID) {
		return
	}
	if n := cleanName(msg.PersonaName); n != "" {
		cs.names[msg.AccountID] = n
	}
	now := m.now()
	key := cooldownKey{msg.AccountID, cooldownGroup(cmd)}
	if until, ok := cs.cooldowns[key]; ok && now.Before(until) {
		return
	}
	out := runCommand(cmd, chatInput{
		Lobby: l, View: view, Host: st.SteamID, Account: msg.AccountID,
		Admin: isAdmin(l, cfg, msg.AccountID), Countdown: cs.countdown != nil, Names: cs.names, Cfg: cfg,
	})
	if out.Reply == "" && out.Effect == effNone {
		return
	}
	m.log.Info("chat command", "lobby", l.ID, "account_id", msg.AccountID, "command", cmd)
	if out.Cooldown > 0 {
		cs.cooldowns[key] = now.Add(out.Cooldown)
	}
	switch out.Effect {
	case effReady, effUnready:
		if err := m.setReady(e, msg.AccountID, out.Effect == effReady); err != nil {
			m.log.Warn("ready flag not saved", "lobby", l.ID, "account_id", msg.AccountID, "err", err)
			return
		}
	case effStart:
		cs.countdown = &countdown{
			by: msg.AccountID, force: out.Force,
			at: now.Add(cfg.Chat.StartCountdown), timer: time.NewTimer(cfg.Chat.StartCountdown),
		}
	case effCancel:
		cs.stopCountdown()
	}
	e.say(out.Reply)
}

// setReady stores a player's ready flag with its event. The caller holds
// op.
func (m *Manager) setReady(e *entry, account uint32, ready bool) error {
	_, err := m.commit(e, func(cur *Lobby) ([]newEvent, error) {
		p := cur.Spec.Teams.Player(account)
		if p == nil {
			return nil, ErrInvalidState
		}
		p.Ready = ready
		typ, data := "lobby.player_unready", map[string]any{"account_id": account, "reason": "chat"}
		if ready {
			typ, data = "lobby.player_ready", map[string]any{"account_id": account}
		}
		return []newEvent{{Type: typ, Data: data}}, nil
	})
	return err
}

// fireCountdown launches once a start countdown has run out by the
// manager's clock. The launch attempts are checked again here: an admin's
// start is forced past a lobby that is not ready, but after
// lobby.max_launch_attempts only a forced API launch may try again.
func (m *Manager) fireCountdown(e *entry, cs *chatSession) {
	if cs == nil || cs.countdown == nil || m.now().Before(cs.countdown.at) {
		return
	}
	cd := cs.stopCountdown()
	e.op.Lock()
	defer e.op.Unlock()
	if !canAutoLaunch(e.snapshot(), m.opts.Lobby) {
		m.log.Info("chat start refused", "lobby", e.id, "account_id", cd.by, "reason", "launch_attempts_exhausted")
		e.say("Start failed: no launch attempts left.")
		return
	}
	if _, err := m.launchHeld(e, LaunchRequest{Force: cd.force, Trigger: TriggerChat, By: cd.by}); err != nil {
		m.log.Info("chat start refused", "lobby", e.id, "account_id", cd.by, "err", err)
		e.say("Start failed: " + startFailure(err) + ".")
		return
	}
	m.log.Info("chat start", "lobby", e.id, "account_id", cd.by, "force", cd.force)
	e.say("Launching.")
}

// startFailure words a refused launch for the lobby chat.
func startFailure(err error) string {
	switch {
	case errors.Is(err, ErrNotReady):
		return "not everyone is ready"
	case errors.Is(err, ErrHostNotSeated):
		return "the bot is still in a player slot"
	case errors.Is(err, ErrUnavailable):
		return "the bot cannot see the lobby right now"
	case errors.Is(err, ErrInvalidState):
		return "the lobby cannot start now"
	}
	return "an internal error"
}
