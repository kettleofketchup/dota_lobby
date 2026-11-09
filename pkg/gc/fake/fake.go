// Package fake is a scriptable gc.Client for supervisor and pool tests. It
// records every call, can emit scripted events when a method is called, and
// returns configured results or errors. It talks to no network.
package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Call is one recorded method call.
type Call struct {
	Method string
	Args   []any
}

// Client is a fake gc.Client. The zero value is not usable; call New.
type Client struct {
	events chan gc.Event

	mu      sync.Mutex
	calls   []Call
	changed chan struct{} // closed and replaced on every call
	errs    map[string][]error
	scripts map[string][][]gc.Event
	every   map[string][]gc.Event

	connected    bool
	seq          uint64 // successful Connects, as in Disconnected.Conn
	steamID      uint64
	flags        uint32
	flagsKnown   bool
	token        string
	lobby        *gc.LobbyView
	createLobby  *gc.LobbyView
	leagues      []gc.LeagueInfo
	leagueAdmin  *bool
	leagueNodes  map[uint32][]gc.LeagueNode
	matches      map[uint64]*gc.MatchResult
	lobbySubs    map[chan gc.LobbyEvent]struct{}
	inviteSubs   map[chan gc.InvitationCreated]struct{}
	chatSubs     map[chan gc.ChatMessage]struct{}
	chatChannel  uint64
	reactions    map[string]func(args []any)
	blockOnCtx   map[string]bool
	stickyErrors map[string]error
}

var _ gc.Client = (*Client)(nil)

// New returns a disconnected fake with an empty script.
func New() *Client {
	return &Client{
		events:       make(chan gc.Event, gc.EventBuffer),
		changed:      make(chan struct{}),
		errs:         map[string][]error{},
		scripts:      map[string][][]gc.Event{},
		every:        map[string][]gc.Event{},
		leagueNodes:  map[uint32][]gc.LeagueNode{},
		matches:      map[uint64]*gc.MatchResult{},
		lobbySubs:    map[chan gc.LobbyEvent]struct{}{},
		inviteSubs:   map[chan gc.InvitationCreated]struct{}{},
		chatSubs:     map[chan gc.ChatMessage]struct{}{},
		reactions:    map[string]func(args []any){},
		blockOnCtx:   map[string]bool{},
		stickyErrors: map[string]error{},
	}
}

// --- scripting ---

// Emit pushes events onto the Events channel now. A Disconnected marks the
// fake disconnected first, as the real adapter finishes the connection
// before forwarding it, so the receiver can Connect again at once.
func (f *Client) Emit(events ...gc.Event) {
	for _, ev := range events {
		if _, ok := ev.(gc.Disconnected); ok {
			f.mu.Lock()
			f.connected = false
			f.mu.Unlock()
		}
		f.events <- ev
	}
}

// Script queues one batch of events for the next call to method. Each call
// consumes one batch, in order; an empty batch scripts a call that emits
// nothing (a lost hello, say). After the queue is used up, the batch set by
// ScriptEvery is emitted, if any.
func (f *Client) Script(method string, events ...gc.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[method] = append(f.scripts[method], events)
}

// ScriptEvery sets the batch emitted on every call to method once its
// Script queue is empty.
func (f *Client) ScriptEvery(method string, events ...gc.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.every[method] = events
}

// FailNext makes the next call to method return err. Calls queue in order.
// Only methods that return an error use it.
func (f *Client) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[method] = append(f.errs[method], err)
}

// FailAlways makes every call to method return err; nil clears it. FailNext
// entries take precedence.
func (f *Client) FailAlways(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.stickyErrors, method)
		return
	}
	f.stickyErrors[method] = err
}

// BlockUntilDone makes a ctx-taking method wait for its ctx and return
// ctx.Err(), to test timeouts. false restores the normal behaviour.
func (f *Client) BlockUntilDone(method string, block bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockOnCtx[method] = block
}

// SetSteamID sets what SteamID returns.
func (f *Client) SetSteamID(id uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steamID = id
}

// SetAccountFlags sets what AccountFlags returns.
func (f *Client) SetAccountFlags(flags uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flags, f.flagsKnown = flags, true
}

// SetToken sets the token Authenticate returns.
func (f *Client) SetToken(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = token
}

// SetLeagues sets what ListAdminLeagues returns.
func (f *Client) SetLeagues(l []gc.LeagueInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leagues = slices.Clone(l)
}

// SetLeagueAdmin sets what IsLeagueAdmin returns (known = true).
func (f *Client) SetLeagueAdmin(admin bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leagueAdmin = &admin
}

// SetLeagueNodes sets what LeagueNodes returns for one league.
func (f *Client) SetLeagueNodes(leagueID uint32, nodes []gc.LeagueNode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leagueNodes[leagueID] = slices.Clone(nodes)
}

