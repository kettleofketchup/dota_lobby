package league

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// syncBuffer is a log sink safe for the registry's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestInventoryFirstSeenAndChanges(t *testing.T) {
	ctx := context.Background()
	bc := botCfg("bot1")
	e := newEnv(bc)
	var logs syncBuffer
	r := New(nil, e.src, leagueCfg(false), []config.BotConfig{bc},
		Options{Now: e.clk.Now, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	e.bots["bot1"].SetReady(t0)

	steps := []struct {
		name       string
		advance    time.Duration
		ids        []uint32
		wantFirst  map[uint32]time.Time
		wantChange []Change // newest first, all of them
		wantLog    bool
	}{
		{"first list", 0, []uint32{100, 200}, map[uint32]time.Time{100: t0, 200: t0},
			[]Change{{200, store.ChangeAdded, t0}, {100, store.ChangeAdded, t0}}, true},
		{"unchanged keeps first seen", time.Hour, []uint32{100, 200}, map[uint32]time.Time{100: t0, 200: t0},
			[]Change{{200, store.ChangeAdded, t0}, {100, store.ChangeAdded, t0}}, false},
		{"one removed, one added", time.Hour, []uint32{200, 300}, map[uint32]time.Time{200: t0, 300: t0.Add(2 * time.Hour)},
			[]Change{
				{300, store.ChangeAdded, t0.Add(2 * time.Hour)}, {100, store.ChangeRemoved, t0.Add(2 * time.Hour)},
				{200, store.ChangeAdded, t0}, {100, store.ChangeAdded, t0},
			}, true},
	}
	for _, st := range steps {
		e.clk.Advance(st.advance)
		before := strings.Count(logs.String(), "bot league tickets changed")
		e.fcs["bot1"].SetLeagues(infos(st.ids...))
		if err := r.Refresh(ctx, "bot1"); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		inv, ok := r.Inventory("BOT1")
		if !ok || inv.Bot != "bot1" || !inv.OKAt.Equal(e.clk.Now()) {
			t.Fatalf("%s: Inventory = %+v, %t", st.name, inv, ok)
		}
		first := map[uint32]time.Time{}
		for _, bl := range inv.Leagues {
			first[bl.LeagueID] = bl.FirstSeenAt
			if bl.Source != SourceGC || !bl.Verified || !bl.RefreshedAt.Equal(e.clk.Now()) {
				t.Errorf("%s: league %+v", st.name, bl)
			}
		}
		if !reflect.DeepEqual(first, st.wantFirst) {
			t.Errorf("%s: first seen = %v, want %v", st.name, first, st.wantFirst)
		}
		if !reflect.DeepEqual(inv.Changes, st.wantChange) {
			t.Errorf("%s: changes = %+v, want %+v", st.name, inv.Changes, st.wantChange)
		}
		logged := strings.Count(logs.String(), "bot league tickets changed") > before
		if logged != st.wantLog {
			t.Errorf("%s: change logged = %t, want %t", st.name, logged, st.wantLog)
		}
	}
	if !strings.Contains(logs.String(), "level=INFO msg=\"bot league tickets changed\" bot=bot1 added=[300] removed=[100]") {
		t.Errorf("change log line missing:\n%s", logs.String())
	}
	if _, ok := r.Inventory("nobody"); ok {
		t.Error("Inventory(nobody) found a bot")
	}
}

func TestInventoryRefreshError(t *testing.T) {
	ctx := context.Background()
	bc := botCfg("bot1", 100)
	e := newEnv(bc)
	r := e.registry(nil, leagueCfg(false), bc)

	// Never fetched: the configured league, unverified, and no status.
	inv, _ := r.Inventory("bot1")
	want := []BotLeague{{LeagueID: 100, LeagueBot: LeagueBot{Name: "bot1", Source: SourceConfig}}}
	if !reflect.DeepEqual(inv.Leagues, want) || !inv.OKAt.IsZero() || inv.Error != "" {
		t.Errorf("before a refresh = %+v", inv)
	}

	e.bots["bot1"].SetReady(t0)
	e.fcs["bot1"].FailNext("ListAdminLeagues", errors.New("gc timeout"))
	if err := r.Refresh(ctx, "bot1"); err == nil {
		t.Fatal("failed refresh returned nil")
	}
	inv, _ = r.Inventory("bot1")
	if !strings.Contains(inv.Error, "gc timeout") || !inv.ErrorAt.Equal(t0) || !inv.OKAt.IsZero() {
		t.Errorf("after a failure = %+v", inv)
	}
}

func TestRefreshRefusals(t *testing.T) {
	ctx := context.Background()
	bc := botCfg("bot1", 100)
	tests := []struct {
		name  string
		cfg   config.LeagueConfig
		ready bool
		bot   string
		want  error
	}{
		{"unknown bot", leagueCfg(false), true, "nobody", bot.ErrUnknownBot},
		{"not ready", leagueCfg(false), false, "bot1", bot.ErrNotReady},
		{"gc source disabled", leagueCfg(false, "config"), true, "bot1", ErrGCSourceDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(bc)
			r := e.registry(nil, tt.cfg, bc)
			if tt.ready {
				e.bots["bot1"].SetReady(t0)
			}
			if err := r.Refresh(ctx, tt.bot); !errors.Is(err, tt.want) {
				t.Fatalf("Refresh = %v, want %v", err, tt.want)
			}
			if n := e.fcs["bot1"].CallCount("ListAdminLeagues"); n != 0 {
				t.Errorf("asked the GC %d times", n)
			}
			// A refusal is not a refresh failure.
			if inv, _ := r.Inventory("bot1"); inv.Error != "" {
				t.Errorf("refusal recorded as an error: %+v", inv)
			}
		})
	}
}

