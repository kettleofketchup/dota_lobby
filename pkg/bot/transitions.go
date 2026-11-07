package bot

import (
	"errors"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// State is a bot's availability state (plan §4.1, §5.2). Only StateGCReady
// means the bot can take GC work.
type State string

// States of the supervisor's state machine.
const (
	StateDisconnected   State = "disconnected"
	StateConnecting     State = "connecting"
	StateLoggingOn      State = "logging_on"
	StateAuthenticating State = "authenticating"
	StateSteamOnline    State = "steam_online"
	StateGCHello        State = "gc_hello"
	StateGCQueued       State = "gc_queued"
	StateGCReady        State = "gc_ready"
	StateBackoff        State = "backoff"
	StateCoolingDown    State = "cooling_down"
	StateNeedsAttention State = "needs_attention"
	StateSuspended      State = "suspended"
	StateStopped        State = "stopped"
)

// online reports whether a state has (or is opening) a Steam connection, so
// that a Disconnected event in it decides what happens next.
func (s State) online() bool {
	switch s {
	case StateConnecting, StateLoggingOn, StateSteamOnline, StateGCHello, StateGCQueued, StateGCReady, StateSuspended:
		return true
	}
	return false
}

// Reasons the supervisor adds to the auth package's policy reasons.
const (
	ReasonDisconnected      = "disconnected"
	ReasonLogonTimeout      = "logon_timeout"
	ReasonAlreadyConnected  = "already_connected"
	ReasonNoCredentials     = "no_credentials"
	ReasonBlockedElsewhere  = "blocked_elsewhere"
	ReasonAgreementRequired = "agreement_required"
	ReasonShutdown          = "shutdown"
)

// TimerKind names what the supervisor's single timer is waiting for.
type TimerKind int

// Timer kinds. Only one is armed at a time; arming another replaces it.
const (
	TimerNone TimerKind = iota
	// TimerSlot fires when the shared logon gate lets this bot log on.
	TimerSlot
	// TimerLogon bounds dial plus logon.
	TimerLogon
	// TimerDisconnect bounds the wait for Disconnected after Disconnect.
	TimerDisconnect
	// TimerSettle spaces logon, SetPlaying and the first hello.
	TimerSettle
	// TimerHello repeats the GC hello until welcomed.
	TimerHello
	// TimerStable resets the backoff after a stable gc_ready.
	TimerStable
	// TimerBackoff and TimerCooldown start the next attempt.
	TimerBackoff
	TimerCooldown
)

var timerNames = [...]string{"none", "slot", "logon", "disconnect", "settle", "hello", "stable", "backoff", "cooldown"}

func (k TimerKind) String() string {
	if k >= 0 && int(k) < len(timerNames) {
		return timerNames[k]
	}
	return "unknown"
}

// Params are the machine's timings. They are part of the Snapshot so next
// stays a pure function of its arguments.
type Params struct {
	BackoffMin, BackoffMax time.Duration
	// HelloInterval is the first hello repeat; it doubles up to HelloMax.
	HelloInterval, HelloMax time.Duration
	LogonTimeout            time.Duration
	DisconnectTimeout       time.Duration
	// SettleDelay is the pause after logon before SetPlaying, which gives a
	// PlayingSession{Blocked} the chance to arrive first, and again between
	// SetPlaying and the first hello (plan §4.1: 500 ms).
	SettleDelay time.Duration
	// StableAfter is how long gc_ready must last before the backoff resets.
	StableAfter time.Duration
	// TOTPRetryDelay waits out the current TOTP window after an 88.
	TOTPRetryDelay time.Duration
	// RateLimitPause holds every bot's logons after an 84/87 (plan §9).
	RateLimitPause time.Duration
	Classifier     auth.Classifier
}

// Default timings that config does not cover.
const (
	DefaultHelloMax          = 30 * time.Second
	DefaultLogonTimeout      = 90 * time.Second
	DefaultDisconnectTimeout = 30 * time.Second
	DefaultSettleDelay       = 500 * time.Millisecond
	DefaultStableAfter       = 5 * time.Minute
	DefaultTOTPRetryDelay    = 30 * time.Second
	DefaultRateLimitPause    = 5 * time.Minute
	// minBackoff keeps full jitter from turning into a hot reconnect loop.
	minBackoff = time.Second
)

// ParamsFromConfig builds Params from the steam and gc config sections,
// filling zero values with the documented defaults.
func ParamsFromConfig(steam config.SteamConfig, gcc config.GCConfig) Params {
	p := Params{
		BackoffMin:        orDefault(steam.ReconnectBackoffMin, 5*time.Second),
		BackoffMax:        orDefault(steam.ReconnectBackoffMax, 5*time.Minute),
		HelloInterval:     orDefault(gcc.HelloInterval, 10*time.Second),
		LogonTimeout:      DefaultLogonTimeout,
		DisconnectTimeout: DefaultDisconnectTimeout,
		SettleDelay:       DefaultSettleDelay,
		StableAfter:       DefaultStableAfter,
		TOTPRetryDelay:    DefaultTOTPRetryDelay,
		RateLimitPause:    DefaultRateLimitPause,
		Classifier: auth.Classifier{
			CooldownRateLimited: steam.CooldownRateLimited,
			CooldownElsewhere:   steam.CooldownElsewhere,
		},
	}
	p.HelloMax = max(DefaultHelloMax, p.HelloInterval)
	p.BackoffMax = max(p.BackoffMax, p.BackoffMin)
	return p
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Snapshot is the supervisor's whole state. next takes one and returns the
// following one; nothing else mutates it.
type Snapshot struct {
	State  State
	Reason string
	// LastResult is the last EResult Steam sent, 0 if none.
	LastResult auth.EResult
	LastError  string

	// Attempt counts failed attempts since the last stable gc_ready; it is
	// the backoff exponent.
	Attempt int
	// Retries counts TOTP-mismatch retries (RetryThenAlert).
	Retries int
	// ReauthTried is set once a token has been minted since the last good
	// logon, so a dead-token failure then goes to needs_attention instead
	// of looping.
	ReauthTried bool

	HasToken        bool // a stored refresh token exists
	CanAuth         bool // a password is configured
	HasSharedSecret bool // TOTP is available

	Conn    bool // a connection is open (Connect succeeded, no Disconnected yet)
	Playing bool // SetPlaying(true) was sent on this connection
	// ConnSeq counts successful Connects, matching gc.Disconnected.Conn.
	// Abandoned is a connection given up on after TimerDisconnect, whose
	// late Disconnected must not decide again.
	ConnSeq, Abandoned uint64
	// Draining is set from our Disconnect until the connection is gone.
	// GC input is ignored meanwhile: SetPlaying(false) itself reports
	// NO_SESSION, and reacting to that would send a hello on an account
	// another session may hold, and replace the disconnect timer.
	Draining bool
	// Pending is what the Disconnected that ends this connection will do.
	// LogOnFailed, LoggedOff and PlayingBlocked only set it (plan §4.1:
	// a single reconnect owner).
	Pending    auth.Policy
	HasPending bool

	Timer    TimerKind
	TimerGen uint64
	Hellos   int

	// LobbyStale is set when the GC session was lost: cached lobby objects
	// are unknown until the next welcome (plan §4.1).
	LobbyStale    bool
	SteamID       uint64
	Limited       bool
	QueuePosition int32

	Params Params
	// Rand is the jitter PRNG state. Seed it from a random source; tests set
	// it to get repeatable delays.
	Rand uint64
}

// Ready reports whether the bot can take GC work.
func (s Snapshot) Ready() bool { return s.State == StateGCReady && !s.Draining }

// Input is anything that drives the state machine.
type Input interface{ isInput() }

// Start begins the first logon.
type Start struct{}

// Stop shuts the bot down: SetPlaying(false), Disconnect, then stopped.
type Stop struct{}

// Retry is an operator asking a bot in needs_attention, backoff or
// cooling_down to try again now.
type Retry struct{}

// Event wraps a gc.Event.
type Event struct{ Event gc.Event }

// TimerFired is the supervisor's timer going off. Gen must match the
// snapshot's TimerGen or the input is stale and ignored.
type TimerFired struct {
	Kind TimerKind
	Gen  uint64
}

// ConnectResult is what Client.Connect returned.
type ConnectResult struct{ Err error }

// AuthResult is the outcome of an Authenticate action. A nil Err means a new
// token has been saved to the store.
type AuthResult struct{ Err error }

func (Start) isInput()         {}
func (Stop) isInput()          {}
func (Retry) isInput()         {}
func (Event) isInput()         {}
func (TimerFired) isInput()    {}
func (ConnectResult) isInput() {}
func (AuthResult) isInput()    {}

// Action is a side effect next asks the supervisor to perform.
type Action interface{ isAction() }

// AwaitSlot asks the shared logon gate for a slot. The supervisor arms the
// timer with Gen and fires TimerSlot when the slot comes (at once if free).
type AwaitSlot struct{ Gen uint64 }

// Connect logs on with the stored token.
type Connect struct{}

// Authenticate runs a password + TOTP Authenticate and saves the token; an
// AuthResult follows.
type Authenticate struct{}

// DeleteToken removes the stored token.
type DeleteToken struct{}

// Disconnect ends the connection; a Disconnected event follows.
type Disconnect struct{}

// SetPlaying announces Dota 2 as running or not.
type SetPlaying struct{ Playing bool }

// SayHello sends one GC hello.
type SayHello struct{}

// StartTimer (re)arms the single timer.
type StartTimer struct {
	Kind  TimerKind
	After time.Duration
	Gen   uint64
}

// StopTimer disarms the timer.
type StopTimer struct{}

// PauseLogons holds every bot's logons for For (after a rate limit).
type PauseLogons struct{ For time.Duration }

func (AwaitSlot) isAction()    {}
func (Connect) isAction()      {}
func (Authenticate) isAction() {}
func (DeleteToken) isAction()  {}
func (Disconnect) isAction()   {}
func (SetPlaying) isAction()   {}
func (SayHello) isAction()     {}
func (StartTimer) isAction()   {}
func (StopTimer) isAction()    {}
func (PauseLogons) isAction()  {}

// next is the supervisor policy. It is pure: the same snapshot and input
// always give the same result, so the whole of plan §4.1 is table-testable.
func next(s Snapshot, in Input) (Snapshot, []Action) {
	st := &step{s: s}
	switch in := in.(type) {
	case Start:
		if st.s.State == StateDisconnected {
			st.begin()
		}
	case Stop:
		st.stop()
	case Retry:
		switch st.s.State {
		case StateNeedsAttention, StateBackoff, StateCoolingDown:
			st.s.Attempt, st.s.Retries, st.s.ReauthTried = 0, 0, false
			st.begin()
		}
	case TimerFired:
		if in.Gen == st.s.TimerGen && in.Kind == st.s.Timer {
			st.s.Timer = TimerNone
			st.timer(in.Kind)
		}
	case ConnectResult:
		st.connectResult(in.Err)
	case AuthResult:
		st.authResult(in.Err)
	case Event:
		st.event(in.Event)
	}
	return st.s, st.acts
}

type step struct {
	s    Snapshot
	acts []Action
}

func (st *step) do(a ...Action) { st.acts = append(st.acts, a...) }

func (st *step) enter(state State, reason string) {
	st.s.State, st.s.Reason = state, reason
}

func (st *step) arm(kind TimerKind, d time.Duration) {
	st.s.TimerGen++
	st.s.Timer = kind
	st.do(StartTimer{Kind: kind, After: d, Gen: st.s.TimerGen})
}

func (st *step) disarm() {
	if st.s.Timer == TimerNone {
		return
	}
	st.s.TimerGen++
	st.s.Timer = TimerNone
	st.do(StopTimer{})
}

func (st *step) awaitSlot() {
	st.s.TimerGen++
	st.s.Timer = TimerSlot
	st.do(AwaitSlot{Gen: st.s.TimerGen})
}

// begin starts an attempt: a token logon if there is a token, otherwise a
// credential Authenticate first (plan §4.8 login order).
func (st *step) begin() {
	st.disarm()
	st.s.Pending, st.s.HasPending = auth.Policy{}, false
	st.s.Draining = false
	switch {
	case st.s.HasToken:
		st.enter(StateConnecting, "")
		st.awaitSlot()
	case st.s.CanAuth:
		st.enter(StateAuthenticating, "")
		st.awaitSlot()
	default:
		st.needsAttention(ReasonNoCredentials)
	}
}

func (st *step) connect() {
	st.enter(StateConnecting, "")
	st.do(Connect{})
	st.arm(TimerLogon, st.s.Params.LogonTimeout)
}

func (st *step) authenticate() {
	st.disarm()
	st.enter(StateAuthenticating, "")
	st.do(Authenticate{})
}

func (st *step) needsAttention(reason string) {
	st.disarm()
	st.enter(StateNeedsAttention, reason)
}

// backoff waits a full-jitter delay: uniform in [0, min(max, min*2^n)],
// floored at one second.
func (st *step) backoff(reason string) {
	p := st.s.Params
	ceil := p.BackoffMax
	if n := st.s.Attempt; n < 32 {
		if d := p.BackoffMin << n; d > 0 && d < ceil {
			ceil = d
		}
	}
	var r uint64
	r, st.s.Rand = splitmix(st.s.Rand)
	d := max(time.Duration(r%(uint64(ceil)+1)), minBackoff)
	st.s.Attempt++
	st.enter(StateBackoff, reason)
	st.arm(TimerBackoff, d)
}

func (st *step) coolDown(reason string, d time.Duration) {
	st.enter(StateCoolingDown, reason)
	st.arm(TimerCooldown, d)
	if reason == auth.ReasonRateLimited {
		st.do(PauseLogons{For: st.s.Params.RateLimitPause})
	}
}

// decide applies a policy once the connection (if any) is gone.
func (st *step) decide(p auth.Policy) {
	if p.Result != 0 {
		st.s.LastResult = p.Result
	}
	switch p.Action {
	case auth.ActionDropTokenAndReauthOnce:
		if st.s.ReauthTried || !st.s.CanAuth {
			st.needsAttention(auth.ReasonDeadToken)
			return
		}
		st.s.ReauthTried = true
		st.s.HasToken = false
		st.do(DeleteToken{})
		st.disarm()
		st.enter(StateAuthenticating, "")
		st.awaitSlot()
	case auth.ActionNeedsAttention:
		st.needsAttention(p.Reason)
	case auth.ActionCoolDown:
		st.coolDown(p.Reason, p.Cooldown)
	case auth.ActionRetryThenAlert:
		st.s.Retries++
		if st.s.Retries > p.MaxRetries {
			st.needsAttention(p.Reason)
			return
		}
		st.enter(StateBackoff, p.Reason)
		st.arm(TimerBackoff, st.s.Params.TOTPRetryDelay)
	default:
		reason := p.Reason
		if reason == "" || reason == auth.ReasonOK {
			reason = ReasonDisconnected
		}
		st.backoff(reason)
	}
}

// dropConn ends the connection on our side. The Disconnected that follows
// applies pending; the timer covers a Disconnected that never comes. While
// already draining it only updates pending.
func (st *step) dropConn(pending auth.Policy, override bool) {
	if override || !st.s.HasPending {
		st.s.Pending, st.s.HasPending = pending, true
	}
	if st.s.Draining {
		return
	}
	st.s.Draining = true
	if st.s.Playing {
		st.do(SetPlaying{Playing: false})
		st.s.Playing = false
	}
	st.do(Disconnect{})
	st.arm(TimerDisconnect, st.s.Params.DisconnectTimeout)
}

// connectionGone is the single reconnect decision for an online state.
func (st *step) connectionGone() {
	p := auth.Policy{Action: auth.ActionRetryBackoff, Reason: ReasonDisconnected}
	if st.s.HasPending {
		p = st.s.Pending
	}
	st.s.Pending, st.s.HasPending = auth.Policy{}, false
	st.s.Playing, st.s.Draining = false, false
	st.s.LobbyStale = true
	st.s.QueuePosition = 0
	st.disarm()
	st.decide(p)
}

func (st *step) stop() {
	st.disarm()
	if st.s.Playing {
		st.do(SetPlaying{Playing: false})
		st.s.Playing = false
	}
	if st.s.Conn {
		st.do(Disconnect{})
	}
	st.enter(StateStopped, ReasonShutdown)
}

func (st *step) timer(kind TimerKind) {
	s := &st.s
	switch kind {
	case TimerSlot:
		switch s.State {
		case StateConnecting:
			st.connect()
		case StateAuthenticating:
			st.authenticate()
		}
	case TimerLogon:
		if s.State == StateConnecting || s.State == StateLoggingOn {
			st.dropConn(auth.Policy{Action: auth.ActionRetryBackoff, Reason: ReasonLogonTimeout}, false)
		}
	case TimerDisconnect:
		// Disconnected never came. Decide anyway; Conn stays set, so the
		// next Connect fails with ErrAlreadyConnected and tries again, and
		// the late Disconnected is recognised as this connection's.
		if s.State.online() {
			if s.Conn {
				s.Abandoned = s.ConnSeq
			}
			st.connectionGone()
		}
	case TimerSettle:
		if s.State == StateSteamOnline {
			st.do(SetPlaying{Playing: true})
			s.Playing = true
			s.Hellos = 0
			st.enter(StateGCHello, "")
			st.arm(TimerHello, s.Params.SettleDelay)
		}
	case TimerHello:
		if s.State == StateGCHello || s.State == StateGCQueued {
			st.hello()
		}
	case TimerStable:
		if s.State == StateGCReady {
			s.Attempt = 0
		}
	case TimerBackoff:
		if s.State == StateBackoff {
			st.begin()
		}
	case TimerCooldown:
		if s.State == StateCoolingDown {
			st.begin()
		}
	}
}

// hello sends one hello and schedules the next, doubling from
// HelloInterval up to HelloMax.
func (st *step) hello() {
	p := st.s.Params
	st.do(SayHello{})
	st.s.Hellos++
	d := p.HelloInterval
	for i := 1; i < st.s.Hellos && d < p.HelloMax; i++ {
		d *= 2
	}
	st.arm(TimerHello, min(d, p.HelloMax))
}

func (st *step) restartHello(state State) {
	st.enter(state, "")
	st.s.Hellos = 0
	st.hello()
}

func (st *step) ready() {
	st.enter(StateGCReady, "")
	st.s.LobbyStale = false
	st.s.Hellos = 0
	st.s.QueuePosition = 0
	st.arm(TimerStable, st.s.Params.StableAfter)
}

func (st *step) connectResult(err error) {
	s := &st.s
	if s.State != StateConnecting {
		return
	}
	switch {
	case err == nil:
		s.Conn = true
		s.ConnSeq++
	case errors.Is(err, gc.ErrAlreadyConnected):
		// A previous connection has not delivered its Disconnected yet.
		// Dropping it again means its Disconnected decides after all.
		s.Conn = true
		s.Abandoned = 0
		s.LastError = err.Error()
		st.dropConn(auth.Policy{Action: auth.ActionRetryBackoff, Reason: ReasonAlreadyConnected}, false)
	case errors.Is(err, auth.ErrNoToken), errors.Is(err, gc.ErrNoCredentials):
		s.HasToken = false
		if s.CanAuth {
			st.authenticate()
		} else {
			st.needsAttention(ReasonNoCredentials)
		}
	default:
		s.LastError = err.Error()
		st.disarm()
		st.backoff(auth.ReasonTransient)
	}
}

func (st *step) authResult(err error) {
	if st.s.State != StateAuthenticating {
		return
	}
	if err == nil {
		// A token minted moments ago that Steam then refuses will not be
		// fixed by minting another, so this counts as the one re-auth.
		st.s.HasToken, st.s.ReauthTried = true, true
		st.s.LastError = ""
		st.connect()
		return
	}
	st.s.LastError = err.Error()
	st.decide(classifyAuthError(err, st.s.HasSharedSecret, st.s.Params.Classifier))
}

func (st *step) event(ev gc.Event) {
	s := &st.s
	if s.Draining {
		switch ev.(type) {
		case gc.Connected, gc.LoggedOn, gc.PlayingSession, gc.GCWelcomed, gc.GCStatus:
			return
		}
	}
	switch ev := ev.(type) {
	case gc.Connected:
		if s.State == StateConnecting {
			st.enter(StateLoggingOn, "")
		}
	case gc.LoggedOn:
		if s.State != StateConnecting && s.State != StateLoggingOn {
			return
		}
		s.SteamID = ev.SteamID
		s.Limited = ev.Limited()
		s.LastResult = auth.EResultOK
		s.LastError = ""
		s.Pending, s.HasPending = auth.Policy{}, false
		s.ReauthTried, s.Retries = false, 0
		st.enter(StateSteamOnline, "")
		st.arm(TimerSettle, s.Params.SettleDelay)
	case gc.LogOnFailed:
		// Record only: the Disconnected that follows decides (plan §4.1).
		if ev.Result != 0 {
			s.LastResult = auth.EResult(ev.Result)
		}
		if ev.Err != nil {
			s.LastError = ev.Err.Error()
		}
		if !s.State.online() {
			return
		}
		var p auth.Policy
		if ev.Err != nil && ev.Result == 0 {
			p = classifyAuthError(ev.Err, s.HasSharedSecret, s.Params.Classifier)
		} else {
			p = s.Params.Classifier.Classify(int32(ev.Result), true)
		}
		s.Pending, s.HasPending = p, true
	case gc.LoggedOff:
		s.LastResult = auth.EResult(ev.Result)
		if s.State.online() {
			st.dropConn(s.Params.Classifier.Classify(int32(ev.Result), true), true)
		}
	case gc.SteamFailure:
		s.LastResult = auth.EResult(ev.Result)
		if s.State.online() && !s.HasPending {
			s.Pending, s.HasPending = s.Params.Classifier.Classify(int32(ev.Result), true), true
		}
	case gc.Fatal:
		if ev.Err != nil {
			s.LastError = ev.Err.Error()
		}
	case gc.Disconnected:
		if ev.Conn != 0 {
			if ev.Conn < s.ConnSeq {
				return // an older connection's; the current one is open
			}
			s.ConnSeq = ev.Conn // equal, unless the counts drifted
			if ev.Conn == s.Abandoned {
				// Already decided by TimerDisconnect.
				s.Conn, s.Abandoned = false, 0
				return
			}
		}
		// While connecting, only a connection we opened counts: a late
		// Disconnected from an abandoned one must not decide twice.
		live := s.State.online() && (s.State != StateConnecting || s.Conn)
		s.Conn = false
		if live {
			st.connectionGone()
		}
	case gc.AccountInfo:
		s.Limited = ev.Limited()
	case gc.PlayingSession:
		// Another session holds the playing slot. Never kick it: stop
		// playing, drop off and cool down (plan §4.1).
		if ev.Blocked && s.State.online() {
			st.dropConn(auth.Policy{
				Action:   auth.ActionCoolDown,
				Cooldown: orDefault(s.Params.Classifier.CooldownElsewhere, auth.DefaultCooldownElsewhere),
				Reason:   ReasonBlockedElsewhere,
			}, true)
		}
	case gc.GCWelcomed:
		if s.State == StateGCHello || s.State == StateGCQueued {
			st.ready()
		}
	case gc.GCStatus:
		st.gcStatus(ev)
	}
}

func (st *step) gcStatus(ev gc.GCStatus) {
	s := &st.s
	switch s.State {
	case StateGCHello, StateGCQueued, StateGCReady, StateSuspended:
	default:
		return
	}
	wasReady := s.State == StateGCReady
	switch ev.New {
	case gc.GCHaveSession:
		if !wasReady {
			st.ready()
		}
	case gc.GCNoSessionInLogonQueue:
		s.QueuePosition = ev.QueuePosition
		if s.State != StateGCQueued {
			if wasReady {
				s.LobbyStale = true
			}
			st.restartHello(StateGCQueued)
		}
	case gc.GCSuspended:
		if wasReady {
			s.LobbyStale = true
		}
		st.disarm()
		st.enter(StateSuspended, "")
	default:
		if s.State != StateGCHello {
			if wasReady || s.State == StateSuspended {
				s.LobbyStale = true
			}
			st.restartHello(StateGCHello)
		}
	}
}

// classifyAuthError maps a failed Authenticate (or password logon) to a
// policy. Transport trouble retries; a refused password stops, because
// retrying it is what gets an IP throttled.
func classifyAuthError(err error, hasSharedSecret bool, c auth.Classifier) auth.Policy {
	var ae *gc.AuthError
	switch {
	case errors.Is(err, gc.ErrNoCredentials):
		return auth.Policy{Action: auth.ActionNeedsAttention, Reason: ReasonNoCredentials}
	case errors.As(err, &ae) && ae.NeedsGuard():
		return auth.GuardPrompt(hasSharedSecret)
	case errors.As(err, &ae) && ae.State == "denied":
		// A Web API x-eresult goes through the same table as a CM logon:
		// 88 retries in a new TOTP window, 84/87 cool down, 2/20 back off.
		// A denial without one (an extended error message) is taken as a
		// refused password.
		if ae.Result != 0 {
			return c.Classify(int32(ae.Result), false)
		}
		return c.Classify(int32(auth.EResultInvalidPassword), false)
	case errors.As(err, &ae) && ae.State == "agreement":
		return auth.Policy{Action: auth.ActionNeedsAttention, Reason: ReasonAgreementRequired}
	}
	return auth.Policy{Action: auth.ActionRetryBackoff, Reason: auth.ReasonTransient}
}

// splitmix is SplitMix64: a tiny PRNG whose state lives in the Snapshot.
func splitmix(state uint64) (out, nextState uint64) {
	nextState = state + 0x9e3779b97f4a7c15
	z := nextState
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31), nextState
}
