package gc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	dota2 "github.com/paralin/go-dota2"
	"github.com/paralin/go-dota2/cso"
	devents "github.com/paralin/go-dota2/events"
	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-dota2/socache"
	"github.com/paralin/go-steam"
	sprotocol "github.com/paralin/go-steam/protocol"
	"github.com/paralin/go-steam/protocol/gamecoordinator"
	"github.com/paralin/go-steam/protocol/protobuf/unified"
	"github.com/paralin/go-steam/steamid"
)

// EventBuffer is the size of the channel returned by Events.
const EventBuffer = 256

// Options configures an Adapter.
type Options struct {
	// Logger receives adapter and go-dota2 logs; nil discards them.
	Logger *slog.Logger
	// DialTimeout bounds CM discovery and dialing; 0 means go-steam's 30 s.
	DialTimeout time.Duration
	// LeaveTimeout bounds how long CreateLobby waits for a stale lobby to go
	// away; 0 means 15 s.
	LeaveTimeout time.Duration
	// DestroyTimeout bounds the destroy of a stale lobby we lead; 0 means 10 s.
	DestroyTimeout time.Duration
}

// Adapter is the Client backed by go-steam and go-dota2. Create one per bot
// and keep it across reconnects; each Connect builds a fresh steam.Client
// and dota2.Dota2 pair with every handler registered before dialing, and the
// pair is closed and dropped on disconnect.
type Adapter struct {
	opts   Options
	log    *slog.Logger
	events chan Event

	// Seams for tests; production uses go-steam.
	newTransport func(dialTimeout time.Duration) transport
	authenticate func(ctx context.Context, d *steam.LogOnDetails) (string, error)

	mu  sync.Mutex
	cur *conn
	// seq numbers successful Connects for Disconnected.Conn.
	seq uint64

	steamID atomic.Uint64
	// flags holds EAccountFlags in the low 32 bits and a "known" bit above.
	flags atomic.Uint64

	subMu sync.Mutex
	subs  map[chan LobbyEvent]struct{}
}

var _ Client = (*Adapter)(nil)

// New returns an Adapter that is not yet connected.
func New(opts Options) *Adapter {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.LeaveTimeout <= 0 {
		opts.LeaveTimeout = 15 * time.Second
	}
	if opts.DestroyTimeout <= 0 {
		opts.DestroyTimeout = 10 * time.Second
	}
	return &Adapter{
		opts:         opts,
		log:          opts.Logger,
		events:       make(chan Event, EventBuffer),
		newTransport: newSteamTransport,
		authenticate: func(ctx context.Context, d *steam.LogOnDetails) (string, error) {
			// Authenticate only talks to the Web API; a throwaway client is fine.
			return steam.NewClient().Auth.Authenticate(ctx, d)
		},
		subs: map[chan LobbyEvent]struct{}{},
	}
}

// transport is the part of a steam.Client one connection uses. It embeds
// dota2.Coordinator so go-dota2 can run on it unchanged.
type transport interface {
	dota2.Coordinator
	Events() <-chan any
	Dial(ctx context.Context) error
	LogOn(ctx context.Context, d *steam.LogOnDetails) error
}

type steamTransport struct {
	*dota2.SteamCoordinator
	sc *steam.Client
}

func newSteamTransport(dialTimeout time.Duration) transport {
	sc := steam.NewClient()
	sc.ConnectionTimeout = dialTimeout
	return &steamTransport{SteamCoordinator: dota2.NewSteamCoordinator(sc), sc: sc}
}

func (t *steamTransport) Events() <-chan any { return t.sc.Events() }

func (t *steamTransport) Dial(ctx context.Context) error {
	_, err := t.sc.ConnectContext(ctx)
	return err
}

func (t *steamTransport) LogOn(ctx context.Context, d *steam.LogOnDetails) error {
	return t.sc.Auth.LogOn(ctx, d)
}

// conn is one Steam connection: its transport, Dota2 session and tap.
type conn struct {
	id    uint64
	t     transport
	d     *dota2.Dota2
	tap   *tap
	creds Credentials

	ctx    context.Context
	cancel context.CancelFunc

	// dota carries go-dota2 and tap events to the pump. They do not go
	// through the steam event channel, so closing the Dota2 on disconnect
	// can never block on a channel nobody reads.
	dota     chan any
	dialErr  chan error
	stop     chan struct{}
	stopOnce sync.Once
	logon    sync.WaitGroup

	status atomic.Int32 // GCConnStatus, updated on the reader goroutine

	// wantPlay is the latest SetPlaying request (playNone, playOff, playOn)
	// and playKick wakes playLoop to apply it.
	wantPlay atomic.Int32
	playKick chan struct{}
}

