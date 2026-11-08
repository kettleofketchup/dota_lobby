package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
)

func TestColdStartWithSeedToken(t *testing.T) {
	b := headlessBot()
	seed := jwt(t, "76561198000000001", newFakeClock().Now().Add(180*24*time.Hour))
	b.Credentials.RefreshToken = config.Secret(seed)
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		h.fc.Script("SayHello") // the first hello is often lost
		welcomeOnHello(h)
	})
	h.start()
	st := h.untilState(StateGCReady, time.Minute)

	if st.SteamID != testSteamID || !st.Ready || st.TokenExpiresAt.IsZero() {
		t.Errorf("status = %+v", st)
	}
	creds := connectCreds(t, h.fc)
	if len(creds) != 1 || creds[0].AccessToken.Reveal() != seed || creds[0].Password.IsSet() {
		t.Fatalf("Connect creds = %+v", creds)
	}
	if n := h.fc.CallCount("Authenticate"); n != 0 {
		t.Errorf("Authenticate called %d times with a seed token", n)
	}
	tok, err := h.store.Load()
	if err != nil || tok.RefreshToken != seed || tok.Source != auth.SourceSeed {
		t.Errorf("stored token = %v, %v", tok, err)
	}
	// SetPlaying(true) comes before the first hello, and hellos repeat.
	calls := h.fc.Calls("SetPlaying", "SayHello")
	if len(calls) < 3 || calls[0].Method != "SetPlaying" || calls[0].Args[0] != true {
		t.Errorf("calls = %+v", calls)
	}
}

