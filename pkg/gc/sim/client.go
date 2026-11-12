package sim

import (
	"context"
	"fmt"
	"slices"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// subscriberBuffer is the channel size of every subscription.
const subscriberBuffer = 64

// Client is one simulated Steam account with its GC session. It
// implements gc.Client; every call answers at once.
type Client struct {
	w      *World
	bot    Bot
	events chan gc.Event

	lobbies *fanout[gc.LobbyEvent]
	invites *fanout[gc.InvitationCreated]
	chats   *fanout[gc.ChatMessage]

	// Guarded by w.mu.
	connected bool
	playing   bool
	welcomed  bool
	loggedOn  bool
	seq       uint64
	stop      chan struct{}
	lobby     *lobby
	current   *gc.LobbyView
}

var _ gc.Client = (*Client)(nil)

func newClient(w *World, b Bot) *Client {
	return &Client{
		w:       w,
		bot:     b,
		events:  make(chan gc.Event, gc.EventBuffer),
		lobbies: newFanout[gc.LobbyEvent](subscriberBuffer),
		invites: newFanout[gc.InvitationCreated](subscriberBuffer),
		chats:   newFanout[gc.ChatMessage](subscriberBuffer),
	}
}

// Bot returns the account the client simulates.
func (c *Client) Bot() Bot {
	b := c.bot
	b.Leagues = slices.Clone(b.Leagues)
	return b
}

// emit sends events in order. Never call it with the World's lock held:
// the events channel is drained by the supervisor, which may be calling
// into the World.
func (c *Client) emit(ctx context.Context, evs ...gc.Event) {
	for _, ev := range evs {
		select {
		case c.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// emitNow sends an event the receiver may miss (a popup, an invite
// confirmation) without blocking.
func (c *Client) emitNow(ev gc.Event) {
	select {
	case c.events <- ev:
	default:
	}
}

// Connect logs on at once: Connected, LoggedOn and AccountInfo follow on
// Events(). The credentials are ignored. Cancelling ctx disconnects.
func (c *Client) Connect(ctx context.Context, _ gc.Credentials) error {
	c.w.mu.Lock()
	if c.connected {
		c.w.mu.Unlock()
		return gc.ErrAlreadyConnected
	}
	c.connected, c.loggedOn = true, true
	c.seq++
	seq, stop := c.seq, make(chan struct{})
	c.stop = stop
	c.w.mu.Unlock()
	c.w.log.Info("sim: bot logged on", "bot", c.bot.Name, "conn", seq)

	steamID := gc.SteamID64(c.bot.AccountID)
	c.emit(context.Background(),
		gc.Connected{},
		gc.LoggedOn{SteamID: steamID, ExtendedResult: gc.EResultOK},
		gc.AccountInfo{PersonaName: c.bot.PersonaName},
	)
	go func() {
		select {
		case <-ctx.Done():
			c.disconnect(seq)
		case <-stop:
		}
	}()
	return nil
}

// Authenticate never succeeds: a simulated account has no password.
func (c *Client) Authenticate(context.Context, gc.Credentials) (string, error) {
	return "", fmt.Errorf("simulated account %s: %w", c.bot.Name, gc.ErrNoCredentials)
}

// Disconnect ends the connection; Disconnected follows.
func (c *Client) Disconnect() {
	c.w.mu.Lock()
	seq := c.seq
	c.w.mu.Unlock()
	c.disconnect(seq)
}

func (c *Client) disconnect(seq uint64) {
	c.w.mu.Lock()
	if !c.connected || c.seq != seq {
		c.w.mu.Unlock()
		return
	}
	c.connected, c.playing, c.welcomed = false, false, false
	close(c.stop)
	c.w.mu.Unlock()
	c.w.log.Info("sim: bot disconnected", "bot", c.bot.Name, "conn", seq)
	c.emit(context.Background(), gc.Disconnected{Conn: seq})
}

// Events implements gc.Client.
func (c *Client) Events() <-chan gc.Event { return c.events }

// SetPlaying implements gc.Client.
func (c *Client) SetPlaying(playing bool) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if !c.connected {
		return
	}
	c.playing = playing
	if !playing {
		c.welcomed = false
	}
}

// SayHello is welcomed at once while playing.
func (c *Client) SayHello() {
	c.w.mu.Lock()
	ok := c.connected && c.playing
	if ok {
		c.welcomed = true
	}
	c.w.mu.Unlock()
	if ok {
		c.emit(context.Background(), gc.GCWelcomed{})
	}
}

// SteamID implements gc.Client.
func (c *Client) SteamID() uint64 {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if !c.loggedOn {
		return 0
	}
	return gc.SteamID64(c.bot.AccountID)
}

// AccountFlags reports a full (not limited) account after the first logon.
func (c *Client) AccountFlags() (uint32, bool) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	return 0, c.loggedOn
}

// CurrentLobby implements gc.Client.
func (c *Client) CurrentLobby() (*gc.LobbyView, bool) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	return c.current, c.current != nil
}

// SubscribeLobby implements gc.Client.
func (c *Client) SubscribeLobby() (<-chan gc.LobbyEvent, func()) { return c.lobbies.subscribe() }

// SubscribeInvites implements gc.Client.
func (c *Client) SubscribeInvites() (<-chan gc.InvitationCreated, func()) {
	return c.invites.subscribe()
}

// SubscribeChat implements gc.Client.
func (c *Client) SubscribeChat() (<-chan gc.ChatMessage, func()) { return c.chats.subscribe() }

// sessionLocked is the adapter's check that a GC session is up.
func (c *Client) sessionLocked(op string) error {
	switch {
	case !c.connected:
		return fmt.Errorf("%s: %w", op, gc.ErrNotConnected)
	case !c.welcomed:
		return fmt.Errorf("%s: %w", op, gc.ErrNotReady)
	}
	return nil
}

func (c *Client) admin(league uint32) bool { return slices.Contains(c.bot.Leagues, league) }

// CreateLobby leaves any lobby the bot is in and creates a new one with
// the details. A league the account does not administer comes back as
// league 0 with a NOT_LEAGUE_ADMIN popup, which is a *gc.CreateError, as
// the GC answers it; the lobby is left for the caller to destroy.
func (c *Client) CreateLobby(_ context.Context, d gc.LobbyDetails) (*gc.LobbyView, error) {
	if d.TVDelaySeconds != 0 {
		if _, ok := gc.TVDelayToWire(d.TVDelaySeconds); !ok {
			return nil, fmt.Errorf("create lobby: unsupported dota_tv_delay %ds", d.TVDelaySeconds)
		}
	}
	w := c.w
	w.mu.Lock()
	if err := c.sessionLocked("create lobby"); err != nil {
		w.mu.Unlock()
		return nil, err
	}
	if old := c.lobby; old != nil {
		w.dropLocked(old)
	}
	d.LobbyID = 0
	rejected := d.LeagueID != 0 && !c.admin(d.LeagueID)
	if rejected {
		d.LeagueID, d.LeagueNodeID = 0, 0
	}
	if d.TVDelaySeconds == 0 {
		// A league lobby created without a delay reads back 120 s live;
		// any other gets the CSODOTALobby default, 10 s.
		d.TVDelaySeconds = 10
		if d.LeagueID != 0 {
			d.TVDelaySeconds = 120
		}
	}
	w.nextLobby++
	w.nextChannel++
	l := &lobby{
		w: w, id: w.nextLobby, host: c, created: w.opts.Now(), details: d, state: gc.LobbyUI,
		channel: w.nextChannel,
		// The creator starts in the first Radiant slot, as in the client.
		members: []*member{{account: c.bot.AccountID, team: gc.TeamRadiant, persona: c.bot.PersonaName}},
	}
	w.lobbies[l.id] = l
	l.publish(gc.LobbyCreated)
	v := c.current
	w.log.Info("sim: lobby created", "bot", c.bot.Name, "lobby_id", l.id, "league_id", d.LeagueID, "league_rejected", rejected)
	w.mu.Unlock()
	if rejected {
		c.emitNow(gc.Popup{ID: gc.PopupNotLeagueAdmin, Name: "NOT_LEAGUE_ADMIN"})
		return nil, &gc.CreateError{Reason: gc.CreateLeagueRejected, PopupID: gc.PopupNotLeagueAdmin}
	}
	return v, nil
}

// SetLobbyDetails replaces the lobby's details. A league the account does
// not administer is not applied, as the GC ignores it.
func (c *Client) SetLobbyDetails(d gc.LobbyDetails) error {
	if d.TVDelaySeconds != 0 {
		if _, ok := gc.TVDelayToWire(d.TVDelaySeconds); !ok {
			return fmt.Errorf("set lobby details: unsupported dota_tv_delay %ds", d.TVDelaySeconds)
		}
	}
	return c.lobbyOp("set lobby details", func(l *lobby) bool {
		if d.LobbyID != 0 && d.LobbyID != l.id {
			return false
		}
		if d.LeagueID != l.details.LeagueID && d.LeagueID != 0 && !c.admin(d.LeagueID) {
			d.LeagueID, d.LeagueNodeID = l.details.LeagueID, l.details.LeagueNodeID
		}
		if d.TVDelaySeconds == 0 {
			d.TVDelaySeconds = l.details.TVDelaySeconds
		}
		d.LobbyID = 0
		l.details = d
		return true
	})
}

// lobbyOp runs fn on the bot's lobby under the World's lock and publishes
// a snapshot when fn reports a change. Without a lobby it does nothing,
// as the GC ignores such a message.
func (c *Client) lobbyOp(op string, fn func(l *lobby) bool) error {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if err := c.sessionLocked(op); err != nil {
		return err
	}
	if l := c.lobby; l != nil && fn(l) {
		l.publish(gc.LobbyUpdated)
	}
	return nil
}

// Invite adds the account to the pending invites and confirms it with
// InvitationCreated. In auto mode the player joins later.
func (c *Client) Invite(steamID64 uint64) error {
	account := gc.AccountID(steamID64)
	var sent bool
	err := c.lobbyOp("invite", func(l *lobby) bool {
		sent = true
		if l.member(account) != nil || slices.Contains(l.pending, account) {
			return false
		}
		l.pending = append(l.pending, account)
		c.w.invited(l, account)
		return true
	})
	if err == nil && sent {
		ev := gc.InvitationCreated{SteamID: steamID64}
		c.invites.publish(ev)
		c.emitNow(ev)
	}
	return err
}

// KickFromTeam moves a member to the unassigned pool.
func (c *Client) KickFromTeam(accountID uint32) error {
	return c.lobbyOp("kick from team", func(l *lobby) bool {
		m := l.member(accountID)
		if m == nil || m.team == gc.TeamPlayerPool {
			return false
		}
		m.team, m.slot = gc.TeamPlayerPool, 0
		return true
	})
}

// Kick removes a member from the lobby. The host cannot kick itself.
func (c *Client) Kick(accountID uint32) error {
	return c.lobbyOp("kick", func(l *lobby) bool {
		if accountID == c.bot.AccountID || l.member(accountID) == nil {
			return false
		}
		l.remove(accountID)
		return true
	})
}

// JoinTeam moves the bot. A taken Radiant or Dire slot is ignored, as the
// GC ignores it.
func (c *Client) JoinTeam(team gc.Team, slot uint32) error {
	return c.lobbyOp("join team", func(l *lobby) bool {
		m := l.member(c.bot.AccountID)
		if m == nil {
			return false
		}
		return l.seat(m, team, &slot) == nil
	})
}

// JoinBroadcastChannel moves the bot to a caster slot.
func (c *Client) JoinBroadcastChannel(channel uint32) error {
	return c.lobbyOp("join broadcast channel", func(l *lobby) bool {
		m := l.member(c.bot.AccountID)
		if m == nil {
			return false
		}
		m.team, m.slot = gc.TeamBroadcaster, channel
		return true
	})
}

// FlipTeams swaps Radiant and Dire.
func (c *Client) FlipTeams() error {
	return c.lobbyOp("flip teams", func(l *lobby) bool {
		l.flip()
		return true
	})
}

// BalancedShuffle deals the seated players to the sides by account ID.
func (c *Client) BalancedShuffle() error {
	return c.lobbyOp("balanced shuffle", func(l *lobby) bool {
		l.shuffle()
		return true
	})
}

// Launch moves a lobby in the UI state to server assignment; in auto mode
// the match starts later. A lobby that is past the UI state ignores it.
func (c *Client) Launch() error {
	return c.lobbyOp("launch lobby", func(l *lobby) bool {
		c.w.launched(l)
		return false // launched publishes itself
	})
}

// Leave takes the bot out of its lobby, which then ends: nobody hosts it.
func (c *Client) Leave() {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if l := c.lobby; l != nil {
		c.w.log.Info("sim: host left the lobby", "bot", c.bot.Name, "lobby_id", l.id)
		c.w.dropLocked(l)
	}
}

// Abandon does nothing: a simulated match has no game to abandon.
func (c *Client) Abandon() {}

// Destroy destroys the bot's lobby.
func (c *Client) Destroy(context.Context) error {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if !c.connected {
		return fmt.Errorf("destroy lobby: %w", gc.ErrNotConnected)
	}
	l := c.lobby
	if l == nil {
		return fmt.Errorf("destroy lobby: no lobby")
	}
	c.w.log.Info("sim: lobby destroyed", "bot", c.bot.Name, "lobby_id", l.id)
	c.w.dropLocked(l)
	return nil
}

// JoinLobbyChat joins the chat of the bot's lobby. Another lobby's chat is
// refused.
func (c *Client) JoinLobbyChat(_ context.Context, gcLobbyID uint64) (uint64, error) {
	w := c.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := c.sessionLocked("join lobby chat"); err != nil {
		return 0, err
	}
	l, ok := w.lobbies[gcLobbyID]
	if !ok || l.host != c {
		return 0, &gc.ChatJoinError{Channel: gc.LobbyChannelName(gcLobbyID), Result: "INVALID_CHANNEL_TYPE"}
	}
	if !l.chatJoined {
		l.chatJoined = true
		w.chatJoinedLocked(l)
	}
	return l.channel, nil
}

// SendChat says text in a joined channel as the bot.
func (c *Client) SendChat(channelID uint64, text string) error {
	w := c.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := c.sessionLocked("send chat"); err != nil {
		return err
	}
	l := c.lobby
	if l == nil || l.channel != channelID || !l.chatJoined {
		return fmt.Errorf("send chat: not in channel %d", channelID)
	}
	if m := l.member(c.bot.AccountID); m != nil {
		l.say(m, text)
		return nil
	}
	l.say(&member{account: c.bot.AccountID, persona: c.bot.PersonaName}, text)
	return nil
}

// LeaveChat leaves a joined channel.
func (c *Client) LeaveChat(channelID uint64) error {
	w := c.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := c.sessionLocked("leave chat"); err != nil {
		return err
	}
	for _, l := range w.lobbies {
		if l.host == c && l.channel == channelID {
			l.chatJoined = false
		}
	}
	return nil
}

// ListAdminLeagues lists the configured leagues.
func (c *Client) ListAdminLeagues(context.Context) ([]gc.LeagueInfo, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if err := c.sessionLocked("list admin leagues"); err != nil {
		return nil, err
	}
	out := make([]gc.LeagueInfo, 0, len(c.bot.Leagues))
	for _, id := range c.bot.Leagues {
		out = append(out, gc.LeagueInfo{LeagueID: id, Name: fmt.Sprintf("Simulated league %d", id)})
	}
	return out, nil
}

// IsLeagueAdmin reports whether any league is configured, once the GC
// session is up.
func (c *Client) IsLeagueAdmin() (bool, bool) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	return len(c.bot.Leagues) > 0, c.welcomed
}

// LeagueNodes answers no nodes: the simulator has no league schedules.
func (c *Client) LeagueNodes(context.Context, uint32) ([]gc.LeagueNode, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if err := c.sessionLocked("league nodes"); err != nil {
		return nil, err
	}
	return []gc.LeagueNode{}, nil
}

// MatchDetails answers a match the simulator ended.
func (c *Client) MatchDetails(_ context.Context, matchID uint64) (*gc.MatchResult, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if err := c.sessionLocked("match details"); err != nil {
		return nil, err
	}
	m, ok := c.w.matches[matchID]
	if !ok {
		return nil, fmt.Errorf("match details: no simulated match %d", matchID)
	}
	cp := *m
	cp.Players = slices.Clone(m.Players)
	return &cp, nil
}