// SetMatch sets what MatchDetails returns for one match.
func (f *Client) SetMatch(m *gc.MatchResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matches[m.MatchID] = m
}

// SetCreateResult sets the lobby CreateLobby returns. Without it CreateLobby
// builds one from the details and the fake's SteamID.
func (f *Client) SetCreateResult(v *gc.LobbyView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createLobby = v
}

// SetLobby replaces the current lobby (nil for none) and notifies lobby
// subscribers with the matching event kind.
func (f *Client) SetLobby(v *gc.LobbyView) {
	f.mu.Lock()
	old := f.lobby
	f.lobby = v
	f.mu.Unlock()
	switch {
	case v == nil && old != nil:
		f.publish(gc.LobbyEvent{Kind: gc.LobbyDestroyed, Lobby: old})
	case v != nil && old == nil:
		f.publish(gc.LobbyEvent{Kind: gc.LobbyCreated, Lobby: v})
	case v != nil:
		f.publish(gc.LobbyEvent{Kind: gc.LobbyUpdated, Lobby: v})
	}
}

// UpdateLobby applies fn to a copy of the current lobby (Members cloned)
// and makes the copy current, as a GC SO update would. It does nothing
// without a lobby.
func (f *Client) UpdateLobby(fn func(v *gc.LobbyView)) {
	f.mu.Lock()
	cur := f.lobby
	f.mu.Unlock()
	if cur == nil {
		return
	}
	v := *cur
	v.Members = slices.Clone(cur.Members)
	v.Pending = slices.Clone(cur.Pending)
	fn(&v)
	f.SetLobby(&v)
}

// OnCall runs fn after every successful call to method, with the call's
// arguments, outside the fake's lock: fn may call SetLobby or
// UpdateLobby to play the GC's answer. nil removes it.
func (f *Client) OnCall(method string, fn func(args []any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fn == nil {
		delete(f.reactions, method)
		return
	}
	f.reactions[method] = fn
}

// EmitInvite delivers an InvitationCreated to SubscribeInvites
// subscribers. Unlike the adapter it does not also put it on Events().
func (f *Client) EmitInvite(ev gc.InvitationCreated) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.inviteSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SetChatChannel sets the channel ID JoinLobbyChat returns. Without it
// the channel ID is the lobby ID plus 1<<32. Use FailNext or FailAlways
// on "JoinLobbyChat" for a refused join.
func (f *Client) SetChatChannel(id uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatChannel = id
}

// EmitChat delivers a chat message to SubscribeChat subscribers, as the
// GC pushes one.
func (f *Client) EmitChat(m gc.ChatMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.chatSubs {
		select {
		case ch <- m:
		default:
		}
	}
}

// ChatSent returns the texts sent with SendChat, in order.
func (f *Client) ChatSent() []string {
	var out []string
	for _, c := range f.Calls("SendChat") {
		out = append(out, c.Args[1].(string))
	}
	return out
}

// --- inspection ---

// Calls returns every recorded call, optionally only those to the given
// methods.
func (f *Client) Calls(methods ...string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(methods) == 0 {
		return slices.Clone(f.calls)
	}
	var out []Call
	for _, c := range f.calls {
		if slices.Contains(methods, c.Method) {
			out = append(out, c)
		}
	}
	return out
}

// CallCount returns how many times method was called.
func (f *Client) CallCount(method string) int { return len(f.Calls(method)) }

// WaitForCalls blocks until method has been called at least n times.
func (f *Client) WaitForCalls(ctx context.Context, method string, n int) error {
	for {
		f.mu.Lock()
		count := 0
		for _, c := range f.calls {
			if c.Method == method {
				count++
			}
		}
		changed := f.changed
		f.mu.Unlock()
		if count >= n {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("waiting for %d %s calls (have %d): %w", n, method, count, ctx.Err())
		}
	}
}

// Connected reports whether Connect was called without a later Disconnect.
func (f *Client) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

// record logs a call, returns its scripted error, and emits its script
// after the lock is released (emitting may block on a full channel).
func (f *Client) record(method string, args ...any) error {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Method: method, Args: args})
	close(f.changed)
	f.changed = make(chan struct{})

	var err error
	if q := f.errs[method]; len(q) > 0 {
		err, f.errs[method] = q[0], q[1:]
	} else {
		err = f.stickyErrors[method]
	}
	var batch []gc.Event
	if q := f.scripts[method]; len(q) > 0 {
		batch, f.scripts[method] = q[0], q[1:]
	} else {
		batch = f.every[method]
	}
	f.mu.Unlock()

	f.Emit(batch...)
	return err
}