func TestColdStartAuthenticatesWithTOTP(t *testing.T) {
	minted := jwt(t, "76561198000000001", newFakeClock().Now().Add(200*24*time.Hour))
	h := newHarness(t, headlessBot(), func(h *harness) {
		h.fc.SetToken(minted)
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	auths := h.fc.Calls("Authenticate")
	if len(auths) != 1 {
		t.Fatalf("Authenticate calls = %d", len(auths))
	}
	ac := auths[0].Args[0].(gc.Credentials)
	// The clock has moved by well under one 30 s window since the call.
	want, _ := auth.Code(testSharedSecret, newFakeClock().Now())
	if ac.Username != "bota" || ac.Password.Reveal() != "hunter2" || len(ac.TwoFactorCode.Reveal()) != 5 || ac.TwoFactorCode.Reveal() != want {
		t.Errorf("Authenticate creds = %+v (code want %s)", ac, want)
	}
	if creds := connectCreds(t, h.fc); len(creds) != 1 || creds[0].AccessToken.Reveal() != minted {
		t.Errorf("Connect creds = %+v", creds)
	}
	tok, err := h.store.Load()
	if err != nil || tok.RefreshToken != minted || tok.Source != auth.SourceAuthenticate {
		t.Errorf("stored token = %v, %v", tok, err)
	}
}

func TestDeadTokenReauthenticates(t *testing.T) {
	now := newFakeClock().Now()
	old := jwt(t, "76561198000000001", now.Add(100*24*time.Hour))
	minted := jwt(t, "76561198000000001", now.Add(200*24*time.Hour))
	h := newHarness(t, headlessBot(), func(h *harness) {
		tok, err := auth.NewToken(old, now, auth.SourceSeed)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.Save(tok); err != nil {
			t.Fatal(err)
		}
		h.fc.SetToken(minted)
		h.fc.Script("Connect", gc.Connected{}, gc.LogOnFailed{Result: gc.EResultExpired}, gc.Disconnected{})
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	st := h.untilState(StateGCReady, time.Minute)

	creds := connectCreds(t, h.fc)
	if len(creds) != 2 || creds[0].AccessToken.Reveal() != old || creds[1].AccessToken.Reveal() != minted {
		t.Fatalf("Connect tokens wrong: %d calls", len(creds))
	}
	if n := h.fc.CallCount("Authenticate"); n != 1 {
		t.Errorf("Authenticate calls = %d", n)
	}
	tok, err := h.store.Load()
	if err != nil || tok.RefreshToken != minted || tok.Source != auth.SourceAuthenticate {
		t.Errorf("stored token = %v, %v", tok, err)
	}
	if st.LastResult != "OK" {
		t.Errorf("LastResult = %q", st.LastResult)
	}
}

func TestDeadTokenTwiceNeedsAttention(t *testing.T) {
	now := newFakeClock().Now()
	h := newHarness(t, headlessBot(), func(h *harness) {
		h.fc.SetToken(jwt(t, "1", now.Add(time.Hour*24*200)))
		h.fc.ScriptEvery("Connect", gc.Connected{}, gc.LogOnFailed{Result: gc.EResultInvalidPassword}, gc.Disconnected{})
	})
	h.start()
	st := h.untilState(StateNeedsAttention, time.Minute)
	if st.Reason != auth.ReasonDeadToken || st.LastResult != "InvalidPassword" {
		t.Errorf("status = %+v", st)
	}
	if n := h.fc.CallCount("Authenticate"); n != 1 {
		t.Errorf("Authenticate calls = %d, want exactly one re-auth", n)
	}
}

func TestDialFailureBacksOffAndRecovers(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", gc.Fatal{Err: errors.New("dial steam: connection refused")}, gc.Disconnected{})
		h.fc.Script("Connect", gc.Fatal{Err: errors.New("dial steam: connection refused")}, gc.Disconnected{})
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.until("backoff", time.Millisecond, time.Second, func(s Status) bool { return s.State == StateBackoff })
	st := h.untilState(StateGCReady, 10*time.Minute)
	if n := h.fc.CallCount("Connect"); n != 3 {
		t.Errorf("Connect calls = %d", n)
	}
	if st.Attempt != 2 {
		t.Errorf("Attempt = %d after two failures", st.Attempt)
	}
	h.until("backoff reset", 10*time.Second, 6*time.Minute, func(s Status) bool { return s.Attempt == 0 })
}

func TestForcedDisconnectReconnects(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.ScriptEvery("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	h.fc.Emit(gc.Disconnected{})
	h.until("lobby stale", time.Millisecond, time.Second, func(s Status) bool { return s.LobbyStale && !s.Ready })
	st := h.untilState(StateGCReady, 10*time.Minute)
	if st.LobbyStale {
		t.Error("lobby still stale after welcome")
	}
	if n := h.fc.CallCount("Connect"); n != 2 {
		t.Errorf("Connect calls = %d", n)
	}
	var plays int
	for _, c := range h.fc.Calls("SetPlaying") {
		if c.Args[0] == true {
			plays++
		}
	}
	if plays != 2 {
		t.Errorf("SetPlaying(true) calls = %d, want one per connection", plays)
	}
}

func TestGCSessionLossRestartsHello(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		h.fc.Script("SayHello", gc.GCWelcomed{})
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)
	hellos := h.fc.CallCount("SayHello")

	h.fc.Emit(gc.GCStatus{Old: gc.GCHaveSession, New: gc.GCNoSession})
	st := h.untilState(StateGCHello, 0)
	if !st.LobbyStale {
		t.Error("lobby not stale after GC session loss")
	}
	h.fc.Script("SayHello")
	h.fc.Script("SayHello", gc.GCStatus{Old: gc.GCNoSession, New: gc.GCHaveSession})
	h.untilState(StateGCReady, 2*time.Minute)
	if n := h.fc.CallCount("SayHello"); n < hellos+2 {
		t.Errorf("SayHello calls = %d, want at least %d", n, hellos+2)
	}
	if n := h.fc.CallCount("Connect"); n != 1 {
		t.Errorf("GC loss reconnected Steam: %d Connect calls", n)
	}
}

func TestLoggedInElsewhereCoolsDown(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.ScriptEvery("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	h.fc.Emit(gc.LoggedOff{Result: gc.EResultLoggedInElsewhere})
	st := h.untilState(StateCoolingDown, 0)
	if st.Reason != auth.ReasonElsewhere || st.LastResult != "LoggedInElsewhere" {
		t.Errorf("status = %+v", st)
	}
	if h.fc.CallCount("Disconnect") != 1 {
		t.Error("Disconnect not called")
	}
	if c := h.fc.Calls("SetPlaying"); c[len(c)-1].Args[0] != false {
		t.Error("SetPlaying(false) not sent before disconnecting")
	}
	// No logon for the cooldown, then one.
	h.clk.Advance(9 * time.Minute)
	time.Sleep(20 * time.Millisecond)
	if n := h.fc.CallCount("Connect"); n != 1 {
		t.Fatalf("reconnected during cooldown: %d Connect calls", n)
	}
	h.untilState(StateGCReady, 2*time.Minute)
	if n := h.fc.CallCount("Connect"); n != 2 {
		t.Errorf("Connect calls = %d", n)
	}
}

func TestPlayingBlockedNeverKicks(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", gc.Connected{}, gc.LoggedOn{SteamID: testSteamID}, gc.PlayingSession{Blocked: true, AppID: 570})
	})
	h.start()
	st := h.untilState(StateCoolingDown, time.Minute)
	if st.Reason != ReasonBlockedElsewhere {
		t.Errorf("reason = %q", st.Reason)
	}
	for _, c := range h.fc.Calls("SetPlaying") {
		if c.Args[0] == true {
			t.Error("SetPlaying(true) sent while another session plays")
		}
	}
	if n := h.fc.CallCount("SayHello"); n != 0 {
		t.Errorf("SayHello sent %d times", n)
	}
}

func TestRateLimitPausesGate(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", gc.Connected{}, gc.LogOnFailed{Result: gc.EResultRateLimitExceeded}, gc.Disconnected{})
	})
	h.start()
	st := h.untilState(StateCoolingDown, time.Minute)
	if st.Reason != auth.ReasonRateLimited {
		t.Errorf("reason = %q", st.Reason)
	}
	if d := h.sup.gate.Reserve("probe", h.clk.Now()); d < 4*time.Minute {
		t.Errorf("gate wait after rate limit = %s, want about 5m", d)
	}
}

// TestOwnNoSessionWhileDraining: go-dota2 reports NO_SESSION for our own
// SetPlaying(false). Until the Disconnected arrives the bot must not say
// hello on an account another session holds, nor count as ready.
func TestOwnNoSessionWhileDraining(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
		h.fc.Script("SetPlaying") // true
		h.fc.Script("SetPlaying", gc.GCStatus{Old: gc.GCHaveSession, New: gc.GCNoSession})
		h.fc.Script("Disconnect") // Disconnected comes later
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)
	hellos := h.fc.CallCount("SayHello")

	h.fc.Emit(gc.PlayingSession{Blocked: true, AppID: 570})
	h.until("draining", time.Millisecond, time.Second, func(s Status) bool { return !s.Ready })
	// A restarted hello loop would send at once and again after 10 s; stay
	// inside the 30 s disconnect timeout.
	for range 5 {
		h.clk.Advance(5 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if n := h.fc.CallCount("SayHello"); n != hellos {
		t.Errorf("SayHello sent %d times while draining", n-hellos)
	}
	if st := h.sup.Status(); st.State != StateGCReady || st.Ready {
		t.Errorf("status while draining = %s ready=%t", st.State, st.Ready)
	}

	h.fc.Emit(gc.Disconnected{})
	st := h.untilState(StateCoolingDown, time.Second)
	if st.Reason != ReasonBlockedElsewhere {
		t.Errorf("reason = %q", st.Reason)
	}
}

// TestDisconnectTimeoutStillCoversDraining: with the NO_SESSION ignored, the
// disconnect timer is what moves on when Disconnected never arrives.
func TestDisconnectTimeoutStillCoversDraining(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
		h.fc.Script("SetPlaying")
		h.fc.Script("SetPlaying", gc.GCStatus{Old: gc.GCHaveSession, New: gc.GCNoSession})
		h.fc.Script("Disconnect")
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)
	h.fc.Emit(gc.LoggedOff{Result: gc.EResultLoggedInElsewhere})
	st := h.untilState(StateCoolingDown, 2*time.Minute)
	if st.Reason != auth.ReasonElsewhere {
		t.Errorf("reason = %q", st.Reason)
	}
}

// TestPauseHoldsReservedSlots: a rate limit after a bot booked its slot
// still holds that bot (pause the other bots' logons).
func TestPauseHoldsReservedSlots(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.gate = NewGate(10 * time.Second)
		h.gate.Reserve("other", h.clk.Now()) // another bot holds the first slot
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	time.Sleep(10 * time.Millisecond)
	// The other bot is rate-limited before this bot's slot comes due.
	h.gate.Pause(h.clk.Now().Add(5 * time.Minute))
	for range 30 {
		h.clk.Advance(9 * time.Second) // 4.5 minutes
		time.Sleep(time.Millisecond)
	}
	if n := h.fc.CallCount("Connect"); n != 0 {
		t.Fatalf("logged on during the pause: %d Connect calls", n)
	}
	h.untilState(StateGCReady, 2*time.Minute)
}

// TestRetryPicksUpNewToken: an operator mints a token with `auth --bot`
// while the bot is in needs_attention, then calls retry.
func TestRetryPicksUpNewToken(t *testing.T) {
	b := config.BotConfig{Name: "bot-a", Username: "bota", Enabled: true}
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateNeedsAttention, time.Second)

	tok, err := auth.NewToken(jwt(t, "1", h.clk.Now().Add(200*24*time.Hour)), h.clk.Now(), auth.SourceAuthenticate)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(tok); err != nil {
		t.Fatal(err)
	}
	if err := h.sup.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.untilState(StateGCReady, time.Minute)
	if creds := connectCreds(t, h.fc); len(creds) != 1 || creds[0].AccessToken.Reveal() != tok.RefreshToken {
		t.Errorf("Connect creds = %+v", creds)
	}
}