func TestRefreshAll(t *testing.T) {
	ctx := context.Background()
	b1, b2, b3 := botCfg("bot1"), botCfg("bot2"), botCfg("bot3")
	e := newEnv(b1, b2, b3)
	r := e.registry(nil, leagueCfg(false), b1, b2, b3)
	e.bots["bot1"].SetReady(t0)
	e.bots["bot2"].SetReady(t0)
	e.fcs["bot1"].SetLeagues(infos(100))
	e.fcs["bot2"].FailNext("ListAdminLeagues", errors.New("gc said no"))

	got, err := r.RefreshAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Bot != "bot1" || got[0].Err != nil || got[0].NotReady ||
		got[1].Bot != "bot2" || got[1].Err == nil ||
		got[2].Bot != "bot3" || !got[2].NotReady || got[2].Err != nil {
		t.Fatalf("RefreshAll = %+v", got)
	}
	if v, ok := r.Holds("bot1", 100); !v || !ok {
		t.Error("bot1's list was not refreshed")
	}
	if n := e.fcs["bot3"].CallCount("ListAdminLeagues"); n != 0 {
		t.Errorf("asked a bot that is not ready %d times", n)
	}

	r = e.registry(nil, leagueCfg(false, "config"), b1, b2, b3)
	if _, err := r.RefreshAll(ctx); !errors.Is(err, ErrGCSourceDisabled) {
		t.Errorf("RefreshAll without the gc source = %v", err)
	}
}

func TestLeague(t *testing.T) {
	ctx := context.Background()
	b1, b2 := botCfg("bot1", 100), botCfg("bot2")
	e := newEnv(b1, b2)
	r := e.registry(nil, leagueCfg(false), b1, b2)
	e.bots["bot2"].SetReady(t0)
	e.fcs["bot2"].SetLeagues(infos(100, 200))
	if err := r.Refresh(ctx, "bot2"); err != nil {
		t.Fatal(err)
	}
	l, ok := r.League(100)
	want := League{LeagueID: 100, Bots: []LeagueBot{
		{Name: "bot1", Source: SourceConfig},
		{Name: "bot2", Source: SourceGC, Verified: true, RefreshedAt: t0, FirstSeenAt: t0},
	}}
	if !ok || !reflect.DeepEqual(l, want) {
		t.Errorf("League(100) = %+v, %t", l, ok)
	}
	if _, ok := r.League(999); ok {
		t.Error("League(999) found")
	}
}

func TestPersistedChangesReload(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := botCfg("bot1")
	e := newEnv(bc)
	r := e.registry(st.Leagues(), leagueCfg(false), bc)
	e.bots["bot1"].SetReady(t0)
	e.fcs["bot1"].SetLeagues(infos(100))
	if err := r.Refresh(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Hour)
	e.fcs["bot1"].SetLeagues(infos(100, 200))
	if err := r.Refresh(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}

	r2 := newEnv(bc).registry(st.Leagues(), leagueCfg(false), bc)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	inv, _ := r2.Inventory("bot1")
	wantChanges := []Change{{200, store.ChangeAdded, t0.Add(time.Hour)}, {100, store.ChangeAdded, t0}}
	if !reflect.DeepEqual(inv.Changes, wantChanges) {
		t.Errorf("changes after reload = %+v", inv.Changes)
	}
	if len(inv.Leagues) != 2 || !inv.Leagues[0].FirstSeenAt.Equal(t0) || !inv.Leagues[1].FirstSeenAt.Equal(t0.Add(time.Hour)) ||
		!inv.Leagues[0].Stale {
		t.Errorf("leagues after reload = %+v", inv.Leagues)
	}
}