// op records a fire-and-forget lobby operation and, if it succeeds, runs
// its OnCall reaction.
func (f *Client) op(method string, args ...any) error {
	if err := f.record(method, args...); err != nil {
		return err
	}
	f.mu.Lock()
	react := f.reactions[method]
	f.mu.Unlock()
	if react != nil {
		react(args)
	}
	return nil
}

// wait applies BlockUntilDone for ctx-taking methods.
func (f *Client) wait(ctx context.Context, method string) error {
	f.mu.Lock()
	block := f.blockOnCtx[method]
	f.mu.Unlock()
	if !block {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *Client) publish(ev gc.LobbyEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.lobbySubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// --- gc.Client ---

// Connect records the call. Unless a FailNext error is queued, the fake
// counts as connected and emits the Connect script (e.g. Connected, LoggedOn).
// It is marked connected before the script runs, so a scripted Disconnected
// (a failed dial) leaves it disconnected.
func (f *Client) Connect(_ context.Context, creds gc.Credentials) error {
	f.mu.Lock()
	if f.connected {
		f.mu.Unlock()
		return gc.ErrAlreadyConnected
	}
	f.connected = true
	f.seq++
	f.mu.Unlock()
	if err := f.record("Connect", creds); err != nil {
		f.mu.Lock()
		f.connected = false
		f.seq--
		f.mu.Unlock()
		return err
	}
	return nil
}

// ConnSeq is the number of the current (or last) connection, the value its
// Disconnected carries.
func (f *Client) ConnSeq() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq
}

// Authenticate returns the SetToken token unless an error is scripted.
func (f *Client) Authenticate(ctx context.Context, creds gc.Credentials) (string, error) {
	if err := f.record("Authenticate", creds); err != nil {
		return "", err
	}
	if err := f.wait(ctx, "Authenticate"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, nil
}

// Disconnect marks the fake disconnected and, unless the script says
// otherwise, emits Disconnected like the real adapter.
func (f *Client) Disconnect() {
	f.mu.Lock()
	wasConnected := f.connected
	f.connected = false
	seq := f.seq
	scripted := len(f.scripts["Disconnect"]) > 0 || f.every["Disconnect"] != nil
	f.mu.Unlock()
	_ = f.record("Disconnect")
	if wasConnected && !scripted {
		f.Emit(gc.Disconnected{Conn: seq})
	}
}

// Events implements gc.Client.
func (f *Client) Events() <-chan gc.Event { return f.events }

// SetPlaying implements gc.Client.
func (f *Client) SetPlaying(playing bool) { _ = f.record("SetPlaying", playing) }

// SayHello implements gc.Client.
func (f *Client) SayHello() { _ = f.record("SayHello") }

// SteamID implements gc.Client.
func (f *Client) SteamID() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.steamID
}

// AccountFlags implements gc.Client.
func (f *Client) AccountFlags() (uint32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flags, f.flagsKnown
}

// CurrentLobby implements gc.Client.
func (f *Client) CurrentLobby() (*gc.LobbyView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lobby, f.lobby != nil
}

// SubscribeLobby implements gc.Client.
func (f *Client) SubscribeLobby() (<-chan gc.LobbyEvent, func()) {
	ch := make(chan gc.LobbyEvent, 16)
	f.mu.Lock()
	f.lobbySubs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.lobbySubs, ch)
		f.mu.Unlock()
	}
}

// SubscribeInvites implements gc.Client.
func (f *Client) SubscribeInvites() (<-chan gc.InvitationCreated, func()) {
	ch := make(chan gc.InvitationCreated, 16)
	f.mu.Lock()
	f.inviteSubs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.inviteSubs, ch)
		f.mu.Unlock()
	}
}