// TestStaleAuthResultsDropped: a stale renewal result, or an Authenticate
// superseded by another, must not touch the store.
func TestStaleAuthResultsDropped(t *testing.T) {
	now := newFakeClock().Now()
	tok, err := auth.NewToken(jwt(t, "1", now.Add(200*24*time.Hour)), now, auth.SourceAuthenticate)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(headlessBot(), fake.New(), SupervisorOptions{Store: auth.NewStore(t.TempDir()), Clock: newFakeClock()})
	s.ctx, s.cancel = context.WithCancel(t.Context())
	defer s.cancel()

	s.renewCancel = func() {}
	s.renewGen = 5
	if _, ok := s.authFinished(authDone{gen: 4, renew: true, tok: tok}); ok {
		t.Error("stale renewal result fed to the state machine")
	}
	if _, err := s.store.Load(); !errors.Is(err, auth.ErrNoToken) {
		t.Errorf("superseded renewal saved a token: %v", err)
	}
	s.renewCancel = nil
	s.startAuth()
	if _, ok := s.authFinished(authDone{gen: s.authGen - 1, tok: tok}); ok {
		t.Error("stale Authenticate result accepted")
	}
	in, ok := s.authFinished(authDone{gen: s.authGen, tok: tok})
	if ar, isAR := in.(AuthResult); !ok || !isAR || ar.Err != nil {
		t.Errorf("current result = %#v, %t", in, ok)
	}
	if got, err := s.store.Load(); err != nil || got.RefreshToken != tok.RefreshToken {
		t.Errorf("stored token = %v, %v", got, err)
	}
}

