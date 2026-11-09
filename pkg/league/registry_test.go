package league

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/bot/bottest"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// clock is a settable Now.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func leagueCfg(allowUnverified bool, sources ...string) config.LeagueConfig {
	if len(sources) == 0 {
		sources = []string{"gc", "config"}
	}
	return config.LeagueConfig{Sources: sources, RefreshInterval: 30 * time.Minute, AllowUnverified: allowUnverified}
}

type env struct {
	fcs  map[string]*fake.Client
	bots map[string]*bottest.Bot
	src  *bottest.Source
	clk  *clock
}

// newEnv builds bots named after cfgs, each on its own fake client.
func newEnv(cfgs ...config.BotConfig) *env {
	e := &env{fcs: map[string]*fake.Client{}, bots: map[string]*bottest.Bot{}, clk: &clock{now: t0}}
	var list []*bottest.Bot
	for _, c := range cfgs {
		fc := fake.New()
		b := bottest.NewBot(c.Name, fc)
		e.fcs[c.Name], e.bots[c.Name] = fc, b
		list = append(list, b)
	}
	e.src = bottest.NewSource(list...)
	return e
}

func botCfg(name string, leagues ...uint32) config.BotConfig {
	return config.BotConfig{Name: name, Username: name, Enabled: true, Leagues: leagues}
}

func infos(ids ...uint32) []gc.LeagueInfo {
	out := make([]gc.LeagueInfo, len(ids))
	for i, id := range ids {
		out[i] = gc.LeagueInfo{LeagueID: id}
	}
	return out
}

func (e *env) registry(st Store, cfg config.LeagueConfig, cfgs ...config.BotConfig) *Registry {
	return New(st, e.src, cfg, cfgs, Options{Now: e.clk.Now, PollInterval: time.Millisecond})
}

func TestHolds(t *testing.T) {
	ctx := context.Background()
	type check struct {
		league       uint32
		verified, ok bool
	}
	tests := []struct {
		name     string
		cfg      config.LeagueConfig
		configs  []uint32
		gc       []uint32 // nil: never fetched
		gcErr    error
		checks   []check
		holdings []uint32
	}{
		{
			name: "never fetched uses config, unverified", cfg: leagueCfg(false),
			configs: []uint32{100},
			checks:  []check{{100, false, true}, {200, false, false}, {0, true, true}},
		},
		{
			name: "fetch failed keeps config fallback", cfg: leagueCfg(false),
			configs: []uint32{100}, gcErr: errors.New("gc timeout"),
			checks: []check{{100, false, true}},
		},
		{
			name: "gc list is authoritative", cfg: leagueCfg(false),
			configs: []uint32{100}, gc: []uint32{200},
			checks:   []check{{200, true, true}, {100, false, false}},
			holdings: []uint32{200},
		},
		{
			name: "gc and config agree", cfg: leagueCfg(false),
			configs: []uint32{100}, gc: []uint32{100},
			checks:   []check{{100, true, true}},
			holdings: []uint32{100},
		},
		{
			name: "allow_unverified keeps config after gc", cfg: leagueCfg(true),
			configs: []uint32{100}, gc: []uint32{200},
			checks:   []check{{100, false, true}, {200, true, true}},
			holdings: []uint32{100, 200},
		},
		{
			name: "empty gc list holds nothing", cfg: leagueCfg(false),
			configs: []uint32{100}, gc: []uint32{},
			checks: []check{{100, false, false}, {0, true, true}},
		},
		{
			name: "config source disabled", cfg: leagueCfg(false, "gc"),
			configs: []uint32{100},
			checks:  []check{{100, false, false}},
		},
		{
			name: "gc source disabled", cfg: leagueCfg(false, "config"),
			configs: []uint32{100}, gc: []uint32{200},
			checks:   []check{{100, false, true}, {200, false, false}},
			holdings: []uint32{100},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := botCfg("bot1", tt.configs...)
			e := newEnv(bc)
			r := e.registry(nil, tt.cfg, bc)
			if tt.gc != nil || tt.gcErr != nil {
				e.bots["bot1"].SetReady(t0)
				e.fcs["bot1"].SetLeagues(infos(tt.gc...))
				if tt.gcErr != nil {
					e.fcs["bot1"].FailNext("ListAdminLeagues", tt.gcErr)
				}
				if r.useGC {
					err := r.Refresh(ctx, "bot1")
					if (err != nil) != (tt.gcErr != nil) {
						t.Fatalf("Refresh = %v", err)
					}
				}
			}
			for _, c := range tt.checks {
				v, ok := r.Holds("BOT1", c.league)
				if v != c.verified || ok != c.ok {
					t.Errorf("Holds(%d) = %t, %t; want %t, %t", c.league, v, ok, c.verified, c.ok)
				}
			}
			if tt.holdings != nil {
				if got := r.BotLeagues("bot1"); !reflect.DeepEqual(got, tt.holdings) {
					t.Errorf("BotLeagues = %v, want %v", got, tt.holdings)
				}
			}
		})
	}
}

