package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
)

// fakeClock is a manual Clock. Advance fires due timers in deadline order
// on the caller's goroutine.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	at   time.Time
	f    func()
	done bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.done
	t.done = true
	return was
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	live := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.done:
		case !t.at.After(c.now):
			t.done = true
			due = append(due, t)
		default:
			live = append(live, t)
		}
	}
	c.timers = live
	c.mu.Unlock()
	slices.SortStableFunc(due, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
	for _, t := range due {
		t.f()
	}
}

// testLogger shows supervisor logs with -v only.
func testLogger(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("test", t.Name())
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// jwt builds an unsigned refresh-token-shaped JWT.
func jwt(t *testing.T, sub string, exp time.Time) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"sub": sub, "iat": exp.Add(-200 * 24 * time.Hour).Unix(), "exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"EdDSA","typ":"JWT"}`)) + "." + enc(b) + ".c2ln"
}

// testSharedSecret is a syntactically valid, made-up TOTP seed.
var testSharedSecret = config.Secret(base64.StdEncoding.EncodeToString([]byte("01234567890123456789")))

const testSteamID = 76561198000000001

type harness struct {
	t     *testing.T
	clk   *fakeClock
	fc    *fake.Client
	store *auth.Store
	gate  *Gate // optional, set in setup
	sup   *Supervisor
}

func headlessBot() config.BotConfig {
	return config.BotConfig{
		Name: "bot-a", Username: "bota", Enabled: true,
		Credentials: config.Credentials{Password: "hunter2", SharedSecret: testSharedSecret, DeviceName: "test"},
	}
}

func newHarness(t *testing.T, b config.BotConfig, setup func(h *harness)) *harness {
	t.Helper()
	h := &harness{t: t, clk: newFakeClock(), fc: fake.New(), store: auth.NewStore(t.TempDir())}
	h.fc.SetSteamID(testSteamID)
	if setup != nil {
		setup(h)
	}
	h.sup = NewSupervisor(b, h.fc, SupervisorOptions{
		Store:  h.store,
		Gate:   h.gate,
		Clock:  h.clk,
		Logger: testLogger(t),
		Seed:   1,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.sup.Stop(ctx)
	})
	return h
}

func (h *harness) start() { h.sup.Start(context.Background()) }

// until advances the fake clock in steps until cond holds, giving the
// supervisor goroutine real time to react between steps.
func (h *harness) until(desc string, step, limit time.Duration, cond func(Status) bool) Status {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var waited time.Duration
	for {
		// Let the supervisor drain what is queued before judging.
		for range 20 {
			if st := h.sup.Status(); cond(st) {
				return st
			}
			time.Sleep(100 * time.Microsecond)
		}
		if waited >= limit || time.Now().After(deadline) {
			h.t.Fatalf("%s: not reached after %s of fake time; status %+v", desc, waited, h.sup.Status())
		}
		h.clk.Advance(step)
		waited += step
	}
}

func (h *harness) untilState(want State, limit time.Duration) Status {
	h.t.Helper()
	step := max(250*time.Millisecond, limit/100)
	return h.until(fmt.Sprintf("state %s", want), step, limit, func(s Status) bool { return s.State == want })
}

// welcomeOnHello answers every hello with a welcome.
func welcomeOnHello(h *harness) { h.fc.ScriptEvery("SayHello", gc.GCWelcomed{}) }

func logonOK() []gc.Event {
	return []gc.Event{gc.Connected{}, gc.LoggedOn{SteamID: testSteamID}}
}

func connectCreds(t *testing.T, fc *fake.Client) []gc.Credentials {
	t.Helper()
	var out []gc.Credentials
	for _, c := range fc.Calls("Connect") {
		out = append(out, c.Args[0].(gc.Credentials))
	}
	return out
}
