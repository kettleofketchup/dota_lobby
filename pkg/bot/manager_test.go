package bot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
)

func TestGate(t *testing.T) {
	t0 := newFakeClock().Now()
	g := NewGate(10 * time.Second)
	steps := []struct {
		name string
		at   time.Duration
		want time.Duration
	}{
		{"first is free", 0, 0},
		{"second waits a stagger", 0, 10 * time.Second},
		{"third waits two", time.Second, 19 * time.Second},
		{"later is free again", time.Minute, 0},
	}
	for i, s := range steps {
		if got := g.Reserve(fmt.Sprint(i), t0.Add(s.at)); got != s.want {
			t.Errorf("%s: wait %s, want %s", s.name, got, s.want)
		}
	}
	g.Pause(t0.Add(10 * time.Minute))
	g.Pause(t0.Add(5 * time.Minute)) // never shortens
	if got := g.Reserve("late", t0.Add(2*time.Minute)); got != 8*time.Minute {
		t.Errorf("paused wait = %s", got)
	}
}

func managerConfig(t *testing.T, names ...string) *config.Config {
	cfg := &config.Config{StateDir: t.TempDir()}
	cfg.Steam.LoginStagger = 10 * time.Second
	exp := newFakeClock().Now().Add(180 * 24 * time.Hour)
	for _, n := range names {
		b := headlessBot()
		b.Name, b.Username = n, n
		b.Credentials.RefreshToken = config.Secret(jwt(t, "1", exp))
		cfg.Bots = append(cfg.Bots, b)
	}
	return cfg
}

func TestManager(t *testing.T) {
	cfg := managerConfig(t, "bot-a", "bot-b", "bot-c")
	cfg.Bots[2].Enabled = false
	clk := newFakeClock()

	var mu sync.Mutex
	clients := map[string]*fake.Client{}
	m := NewManager(cfg, ManagerOptions{
		Logger: testLogger(t),
		Clock:  clk,
		Seed:   func(config.BotConfig) uint64 { return 1 },
		NewClient: func(b config.BotConfig) gc.Client {
			fc := fake.New()
			fc.ScriptEvery("Connect", logonOK()...)
			fc.ScriptEvery("SayHello", gc.GCWelcomed{})
			mu.Lock()
			clients[b.Name] = fc
			mu.Unlock()
			return fc
		},
	})
	if len(m.Statuses()) != 2 {
		t.Fatalf("disabled bot got a supervisor: %+v", m.Statuses())
	}
	if _, ok := m.Supervisor("BOT-B"); !ok {
		t.Error("Supervisor lookup is not case-insensitive")
	}
	if got := m.Names(); len(got) != 2 || got[0] != "bot-a" || got[1] != "bot-b" {
		t.Errorf("Names = %v, want the enabled bots in config order", got)
	}
	if h, ok := m.Bot("BOT-A"); !ok || h.Name() != "bot-a" {
		t.Errorf("Bot(BOT-A) = %v, %t", h, ok)
	}
	if _, ok := m.Bot("bot-c"); ok {
		t.Error("Bot found a disabled bot")
	}
	var _ Source = m
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The state dir is locked against a second process.
	if _, err := auth.AcquireLock(cfg.StateDir); !errors.Is(err, auth.ErrLocked) {
		t.Errorf("second lock = %v", err)
	}

	waitFor := func(desc string, advance bool, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("%s; statuses %+v", desc, m.Statuses())
			}
			if advance {
				clk.Advance(250 * time.Millisecond)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}
	// Slots are first come, first served: one bot logs on at once and the
	// other waits out the stagger, whichever goroutine asked first.
	connects := func() int {
		return clients["bot-a"].CallCount("Connect") + clients["bot-b"].CallCount("Connect")
	}
	waitFor("first bot connects", false, func() bool { return connects() >= 1 })
	time.Sleep(20 * time.Millisecond)
	if n := connects(); n != 1 {
		t.Errorf("%d bots connected inside the stagger", n)
	}
	waitFor("all ready", true, func() bool { return m.ReadyCount() == 2 })
	if clk.Now().Sub(newFakeClock().Now()) < 10*time.Second {
		t.Errorf("both bots ready %s after start, before the stagger", clk.Now().Sub(newFakeClock().Now()))
	}
	for _, st := range m.Statuses() {
		if st.State != StateGCReady {
			t.Errorf("%s: %s", st.Name, st.State)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for name, fc := range clients {
		if fc.Connected() {
			t.Errorf("%s still connected", name)
		}
	}
	// The lock is released.
	l, err := auth.AcquireLock(cfg.StateDir)
	if err != nil {
		t.Fatalf("lock after Stop: %v", err)
	}
	_ = l.Release()
	if err := m.Stop(ctx); err != nil {
		t.Errorf("second Stop = %v", err)
	}
}

func TestManagerLocked(t *testing.T) {
	cfg := managerConfig(t, "bot-a")
	l, err := auth.AcquireLock(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()
	m := NewManager(cfg, ManagerOptions{NewClient: func(config.BotConfig) gc.Client { return fake.New() }})
	if err := m.Start(context.Background()); !errors.Is(err, auth.ErrLocked) {
		t.Fatalf("Start = %v, want ErrLocked", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("Stop after failed Start = %v", err)
	}
}

// A bot holds one booking: booking again replaces the old slot, so a bot
// looping through backoff does not push the others' first logons later.
func TestGateOneBookingPerBot(t *testing.T) {
	t0 := newFakeClock().Now()
	g := NewGate(10 * time.Second)
	steps := []struct {
		name  string
		owner string
		at    time.Duration
		want  time.Duration
	}{
		{"a books the first slot", "a", 0, 0},
		{"b books the next", "b", 0, 10 * time.Second},
		{"a rebooks behind b", "a", time.Second, 19 * time.Second},
		{"a rebooks again, replacing its slot", "a", 2 * time.Second, 18 * time.Second},
		{"and again", "a", 3 * time.Second, 17 * time.Second},
		{"c lands after a, not after a's old bookings", "c", 3 * time.Second, 27 * time.Second},
	}
	for _, s := range steps {
		if got := g.Reserve(s.owner, t0.Add(s.at)); got != s.want {
			t.Errorf("%s: wait %s, want %s", s.name, got, s.want)
		}
	}
}

func TestManagerRetry(t *testing.T) {
	m := &Manager{byName: map[string]*Supervisor{}}
	if err := m.Retry(t.Context(), "nobody"); !errors.Is(err, ErrUnknownBot) {
		t.Errorf("unknown bot = %v", err)
	}
}