func TestHoldersAndAll(t *testing.T) {
	ctx := context.Background()
	b1, b2, b3 := botCfg("bot1", 100), botCfg("bot2", 100, 300), botCfg("bot3")
	e := newEnv(b1, b2, b3)
	r := e.registry(nil, leagueCfg(false), b1, b2, b3)
	e.bots["bot1"].SetReady(t0)
	e.fcs["bot1"].SetLeagues([]gc.LeagueInfo{{LeagueID: 100, Name: "Spring Cup"}, {LeagueID: 200}})
	if err := r.Refresh(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}

	got := r.Holders(100)
	want := []Holding{{Bot: "bot1", Verified: true, Source: SourceGC}, {Bot: "bot2", Source: SourceConfig}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Holders(100) = %+v, want %+v", got, want)
	}
	if got := r.Holders(999); len(got) != 0 {
		t.Errorf("Holders(999) = %+v", got)
	}
	if got := r.Holders(0); len(got) != 3 {
		t.Errorf("Holders(0) = %+v, want every bot", got)
	}

	all := r.All()
	wantAll := []League{
		{LeagueID: 100, Name: "Spring Cup", Bots: []LeagueBot{
			{Name: "bot1", Source: SourceGC, Verified: true, RefreshedAt: t0, FirstSeenAt: t0},
			{Name: "bot2", Source: SourceConfig},
		}},
		{LeagueID: 200, Bots: []LeagueBot{{Name: "bot1", Source: SourceGC, Verified: true, RefreshedAt: t0, FirstSeenAt: t0}}},
		{LeagueID: 300, Bots: []LeagueBot{{Name: "bot2", Source: SourceConfig}}},
	}
	if !reflect.DeepEqual(all, wantAll) {
		t.Errorf("All =\n%+v\nwant\n%+v", all, wantAll)
	}
}