const (
	playNone int32 = iota
	playOff
	playOn
)

// playLoop applies SetPlaying requests off the caller's goroutine.
// go-dota2's SetPlaying(false) emits a status change synchronously, and that
// emit waits for the pump, which may itself be waiting for the caller to
// drain Events(). Only the latest request matters, so requests coalesce.
func (c *conn) playLoop() {
	for {
		select {
		case <-c.playKick:
			if v := c.wantPlay.Swap(playNone); v != playNone {
				c.d.SetPlaying(v == playOn)
			}
		case <-c.stop:
			return
		}
	}
}

// emitDota is go-dota2's emit func. It runs on the Steam reader goroutine.
func (c *conn) emitDota(ev any) {
	if s, ok := ev.(*devents.GCConnectionStatusChanged); ok {
		c.status.Store(int32(s.NewState))
	}
	select {
	case c.dota <- ev:
	case <-c.stop:
	}
}

func (c *conn) emitEvent(ev Event) { c.emitDota(ev) }

func (c *conn) gcReady() bool { return GCConnStatus(c.status.Load()) == GCHaveSession }

func (c *conn) lobbyProto() *protocol.CSODOTALobby {
	ctr, err := c.d.GetCache().GetContainerForTypeID(uint32(cso.Lobby))
	if err != nil {
		return nil
	}
	l, _ := ctr.GetOne().(*protocol.CSODOTALobby)
	return l
}

// Connect implements Client.
func (a *Adapter) Connect(ctx context.Context, creds Credentials) error {
	if creds.Username == "" || (!creds.AccessToken.IsSet() && !creds.Password.IsSet()) {
		return fmt.Errorf("connect: %w: need a username and an access token or password", ErrNoCredentials)
	}
	a.mu.Lock()
	if a.cur != nil {
		a.mu.Unlock()
		return ErrAlreadyConnected
	}
	c := &conn{
		t:        a.newTransport(a.opts.DialTimeout),
		creds:    creds,
		dota:     make(chan any, 64),
		dialErr:  make(chan error, 1),
		stop:     make(chan struct{}),
		playKick: make(chan struct{}, 1),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.status.Store(int32(GCNoSession))
	// The tap goes first so it sees packets before go-dota2 acts on them,
	// and both are registered before the dial (D11, gap-01 §5).
	c.tap = newTap(a.log, c.emitEvent)
	c.t.RegisterPacketHandler(c.tap)
	c.d = dota2.NewWithCoordinator(c.t, c.emitDota, newLogrus(a.log))
	lobbies, unsub, err := c.d.GetCache().SubscribeType(cso.Lobby)
	if err != nil {
		a.mu.Unlock()
		c.cancel()
		c.d.Close()
		return fmt.Errorf("subscribe lobby cache: %w", err)
	}
	a.seq++
	c.id = a.seq
	a.cur = c
	a.mu.Unlock()

	go a.pump(c)
	go c.playLoop()
	go a.forwardLobbies(c, lobbies, unsub)
	go func() {
		if err := c.t.Dial(c.ctx); err != nil {
			c.dialErr <- err
		}
	}()
	return nil
}

// Disconnect implements Client. Cancelling the connection context covers
// both a dial in progress and an open socket.
func (a *Adapter) Disconnect() {
	if c := a.current(); c != nil {
		c.cancel()
	}
}

// Events implements Client.
func (a *Adapter) Events() <-chan Event { return a.events }

func (a *Adapter) current() *conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cur
}

// pump is the one goroutine per connection that drains go-steam's events.
// It only translates and forwards; it never makes a GC request.
func (a *Adapter) pump(c *conn) {
	for {
		select {
		case raw := <-c.t.Events():
			if _, ok := raw.(*steam.DisconnectedEvent); ok {
				a.finish(c)
				a.forward(Disconnected{Conn: c.id})
				a.drain(c)
				return
			}
			if _, ok := raw.(*steam.ConnectedEvent); ok {
				a.startLogOn(c)
			}
			if ev, ok := a.translate(c, raw); ok {
				a.forward(ev)
			}
		case raw := <-c.dota:
			if ev, ok := a.translate(c, raw); ok {
				a.forward(ev)
			}
		case err := <-c.dialErr:
			a.forward(Fatal{Err: fmt.Errorf("dial steam: %w", err)})
			a.finish(c)
			a.forward(Disconnected{Conn: c.id})
			a.drain(c)
			return
		}
	}
}

