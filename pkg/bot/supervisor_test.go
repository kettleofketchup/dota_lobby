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
	if ac.Username != "bota" || ac.Password.Reveal() != "hunter2" || len(ac.TwoFactorCode) != 5 || ac.TwoFactorCode != want {
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
	if d := h.sup.gate.Reserve(h.clk.Now()); d < 4*time.Minute {
		t.Errorf("gate wait after rate limit = %s, want about 5m", d)
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
	if ac := h.fc.Calls("Authenticate")[0].Args[0].(gc.Credentials); ac.TwoFactorCode != "" {
		t.Errorf("TOTP code sent without a shared secret: %q", ac.TwoFactorCode)
	}
	if h.fc.CallCount("Connect") != 0 {
		t.Error("Connect called without a token")
	}

	// An operator fixes it and retries.
	h.fc.SetToken(jwt(t, "1", newFakeClock().Now().Add(180*24*time.Hour)))
	h.fc.Script("Connect", logonOK()...)
	welcomeOnHello(h)
	h.sup.Retry()
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