// TestStopWithFullQueue: a wedged loop with a full input queue must not
// hold Stop past its ctx.
func TestStopWithFullQueue(t *testing.T) {
	s := NewSupervisor(headlessBot(), fake.New(), SupervisorOptions{Store: auth.NewStore(t.TempDir()), Clock: newFakeClock()})
	s.startOnce.Do(func() {
		s.ctx, s.cancel = context.WithCancel(t.Context())
		// Stands in for a run loop stuck in handle that exits on cancel.
		go func() {
			<-s.ctx.Done()
			close(s.done)
		}()
	})
	for range cap(s.inputs) {
		s.inputs <- heartbeat{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- s.Stop(ctx) }()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked on a full input queue")
	}
}

func TestGuardWithoutSharedSecretNeedsAttention(t *testing.T) {
	b := headlessBot()
	b.Credentials.SharedSecret = ""
	h := newHarness(t, b, func(h *harness) {
		h.fc.FailNext("Authenticate", &gc.AuthError{State: "email_code", Guards: []gc.GuardKind{gc.GuardEmailCode}, Err: gc.ErrSteamGuardRequired})
	})
	h.start()
	st := h.untilState(StateNeedsAttention, time.Minute)
	if st.Reason != auth.ReasonGuardRequired {
		t.Errorf("reason = %q", st.Reason)
	}
	if ac := h.fc.Calls("Authenticate")[0].Args[0].(gc.Credentials); ac.TwoFactorCode.IsSet() {
		t.Errorf("TOTP code sent without a shared secret: %q", ac.TwoFactorCode)
	}
	if h.fc.CallCount("Connect") != 0 {
		t.Error("Connect called without a token")
	}

	// An operator fixes it and retries.
	h.fc.SetToken(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h.fc.Script("Connect", logonOK()...)
	welcomeOnHello(h)
	if err := h.sup.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.untilState(StateGCReady, time.Minute)
}

func TestNoCredentials(t *testing.T) {
	h := newHarness(t, config.BotConfig{Name: "empty", Username: "empty", Enabled: true}, nil)
	h.start()
	st := h.untilState(StateNeedsAttention, time.Second)
	if st.Reason != ReasonNoCredentials {
		t.Errorf("reason = %q", st.Reason)
	}
}

func TestStopDisconnects(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.sup.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if st := h.sup.Status(); st.State != StateStopped || st.Ready {
		t.Errorf("status = %+v", st)
	}
	calls := h.fc.Calls("SetPlaying", "Disconnect")
	last := calls[len(calls)-2:]
	if last[0].Method != "SetPlaying" || last[0].Args[0] != false || last[1].Method != "Disconnect" {
		t.Errorf("shutdown calls = %+v", last)
	}
	if h.fc.Connected() {
		t.Error("still connected")
	}
	if err := h.sup.Worker().Do(ctx, "x", func(context.Context, gc.Client) error { return nil }); !errors.Is(err, ErrNotReady) {
		t.Errorf("Do after stop = %v", err)
	}
}

func TestStopBudget(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		h.fc.Script("Disconnect") // the Disconnected never comes
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := h.sup.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want deadline exceeded", err)
	}
}