// startLogOn sends the logon once the CM handshake is done. It runs off the
// pump because a password logon does HTTP and LogOn emits events.
func (a *Adapter) startLogOn(c *conn) {
	details := logOnDetails(c.creds, c.creds.AccessToken.IsSet())
	c.logon.Go(func() {
		if err := c.t.LogOn(c.ctx, details); err != nil {
			a.log.Warn("steam logon not sent", "user", c.creds.Username, "err", err)
		}
	})
}

// finish tears a connection down once: no more dota events, the Dota2 is
// closed for good, and the adapter can Connect again. It runs before the
// Disconnected event is forwarded so a supervisor reacting to that event
// can reconnect immediately.
func (a *Adapter) finish(c *conn) {
	c.stopOnce.Do(func() {
		close(c.stop)
		c.cancel()
		c.d.Close()
		a.mu.Lock()
		if a.cur == c {
			a.cur = nil
		}
		a.mu.Unlock()
	})
}

// drain discards late events from a finished connection until its logon
// goroutine is gone, so nothing stays blocked on go-steam's Emit.
func (a *Adapter) drain(c *conn) {
	quiet := make(chan struct{})
	go func() {
		c.logon.Wait()
		close(quiet)
	}()
	for {
		select {
		case <-c.t.Events():
		case <-quiet:
			return
		}
	}
}

// forward delivers an event to the supervisor. A full channel blocks the
// pump (and so go-steam's reader) on purpose, so it is logged to make the
// stall visible.
func (a *Adapter) forward(ev Event) {
	select {
	case a.events <- ev:
		return
	default:
	}
	a.log.Warn("gc event channel full; steam reader is blocked until events are drained", "event", fmt.Sprintf("%T", ev))
	a.events <- ev
}

// translate maps a go-steam or go-dota2 event to a gc.Event. Events the
// service has no use for are dropped.
func (a *Adapter) translate(c *conn, raw any) (Event, bool) {
	switch e := raw.(type) {
	case Event:
		return e, true
	case *steam.ConnectedEvent:
		return Connected{}, true
	case *steam.LoggedOnEvent:
		id := c.t.SteamID().ToUint64()
		a.steamID.Store(id)
		a.setFlags(uint32(e.AccountFlags))
		return LoggedOn{SteamID: id, AccountFlags: uint32(e.AccountFlags), ExtendedResult: EResult(e.ExtendedResult)}, true
	case *steam.LogOnFailedEvent:
		ev := LogOnFailed{Result: EResult(e.Result)}
		if e.Err != nil {
			ev.Err = authError(e.Err)
		}
		return ev, true
	case *steam.LoggedOffEvent:
		return LoggedOff{Result: EResult(e.Result)}, true
	case *steam.SteamFailureEvent:
		return SteamFailure{Result: EResult(e.Result)}, true
	case *steam.AccountInfoEvent:
		a.setFlags(uint32(e.AccountFlags))
		return AccountInfo{PersonaName: e.PersonaName, AccountFlags: uint32(e.AccountFlags)}, true
	case *steam.PlayingSessionStateEvent:
		return PlayingSession{Blocked: e.PlayingBlocked, AppID: e.PlayingApp}, true
	case error:
		// FatalErrorEvent is `type FatalErrorEvent error`, so once it is in
		// an `any` it is indistinguishable from client.Errorf's errors.
		return Fatal{Err: e}, true
	case *devents.GCConnectionStatusChanged:
		ev := GCStatus{Old: GCConnStatus(e.OldState), New: GCConnStatus(e.NewState)}
		if e.Update != nil {
			ev.QueuePosition = e.Update.GetQueuePosition()
			ev.QueueSize = e.Update.GetQueueSize()
		}
		return ev, true
	case *devents.ClientWelcomed:
		return GCWelcomed{}, true
	case *devents.Popup:
		return popupEvent(&e.CMsgDOTAPopup), true
	case *devents.InvitationCreated:
		return InvitationCreated{SteamID: e.GetSteamId(), Offline: e.GetUserOffline()}, true
	case *devents.MatchSignedOut:
		return MatchSignedOut{MatchID: e.GetMatchId()}, true
	}
	return nil, false
}