// CreateLobby makes the created lobby current and returns it.
func (f *Client) CreateLobby(ctx context.Context, d gc.LobbyDetails) (*gc.LobbyView, error) {
	if err := f.record("CreateLobby", d); err != nil {
		return nil, err
	}
	if err := f.wait(ctx, "CreateLobby"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	v := f.createLobby
	if v == nil {
		tv := d.TVDelaySeconds
		if tv == 0 {
			tv = 10 // the CSODOTALobby default
		}
		wire, _ := gc.TVDelayToWire(tv)
		v = &gc.LobbyView{
			LobbyID:        uint64(len(f.calls)) + 1000,
			LeaderID:       f.steamID,
			State:          gc.LobbyUI,
			LeagueID:       d.LeagueID,
			LeagueNodeID:   d.LeagueNodeID,
			ServerRegion:   d.ServerRegion,
			GameMode:       d.GameMode,
			GameName:       d.GameName,
			Visibility:     d.Visibility,
			TVDelaySeconds: tv,
			TVDelayWire:    wire,
			Members:        []gc.Member{{SteamID: f.steamID, Team: gc.TeamRadiant, TeamKnown: true}},
		}
	}
	f.mu.Unlock()
	f.SetLobby(v)
	return v, nil
}

// SetLobbyDetails implements gc.Client. Like the adapter it rejects an
// unsupported TV delay.
func (f *Client) SetLobbyDetails(d gc.LobbyDetails) error {
	if d.TVDelaySeconds != 0 {
		if _, ok := gc.TVDelayToWire(d.TVDelaySeconds); !ok {
			_ = f.record("SetLobbyDetails", d)
			return fmt.Errorf("set lobby details: unsupported dota_tv_delay %ds", d.TVDelaySeconds)
		}
	}
	return f.op("SetLobbyDetails", d)
}

// Invite implements gc.Client.
func (f *Client) Invite(steamID64 uint64) error { return f.op("Invite", steamID64) }

// KickFromTeam implements gc.Client.
func (f *Client) KickFromTeam(accountID uint32) error { return f.op("KickFromTeam", accountID) }

// Kick implements gc.Client.
func (f *Client) Kick(accountID uint32) error { return f.op("Kick", accountID) }

// JoinTeam implements gc.Client.
func (f *Client) JoinTeam(team gc.Team, slot uint32) error { return f.op("JoinTeam", team, slot) }

// JoinBroadcastChannel implements gc.Client.
func (f *Client) JoinBroadcastChannel(channel uint32) error {
	return f.op("JoinBroadcastChannel", channel)
}

// FlipTeams implements gc.Client.
func (f *Client) FlipTeams() error { return f.op("FlipTeams") }

// BalancedShuffle implements gc.Client.
func (f *Client) BalancedShuffle() error { return f.op("BalancedShuffle") }

// Launch implements gc.Client.
func (f *Client) Launch() error { return f.op("Launch") }

// Leave clears the current lobby.
func (f *Client) Leave() {
	_ = f.record("Leave")
	f.SetLobby(nil)
}

// Abandon implements gc.Client.
func (f *Client) Abandon() { _ = f.record("Abandon") }

// Destroy clears the current lobby unless an error is scripted.
func (f *Client) Destroy(ctx context.Context) error {
	if err := f.record("Destroy"); err != nil {
		return err
	}
	if err := f.wait(ctx, "Destroy"); err != nil {
		return err
	}
	f.SetLobby(nil)
	return nil
}

// JoinLobbyChat implements gc.Client.
func (f *Client) JoinLobbyChat(ctx context.Context, gcLobbyID uint64) (uint64, error) {
	if err := f.record("JoinLobbyChat", gcLobbyID); err != nil {
		return 0, err
	}
	if err := f.wait(ctx, "JoinLobbyChat"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatChannel != 0 {
		return f.chatChannel, nil
	}
	return gcLobbyID + 1<<32, nil
}

// SendChat implements gc.Client.
func (f *Client) SendChat(channelID uint64, text string) error {
	return f.op("SendChat", channelID, text)
}

// LeaveChat implements gc.Client.
func (f *Client) LeaveChat(channelID uint64) error { return f.op("LeaveChat", channelID) }

// SubscribeChat implements gc.Client.
func (f *Client) SubscribeChat() (<-chan gc.ChatMessage, func()) {
	ch := make(chan gc.ChatMessage, 32)
	f.mu.Lock()
	f.chatSubs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.chatSubs, ch)
		f.mu.Unlock()
	}
}

// ListAdminLeagues implements gc.Client.
func (f *Client) ListAdminLeagues(ctx context.Context) ([]gc.LeagueInfo, error) {
	if err := f.record("ListAdminLeagues"); err != nil {
		return nil, err
	}
	if err := f.wait(ctx, "ListAdminLeagues"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.leagues), nil
}

// IsLeagueAdmin implements gc.Client.
func (f *Client) IsLeagueAdmin() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leagueAdmin == nil {
		return false, false
	}
	return *f.leagueAdmin, true
}

// LeagueNodes implements gc.Client.
func (f *Client) LeagueNodes(ctx context.Context, leagueID uint32) ([]gc.LeagueNode, error) {
	if err := f.record("LeagueNodes", leagueID); err != nil {
		return nil, err
	}
	if err := f.wait(ctx, "LeagueNodes"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.leagueNodes[leagueID]), nil
}

// MatchDetails implements gc.Client.
func (f *Client) MatchDetails(ctx context.Context, matchID uint64) (*gc.MatchResult, error) {
	if err := f.record("MatchDetails", matchID); err != nil {
		return nil, err
	}
	if err := f.wait(ctx, "MatchDetails"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.matches[matchID]
	if !ok {
		return nil, fmt.Errorf("fake: no match %d", matchID)
	}
	return m, nil
}