// Stop must not wait out a GC request that is stuck: the running call is
// cancelled when the stop budget ends.
func TestStopCutsRunningRequest(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	started := make(chan struct{})
	cut := make(chan struct{})
	go func() {
		_ = h.sup.Worker().Do(context.Background(), "stuck", func(ctx context.Context, _ gc.Client) error {
			close(started)
			<-ctx.Done() // waiting on a reply that never comes (request_timeout is 10s)
			close(cut)
			return ctx.Err()
		})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := h.sup.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want deadline exceeded", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("Stop took %s with a stuck request", d)
	}
	select {
	case <-cut:
	case <-time.After(2 * time.Second):
		t.Error("the stuck request was not cancelled")
	}
}

func TestRenewalBeforeExpiry(t *testing.T) {
	now := newFakeClock().Now()
	old := jwt(t, "1", now.Add(10*24*time.Hour)) // inside the 14-day window
	minted := jwt(t, "1", now.Add(200*24*time.Hour))
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(old)
	h := newHarness(t, b, func(h *harness) {
		h.fc.SetToken(minted)
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)
	st := h.until("token renewed", time.Millisecond, time.Second, func(s Status) bool {
		return s.TokenExpiresAt.Equal(now.Add(200 * 24 * time.Hour).Truncate(time.Second))
	})
	if !st.Ready {
		t.Error("renewal disturbed the connection")
	}
	tok, err := h.store.Load()
	if err != nil || tok.RefreshToken != minted {
		t.Errorf("stored token = %v, %v", tok, err)
	}
	if n := h.fc.CallCount("Connect"); n != 1 {
		t.Errorf("renewal reconnected: %d Connect calls", n)
	}
}

func TestSupervisorHeartbeatKeepsIdleLoopAlive(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "76561198000000001", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)
	// Five idle minutes: only the heartbeat wakes the loop.
	for range 30 {
		h.clk.Advance(10 * time.Second)
		time.Sleep(time.Millisecond)
	}
	h.until("heartbeat", time.Second, 20*time.Second, func(Status) bool { return !h.sup.Stalled(30 * time.Second) })
}

func TestSupervisorStalled(t *testing.T) {
	clk := newFakeClock()
	s := NewSupervisor(headlessBot(), fake.New(), SupervisorOptions{Store: auth.NewStore(t.TempDir()), Clock: clk})
	if s.Stalled(time.Second) {
		t.Fatal("a supervisor that never ran is not stalled")
	}
	// Pretend the loop ran once and then wedged.
	s.lastBeat.Store(clk.Now().UnixNano())
	clk.Advance(31 * time.Second)
	tests := []struct {
		maxAge time.Duration
		want   bool
	}{
		{30 * time.Second, true},
		{time.Minute, false},
	}
	for _, tt := range tests {
		if got := s.Stalled(tt.maxAge); got != tt.want {
			t.Errorf("Stalled(%s) = %v, want %v", tt.maxAge, got, tt.want)
		}
	}
	close(s.done)
	if s.Stalled(30 * time.Second) {
		t.Error("an exited loop is not stalled")
	}
}

