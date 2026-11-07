// Package bot keeps each Steam bot logged on with a live Dota 2 GC session.
// A Supervisor per bot owns the reconnect policy (plan §4.1): it executes
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
	"strings"
	"sync"
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
	// within this window, which is also the alert threshold (plan §4.8).
	DefaultRenewBefore = 14 * 24 * time.Hour
	// DefaultRenewCheck is how often the token's expiry is looked at.
	DefaultRenewCheck = 6 * time.Hour
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

	// Owned by the run goroutine.
	snap       Snapshot
	timer      Timer
	renewTimer Timer
	authCancel context.CancelFunc
	renewing   bool

	mu     sync.Mutex
	status Status
}

// Internal inputs the supervisor handles itself; next never sees them.
type (
	renewTick struct{}
	renewDone struct{ err error }
)

func (renewTick) isInput() {}
func (renewDone) isInput() {}

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

// Stop shuts the bot down (plan §6): SetPlaying(false), Disconnect, then
// waits for Disconnected. If ctx ends first the connection is cut hard and
// ctx's error returned. Stop never logs off.
func (s *Supervisor) Stop(ctx context.Context) error {
	var err error
	s.startOnce.Do(func() {}) // a never-started supervisor has nothing to stop
	s.stopOnce.Do(func() {
		if s.cancel == nil {
			s.worker.Close()
			return
		}
		s.push(Stop{})
		select {
		case <-s.done:
		case <-ctx.Done():
			err = fmt.Errorf("stop bot %s: %w", s.bot.Name, ctx.Err())
			s.cancel()
			<-s.done
		}
		s.cancel()
		s.worker.Close()
	})
	return err
}

// Retry asks a bot in needs_attention, backoff or cooling_down to try again
// now, e.g. after an operator fixed its credentials.
func (s *Supervisor) Retry() { s.push(Retry{}) }

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

func (s *Supervisor) cleanup() {
	s.stopTimer()
	if s.renewTimer != nil {
		s.renewTimer.Stop()
	}
	if s.authCancel != nil {
		s.authCancel()
	}
}

// handle runs one input through next and executes the actions. Actions
// that complete at once (Connect's result, a free logon slot) feed their
// result back in before the status is published.
func (s *Supervisor) handle(in Input) {
	switch in := in.(type) {
	case renewTick:
		s.maybeRenew()
		s.scheduleRenew()
		return
	case renewDone:
		s.renewing = false
		if in.err != nil {
			s.log.Warn("token renewal failed; will retry", "err", in.err)
		}
		return
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
		d := s.gate.Reserve(s.clock.Now())
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
		if err := s.store.Delete(); err != nil {
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
// take a while) and reports back with an AuthResult.
func (s *Supervisor) startAuth() {
	if s.authCancel != nil {
		s.authCancel()
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.AuthTimeout)
	s.authCancel = cancel
	go func() {
		defer cancel()
		s.push(AuthResult{Err: s.authenticate(ctx)})
	}()
}

// authenticate exchanges password + TOTP for a refresh token and saves it.
// Renewed tokens go to the store only, never back to the secrets source.
func (s *Supervisor) authenticate(ctx context.Context) error {
	c := s.bot.Credentials
	creds := gc.Credentials{Username: s.bot.Username, Password: c.Password, DeviceName: c.DeviceName}
	if c.SharedSecret.IsSet() {
		code, err := auth.TOTP{SharedSecret: c.SharedSecret, Now: s.clock.Now}.Code()
		if err != nil {
			// A broken shared_secret will not fix itself.
			return fmt.Errorf("%w: shared_secret: %w", gc.ErrNoCredentials, err)
		}
		creds.TwoFactorCode = code
	}
	raw, err := s.client.Authenticate(ctx, creds)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	tok, err := auth.NewToken(raw, now, auth.SourceAuthenticate)
	if errors.Is(err, auth.ErrNotJWT) {
		// Still usable for logon; only the expiry is unknown.
		tok = auth.Token{RefreshToken: strings.TrimSpace(raw), ObtainedAt: now.UTC(), Source: auth.SourceAuthenticate}
	} else if err != nil {
		return err
	}
	if err := s.store.Save(tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	s.setTokenExpiry(tok.ExpiresAt)
	s.log.Info("refresh token saved", "token", tok)
	return nil
}

func (s *Supervisor) scheduleRenew() {
	if s.renewTimer != nil {
		s.renewTimer.Stop()
	}
	s.renewTimer = s.clock.AfterFunc(s.opts.RenewCheck, func() { s.push(renewTick{}) })
}

// maybeRenew re-authenticates in the background when the stored token is
// close to expiry. The connection is untouched: the new token is used at the
// next logon.
func (s *Supervisor) maybeRenew() {
	switch {
	case s.renewing, !s.bot.Credentials.Headless():
		return
	case s.snap.State == StateAuthenticating, s.snap.State == StateStopped:
		return
	}
	tok, err := s.store.Load()
	if err != nil || !tok.ExpiresWithin(s.clock.Now(), s.opts.RenewBefore) {
		return
	}
	s.log.Info("refresh token expires soon; renewing", "token", tok)
	s.renewing = true
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.AuthTimeout)
	go func() {
		defer cancel()
		s.push(renewDone{err: s.authenticate(ctx)})
	}()
}
