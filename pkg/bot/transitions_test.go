package bot

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

func testParams() Params {
	return ParamsFromConfig(config.SteamConfig{}, config.GCConfig{})
}

// snap builds a snapshot in state with the flags a bot in that state would
// have: a token, password and shared secret, an open connection when
// online, and Playing once past steam_online.
func snap(state State, mods ...func(*Snapshot)) Snapshot {
	s := Snapshot{
		State: state, Params: testParams(), Rand: 42, TimerGen: 10,
		HasToken: true, CanAuth: true, HasSharedSecret: true,
	}
	if state.online() {
		s.Conn = true
	}
	switch state {
	case StateGCHello, StateGCQueued, StateGCReady, StateSuspended:
		s.Playing = true
	}
	s.Timer = map[State]TimerKind{
		StateConnecting: TimerLogon, StateLoggingOn: TimerLogon, StateSteamOnline: TimerSettle,
		StateGCHello: TimerHello, StateGCQueued: TimerHello, StateGCReady: TimerStable,
		StateBackoff: TimerBackoff, StateCoolingDown: TimerCooldown,
	}[state]
	for _, m := range mods {
		m(&s)
	}
	return s
}

func pending(p auth.Policy) func(*Snapshot) {
	return func(s *Snapshot) { s.Pending, s.HasPending = p, true }
}

func classify(r auth.EResult) auth.Policy { return auth.Classify(int32(r), true) }

// deniedErr is the AuthError the adapter builds from a go-steam Web API
// denial (auth_session.go authServiceCall).
func deniedErr(method string, r gc.EResult) error {
	return &gc.AuthError{State: "denied", Result: r, Err: fmt.Errorf("%s failed: eresult %d (200 OK)", method, int32(r))}
}

// describe renders actions compactly. Backoff durations are jittered, so
// they are checked separately.
func describe(acts []Action) []string {
	out := []string{}
	for _, a := range acts {
		switch a := a.(type) {
		case AwaitSlot:
			out = append(out, "await_slot")
		case Connect:
			out = append(out, "connect")
		case Authenticate:
			out = append(out, "authenticate")
		case DeleteToken:
			out = append(out, "delete_token")
		case Disconnect:
			out = append(out, "disconnect")
		case SetPlaying:
			out = append(out, fmt.Sprintf("set_playing(%t)", a.Playing))
		case SayHello:
			out = append(out, "say_hello")
		case StartTimer:
			if a.Kind == TimerBackoff {
				out = append(out, "timer(backoff)")
			} else {
				out = append(out, fmt.Sprintf("timer(%s,%s)", a.Kind, a.After))
			}
		case StopTimer:
			out = append(out, "stop_timer")
		case PauseLogons:
			out = append(out, fmt.Sprintf("pause(%s)", a.For))
		default:
			out = append(out, fmt.Sprintf("%T", a))
		}
	}
	return out
}

