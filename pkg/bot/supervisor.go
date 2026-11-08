// Package bot keeps each Steam bot logged on with a live Dota 2 GC session.
// A Supervisor per bot owns the reconnect policy: it executes
// what the pure next() decides, against a gc.Client, and never blocks on a
// GC request. A Worker per bot serializes GC requests, and the Manager
// builds both from config and owns the state-dir lock.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Defaults for SupervisorOptions.
const (
	// DefaultAuthTimeout bounds one Authenticate, Steam Guard included.
	DefaultAuthTimeout = 2 * time.Minute
	// DefaultRenewBefore re-authenticates when the refresh token expires
	// within this window.
	DefaultRenewBefore = 14 * 24 * time.Hour
	// DefaultRenewCheck is how often the token's expiry is looked at.
	DefaultRenewCheck = 6 * time.Hour
	// DefaultHeartbeat is how often an idle run loop wakes up so the /livez
	// watchdog can tell idle from wedged.
	DefaultHeartbeat = 10 * time.Second
)

// Status is a point-in-time view of one bot, safe to hand to the API.
type Status struct {
	Name           string    `json:"name"`
	Username       string    `json:"username"`
	State          State     `json:"state"`
	Ready          bool      `json:"ready"`
	Reason         string    `json:"reason,omitempty"`
	LastResult     string    `json:"last_eresult,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	Since          time.Time `json:"since"`
	SteamID        uint64    `json:"steam_id,string,omitempty"`
	Limited        bool      `json:"limited"`
	LobbyStale     bool      `json:"lobby_stale"`
	TokenExpiresAt time.Time `json:"token_expires_at,omitzero"`
	Attempt        int       `json:"attempt"`
	QueuePosition  int32     `json:"queue_position,omitempty"`
	// RenewBlocked is set while background token renewal is stopped for a
	// reason only an operator can fix (bad_credentials, guard_required,
	// totp_mismatch, ...). The connection is not affected.
	RenewBlocked string `json:"renew_blocked,omitempty"`
}

// SupervisorOptions configures a Supervisor. Store is required.
type SupervisorOptions struct {
	// Params are the machine timings; the zero value means
	// ParamsFromConfig with an empty config (the defaults).
	Params Params
	// Store holds this bot's refresh token (cfg.BotStateDir).
	Store *auth.Store
	// Gate spaces logons across bots; nil means a private gate without
	// spacing.
	Gate *Gate
	// Clock defaults to SystemClock.
	Clock  Clock
	Logger *slog.Logger
	// RequestTimeout is the Worker's per-call default (gc.request_timeout).
	RequestTimeout time.Duration
	AuthTimeout    time.Duration
	RenewBefore    time.Duration
	RenewCheck     time.Duration
	// TOTPSkew is Steam time minus local time, added before computing a
	// Steam Guard code (steam.totp_skew).
	TOTPSkew time.Duration
	// Seed seeds the backoff jitter; 0 picks a random seed.
	Seed uint64
}

// Supervisor runs one bot's state machine on its own goroutine. Its
// methods are safe for concurrent use.
type Supervisor struct {
	bot    config.BotConfig
	client gc.Client
	store  *auth.Store
	gate   *Gate
	clock  Clock
	log    *slog.Logger
	opts   SupervisorOptions
	worker *Worker

	inputs    chan Input
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	ctx       context.Context
	cancel    context.CancelFunc

	// Owned by the run goroutine. authGen and renewGen tag the background
	// Authenticate calls so a superseded one's result is dropped.
	snap        Snapshot
	timer       Timer
	renewTimer  Timer
	authCancel  context.CancelFunc
	authGen     uint64
	renewCancel context.CancelFunc
	renewGen    uint64
	// adoptRenew is set when startAuth found a renewal in flight: its
	// result is then the logon path's Authenticate result.
	adoptRenew bool
	beatTimer  Timer

	// Background renewal's own failure policy, also owned by the run
	// goroutine. renewBlocked is the error that stopped renewal for good
	// until an operator retries; renewNotBefore and renewRetries pace the
	// retryable failures (cooldowns, TOTP window misses).
	renewBlocked   error
	renewReason    string
	renewNotBefore time.Time
	renewRetries   int

	// lastBeat is when the run loop last finished an input (unix nanos on
	// clock); beatArmed is set while a heartbeat timer is pending.
	lastBeat  atomic.Int64
	beatArmed atomic.Bool

	mu     sync.Mutex
	status Status
}

// Internal inputs the supervisor handles itself; next never sees them.
// authDone becomes an AuthResult once its token is saved.
type (
	renewTick struct{}
	authDone  struct {
		gen   uint64
		renew bool
		tok   auth.Token
		err   error
	}
	heartbeat struct{}
)

func (renewTick) isInput() {}
func (authDone) isInput()  {}
func (heartbeat) isInput() {}

// NewSupervisor builds a stopped supervisor for b. Call Start to run it.
func NewSupervisor(b config.BotConfig, client gc.Client, opts SupervisorOptions) *Supervisor {
	if opts.Params.BackoffMin == 0 {
		opts.Params = ParamsFromConfig(config.SteamConfig{}, config.GCConfig{})
	}
	if opts.Gate == nil {
		opts.Gate = NewGate(0)
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.AuthTimeout <= 0 {
		opts.AuthTimeout = DefaultAuthTimeout
	}
	if opts.RenewBefore <= 0 {
		opts.RenewBefore = DefaultRenewBefore
	}
	if opts.RenewCheck <= 0 {
		opts.RenewCheck = DefaultRenewCheck
	}
	if opts.Seed == 0 {
		opts.Seed = rand.Uint64()
	}
	s := &Supervisor{
		bot:    b,
		client: client,
		store:  opts.Store,
		gate:   opts.Gate,
		clock:  opts.Clock,
		log:    opts.Logger.With("bot", b.Name),
		opts:   opts,
		inputs: make(chan Input, 32),
		done:   make(chan struct{}),
	}
	s.snap = Snapshot{
		State:           StateDisconnected,
		CanAuth:         b.Credentials.CanAuthenticate(),
		HasSharedSecret: b.Credentials.SharedSecret.IsSet(),
		Params:          opts.Params,
		Rand:            opts.Seed,
	}
	s.worker = NewWorker(client, s.Ready, opts.RequestTimeout)
	s.status = Status{Name: b.Name, Username: b.Username, State: StateDisconnected, Since: s.clock.Now()}
	return s
}

// Name is the bot's configured name.
func (s *Supervisor) Name() string { return s.bot.Name }

// Worker returns the bot's serialized GC request queue.
func (s *Supervisor) Worker() *Worker { return s.worker }

// Status returns the current status.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Ready reports whether the bot is in gc_ready.
func (s *Supervisor) Ready() bool { return s.Status().Ready }

// Stalled reports whether the run loop is running but has not finished an
// input for longer than maxAge. An idle loop wakes every DefaultHeartbeat,
// so a stall means it is stuck, and with it the event pump that feeds it.
// A supervisor that is not running is never stalled.
func (s *Supervisor) Stalled(maxAge time.Duration) bool {
	last := s.lastBeat.Load()
	if last == 0 {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
	}
	return s.clock.Now().Sub(time.Unix(0, last)) > maxAge
}

// Start seeds the token store, then starts the run goroutine and the first
// logon. ctx bounds the supervisor's life; cancelling it is a hard stop that
// skips the graceful Stop sequence. Start is a no-op after the first call.
func (s *Supervisor) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.ctx, s.cancel = context.WithCancel(ctx)
		s.snap.HasToken = s.loadToken()
		go s.run()
		s.push(Start{})
	})
}

// Stop shuts the bot down: SetPlaying(false), Disconnect, then
// waits for Disconnected. If ctx ends first the connection is cut hard and
// ctx's error returned. Stop never logs off.
func (s *Supervisor) Stop(ctx context.Context) error {
	var err error
	s.startOnce.Do(func() {}) // a never-started supervisor has nothing to stop
	s.stopOnce.Do(func() {
		if s.cancel == nil {
			err = s.worker.Shutdown(ctx)
			return
		}
		// A wedged loop with a full input queue must not hold Stop past
		// ctx, so the push races ctx too.
		select {
		case s.inputs <- Stop{}:
		case <-s.done:
		case <-ctx.Done():
		}
		select {
		case <-s.done:
		case <-ctx.Done():
			err = fmt.Errorf("stop bot %s: %w", s.bot.Name, ctx.Err())
			s.cancel()
			<-s.done
		}
		s.cancel()
		// The running GC request gets what is left of ctx, then is cut.
		if werr := s.worker.Shutdown(ctx); werr != nil && err == nil {
			err = fmt.Errorf("stop bot %s: %w", s.bot.Name, werr)
		}
	})
	return err
}

// ErrBusy is returned by Retry when the run loop did not take the request
// before ctx ended, e.g. because it is wedged with a full input queue.
var ErrBusy = errors.New("bot: run loop busy")

// Retry asks a bot in needs_attention, backoff or cooling_down to try again
// now, e.g. after an operator fixed its credentials. It gives up with
// ErrBusy (wrapping ctx's error) when the input queue stays full until ctx
// ends. Retrying a stopped bot is a no-op.
func (s *Supervisor) Retry(ctx context.Context) error {
	select {
	case s.inputs <- Retry{}:
		return nil
	case <-s.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: retry %s: %w", ErrBusy, s.bot.Name, ctx.Err())
	}
}

// push delivers an input to the run goroutine unless it has exited.
func (s *Supervisor) push(in Input) {
	select {
	case s.inputs <- in:
	case <-s.done:
	}
}

// loadToken seeds the store from the configured refresh token when it has
// none, and reports whether a usable token exists.
func (s *Supervisor) loadToken() bool {
	now := s.clock.Now()
	tok, seeded, err := s.store.LoadOrSeed(s.bot.Credentials.RefreshToken, now)
	switch {
	case errors.Is(err, auth.ErrNoToken):
		return false
	case err != nil:
		s.log.Warn("stored token unusable; will authenticate", "err", err)
		return false
	}
	if seeded {
		s.log.Info("token store seeded from configured refresh token", "token", tok)
	}
	s.setTokenExpiry(tok.ExpiresAt)
	if tok.Expired(now) {
		s.log.Warn("stored token has expired; will authenticate", "token", tok)
		return false
	}
	return true
}

func (s *Supervisor) run() {
	defer close(s.done)
	defer s.cleanup()
	s.scheduleRenew()
	events := s.client.Events()
	for {
		s.beat()
		select {
		case <-s.ctx.Done():
			return
		case ev := <-events:
			s.handle(Event{Event: ev})
		case in := <-s.inputs:
			s.handle(in)
		}
		if s.snap.State == StateStopped && !s.snap.Conn {
			return
		}
	}
}

// beat records that the loop is alive and keeps one heartbeat pending. The
// timer never blocks: when the input queue is full the beat is dropped and
// re-armed on the next pass, which the queued inputs guarantee.
func (s *Supervisor) beat() {
	s.lastBeat.Store(s.clock.Now().UnixNano())
	if !s.beatArmed.CompareAndSwap(false, true) {
		return
	}
	s.beatTimer = s.clock.AfterFunc(DefaultHeartbeat, func() {
		select {
		case s.inputs <- heartbeat{}:
		default:
			s.beatArmed.Store(false)
		}
	})
}

func (s *Supervisor) cleanup() {
	s.stopTimer()
	if s.renewTimer != nil {
		s.renewTimer.Stop()
	}
	if s.beatTimer != nil {
		s.beatTimer.Stop()
	}
	if s.authCancel != nil {
		s.authCancel()
	}
	if s.renewCancel != nil {
		s.renewCancel()
	}
}

// handle runs one input through next and executes the actions. Actions
// that complete at once (Connect's result, a free logon slot) feed their
// result back in before the status is published.
func (s *Supervisor) handle(in Input) {
	switch v := in.(type) {
	case heartbeat:
		s.beatArmed.Store(false)
		return
	case renewTick:
		s.maybeRenew()
		s.scheduleRenew()
		return
	case authDone:
		fb, ok := s.authFinished(v)
		if !ok {
			return
		}
		in = fb
	case TimerFired:
		// A rate-limit pause can start after this slot was booked; the
		// slot then waits out the pause like a fresh Reserve would.
		if v.Kind == TimerSlot && v.Gen == s.snap.TimerGen && s.snap.Timer == TimerSlot {
			if d := s.gate.Recheck(s.bot.Name, s.clock.Now()); d > 0 {
				s.armTimer(TimerSlot, d, v.Gen)
				return
			}
		}
	case Retry:
		// An operator may have put a new token in place (auth --bot).
		switch s.snap.State {
		case StateNeedsAttention, StateBackoff, StateCoolingDown:
			s.snap.HasToken = s.loadToken()
		}
		// The operator has had their say about the credentials too.
		s.clearRenewBlock()
		s.maybeRenew()
	}
	queue := []Input{in}
	for len(queue) > 0 {
		in, queue = queue[0], queue[1:]
		prev := s.snap.State
		var acts []Action
		s.snap, acts = next(s.snap, in)
		if s.snap.State != prev {
			s.transitioned(prev)
		}
		for _, a := range acts {
			if fb := s.exec(a); fb != nil {
				queue = append(queue, fb)
			}
		}
	}
	s.publish()
}

func (s *Supervisor) transitioned(prev State) {
	sn := s.snap
	attrs := []any{"from", prev, "to", sn.State}
	if sn.Reason != "" {
		attrs = append(attrs, "reason", sn.Reason)
	}
	if sn.LastResult != 0 {
		attrs = append(attrs, "eresult", sn.LastResult.String())
	}
	if sn.LastError != "" && !sn.State.online() {
		attrs = append(attrs, "err", sn.LastError)
	}
	switch sn.State {
	case StateNeedsAttention:
		s.log.Error("bot needs attention", attrs...)
	case StateCoolingDown, StateSuspended:
		s.log.Warn("bot state changed", attrs...)
	default:
		s.log.Info("bot state changed", attrs...)
	}
	s.mu.Lock()
	s.status.Since = s.clock.Now()
	s.mu.Unlock()
	if sn.State == StateGCReady {
		s.maybeRenew()
	}
}

func (s *Supervisor) publish() {
	sn := s.snap
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &s.status
	st.State, st.Ready, st.Reason = sn.State, sn.Ready(), sn.Reason
	st.LastResult = ""
	if sn.LastResult != 0 {
		st.LastResult = sn.LastResult.String()
	}
	st.LastError = sn.LastError
	st.SteamID, st.Limited, st.LobbyStale = sn.SteamID, sn.Limited, sn.LobbyStale
	st.Attempt, st.QueuePosition = sn.Attempt, sn.QueuePosition
}

func (s *Supervisor) setTokenExpiry(t time.Time) {
	s.mu.Lock()
	s.status.TokenExpiresAt = t
	s.mu.Unlock()
}

// exec performs one action. It must not block: every call here is
// fire-and-forget or local I/O.
func (s *Supervisor) exec(a Action) Input {
	switch a := a.(type) {
	case AwaitSlot:
		d := s.gate.Reserve(s.bot.Name, s.clock.Now())
		if d <= 0 {
			return TimerFired{Kind: TimerSlot, Gen: a.Gen}
		}
		s.armTimer(TimerSlot, d, a.Gen)
	case StartTimer:
		s.armTimer(a.Kind, a.After, a.Gen)
	case StopTimer:
		s.stopTimer()
	case Connect:
		return ConnectResult{Err: s.connect()}
	case Authenticate:
		s.startAuth()
	case DeleteToken:
		if err := s.store.DeleteRejected(); err != nil {
			s.log.Error("delete dead token", "err", err)
		}
		s.setTokenExpiry(time.Time{})
	case Disconnect:
		s.client.Disconnect()
	case SetPlaying:
		s.client.SetPlaying(a.Playing)
	case SayHello:
		s.client.SayHello()
	case PauseLogons:
		s.log.Warn("pausing logons for every bot", "for", a.For)
		s.gate.Pause(s.clock.Now().Add(a.For))
	}
	return nil
}

func (s *Supervisor) armTimer(kind TimerKind, d time.Duration, gen uint64) {
	s.stopTimer()
	s.timer = s.clock.AfterFunc(d, func() { s.push(TimerFired{Kind: kind, Gen: gen}) })
}

func (s *Supervisor) stopTimer() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// connect logs on with the stored token. An unreadable or expired token
// counts as no token, which sends the machine to Authenticate.
func (s *Supervisor) connect() error {
	tok, err := s.store.Load()
	switch {
	case errors.Is(err, auth.ErrNoToken):
		return err
	case err != nil:
		s.log.Warn("stored token unreadable", "err", err)
		return fmt.Errorf("%w: %w", auth.ErrNoToken, err)
	case tok.Expired(s.clock.Now()):
		return fmt.Errorf("stored token expired: %w", auth.ErrNoToken)
	}
	s.setTokenExpiry(tok.ExpiresAt)
	return s.client.Connect(s.ctx, gc.Credentials{
		Username:    s.bot.Username,
		AccessToken: tok.Secret(),
		DeviceName:  s.bot.Credentials.DeviceName,
	})
}

// startAuth runs Authenticate off the run goroutine (it is HTTP and may
// take a while); an authDone reports back. An in-flight renewal is adopted
// instead of cancelled or duplicated: two auth sessions would submit the
// same TOTP code, and Steam rejects the second (88), while cancelling the
// first would throw away a result that is about to arrive. The renewal's
// outcome then answers this Authenticate.
func (s *Supervisor) startAuth() {
	if s.authCancel != nil {
		s.authCancel()
		s.authCancel = nil
	}
	s.authGen++
	gen := s.authGen
	if s.renewCancel != nil {
		s.adoptRenew = true
		return
	}
	if s.renewBlocked != nil && s.renewReason == auth.ReasonBadCredential {
		// Steam already refused this password; submitting it again is what
		// gets an IP throttled. Fail the same way until an operator retries.
		err := s.renewBlocked
		go s.push(authDone{gen: gen, err: err})
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.AuthTimeout)
	s.authCancel = cancel
	go func() {
		defer cancel()
		tok, err := s.authenticate(ctx)
		s.push(authDone{gen: gen, tok: tok, err: err})
	}()
}

// authFinished saves the token of a current authDone on the run goroutine,
// so a save can never interleave with DeleteToken. For a startAuth result
// it returns the AuthResult to feed to next.
func (s *Supervisor) authFinished(d authDone) (Input, bool) {
	if d.renew {
		if d.gen != s.renewGen {
			return nil, false
		}
		s.renewCancel = nil
		s.renewGen++
		if s.adoptRenew {
			s.adoptRenew = false
			return s.authResult(d), true
		}
		if d.err != nil {
			s.renewFailed(d.err)
			return nil, false
		}
		if err := s.saveToken(d.tok); err != nil {
			s.log.Warn("token renewal failed; will retry", "err", err)
			return nil, false
		}
		// The renewed token is what the next logon must use, not a fresh
		// password Authenticate.
		s.snap.HasToken = true
		s.clearRenewBlock()
		return nil, false
	}
	if d.gen != s.authGen {
		return nil, false
	}
	s.authCancel = nil
	return s.authResult(d), true
}

// authResult saves the token of a finished logon-path Authenticate and
// returns the AuthResult for next.
func (s *Supervisor) authResult(d authDone) Input {
	if d.err == nil {
		d.err = s.saveToken(d.tok)
		if d.err == nil {
			s.clearRenewBlock() // the credentials work
		}
	}
	return AuthResult{Err: d.err}
}

func (s *Supervisor) saveToken(tok auth.Token) error {
	if err := s.store.Save(tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	s.setTokenExpiry(tok.ExpiresAt)
	s.log.Info("refresh token saved", "token", tok)
	return nil
}

// authenticate exchanges password + TOTP for a refresh token. The caller
// saves it, to the store only, never back to the secrets source.
func (s *Supervisor) authenticate(ctx context.Context) (auth.Token, error) {
	c := s.bot.Credentials
	creds := gc.Credentials{Username: s.bot.Username, Password: c.Password, DeviceName: c.DeviceName}
	if c.SharedSecret.IsSet() {
		code, err := auth.TOTP{SharedSecret: c.SharedSecret, Skew: s.opts.TOTPSkew, Now: s.clock.Now}.Code()
		if err != nil {
			// A broken shared_secret will not fix itself.
			return auth.Token{}, fmt.Errorf("%w: shared_secret: %w", gc.ErrNoCredentials, err)
		}
		creds.TwoFactorCode = config.Secret(code)
	}
	raw, err := s.client.Authenticate(ctx, creds)
	if err != nil {
		return auth.Token{}, err
	}
	return auth.FromMinted(raw, s.clock.Now()), nil
}

func (s *Supervisor) scheduleRenew() { s.scheduleRenewIn(s.opts.RenewCheck) }

// maybeRenew re-authenticates in the background when the stored token is
// close to expiry. The connection is untouched: the new token is used at the
// next logon.
//
// It only runs in gc_ready with a token that has not yet expired. In any
// other state the logon path may authenticate by itself, and two sessions in
// one 30 s window would submit the same TOTP code, which Steam answers with
// 88. A bot that is not ready gets its renewal when it reaches gc_ready; if
// the token expires first, the logon path authenticates and mints a fresh
// one. A renewal already in flight when the logon path needs to authenticate
// is adopted by startAuth.
func (s *Supervisor) maybeRenew() {
	switch {
	case s.renewCancel != nil, !s.bot.Credentials.Headless():
		return
	case s.renewBlocked != nil, s.clock.Now().Before(s.renewNotBefore):
		return
	case s.snap.State != StateGCReady:
		return
	}
	now := s.clock.Now()
	tok, err := s.store.Load()
	if err != nil || tok.Expired(now) || !tok.ExpiresWithin(now, s.opts.RenewBefore) {
		return
	}
	s.log.Info("refresh token expires soon; renewing", "token", tok)
	s.renewGen++
	gen := s.renewGen
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.AuthTimeout)
	s.renewCancel = cancel
	go func() {
		defer cancel()
		tok, err := s.authenticate(ctx)
		s.push(authDone{gen: gen, renew: true, tok: tok, err: err})
	}()
}

// renewFailed applies the same classifier as a logon to a failed background
// renewal, so a refusal is never retried blindly:
//
//   - a refused password (5), a missing Steam Guard answer or other
//     needs-attention verdict stops renewal and sets Status.RenewBlocked.
//     The connection is left alone: its token still works until it expires,
//     and dropping a working bot would turn a credential problem into an
//     outage. If the token does expire first, the normal reconnect path
//     reports needs_attention.
//   - a rate limit (84/87) pauses logons for every bot, like a logon would,
//     and renewal waits out the cooldown.
//   - a TOTP miss (88) retries in a new code window, a limited number of
//     times, then stops like a refused password.
//   - anything else is transient: retry at the next check.
func (s *Supervisor) renewFailed(err error) {
	p := classifyAuthError(err, s.snap.HasSharedSecret, s.snap.Params.Classifier)
	now := s.clock.Now()
	switch p.Action {
	case auth.ActionNeedsAttention:
		s.blockRenew(err, p.Reason)
	case auth.ActionCoolDown:
		s.renewNotBefore = now.Add(p.Cooldown)
		if p.Reason == auth.ReasonRateLimited {
			s.log.Warn("pausing logons for every bot", "for", s.snap.Params.RateLimitPause)
			s.gate.Pause(now.Add(s.snap.Params.RateLimitPause))
		}
		s.log.Warn("token renewal refused; backing off", "reason", p.Reason, "for", p.Cooldown, "err", err)
		s.scheduleRenewIn(p.Cooldown)
	case auth.ActionRetryThenAlert:
		s.renewRetries++
		if s.renewRetries > p.MaxRetries {
			s.blockRenew(err, p.Reason)
			return
		}
		delay := s.snap.Params.TOTPRetryDelay
		s.renewNotBefore = now.Add(delay)
		s.log.Warn("token renewal needs a new Steam Guard code; retrying", "reason", p.Reason, "in", delay, "err", err)
		s.scheduleRenewIn(delay)
	default:
		s.log.Warn("token renewal failed; will retry", "err", err)
	}
}

func (s *Supervisor) blockRenew(err error, reason string) {
	s.renewBlocked, s.renewReason = err, reason
	s.mu.Lock()
	s.status.RenewBlocked = reason
	s.mu.Unlock()
	s.log.Error("token renewal stopped; fix the credentials, then retry the bot", "reason", reason, "err", err)
}

// clearRenewBlock forgets every renewal failure: after a successful save or
// an operator retry.
func (s *Supervisor) clearRenewBlock() {
	s.renewBlocked, s.renewReason = nil, ""
	s.renewNotBefore, s.renewRetries = time.Time{}, 0
	s.mu.Lock()
	s.status.RenewBlocked = ""
	s.mu.Unlock()
}

// scheduleRenewIn brings the next renewal check forward to d from now; the
// check itself reschedules the regular period.
func (s *Supervisor) scheduleRenewIn(d time.Duration) {
	if s.renewTimer != nil {
		s.renewTimer.Stop()
	}
	s.renewTimer = s.clock.AfterFunc(d, func() { s.push(renewTick{}) })
}