func (a *Adapter) setFlags(f uint32) { a.flags.Store(1<<32 | uint64(f)) }

// SetPlaying implements Client. It returns at once; playLoop sends it.
func (a *Adapter) SetPlaying(playing bool) {
	c := a.current()
	if c == nil {
		return
	}
	v := playOff
	if playing {
		v = playOn
	}
	c.wantPlay.Store(v)
	select {
	case c.playKick <- struct{}{}:
	default: // a wake-up is already pending and will read the latest value
	}
}

// SayHello implements Client.
func (a *Adapter) SayHello() {
	if c := a.current(); c != nil {
		c.d.SayHello()
	}
}

// SteamID implements Client.
func (a *Adapter) SteamID() uint64 { return a.steamID.Load() }

// AccountFlags implements Client.
func (a *Adapter) AccountFlags() (uint32, bool) {
	f := a.flags.Load()
	return uint32(f), f>>32 != 0
}

// Authenticate implements Client.
func (a *Adapter) Authenticate(ctx context.Context, creds Credentials) (string, error) {
	if creds.Username == "" || !creds.Password.IsSet() {
		return "", fmt.Errorf("authenticate: %w: need a username and password", ErrNoCredentials)
	}
	token, err := a.authenticate(ctx, logOnDetails(creds, false))
	if err != nil {
		return "", authError(err)
	}
	return token, nil
}

// logOnDetails maps Credentials to go-steam. With useToken the logon skips
// the auth session entirely; otherwise the password flow runs, with the TOTP
// code submitted up front and SteamGuard as the in-session fallback.
func logOnDetails(c Credentials, useToken bool) *steam.LogOnDetails {
	d := &steam.LogOnDetails{
		Username:               c.Username,
		ShouldRememberPassword: true,
		DeviceFriendlyName:     c.DeviceName,
	}
	if useToken {
		d.AccessToken = c.AccessToken.Reveal()
		return d
	}
	d.Password = c.Password.Reveal()
	d.TwoFactorCode = c.TwoFactorCode
	d.AuthCode = c.AuthCode
	prompt := c.SteamGuard
	d.ConfirmSteamGuard = func(ctx context.Context, confs []steam.SteamGuardConfirmation) (string, unified.EAuthSessionGuardType, error) {
		kinds := guardKinds(confs)
		codeType, ok := codeGuardType(kinds)
		// Without a prompt, fail now instead of letting go-steam poll for a
		// phone approval nobody will give.
		if prompt == nil || !ok {
			return "", 0, ErrSteamGuardRequired
		}
		code, err := prompt(ctx, kinds)
		if err != nil {
			return "", 0, err
		}
		return code, codeType, nil
	}
	return d
}

// codeGuardType picks the code type to submit: the authenticator if Steam
// offers it, then email.
func codeGuardType(kinds []GuardKind) (unified.EAuthSessionGuardType, bool) {
	for _, want := range []GuardKind{GuardDeviceCode, GuardEmailCode} {
		for _, k := range kinds {
			if k == want {
				return guardType(want), true
			}
		}
	}
	return 0, false
}

var guardKindNames = map[unified.EAuthSessionGuardType]GuardKind{
	unified.EAuthSessionGuardType_k_EAuthSessionGuardType_None:               GuardNone,
	unified.EAuthSessionGuardType_k_EAuthSessionGuardType_EmailCode:          GuardEmailCode,
	unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceCode:         GuardDeviceCode,
	unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceConfirmation: GuardDeviceConfirmation,
	unified.EAuthSessionGuardType_k_EAuthSessionGuardType_EmailConfirmation:  GuardEmailConfirmation,
	unified.EAuthSessionGuardType_k_EAuthSessionGuardType_MachineToken:       GuardMachineToken,
}

func guardType(k GuardKind) unified.EAuthSessionGuardType {
	for t, name := range guardKindNames {
		if name == k {
			return t
		}
	}
	return unified.EAuthSessionGuardType_k_EAuthSessionGuardType_Unknown
}

func guardKinds(confs []steam.SteamGuardConfirmation) []GuardKind {
	out := make([]GuardKind, 0, len(confs))
	for _, c := range confs {
		k, ok := guardKindNames[c.Type]
		if !ok {
			k = GuardUnknown
		}
		out = append(out, k)
	}
	return out
}