func TestNext(t *testing.T) {
	errDial := errors.New("dial tcp: connection refused")
	guardErr := &gc.AuthError{State: "device_code", Err: gc.ErrSteamGuardRequired}
	tests := []struct {
		name       string
		s          Snapshot
		in         Input
		in2        func(Snapshot) Input // optional second input, built from the first result
		wantState  State
		wantReason string
		wantActs   []string
		check      func(t *testing.T, s Snapshot)
	}{
		// --- start ---
		{name: "start with token connects", s: snap(StateDisconnected), in: Start{},
			wantState: StateConnecting, wantActs: []string{"await_slot"}},
		{name: "start without token authenticates", s: snap(StateDisconnected, func(s *Snapshot) { s.HasToken = false }), in: Start{},
			wantState: StateAuthenticating, wantActs: []string{"await_slot"}},
		{name: "start without credentials", s: snap(StateDisconnected, func(s *Snapshot) { s.HasToken, s.CanAuth = false, false }), in: Start{},
			wantState: StateNeedsAttention, wantReason: ReasonNoCredentials, wantActs: []string{}},
		{name: "start ignored when running", s: snap(StateGCReady), in: Start{},
			wantState: StateGCReady, wantActs: []string{}},

		// --- logon slot ---
		{name: "slot connects", s: snap(StateConnecting, func(s *Snapshot) { s.Conn, s.Timer = false, TimerSlot }),
			in:        TimerFired{Kind: TimerSlot, Gen: 10},
			wantState: StateConnecting, wantActs: []string{"connect", "timer(logon,1m30s)"}},
		{name: "slot authenticates", s: snap(StateAuthenticating, func(s *Snapshot) { s.Timer = TimerSlot }),
			in:        TimerFired{Kind: TimerSlot, Gen: 10},
			wantState: StateAuthenticating, wantActs: []string{"authenticate"}},
		{name: "stale timer ignored", s: snap(StateConnecting, func(s *Snapshot) { s.Conn, s.Timer = false, TimerSlot }),
			in:        TimerFired{Kind: TimerSlot, Gen: 9},
			wantState: StateConnecting, wantActs: []string{}},

		// --- connect result ---
		{name: "connect ok", s: snap(StateConnecting, func(s *Snapshot) { s.Conn = false }), in: ConnectResult{},
			wantState: StateConnecting, wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				if !s.Conn {
					t.Error("Conn not set")
				}
			}},
		{name: "connect already connected drops old", s: snap(StateConnecting, func(s *Snapshot) { s.Conn = false }),
			in:        ConnectResult{Err: gc.ErrAlreadyConnected},
			wantState: StateConnecting, wantActs: []string{"disconnect", "timer(disconnect,30s)"},
			check: func(t *testing.T, s Snapshot) {
				if !s.HasPending || s.Pending.Reason != ReasonAlreadyConnected {
					t.Errorf("pending = %+v", s.Pending)
				}
			}},
		{name: "connect without stored token authenticates", s: snap(StateConnecting, func(s *Snapshot) { s.Conn = false }),
			in:        ConnectResult{Err: auth.ErrNoToken},
			wantState: StateAuthenticating, wantActs: []string{"stop_timer", "authenticate"}},
		{name: "connect without token or password", s: snap(StateConnecting, func(s *Snapshot) { s.Conn, s.CanAuth = false, false }),
			in:        ConnectResult{Err: auth.ErrNoToken},
			wantState: StateNeedsAttention, wantReason: ReasonNoCredentials, wantActs: []string{"stop_timer"}},
		{name: "connect error backs off", s: snap(StateConnecting, func(s *Snapshot) { s.Conn = false }),
			in:        ConnectResult{Err: errDial},
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"stop_timer", "timer(backoff)"}},

		// --- logon ---
		{name: "connected event", s: snap(StateConnecting), in: Event{gc.Connected{}},
			wantState: StateLoggingOn, wantActs: []string{}},
		{name: "logged on waits to settle", s: snap(StateLoggingOn, func(s *Snapshot) { s.ReauthTried, s.Retries = true, 2 }),
			in:        Event{gc.LoggedOn{SteamID: 7, AccountFlags: gc.AccountFlagLimitedUser}},
			wantState: StateSteamOnline, wantActs: []string{"timer(settle,500ms)"},
			check: func(t *testing.T, s Snapshot) {
				if s.SteamID != 7 || !s.Limited || s.ReauthTried || s.Retries != 0 || s.LastResult != auth.EResultOK {
					t.Errorf("snapshot = %+v", s)
				}
			}},
		{name: "logged on ignored when ready", s: snap(StateGCReady), in: Event{gc.LoggedOn{}},
			wantState: StateGCReady, wantActs: []string{}},
		{name: "fatal only records", s: snap(StateConnecting), in: Event{gc.Fatal{Err: errDial}},
			wantState: StateConnecting, wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				if s.LastError != errDial.Error() {
					t.Errorf("LastError = %q", s.LastError)
				}
			}},
		{name: "logon timeout disconnects", s: snap(StateLoggingOn), in: TimerFired{Kind: TimerLogon, Gen: 10},
			wantState: StateLoggingOn, wantActs: []string{"disconnect", "timer(disconnect,30s)"}},
		{name: "missing disconnected decides anyway", s: snap(StateLoggingOn, func(s *Snapshot) { s.Timer = TimerDisconnect }, pending(auth.Policy{Reason: ReasonLogonTimeout})),
			in:        TimerFired{Kind: TimerDisconnect, Gen: 10},
			wantState: StateBackoff, wantReason: ReasonLogonTimeout, wantActs: []string{"timer(backoff)"},
			check: func(t *testing.T, s Snapshot) {
				if !s.Conn {
					t.Error("Conn cleared without a Disconnected")
				}
			}},

		// --- LogOnFailed records, Disconnected decides ---
		{name: "logon failed only records", s: snap(StateLoggingOn), in: Event{gc.LogOnFailed{Result: gc.EResultInvalidPassword}},
			wantState: StateLoggingOn, wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				if !s.HasPending || s.Pending.Action != auth.ActionDropTokenAndReauthOnce || s.LastResult != auth.EResultInvalidPassword {
					t.Errorf("snapshot = %+v", s)
				}
			}},
		{name: "dead token reauths once", s: snap(StateLoggingOn, pending(classify(auth.EResultExpired))), in: Event{gc.Disconnected{}},
			wantState: StateAuthenticating, wantActs: []string{"stop_timer", "delete_token", "await_slot"},
			check: func(t *testing.T, s Snapshot) {
				if s.HasToken || !s.ReauthTried || s.Conn {
					t.Errorf("snapshot = %+v", s)
				}
			}},
		{name: "dead token twice needs attention", s: snap(StateLoggingOn, pending(classify(auth.EResultRevoked)), func(s *Snapshot) { s.ReauthTried = true }),
			in:        Event{gc.Disconnected{}},
			wantState: StateNeedsAttention, wantReason: auth.ReasonDeadToken, wantActs: []string{"stop_timer"}},
		{name: "dead token without password", s: snap(StateLoggingOn, pending(classify(auth.EResultAccessDenied)), func(s *Snapshot) { s.CanAuth = false }),
			in:        Event{gc.Disconnected{}},
			wantState: StateNeedsAttention, wantReason: auth.ReasonDeadToken, wantActs: []string{"stop_timer"}},
		{name: "invalid signature is a dead token", s: snap(StateLoggingOn, pending(classify(auth.EResultInvalidSignature))),
			in:        Event{gc.Disconnected{}},
			wantState: StateAuthenticating, wantActs: []string{"stop_timer", "delete_token", "await_slot"}},
		{name: "rate limited cools down and pauses logons", s: snap(StateLoggingOn, pending(classify(auth.EResultRateLimitExceeded))),
			in:        Event{gc.Disconnected{}},
			wantState: StateCoolingDown, wantReason: auth.ReasonRateLimited, wantActs: []string{"stop_timer", "timer(cooldown,30m0s)", "pause(5m0s)"}},
		{name: "throttle cools down", s: snap(StateLoggingOn, pending(classify(auth.EResultAccountLoginDeniedThrottle))),
			in:        Event{gc.Disconnected{}},
			wantState: StateCoolingDown, wantReason: auth.ReasonRateLimited, wantActs: []string{"stop_timer", "timer(cooldown,30m0s)", "pause(5m0s)"}},
		{name: "logged in elsewhere cools down", s: snap(StateLoggingOn, pending(classify(auth.EResultLoggedInElsewhere))),
			in:        Event{gc.Disconnected{}},
			wantState: StateCoolingDown, wantReason: auth.ReasonElsewhere, wantActs: []string{"stop_timer", "timer(cooldown,10m0s)"}},
		{name: "configured cooldown", s: snap(StateLoggingOn, func(s *Snapshot) {
			s.Params.Classifier.CooldownElsewhere = time.Hour
			s.Pending, s.HasPending = s.Params.Classifier.Classify(34, true), true
		}),
			in:        Event{gc.Disconnected{}},
			wantState: StateCoolingDown, wantReason: auth.ReasonElsewhere, wantActs: []string{"stop_timer", "timer(cooldown,1h0m0s)"}},
		{name: "totp mismatch retries in a new window", s: snap(StateLoggingOn, pending(classify(auth.EResultTwoFactorCodeMismatch))),
			in:        Event{gc.Disconnected{}},
			wantState: StateBackoff, wantReason: auth.ReasonTOTPMismatch, wantActs: []string{"stop_timer", "timer(backoff)"},
			check: func(t *testing.T, s Snapshot) {
				if s.Retries != 1 {
					t.Errorf("Retries = %d", s.Retries)
				}
			}},
		{name: "totp mismatch alerts after retries", s: snap(StateLoggingOn, pending(classify(auth.EResultTwoFactorCodeMismatch)), func(s *Snapshot) { s.Retries = 2 }),
			in:        Event{gc.Disconnected{}},
			wantState: StateNeedsAttention, wantReason: auth.ReasonTOTPMismatch, wantActs: []string{"stop_timer"}},
		{name: "transient failure backs off", s: snap(StateLoggingOn, pending(classify(auth.EResultServiceUnavailable))),
			in:        Event{gc.Disconnected{}},
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"stop_timer", "timer(backoff)"}},
		{name: "password logon needing guard without secret", s: snap(StateLoggingOn, func(s *Snapshot) { s.HasSharedSecret = false }),
			in:        Event{gc.LogOnFailed{Err: guardErr}},
			in2:       func(Snapshot) Input { return Event{gc.Disconnected{}} },
			wantState: StateNeedsAttention, wantReason: auth.ReasonGuardRequired, wantActs: []string{"stop_timer"}},
		{name: "steam failure sets pending", s: snap(StateLoggingOn), in: Event{gc.SteamFailure{Result: gc.EResultTryAnotherCM}},
			in2:       func(Snapshot) Input { return Event{gc.Disconnected{}} },
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"stop_timer", "timer(backoff)"}},
		{name: "steam failure keeps earlier pending", s: snap(StateLoggingOn, pending(classify(auth.EResultRateLimitExceeded))),
			in:        Event{gc.SteamFailure{Result: gc.EResultFail}},
			in2:       func(Snapshot) Input { return Event{gc.Disconnected{}} },
			wantState: StateCoolingDown, wantReason: auth.ReasonRateLimited, wantActs: []string{"stop_timer", "timer(cooldown,30m0s)", "pause(5m0s)"}},

		// --- disconnects ---
		{name: "disconnect from ready backs off", s: snap(StateGCReady, func(s *Snapshot) { s.Attempt = 2 }), in: Event{gc.Disconnected{}},
			wantState: StateBackoff, wantReason: ReasonDisconnected, wantActs: []string{"stop_timer", "timer(backoff)"},
			check: func(t *testing.T, s Snapshot) {
				if s.Conn || s.Playing || !s.LobbyStale || s.Attempt != 3 {
					t.Errorf("snapshot = %+v", s)
				}
			}},
		{name: "disconnect while backing off is late", s: snap(StateBackoff, func(s *Snapshot) { s.Conn = true }), in: Event{gc.Disconnected{}},
			wantState: StateBackoff, wantReason: "", wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				if s.Conn {
					t.Error("Conn still set")
				}
			}},
		{name: "late disconnect while waiting for a slot", s: snap(StateConnecting, func(s *Snapshot) { s.Conn, s.Timer = false, TimerSlot }),
			in:        Event{gc.Disconnected{}},
			wantState: StateConnecting, wantActs: []string{}},
		{name: "logged off elsewhere stops playing and disconnects", s: snap(StateGCReady), in: Event{gc.LoggedOff{Result: gc.EResultLoggedInElsewhere}},
			wantState: StateGCReady, wantActs: []string{"set_playing(false)", "disconnect", "timer(disconnect,30s)"}},
		{name: "logged off elsewhere then disconnected", s: snap(StateGCReady), in: Event{gc.LoggedOff{Result: gc.EResultLogonSessionReplaced}},
			in2:       func(Snapshot) Input { return Event{gc.Disconnected{}} },
			wantState: StateCoolingDown, wantReason: auth.ReasonElsewhere, wantActs: []string{"stop_timer", "timer(cooldown,10m0s)"}},
		{name: "playing blocked before playing", s: snap(StateSteamOnline), in: Event{gc.PlayingSession{Blocked: true}},
			wantState: StateSteamOnline, wantActs: []string{"disconnect", "timer(disconnect,30s)"}},
		{name: "playing blocked while playing", s: snap(StateGCHello), in: Event{gc.PlayingSession{Blocked: true}},
			in2:       func(Snapshot) Input { return Event{gc.Disconnected{}} },
			wantState: StateCoolingDown, wantReason: ReasonBlockedElsewhere, wantActs: []string{"stop_timer", "timer(cooldown,10m0s)"}},
		{name: "playing not blocked", s: snap(StateSteamOnline), in: Event{gc.PlayingSession{}},
			wantState: StateSteamOnline, wantActs: []string{}},

		// --- hello loop ---
		{name: "settle starts playing", s: snap(StateSteamOnline), in: TimerFired{Kind: TimerSettle, Gen: 10},
			wantState: StateGCHello, wantActs: []string{"set_playing(true)", "timer(hello,500ms)"},
			check: func(t *testing.T, s Snapshot) {
				if !s.Playing {
					t.Error("Playing not set")
				}
			}},
		{name: "first hello", s: snap(StateGCHello), in: TimerFired{Kind: TimerHello, Gen: 10},
			wantState: StateGCHello, wantActs: []string{"say_hello", "timer(hello,10s)"}},
		{name: "second hello doubles", s: snap(StateGCHello, func(s *Snapshot) { s.Hellos = 1 }), in: TimerFired{Kind: TimerHello, Gen: 10},
			wantState: StateGCHello, wantActs: []string{"say_hello", "timer(hello,20s)"}},
		{name: "hello caps at 30s", s: snap(StateGCHello, func(s *Snapshot) { s.Hellos = 2 }), in: TimerFired{Kind: TimerHello, Gen: 10},
			wantState: StateGCHello, wantActs: []string{"say_hello", "timer(hello,30s)"}},
		{name: "hello stays capped", s: snap(StateGCHello, func(s *Snapshot) { s.Hellos = 9 }), in: TimerFired{Kind: TimerHello, Gen: 10},
			wantState: StateGCHello, wantActs: []string{"say_hello", "timer(hello,30s)"}},
		{name: "hello continues in queue", s: snap(StateGCQueued), in: TimerFired{Kind: TimerHello, Gen: 10},
			wantState: StateGCQueued, wantActs: []string{"say_hello", "timer(hello,10s)"}},
		{name: "welcome is ready", s: snap(StateGCHello, func(s *Snapshot) { s.LobbyStale, s.Hellos = true, 3 }), in: Event{gc.GCWelcomed{}},
			wantState: StateGCReady, wantActs: []string{"timer(stable,5m0s)"},
			check: func(t *testing.T, s Snapshot) {
				if s.LobbyStale || s.Hellos != 0 || !s.Ready() {
					t.Errorf("snapshot = %+v", s)
				}
			}},
		{name: "welcome before playing ignored", s: snap(StateSteamOnline), in: Event{gc.GCWelcomed{}},
			wantState: StateSteamOnline, wantActs: []string{}},
		{name: "have session is ready", s: snap(StateGCHello), in: Event{gc.GCStatus{Old: gc.GCNoSession, New: gc.GCHaveSession}},
			wantState: StateGCReady, wantActs: []string{"timer(stable,5m0s)"}},
		{name: "logon queue", s: snap(StateGCHello), in: Event{gc.GCStatus{New: gc.GCNoSessionInLogonQueue, QueuePosition: 12}},
			wantState: StateGCQueued, wantActs: []string{"say_hello", "timer(hello,10s)"},
			check: func(t *testing.T, s Snapshot) {
				if s.QueuePosition != 12 {
					t.Errorf("QueuePosition = %d", s.QueuePosition)
				}
			}},
		{name: "queue position update", s: snap(StateGCQueued), in: Event{gc.GCStatus{New: gc.GCNoSessionInLogonQueue, QueuePosition: 3}},
			wantState: StateGCQueued, wantActs: []string{}},
		{name: "queue to ready", s: snap(StateGCQueued), in: Event{gc.GCStatus{New: gc.GCHaveSession}},
			wantState: StateGCReady, wantActs: []string{"timer(stable,5m0s)"}},
		{name: "session lost restarts hello", s: snap(StateGCReady), in: Event{gc.GCStatus{Old: gc.GCHaveSession, New: gc.GCNoSession}},
			wantState: StateGCHello, wantActs: []string{"say_hello", "timer(hello,10s)"},
			check: func(t *testing.T, s Snapshot) {
				if !s.LobbyStale {
					t.Error("lobby not marked stale")
				}
			}},
		{name: "gc going down restarts hello", s: snap(StateGCReady), in: Event{gc.GCStatus{New: gc.GCGoingDown}},
			wantState: StateGCHello, wantActs: []string{"say_hello", "timer(hello,10s)"}},
		{name: "session lost into queue", s: snap(StateGCReady), in: Event{gc.GCStatus{New: gc.GCNoSessionInLogonQueue}},
			wantState: StateGCQueued, wantActs: []string{"say_hello", "timer(hello,10s)"}},
		{name: "suspended", s: snap(StateGCReady), in: Event{gc.GCStatus{New: gc.GCSuspended}},
			wantState: StateSuspended, wantActs: []string{"stop_timer"}},
		{name: "suspension lifted", s: snap(StateSuspended), in: Event{gc.GCStatus{New: gc.GCHaveSession}},
			wantState: StateGCReady, wantActs: []string{"timer(stable,5m0s)"}},
		{name: "have session while ready is a no-op", s: snap(StateGCReady), in: Event{gc.GCStatus{New: gc.GCHaveSession}},
			wantState: StateGCReady, wantActs: []string{}},
		{name: "gc status before playing ignored", s: snap(StateSteamOnline), in: Event{gc.GCStatus{New: gc.GCNoSession}},
			wantState: StateSteamOnline, wantActs: []string{}},
		{name: "stable ready resets backoff", s: snap(StateGCReady, func(s *Snapshot) { s.Attempt = 5 }), in: TimerFired{Kind: TimerStable, Gen: 10},
			wantState: StateGCReady, wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				if s.Attempt != 0 {
					t.Errorf("Attempt = %d", s.Attempt)
				}
			}},

		// --- retry timers ---
		{name: "backoff fires", s: snap(StateBackoff), in: TimerFired{Kind: TimerBackoff, Gen: 10},
			wantState: StateConnecting, wantActs: []string{"await_slot"}},
		{name: "cooldown fires", s: snap(StateCoolingDown), in: TimerFired{Kind: TimerCooldown, Gen: 10},
			wantState: StateConnecting, wantActs: []string{"await_slot"}},
		{name: "backoff fires without token", s: snap(StateBackoff, func(s *Snapshot) { s.HasToken = false }), in: TimerFired{Kind: TimerBackoff, Gen: 10},
			wantState: StateAuthenticating, wantActs: []string{"await_slot"}},

		// --- authenticate ---
		{name: "auth ok connects", s: snap(StateAuthenticating, func(s *Snapshot) { s.HasToken = false }), in: AuthResult{},
			wantState: StateConnecting, wantActs: []string{"connect", "timer(logon,1m30s)"},
			check: func(t *testing.T, s Snapshot) {
				if !s.HasToken || !s.ReauthTried {
					t.Errorf("HasToken/ReauthTried = %t/%t", s.HasToken, s.ReauthTried)
				}
			}},
		{name: "auth guard with secret retries", s: snap(StateAuthenticating), in: AuthResult{Err: guardErr},
			wantState: StateBackoff, wantReason: auth.ReasonTOTPMismatch, wantActs: []string{"timer(backoff)"}},
		{name: "auth guard without secret", s: snap(StateAuthenticating, func(s *Snapshot) { s.HasSharedSecret = false }), in: AuthResult{Err: guardErr},
			wantState: StateNeedsAttention, wantReason: auth.ReasonGuardRequired, wantActs: []string{}},
		// go-steam reports Web API failures as "<Method> failed: eresult <N>
		// (<message>)"; the adapter puts N in AuthError.Result.
		{name: "auth bad password", s: snap(StateAuthenticating),
			in:        AuthResult{Err: deniedErr("BeginAuthSessionViaCredentials", 5)},
			wantState: StateNeedsAttention, wantReason: auth.ReasonBadCredential, wantActs: []string{}},
		{name: "auth denied without eresult", s: snap(StateAuthenticating),
			in:        AuthResult{Err: &gc.AuthError{State: "denied", Err: errors.New("begin auth session failed: InvalidPassword")}},
			wantState: StateNeedsAttention, wantReason: auth.ReasonBadCredential, wantActs: []string{}},
		{name: "auth rate limited", s: snap(StateAuthenticating),
			in:        AuthResult{Err: deniedErr("BeginAuthSessionViaCredentials", 84)},
			wantState: StateCoolingDown, wantReason: auth.ReasonRateLimited, wantActs: []string{"timer(cooldown,30m0s)", "pause(5m0s)"}},
		{name: "auth login throttled", s: snap(StateAuthenticating),
			in:        AuthResult{Err: deniedErr("BeginAuthSessionViaCredentials", 87)},
			wantState: StateCoolingDown, wantReason: auth.ReasonRateLimited, wantActs: []string{"timer(cooldown,30m0s)", "pause(5m0s)"}},
		{name: "auth totp mismatch retries in a new window", s: snap(StateAuthenticating),
			in:        AuthResult{Err: deniedErr("UpdateAuthSessionWithSteamGuardCode", 88)},
			wantState: StateBackoff, wantReason: auth.ReasonTOTPMismatch, wantActs: []string{"timer(backoff)"},
			check: func(t *testing.T, s Snapshot) {
				if s.Retries != 1 {
					t.Errorf("Retries = %d", s.Retries)
				}
			}},
		{name: "auth totp mismatch twice more alerts", s: snap(StateAuthenticating, func(s *Snapshot) { s.Retries = 2 }),
			in:        AuthResult{Err: deniedErr("UpdateAuthSessionWithSteamGuardCode", 88)},
			wantState: StateNeedsAttention, wantReason: auth.ReasonTOTPMismatch, wantActs: []string{}},
		{name: "auth steam unavailable backs off", s: snap(StateAuthenticating),
			in:        AuthResult{Err: deniedErr("GetPasswordRSAPublicKey", 20)},
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"timer(backoff)"}},
		{name: "auth steam fail backs off", s: snap(StateAuthenticating),
			in:        AuthResult{Err: deniedErr("BeginAuthSessionViaCredentials", 2)},
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"timer(backoff)"}},
		{name: "auth agreement", s: snap(StateAuthenticating), in: AuthResult{Err: &gc.AuthError{State: "agreement"}},
			wantState: StateNeedsAttention, wantReason: ReasonAgreementRequired, wantActs: []string{}},
		{name: "auth transport error backs off", s: snap(StateAuthenticating), in: AuthResult{Err: &gc.AuthError{State: "transport", Err: errDial}},
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"timer(backoff)"}},
		{name: "auth plain error backs off", s: snap(StateAuthenticating), in: AuthResult{Err: errDial},
			wantState: StateBackoff, wantReason: auth.ReasonTransient, wantActs: []string{"timer(backoff)"}},
		{name: "auth missing credentials", s: snap(StateAuthenticating), in: AuthResult{Err: fmt.Errorf("x: %w", gc.ErrNoCredentials)},
			wantState: StateNeedsAttention, wantReason: ReasonNoCredentials, wantActs: []string{}},
		{name: "auth result after stop ignored", s: snap(StateStopped), in: AuthResult{},
			wantState: StateStopped, wantActs: []string{}},

		// --- draining: our Disconnect is pending ---
		// go-dota2's SetPlaying(false) reports HAVE_SESSION -> NO_SESSION
		// synchronously; it must not restart the hello loop.
		{name: "own NO_SESSION while draining ignored", s: snap(StateGCReady),
			in:        Event{gc.PlayingSession{Blocked: true}},
			in2:       func(Snapshot) Input { return Event{gc.GCStatus{Old: gc.GCHaveSession, New: gc.GCNoSession}} },
			wantState: StateGCReady, wantActs: []string{}, check: drainingCheck},
		{name: "welcome while draining ignored", s: snap(StateGCHello),
			in:        Event{gc.LoggedOff{Result: gc.EResultLoggedInElsewhere}},
			in2:       func(Snapshot) Input { return Event{gc.GCWelcomed{}} },
			wantState: StateGCHello, wantActs: []string{}, check: drainingCheck},
		{name: "queue status while draining ignored", s: snap(StateGCReady),
			in:        Event{gc.LoggedOff{Result: gc.EResultLoggedInElsewhere}},
			in2:       func(Snapshot) Input { return Event{gc.GCStatus{New: gc.GCNoSessionInLogonQueue, QueuePosition: 4}} },
			wantState: StateGCReady, wantActs: []string{}, check: drainingCheck},
		{name: "logon after logon timeout ignored", s: snap(StateLoggingOn),
			in:        TimerFired{Kind: TimerLogon, Gen: 10},
			in2:       func(Snapshot) Input { return Event{gc.LoggedOn{SteamID: 1}} },
			wantState: StateLoggingOn, wantActs: []string{}, check: drainingCheck},
		{name: "second LoggedOff while draining only updates pending", s: snap(StateGCReady),
			in:        Event{gc.PlayingSession{Blocked: true}},
			in2:       func(Snapshot) Input { return Event{gc.LoggedOff{Result: gc.EResultLogonSessionReplaced}} },
			wantState: StateGCReady, wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				drainingCheck(t, s)
				if s.Pending.Reason != auth.ReasonElsewhere {
					t.Errorf("pending = %+v", s.Pending)
				}
			}},
		{name: "disconnected after draining decides", s: snap(StateGCReady),
			in:        Event{gc.PlayingSession{Blocked: true}},
			in2:       func(Snapshot) Input { return Event{gc.Disconnected{}} },
			wantState: StateCoolingDown, wantReason: ReasonBlockedElsewhere, wantActs: []string{"stop_timer", "timer(cooldown,10m0s)"},
			check: func(t *testing.T, s Snapshot) {
				if s.Draining || s.Conn {
					t.Errorf("Draining/Conn = %t/%t", s.Draining, s.Conn)
				}
			}},

		// --- connection numbering ---
		{name: "old connection's Disconnected ignored", s: snap(StateLoggingOn, func(s *Snapshot) { s.ConnSeq = 3 }),
			in:        Event{gc.Disconnected{Conn: 2}},
			wantState: StateLoggingOn, wantActs: []string{},
			check: func(t *testing.T, s Snapshot) {
				if !s.Conn {
					t.Error("current connection marked closed")
				}
			}},
		{name: "current connection's Disconnected decides", s: snap(StateLoggingOn, func(s *Snapshot) { s.ConnSeq = 3 }),
			in:        Event{gc.Disconnected{Conn: 3}},
			wantState: StateBackoff, wantReason: ReasonDisconnected, wantActs: []string{"stop_timer", "timer(backoff)"}},

		// --- stop and retry ---
		{name: "stop while ready", s: snap(StateGCReady), in: Stop{},
			wantState: StateStopped, wantReason: ReasonShutdown, wantActs: []string{"stop_timer", "set_playing(false)", "disconnect", "timer(disconnect,30s)"}},
		{name: "stop while backing off", s: snap(StateBackoff), in: Stop{},
			wantState: StateStopped, wantReason: ReasonShutdown, wantActs: []string{"stop_timer"}},
		{name: "stop before playing", s: snap(StateSteamOnline), in: Stop{},
			wantState: StateStopped, wantReason: ReasonShutdown, wantActs: []string{"stop_timer", "disconnect", "timer(disconnect,30s)"}},
		{name: "retry from needs attention", s: snap(StateNeedsAttention, func(s *Snapshot) { s.Retries, s.ReauthTried, s.Attempt = 3, true, 4 }), in: Retry{},
			wantState: StateConnecting, wantActs: []string{"await_slot"},
			check: func(t *testing.T, s Snapshot) {
				if s.Retries != 0 || s.ReauthTried || s.Attempt != 0 {
					t.Errorf("counters not reset: %+v", s)
				}
			}},
		{name: "retry while cooling down", s: snap(StateCoolingDown), in: Retry{},
			wantState: StateConnecting, wantActs: []string{"stop_timer", "await_slot"}},
		{name: "retry while ready ignored", s: snap(StateGCReady), in: Retry{},
			wantState: StateGCReady, wantActs: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, acts := next(tt.s, tt.in)
			if tt.in2 != nil {
				got, acts = next(got, tt.in2(got))
			}
			if got.State != tt.wantState || got.Reason != tt.wantReason {
				t.Errorf("state = %s/%q, want %s/%q", got.State, got.Reason, tt.wantState, tt.wantReason)
			}
			if d := describe(acts); !slices.Equal(d, tt.wantActs) {
				t.Errorf("actions = %v, want %v", d, tt.wantActs)
			}
			for _, a := range acts {
				if st, ok := a.(StartTimer); ok && (st.Gen != got.TimerGen || st.Kind != got.Timer) {
					t.Errorf("timer %+v does not match snapshot %s/%d", st, got.Timer, got.TimerGen)
				}
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func drainingCheck(t *testing.T, s Snapshot) {
	t.Helper()
	if !s.Draining || s.Timer != TimerDisconnect || s.Ready() {
		t.Errorf("Draining/Timer/Ready = %t/%s/%t, want true/disconnect/false", s.Draining, s.Timer, s.Ready())
	}
}

// TestAbandonedConnection: TimerDisconnect gives up on a connection, the
// next attempt connects, and only then does the old Disconnected arrive. It
// must neither decide again nor tear down the new connection.
func TestAbandonedConnection(t *testing.T) {
	s := snap(StateGCReady, func(s *Snapshot) { s.ConnSeq = 3 })
	s, _ = next(s, Event{gc.LoggedOff{Result: gc.EResultFail}})
	s, _ = next(s, TimerFired{Kind: TimerDisconnect, Gen: s.TimerGen})
	if s.State != StateBackoff || !s.Conn || s.Abandoned != 3 {
		t.Fatalf("after disconnect timeout: %s Conn=%t Abandoned=%d", s.State, s.Conn, s.Abandoned)
	}

	t.Run("late Disconnected before the next connect", func(t *testing.T) {
		got, acts := next(s, Event{gc.Disconnected{Conn: 3}})
		if got.State != StateBackoff || got.Conn || len(acts) != 0 {
			t.Errorf("state %s Conn=%t acts %v", got.State, got.Conn, describe(acts))
		}
	})

	t.Run("late Disconnected after the next connect", func(t *testing.T) {
		got, _ := next(s, TimerFired{Kind: TimerBackoff, Gen: s.TimerGen})
		got, _ = next(got, TimerFired{Kind: TimerSlot, Gen: got.TimerGen})
		got, _ = next(got, ConnectResult{})
		if got.ConnSeq != 4 || !got.Conn {
			t.Fatalf("after reconnect: ConnSeq=%d Conn=%t", got.ConnSeq, got.Conn)
		}
		got, acts := next(got, Event{gc.Disconnected{Conn: 3}})
		if got.State != StateConnecting || !got.Conn || len(acts) != 0 {
			t.Errorf("old Disconnected acted: %s Conn=%t %v", got.State, got.Conn, describe(acts))
		}
		got, _ = next(got, Event{gc.Connected{}})
		got, _ = next(got, Event{gc.LoggedOn{SteamID: 1}})
		if got.State != StateSteamOnline {
			t.Errorf("new connection's logon ignored: %s", got.State)
		}
	})

	t.Run("already connected drops it again and waits for it", func(t *testing.T) {
		got, _ := next(s, TimerFired{Kind: TimerBackoff, Gen: s.TimerGen})
		got, _ = next(got, TimerFired{Kind: TimerSlot, Gen: got.TimerGen})
		got, _ = next(got, ConnectResult{Err: gc.ErrAlreadyConnected})
		got, _ = next(got, Event{gc.Disconnected{Conn: 3}})
		if got.State != StateBackoff || got.Reason != ReasonAlreadyConnected || got.Conn {
			t.Errorf("state %s/%s Conn=%t", got.State, got.Reason, got.Conn)
		}
	})
}

// TestSingleReconnectOwner walks LogOnFailed then Disconnected: only the
// second may schedule anything, and exactly one timer results.
func TestSingleReconnectOwner(t *testing.T) {
	for _, r := range []gc.EResult{gc.EResultFail, gc.EResultInvalidPassword, gc.EResultRateLimitExceeded, gc.EResultLoggedInElsewhere, gc.EResultTwoFactorCodeMismatch, 999} {
		t.Run(r.String(), func(t *testing.T) {
			s, acts := next(snap(StateLoggingOn), Event{gc.LogOnFailed{Result: r}})
			if len(acts) != 0 {
				t.Fatalf("LogOnFailed acted: %v", describe(acts))
			}
			s, acts = next(s, Event{gc.Disconnected{}})
			timers := 0
			for _, a := range acts {
				switch a.(type) {
				case StartTimer, AwaitSlot:
					timers++
				}
			}
			if timers != 1 {
				t.Errorf("timers scheduled = %d (%v)", timers, describe(acts))
			}
			// A second, late Disconnected changes nothing.
			if _, acts := next(s, Event{gc.Disconnected{}}); len(acts) != 0 {
				t.Errorf("late Disconnected acted: %v", describe(acts))
			}
		})
	}
}

func backoffDelay(t *testing.T, s Snapshot) (time.Duration, Snapshot) {
	t.Helper()
	got, acts := next(s, Event{gc.Disconnected{}})
	for _, a := range acts {
		if st, ok := a.(StartTimer); ok && st.Kind == TimerBackoff {
			return st.After, got
		}
	}
	t.Fatalf("no backoff timer in %v", describe(acts))
	return 0, got
}

func TestBackoffFullJitter(t *testing.T) {
	p := testParams()
	for attempt := range 10 {
		ceil := min(p.BackoffMin<<attempt, p.BackoffMax)
		var maxSeen time.Duration
		for seed := range uint64(500) {
			s := snap(StateGCReady, func(s *Snapshot) { s.Attempt, s.Rand = attempt, seed })
			d, got := backoffDelay(t, s)
			if d < minBackoff || d > ceil {
				t.Fatalf("attempt %d seed %d: delay %s outside [%s, %s]", attempt, seed, d, minBackoff, ceil)
			}
			if got.Attempt != attempt+1 {
				t.Fatalf("Attempt = %d", got.Attempt)
			}
			maxSeen = max(maxSeen, d)
		}
		// Full jitter spreads over the whole range, not just near the cap.
		if maxSeen < ceil*3/4 {
			t.Errorf("attempt %d: max delay %s, want near %s", attempt, maxSeen, ceil)
		}
	}
}

func TestBackoffDeterministic(t *testing.T) {
	s := snap(StateGCReady, func(s *Snapshot) { s.Attempt, s.Rand = 4, 7 })
	a, _ := backoffDelay(t, s)
	b, _ := backoffDelay(t, s)
	if a != b {
		t.Errorf("same snapshot gave %s and %s", a, b)
	}
	s.Rand = 8
	if c, _ := backoffDelay(t, s); c == a {
		t.Errorf("different seeds gave the same delay %s", c)
	}
}

func TestParamsFromConfig(t *testing.T) {
	p := ParamsFromConfig(
		config.SteamConfig{ReconnectBackoffMin: 2 * time.Second, ReconnectBackoffMax: time.Minute, CooldownRateLimited: time.Hour},
		config.GCConfig{HelloInterval: 45 * time.Second},
	)
	if p.BackoffMin != 2*time.Second || p.BackoffMax != time.Minute {
		t.Errorf("backoff = %s..%s", p.BackoffMin, p.BackoffMax)
	}
	if p.HelloInterval != 45*time.Second || p.HelloMax != 45*time.Second {
		t.Errorf("hello = %s..%s", p.HelloInterval, p.HelloMax)
	}
	if got := p.Classifier.Classify(84, true).Cooldown; got != time.Hour {
		t.Errorf("rate-limit cooldown = %s", got)
	}
}

// BackoffMin<<attempt wraps for a huge BackoffMin; the ceiling must stay at
// BackoffMax instead of collapsing to the wrapped value (here 8ns).
func TestBackoffCeilingDoesNotOverflow(t *testing.T) {
	tests := []struct {
		name    string
		min     time.Duration
		attempt int
	}{
		{"wraps to a tiny positive", 1<<61 + 1, 3},
		{"wraps to zero", 1 << 61, 3},
		{"wraps negative", 1<<62 + 1, 1},
		{"shift past the guard", time.Second, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var maxSeen time.Duration
			for seed := range uint64(200) {
				s := snap(StateGCReady, func(s *Snapshot) {
					s.Params.BackoffMin, s.Params.BackoffMax = tt.min, time.Hour
					s.Attempt, s.Rand = tt.attempt, seed
				})
				d, _ := backoffDelay(t, s)
				if d < minBackoff || d > time.Hour {
					t.Fatalf("seed %d: delay %s outside [%s, 1h]", seed, d, minBackoff)
				}
				maxSeen = max(maxSeen, d)
			}
			if maxSeen < 30*time.Minute {
				t.Errorf("max delay %s, want the jitter spread over BackoffMax", maxSeen)
			}
		})
	}
}

func TestStop(t *testing.T) {
	tests := []struct {
		name     string
		s        Snapshot
		wantActs []string
		wantConn bool
	}{
		{
			name: "live connection waits for Disconnected under a timer", s: snap(StateGCReady, func(s *Snapshot) { s.ConnSeq = 3 }),
			wantActs: []string{"stop_timer", "set_playing(false)", "disconnect", "timer(disconnect,30s)"}, wantConn: true,
		},
		{
			name: "abandoned connection counts as gone", s: snap(StateBackoff, func(s *Snapshot) { s.Conn, s.ConnSeq, s.Abandoned = true, 3, 3 }),
			wantActs: []string{"stop_timer", "disconnect"}, wantConn: false,
		},
		{
			name: "no connection", s: snap(StateBackoff),
			wantActs: []string{"stop_timer"}, wantConn: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, acts := next(tt.s, Stop{})
			if got.State != StateStopped || got.Conn != tt.wantConn {
				t.Errorf("state %s conn %v, want stopped conn %v", got.State, got.Conn, tt.wantConn)
			}
			if d := describe(acts); !slices.Equal(d, tt.wantActs) {
				t.Errorf("actions %v, want %v", d, tt.wantActs)
			}
		})
	}
}

// A stopping bot whose Disconnected never arrives stops waiting when the
// disconnect timer fires, and a late Disconnected changes nothing.
func TestStopDisconnectTimeout(t *testing.T) {
	s, _ := next(snap(StateGCReady, func(s *Snapshot) { s.ConnSeq = 3 }), Stop{})
	s, _ = next(s, TimerFired{Kind: TimerDisconnect, Gen: s.TimerGen})
	if s.State != StateStopped || s.Conn || s.Abandoned != 3 {
		t.Fatalf("after timeout: state %s conn %v abandoned %d", s.State, s.Conn, s.Abandoned)
	}
	s, _ = next(s, Event{Event: gc.Disconnected{Conn: 3}})
	if s.State != StateStopped || s.Conn {
		t.Errorf("late Disconnected: state %s conn %v", s.State, s.Conn)
	}
}