// renewHarness is a gc_ready bot whose token is inside the renewal window,
// with the renewal's Authenticate failing with err first. It returns once
// that first failed renewal has been made.
func renewHarness(t *testing.T, failures ...error) *harness {
	t.Helper()
	now := newFakeClock().Now()
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", now.Add(10*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.gate = NewGate(0)
		h.fc.SetToken(jwt(t, "1", now.Add(200*24*time.Hour)))
		h.fc.Script("Connect", logonOK()...)
		welcomeOnHello(h)
		for _, err := range failures {
			h.fc.FailNext("Authenticate", err)
		}
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)
	h.waitAuthCalls(1)
	return h
}

// waitAuthCalls waits in real time until Authenticate has been called n
// times, then lets the supervisor finish handling the outcome.
func (h *harness) waitAuthCalls(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.fc.CallCount("Authenticate") < n {
		if time.Now().After(deadline) {
			h.t.Fatalf("Authenticate called %d times, want %d", h.fc.CallCount("Authenticate"), n)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
}

// elapse moves the fake clock forward in steps, giving the supervisor real
// time to react after each.
func (h *harness) elapse(d, step time.Duration) {
	for done := time.Duration(0); done < d; done += step {
		h.clk.Advance(step)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
}

// A renewal that Steam refuses goes through the logon classifier: it must not
// be re-run blindly on every transition and every check.
func TestRenewalFailurePolicy(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantBlocked string
		wantPaused  bool
		// calls is the expected total Authenticate count after each
		// elapsed span.
		spans []struct {
			after time.Duration
			calls int
		}
	}{
		{
			name: "refused password stops renewal", err: deniedErr("BeginAuthSessionViaCredentials", gc.EResultInvalidPassword),
			wantBlocked: auth.ReasonBadCredential,
			spans: []struct {
				after time.Duration
				calls int
			}{{time.Hour, 1}, {24 * time.Hour, 1}, {7 * 24 * time.Hour, 1}},
		},
		{
			name: "rate limit pauses all logons and waits out the cooldown", err: deniedErr("BeginAuthSessionViaCredentials", gc.EResultRateLimitExceeded),
			wantPaused: true,
			spans: []struct {
				after time.Duration
				calls int
			}{{20 * time.Minute, 1}, {20 * time.Minute, 2}},
		},
		{
			name: "login throttle is a rate limit", err: deniedErr("BeginAuthSessionViaCredentials", gc.EResultAccountLoginDeniedThrottle),
			wantPaused: true,
			spans: []struct {
				after time.Duration
				calls int
			}{{20 * time.Minute, 1}, {20 * time.Minute, 2}},
		},
		{
			name: "totp mismatch retries in a new window", err: deniedErr("UpdateAuthSessionWithSteamGuardCode", gc.EResultTwoFactorCodeMismatch),
			spans: []struct {
				after time.Duration
				calls int
			}{{10 * time.Second, 1}, {30 * time.Second, 2}},
		},
		{
			name: "transient failure waits for the next check", err: errors.New("connection reset"),
			spans: []struct {
				after time.Duration
				calls int
			}{{time.Hour, 1}, {6 * time.Hour, 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := renewHarness(t, tt.err)
			if got := h.sup.Status().RenewBlocked; got != tt.wantBlocked {
				t.Errorf("RenewBlocked = %q, want %q", got, tt.wantBlocked)
			}
			if got := h.gate.Recheck("probe", h.clk.Now()) > 0; got != tt.wantPaused {
				t.Errorf("logons paused = %v, want %v", got, tt.wantPaused)
			}
			for _, sp := range tt.spans {
				h.elapse(sp.after, sp.after/20)
				if n := h.fc.CallCount("Authenticate"); n != sp.calls {
					t.Fatalf("after +%s: Authenticate called %d times, want %d", sp.after, n, sp.calls)
				}
			}
			if st := h.sup.Status(); st.State != StateGCReady || !st.Ready {
				t.Errorf("a failed renewal disturbed the connection: %+v", st)
			}
		})
	}
}

func TestRenewalTOTPRetriesAreLimited(t *testing.T) {
	mismatch := deniedErr("UpdateAuthSessionWithSteamGuardCode", gc.EResultTwoFactorCodeMismatch)
	h := renewHarness(t, mismatch, mismatch, mismatch)
	for n := 2; n <= 3; n++ {
		h.elapse(31*time.Second, time.Second)
		if got := h.fc.CallCount("Authenticate"); got != n {
			t.Fatalf("Authenticate called %d times, want %d", got, n)
		}
	}
	if got := h.sup.Status().RenewBlocked; got != auth.ReasonTOTPMismatch {
		t.Errorf("RenewBlocked = %q after the retries ran out", got)
	}
	h.elapse(24*time.Hour, time.Hour)
	if got := h.fc.CallCount("Authenticate"); got != 3 {
		t.Errorf("Authenticate called %d times after giving up, want 3", got)
	}
}

// An operator retry lifts the block and renewal runs again.
func TestRenewalBlockClearedByRetry(t *testing.T) {
	h := renewHarness(t, deniedErr("BeginAuthSessionViaCredentials", gc.EResultInvalidPassword))
	if h.sup.Status().RenewBlocked == "" {
		t.Fatal("renewal not blocked")
	}
	if err := h.sup.Retry(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.waitAuthCalls(2)
	st := h.until("renewal succeeded", time.Millisecond, time.Second, func(s Status) bool {
		return s.RenewBlocked == "" && s.TokenExpiresAt.After(h.clk.Now().Add(100*24*time.Hour))
	})
	if !st.Ready {
		t.Errorf("status = %+v", st)
	}
}

// With the password known to be bad, a dead token must not send it to Steam
// again through the reconnect path either.
func TestKnownBadPasswordIsNotResubmitted(t *testing.T) {
	h := renewHarness(t, deniedErr("BeginAuthSessionViaCredentials", gc.EResultInvalidPassword))
	if err := h.store.Delete(); err != nil { // the token the machine would use is gone
		t.Fatal(err)
	}
	h.fc.Emit(gc.Disconnected{Conn: h.fc.ConnSeq()})
	h.untilState(StateNeedsAttention, 10*time.Minute)
	if got := h.sup.Status().Reason; got != auth.ReasonBadCredential {
		t.Errorf("reason = %q", got)
	}
	if n := h.fc.CallCount("Authenticate"); n != 1 {
		t.Errorf("Authenticate called %d times; the refused password was sent again", n)
	}
}

// unstarted returns a supervisor whose run loop is not running, with ctx
// set, so tests can drive its run-goroutine methods directly.
func unstarted(t *testing.T) (*Supervisor, *fake.Client, *auth.Store) {
	t.Helper()
	fc := fake.New()
	store := auth.NewStore(t.TempDir())
	s := NewSupervisor(headlessBot(), fc, SupervisorOptions{Store: store, Clock: newFakeClock(), Logger: testLogger(t), Seed: 1})
	s.startOnce.Do(func() { s.ctx, s.cancel = context.WithCancel(t.Context()) })
	return s, fc, store
}

func TestRetryWithFullQueue(t *testing.T) {
	s, _, _ := unstarted(t)
	for range cap(s.inputs) {
		s.inputs <- heartbeat{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- s.Retry(ctx) }()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Retry = %v, want ErrBusy wrapping the deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Retry blocked on a full input queue")
	}

	// An exited loop is not an error.
	close(s.done)
	if err := s.Retry(t.Context()); err != nil {
		t.Errorf("Retry after exit = %v", err)
	}
}

// Background renewal runs only in gc_ready with an unexpired token; in any
// other state the logon path may authenticate itself, and two sessions in
// one TOTP window get an 88.
func TestRenewOnlyWhenReady(t *testing.T) {
	now := newFakeClock().Now()
	soon := jwt(t, "1", now.Add(10*24*time.Hour))
	expired := jwt(t, "1", now.Add(-time.Hour))
	far := jwt(t, "1", now.Add(100*24*time.Hour))
	tests := []struct {
		name  string
		state State
		token string
		want  bool
	}{
		{"gc_ready, expiring soon", StateGCReady, soon, true},
		{"gc_ready, far from expiry", StateGCReady, far, false},
		{"gc_ready, already expired", StateGCReady, expired, false},
		{"backoff", StateBackoff, soon, false},
		{"cooling_down", StateCoolingDown, soon, false},
		{"needs_attention", StateNeedsAttention, soon, false},
		{"connecting", StateConnecting, soon, false},
		{"logging_on", StateLoggingOn, soon, false},
		{"authenticating", StateAuthenticating, soon, false},
		{"stopped", StateStopped, soon, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, store := unstarted(t)
			tok, err := auth.NewToken(tt.token, now, auth.SourceSeed)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(tok); err != nil {
				t.Fatal(err)
			}
			s.snap.State = tt.state
			s.maybeRenew()
			if got := s.renewCancel != nil; got != tt.want {
				t.Errorf("renewal started = %v, want %v", got, tt.want)
			}
			if s.renewCancel != nil {
				s.renewCancel()
			}
		})
	}
}

// A renewal that succeeds leaves HasToken set so the next logon uses the
// renewed token rather than a fresh password Authenticate.
func TestRenewalSuccessSetsHasToken(t *testing.T) {
	s, _, store := unstarted(t)
	now := s.clock.Now()
	minted := jwt(t, "1", now.Add(200*24*time.Hour))
	s.snap.HasToken = false
	s.renewGen = 4
	s.renewCancel = func() {}
	tok := auth.FromMinted(minted, now)
	if in, ok := s.authFinished(authDone{gen: 4, renew: true, tok: tok}); ok || in != nil {
		t.Fatalf("authFinished = %v, %v; a renewal feeds nothing to next", in, ok)
	}
	if !s.snap.HasToken {
		t.Error("HasToken not set after a saved renewal")
	}
	if got, err := store.Load(); err != nil || got.RefreshToken != minted {
		t.Errorf("stored = %v, %v", got, err)
	}
	if got := s.Status().TokenExpiresAt; !got.Equal(tok.ExpiresAt) {
		t.Errorf("TokenExpiresAt = %v, want %v", got, tok.ExpiresAt)
	}
}

// When the logon path needs to authenticate while a renewal is in flight it
// adopts the renewal: no second Authenticate, and the renewal's outcome
// answers the logon.
func TestStartAuthAdoptsRenewal(t *testing.T) {
	denied := deniedErr("BeginAuthSessionViaCredentials", gc.EResultInvalidPassword)
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"renewal succeeds", nil, false},
		{"renewal is refused", denied, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, fc, store := unstarted(t)
			now := s.clock.Now()
			minted := jwt(t, "1", now.Add(200*24*time.Hour))
			s.renewGen = 7
			s.renewCancel = func() {}

			s.startAuth()
			if n := fc.CallCount("Authenticate"); n != 0 {
				t.Fatalf("startAuth submitted a second Authenticate (%d calls)", n)
			}
			// Stale renewal results are still dropped.
			if _, ok := s.authFinished(authDone{gen: 6, renew: true}); ok {
				t.Error("stale renewal result accepted")
			}
			in, ok := s.authFinished(authDone{gen: 7, renew: true, tok: auth.FromMinted(minted, now), err: tt.err})
			if !ok {
				t.Fatal("adopted renewal produced no AuthResult")
			}
			res, isRes := in.(AuthResult)
			if !isRes || (res.Err != nil) != tt.wantErr {
				t.Fatalf("input = %#v, wantErr %v", in, tt.wantErr)
			}
			_, lerr := store.Load()
			if (lerr == nil) == tt.wantErr {
				t.Errorf("token store after = %v, wantErr %v", lerr, tt.wantErr)
			}
			if s.adoptRenew {
				t.Error("adoptRenew left set")
			}
		})
	}
}

// Stop must not hold for the whole budget when the Disconnected never
// arrives: the disconnect timer ends the wait.
func TestStopWithoutDisconnected(t *testing.T) {
	b := headlessBot()
	b.Credentials.RefreshToken = config.Secret(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h := newHarness(t, b, func(h *harness) {
		h.fc.Script("Connect", logonOK()...)
		h.fc.Script("Disconnect") // the Disconnected never comes
		welcomeOnHello(h)
	})
	h.start()
	h.untilState(StateGCReady, time.Minute)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- h.sup.Stop(ctx) }()
	for {
		select {
		case err := <-errc:
			if err != nil {
				t.Fatalf("Stop = %v; it waited for a Disconnected that never came", err)
			}
			return
		case <-time.After(time.Millisecond):
			h.clk.Advance(DefaultDisconnectTimeout)
		}
	}
}