// authError turns go-steam's AuthSessionError into ours; other errors are
// wrapped as they are.
func authError(err error) error {
	var ae *steam.AuthSessionError
	if !errors.As(err, &ae) {
		return fmt.Errorf("steam auth: %w", err)
	}
	out := &AuthError{State: string(ae.State), Guards: guardKinds(ae.Confirmations), Err: ae.Err}
	if out.Err == nil {
		out.Err = errors.New(ae.Error())
	}
	out.Result = deniedResult(out.Err.Error())
	return out
}

// eresultInMessage matches go-steam's Web API failure text,
// "<Method> failed: eresult <N> (<message>)" (auth_session.go). go-steam
// keeps the number only in the message.
var eresultInMessage = regexp.MustCompile(`\beresult (\d+)\b`)

// deniedResult extracts the x-eresult from a go-steam auth error message,
// or 0 when there is none.
func deniedResult(msg string) EResult {
	m := eresultInMessage.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil {
		return 0
	}
	return EResult(n)
}

// reqErr maps go-dota2's ErrNotReady to ours.
func reqErr(op string, err error) error {
	if errors.Is(err, dota2.ErrNotReady) {
		return fmt.Errorf("%s: %w", op, ErrNotReady)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// --- lobby ---

// CurrentLobby implements Client.
func (a *Adapter) CurrentLobby() (*LobbyView, bool) {
	c := a.current()
	if c == nil {
		return nil, false
	}
	l := c.lobbyProto()
	return lobbyView(l), l != nil
}

// SubscribeLobby implements Client.
func (a *Adapter) SubscribeLobby() (<-chan LobbyEvent, func()) {
	ch := make(chan LobbyEvent, 16)
	a.subMu.Lock()
	a.subs[ch] = struct{}{}
	a.subMu.Unlock()
	return ch, func() {
		a.subMu.Lock()
		delete(a.subs, ch)
		a.subMu.Unlock()
	}
}

// forwardLobbies relays one connection's lobby SO events to the adapter's
// subscribers, which outlive the connection.
func (a *Adapter) forwardLobbies(c *conn, events <-chan *socache.CacheEvent, unsub socache.CacheUnsubscribeFunc) {
	defer unsub()
	for {
		select {
		case <-c.stop:
			return
		case ev := <-events:
			l, ok := ev.Object.(*protocol.CSODOTALobby)
			if !ok {
				continue
			}
			kind := LobbyUpdated
			switch ev.EventType {
			case socache.EventTypeCreate:
				kind = LobbyCreated
			case socache.EventTypeDestroy:
				kind = LobbyDestroyed
			}
			a.publishLobby(LobbyEvent{Kind: kind, Lobby: lobbyView(l)})
		}
	}
}

// publishLobby never blocks: a full subscriber loses its oldest event, the
// same policy as go-dota2's SO cache. Trackers work from snapshots anyway.
func (a *Adapter) publishLobby(ev LobbyEvent) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for ch := range a.subs {
		for range 2 {
			select {
			case ch <- ev:
			default:
				select {
				case <-ch:
				default:
				}
				continue
			}
			break
		}
	}
}

// CreateLobby implements Client.
func (a *Adapter) CreateLobby(ctx context.Context, d LobbyDetails) (*LobbyView, error) {
	c := a.current()
	if c == nil {
		return nil, ErrNotConnected
	}
	if !c.gcReady() {
		return nil, fmt.Errorf("create lobby: %w", ErrNotReady)
	}
	d.LobbyID = 0
	details, err := detailsProto(d)
	if err != nil {
		return nil, fmt.Errorf("create lobby: %w", err)
	}
	if err := a.leaveStale(ctx, c); err != nil {
		return nil, fmt.Errorf("%w: %w", &CreateError{Reason: CreateStaleLobby}, err)
	}

	// Subscribe to everything before sending, so no reply can slip past.
	lobbies, unsub, err := c.d.GetCache().SubscribeType(cso.Lobby)
	if err != nil {
		return nil, fmt.Errorf("create lobby: subscribe: %w", err)
	}
	defer unsub()
	replies, stopReplies := c.tap.watch(msgPracticeLobbyResponse)
	defer stopReplies()
	popups, stopPopups := c.tap.watch(msgPopup)
	defer stopPopups()

	// go-dota2's CreateLobby sends no source job, and whether the GC then
	// replies at all is unknown, so we send it ourselves (D5, gap-05 §5.2).
	job := c.tap.nextJob()
	msg := gamecoordinator.NewGCMsgProtobuf(dota2.AppID, msgPracticeLobbyCreate,
		&protocol.CMsgPracticeLobbyCreate{PassKey: details.PassKey, LobbyDetails: details})
	msg.SetSourceJobId(sprotocol.JobId(job))
	c.t.Write(msg)
	a.log.Debug("create lobby sent", "source_job", job, "league_id", d.LeagueID)

	for {
		if l := c.lobbyProto(); l != nil {
			return lobbyView(l), nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("create lobby: %w", ctx.Err())
		case <-c.stop:
			return nil, fmt.Errorf("create lobby: %w", ErrNotConnected)
		case <-lobbies:
		case p := <-replies:
			if uint64(p.TargetJobId) != job {
				// 7055 is shared by several lobby ops; only ours counts.
				a.log.Debug("ignoring 7055 for another job", "target_job", uint64(p.TargetJobId), "source_job", job)
				continue
			}
			eresult, debug, err := decodeGenericResult(p.Body)
			if err != nil {
				a.log.Warn("create lobby reply", "err", err)
				continue
			}
			if eresult != uint32(EResultOK) {
				return nil, &CreateError{Reason: CreateFailed, EResult: eresult, DebugMessage: debug}
			}
		case p := <-popups:
			pop, err := decodePopup(p.Body)
			if err != nil {
				a.log.Warn("create lobby popup", "err", err)
				continue
			}
			a.log.Info("popup during create", "id", pop.ID, "name", pop.Name)
			if pop.ID == PopupNotLeagueAdmin && d.LeagueID != 0 {
				return nil, &CreateError{Reason: CreateLeagueRejected, PopupID: pop.ID}
			}
		}
	}
}

// leaveStale destroys (if we lead it) and leaves any lobby in the cache and
// waits until the cache no longer has it.
func (a *Adapter) leaveStale(ctx context.Context, c *conn) error {
	l := c.lobbyProto()
	if l == nil {
		return nil
	}
	events, unsub, err := c.d.GetCache().SubscribeType(cso.Lobby)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer unsub()

	a.log.Info("leaving stale lobby", "lobby_id", l.GetLobbyId(), "state", LobbyState(l.GetState()).String())
	if l.GetLeaderId() == c.t.SteamID().ToUint64() {
		dctx, cancel := context.WithTimeout(ctx, a.opts.DestroyTimeout)
		resp, err := c.d.DestroyLobby(dctx)
		cancel()
		if err != nil {
			a.log.Warn("destroy stale lobby", "lobby_id", l.GetLobbyId(), "err", err)
		} else {
			a.log.Debug("destroy stale lobby", "lobby_id", l.GetLobbyId(), "result", resp.GetResult().String())
		}
	}
	if l.GetState() != protocol.CSODOTALobby_UI {
		c.d.AbandonLobby()
	}
	c.d.LeaveLobby()

	deadline := time.NewTimer(a.opts.LeaveTimeout)
	defer deadline.Stop()
	for c.lobbyProto() != nil {
		select {
		case <-events:
		case <-deadline.C:
			return fmt.Errorf("lobby %d still present after %s", l.GetLobbyId(), a.opts.LeaveTimeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-c.stop:
			return ErrNotConnected
		}
	}
	return nil
}

// SetLobbyDetails implements Client. Validate TVDelaySeconds with
// TVDelayToWire first: an unsupported value is logged and nothing is sent.
func (a *Adapter) SetLobbyDetails(d LobbyDetails) {
	c := a.current()
	if c == nil {
		return
	}
	if d.LobbyID == 0 {
		l := c.lobbyProto()
		if l == nil {
			a.log.Warn("set lobby details: not in a lobby")
			return
		}
		d.LobbyID = l.GetLobbyId()
	}
	p, err := detailsProto(d)
	if err != nil {
		a.log.Warn("set lobby details", "err", err)
		return
	}
	c.d.SetLobbyDetails(p)
}

// Invite implements Client.
func (a *Adapter) Invite(steamID64 uint64) {
	if c := a.current(); c != nil {
		c.d.InviteLobbyMember(steamid.SteamId(steamID64))
	}
}

// KickFromTeam implements Client.
func (a *Adapter) KickFromTeam(accountID uint32) {
	if c := a.current(); c != nil {
		c.d.KickLobbyMemberFromTeam(accountID)
	}
}

// Kick implements Client.
func (a *Adapter) Kick(accountID uint32) {
	if c := a.current(); c != nil {
		c.d.KickLobbyMember(accountID)
	}
}

// JoinTeam implements Client.
func (a *Adapter) JoinTeam(team Team, slot uint32) {
	if c := a.current(); c != nil {
		c.d.JoinLobbyTeam(protocol.DOTA_GC_TEAM(team), slot)
	}
}

// FlipTeams implements Client.
func (a *Adapter) FlipTeams() {
	if c := a.current(); c != nil {
		c.d.FlipLobbyTeams()
	}
}

// BalancedShuffle implements Client.
func (a *Adapter) BalancedShuffle() {
	if c := a.current(); c != nil {
		c.d.SendBalancedShuffleLobby()
	}
}

// Launch implements Client.
func (a *Adapter) Launch() {
	if c := a.current(); c != nil {
		c.d.LaunchLobby()
	}
}

// Leave implements Client.
func (a *Adapter) Leave() {
	if c := a.current(); c != nil {
		c.d.LeaveLobby()
	}
}

// Abandon implements Client.
func (a *Adapter) Abandon() {
	if c := a.current(); c != nil {
		c.d.AbandonLobby()
	}
}

// Destroy implements Client.
func (a *Adapter) Destroy(ctx context.Context) error {
	c := a.current()
	if c == nil {
		return ErrNotConnected
	}
	resp, err := c.d.DestroyLobby(ctx)
	if err != nil {
		return reqErr("destroy lobby", err)
	}
	if resp.GetResult() != protocol.CMsgDOTADestroyLobbyResponse_SUCCESS {
		return fmt.Errorf("destroy lobby: %s", resp.GetResult())
	}
	return nil
}

// --- league / match ---

// ListAdminLeagues implements Client.
func (a *Adapter) ListAdminLeagues(ctx context.Context) ([]LeagueInfo, error) {
	c := a.current()
	if c == nil {
		return nil, ErrNotConnected
	}
	replies, stop := c.tap.watch(msgLeagueAdminsResponse)
	defer stop()

	resp := &protocol.CMsgDOTALeagueInfoList{}
	err := c.d.MakeRequest(ctx, msgLeagueAdminsRequest, &protocol.CMsgDOTALeagueInfoListAdminsRequest{}, msgLeagueAdminsResponse, resp)
	if err == nil {
		return leagueInfos(resp), nil
	}
	// MakeRequest only completes if the GC echoes our job ID. If it did not,
	// the reply still reached the tap (Q18).
	select {
	case p := <-replies:
		infos, derr := decodeLeagueList(p.Body)
		if derr != nil {
			return nil, fmt.Errorf("list admin leagues: %w", derr)
		}
		a.log.Info("7634 matched by type, not job id (Q18)", "target_job", uint64(p.TargetJobId), "count", len(infos))
		return infos, nil
	default:
	}
	return nil, reqErr("list admin leagues", err)
}

// IsLeagueAdmin implements Client.
func (a *Adapter) IsLeagueAdmin() (bool, bool) {
	c := a.current()
	if c == nil {
		return false, false
	}
	ctr, err := c.d.GetCache().GetContainerForTypeID(uint32(cso.GameAccountClient))
	if err != nil {
		return false, false
	}
	o, ok := ctr.GetOne().(*protocol.CSODOTAGameAccountClient)
	if !ok {
		return false, false
	}
	return o.GetIsLeagueAdmin(), true
}

// LeagueNodes implements Client.
func (a *Adapter) LeagueNodes(ctx context.Context, leagueID uint32) ([]LeagueNode, error) {
	c := a.current()
	if c == nil {
		return nil, ErrNotConnected
	}
	resp, err := c.d.RequestLeagueAvailableLobbyNodes(ctx, leagueID)
	if err != nil {
		return nil, reqErr("league nodes", err)
	}
	return leagueNodes(resp), nil
}

// MatchDetails implements Client.
func (a *Adapter) MatchDetails(ctx context.Context, matchID uint64) (*MatchResult, error) {
	c := a.current()
	if c == nil {
		return nil, ErrNotConnected
	}
	resp, err := c.d.RequestMatchDetails(ctx, matchID)
	if err != nil {
		return nil, reqErr("match details", err)
	}
	return matchResult(resp), nil
}