func TestRefreshErrorKeepsPreviousList(t *testing.T) {
	ctx := context.Background()
	bc := botCfg("bot1", 100)
	e := newEnv(bc)
	r := e.registry(nil, leagueCfg(false), bc)
	e.bots["bot1"].SetReady(t0)
	e.fcs["bot1"].SetLeagues(infos(200))
	if err := r.Refresh(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	e.fcs["bot1"].FailNext("ListAdminLeagues", errors.New("gc timeout"))
	if err := r.Refresh(ctx, "bot1"); err == nil {
		t.Fatal("failed refresh returned nil")
	}
	// The earlier GC list still counts; the config fallback does not come
	// back because the GC list is known.
	if v, ok := r.Holds("bot1", 200); !v || !ok {
		t.Errorf("Holds(200) after a failed refresh = %t, %t", v, ok)
	}
	if _, ok := r.Holds("bot1", 100); ok {
		t.Error("config fallback used after a successful GC fetch")
	}
	if err := r.Refresh(ctx, "nobody"); !errors.Is(err, bot.ErrUnknownBot) {
		t.Errorf("Refresh(nobody) = %v", err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPersistedListsReload(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := botCfg("bot1", 100)

	e := newEnv(bc)
	r := e.registry(st.Leagues(), leagueCfg(false), bc)
	e.bots["bot1"].SetReady(t0)
	e.fcs["bot1"].SetLeagues(infos(200))
	if err := r.Refresh(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}

	// A new process: the bot is not ready yet, but the stored GC list
	// already decides, marked stale.
	e2 := newEnv(bc)
	r2 := e2.registry(st.Leagues(), leagueCfg(false), bc)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if v, ok := r2.Holds("bot1", 200); !v || !ok {
		t.Errorf("Holds(200) from the store = %t, %t", v, ok)
	}
	if _, ok := r2.Holds("bot1", 100); ok {
		t.Error("config fallback used although a stored GC list exists")
	}
	all := r2.All()
	if len(all) != 1 || !all[0].Bots[0].Stale || !all[0].Bots[0].RefreshedAt.Equal(t0) {
		t.Errorf("All after reload = %+v", all)
	}

	// A refresh replaces the stored list and clears stale.
	e2.bots["bot1"].SetReady(t0)
	e2.fcs["bot1"].SetLeagues(infos(300))
	if err := r2.Refresh(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	if all := r2.All(); len(all) != 1 || all[0].LeagueID != 300 || all[0].Bots[0].Stale {
		t.Errorf("All after refresh = %+v", all)
	}
	sets, err := st.Leagues().BotLeagues(ctx)
	if err != nil || !reflect.DeepEqual(sets["bot1"].Sources["gc"].IDs, []uint32{300}) {
		t.Errorf("stored = %+v, %v", sets, err)
	}

	// A refresh error is stored too, and the list kept.
	e2.fcs["bot1"].FailNext("ListAdminLeagues", errors.New("boom"))
	_ = r2.Refresh(ctx, "bot1")
	sets, _ = st.Leagues().BotLeagues(ctx)
	if sets["bot1"].Error == "" || len(sets["bot1"].Sources["gc"].IDs) != 1 {
		t.Errorf("stored after error = %+v", sets["bot1"])
	}
}

// waitCalls waits until fc has seen n ListAdminLeagues calls.
func waitCalls(t *testing.T, fc *fake.Client, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fc.WaitForCalls(ctx, "ListAdminLeagues", n); err != nil {
		t.Fatalf("waiting for %d ListAdminLeagues calls: %v (have %d)", n, err, fc.CallCount("ListAdminLeagues"))
	}
}

// waitAttempted waits until Run has finished a refresh of bot for the GC
// session that became ready at session. The next attempt is scheduled from
// the end of a refresh, so the test must not move the clock before then.
func waitAttempted(t *testing.T, r *Registry, name string, session time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		bs := r.state[key(name)]
		done := bs != nil && bs.seenReadySince.Equal(session)
		r.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no refresh of %s finished for the session from %s", name, session)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunRefreshesOnReadyAndInterval(t *testing.T) {
	bc := botCfg("bot1")
	e := newEnv(bc)
	r := e.registry(nil, leagueCfg(false), bc)
	fc := e.fcs["bot1"]
	fc.SetLeagues(infos(100))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Not ready: nothing is fetched.
	time.Sleep(20 * time.Millisecond)
	if n := fc.CallCount("ListAdminLeagues"); n != 0 {
		t.Fatalf("fetched %d times while not ready", n)
	}

	e.bots["bot1"].SetReady(t0)
	waitCalls(t, fc, 1)
	waitAttempted(t, r, "bot1", t0)
	// Still the same session and inside the interval: no more fetches.
	time.Sleep(20 * time.Millisecond)
	if n := fc.CallCount("ListAdminLeagues"); n != 1 {
		t.Fatalf("fetched %d times in one session", n)
	}

	// A new session (ReadySince moved) fetches again at once.
	e.clk.Advance(time.Minute)
	e.bots["bot1"].SetReady(t0.Add(time.Minute))
	waitCalls(t, fc, 2)
	waitAttempted(t, r, "bot1", t0.Add(time.Minute))

	// The refresh interval passing fetches again.
	e.clk.Advance(31 * time.Minute)
	waitCalls(t, fc, 3)
	if v, ok := r.Holds("bot1", 100); !v || !ok {
		t.Errorf("Holds after Run = %t, %t", v, ok)
	}
}

func TestRunRetriesAfterFailure(t *testing.T) {
	bc := botCfg("bot1")
	e := newEnv(bc)
	r := New(nil, e.src, leagueCfg(false), []config.BotConfig{bc},
		Options{Now: e.clk.Now, PollInterval: time.Millisecond, RetryInterval: time.Minute})
	fc := e.fcs["bot1"]
	fc.FailNext("ListAdminLeagues", errors.New("gc timeout"))
	fc.SetLeagues(infos(100))
	e.bots["bot1"].SetReady(t0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()

	waitCalls(t, fc, 1)
	waitAttempted(t, r, "bot1", t0)
	time.Sleep(20 * time.Millisecond)
	if n := fc.CallCount("ListAdminLeagues"); n != 1 {
		t.Fatalf("retried %d times before the retry interval", n-1)
	}
	e.clk.Advance(time.Minute)
	waitCalls(t, fc, 2)
}

func TestRunWithoutGCSourceNeverFetches(t *testing.T) {
	bc := botCfg("bot1", 100)
	e := newEnv(bc)
	r := e.registry(nil, leagueCfg(false, "config"), bc)
	e.bots["bot1"].SetReady(t0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if n := e.fcs["bot1"].CallCount("ListAdminLeagues"); n != 0 {
		t.Errorf("fetched %d times without the gc source", n)
	}
}

func TestNodes(t *testing.T) {
	ctx := context.Background()
	b1, b2 := botCfg("bot1", 100), botCfg("bot2")
	nodes := []gc.LeagueNode{{NodeID: 7, Name: "Final"}}
	tests := []struct {
		name    string
		setup   func(e *env, r *Registry)
		league  uint32
		wantBot string
		wantErr error
	}{
		{"unknown league", nil, 999, "", ErrUnknownLeague},
		{"league zero", nil, 0, "", ErrUnknownLeague},
		{"holder not ready", nil, 100, "", ErrNoReadyHolder},
		{"unverified holder answers", func(e *env, _ *Registry) { e.bots["bot1"].SetReady(t0) }, 100, "bot1", nil},
		{"verified holder preferred", func(e *env, r *Registry) {
			e.bots["bot1"].SetReady(t0)
			e.bots["bot2"].SetReady(t0)
			e.fcs["bot2"].SetLeagues(infos(100))
			if err := r.Refresh(ctx, "bot2"); err != nil {
				panic(err)
			}
		}, 100, "bot2", nil},
		{"next holder answers when the first drops", func(e *env, r *Registry) {
			bothHold(e, r)
			e.fcs["bot2"].FailNext("LeagueNodes", gc.ErrNotReady)
		}, 100, "bot1", nil},
		{"next holder answers when the first fails", func(e *env, r *Registry) {
			bothHold(e, r)
			e.fcs["bot2"].FailNext("LeagueNodes", errors.New("gc said no"))
		}, 100, "bot1", nil},
		{"every holder dropped", func(e *env, r *Registry) {
			bothHold(e, r)
			e.fcs["bot1"].FailNext("LeagueNodes", gc.ErrNotConnected)
			e.fcs["bot2"].FailNext("LeagueNodes", gc.ErrNotReady)
		}, 100, "bot1", ErrNoReadyHolder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(b1, b2)
			r := e.registry(nil, leagueCfg(false), b1, b2)
			for _, fc := range e.fcs {
				fc.SetLeagueNodes(100, nodes)
			}
			if tt.setup != nil {
				tt.setup(e, r)
			}
			gotBot, got, err := r.Nodes(ctx, tt.league)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("Nodes = %v, want %v", err, tt.wantErr)
			}
			if gotBot != tt.wantBot {
				t.Errorf("bot = %q, want %q", gotBot, tt.wantBot)
			}
			if tt.wantErr == nil && !reflect.DeepEqual(got, nodes) {
				t.Errorf("nodes = %+v", got)
			}
		})
	}
}

// bothHold makes bot1 (config, unverified) and bot2 (GC, verified) ready
// holders of league 100, so bot2 is tried first.
func bothHold(e *env, r *Registry) {
	e.bots["bot1"].SetReady(t0)
	e.bots["bot2"].SetReady(t0)
	e.fcs["bot2"].SetLeagues(infos(100))
	if err := r.Refresh(context.Background(), "bot2"); err != nil {
		panic(err)
	}
}

func TestNodesTimeout(t *testing.T) {
	b1, b2 := botCfg("bot1", 100), botCfg("bot2", 100)
	e := newEnv(b1, b2)
	r := e.registry(nil, leagueCfg(false), b1, b2)
	e.bots["bot1"].SetReady(t0)
	e.bots["bot2"].SetReady(t0)
	e.fcs["bot1"].BlockUntilDone("LeagueNodes", true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := r.Nodes(ctx, 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Nodes on a stuck GC = %v, want DeadlineExceeded", err)
	}
	if n := e.fcs["bot2"].CallCount("LeagueNodes"); n != 0 {
		t.Errorf("tried the next holder %d times after a timeout", n)
	}
}
