package pool

import (
	"context"
	"errors"
	"testing"

	"github.com/kettleofketchup/dota_lobby/pkg/bot/bottest"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

// ticketEnv is a pool over a real league registry whose bots answer the
// admin league list from fake GC clients, as organisation admins grant
// and revoke tickets by hand.
type ticketEnv struct {
	fcs  map[string]*fake.Client
	reg  *league.Registry
	pool *Pool
}

func newTicketEnv(t *testing.T, names ...string) *ticketEnv {
	t.Helper()
	e := &ticketEnv{fcs: map[string]*fake.Client{}}
	var list []*bottest.Bot
	var cfgs []config.BotConfig
	for _, n := range names {
		fc := fake.New()
		b := bottest.NewBot(n, fc)
		b.SetReady(t0)
		e.fcs[n] = fc
		list = append(list, b)
		cfgs = append(cfgs, config.BotConfig{Name: n, Username: n, Enabled: true})
	}
	src := bottest.NewSource(list...)
	lc := config.LeagueConfig{Sources: []string{league.SourceGC, league.SourceConfig}}
	e.reg = league.New(nil, src, lc, cfgs, league.Options{})
	e.pool = New(src, e.reg, Options{})
	return e
}

// grant sets bot's GC admin list to ids and refreshes it.
func (e *ticketEnv) grant(t *testing.T, bot string, ids ...uint32) {
	t.Helper()
	infos := make([]gc.LeagueInfo, len(ids))
	for i, id := range ids {
		infos[i] = gc.LeagueInfo{LeagueID: id}
	}
	e.fcs[bot].SetLeagues(infos)
	if err := e.reg.Refresh(context.Background(), bot); err != nil {
		t.Fatal(err)
	}
}

func TestBotWithoutTicketNeverChosen(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		grants  map[string][]uint32
		wantErr error
	}{
		{"nobody holds it", map[string][]uint32{"a": {200}, "b": {300}}, ErrNoHolder},
		{"only the holder", map[string][]uint32{"a": {100}, "b": {200}, "c": nil}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTicketEnv(t, "a", "b", "c")
			for bot, ids := range tt.grants {
				e.grant(t, bot, ids...)
			}
			ls, err := e.pool.Reserve(ctx, 100, "lob_1")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Reserve = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if ls.Bot != "a" || !ls.Verified {
				t.Errorf("lease = %+v, want a, verified", ls)
			}
			// With the holder busy, the free bots without the ticket are
			// still not chosen.
			if _, err := e.pool.Reserve(ctx, 100, "lob_2"); !errors.Is(err, ErrNoneFree) {
				t.Errorf("second Reserve = %v, want ErrNoneFree", err)
			}
		})
	}
}

func TestRefreshedTicketMakesBotEligible(t *testing.T) {
	ctx := context.Background()
	e := newTicketEnv(t, "a")
	e.grant(t, "a")
	if _, err := e.pool.Reserve(ctx, 100, "lob_1"); !errors.Is(err, ErrNoHolder) {
		t.Fatalf("Reserve before the ticket = %v, want ErrNoHolder", err)
	}
	e.grant(t, "a", 100)
	ls, err := e.pool.Reserve(ctx, 100, "lob_1")
	if err != nil || ls.Bot != "a" || !ls.Verified {
		t.Fatalf("Reserve right after the refresh = %+v, %v", ls, err)
	}
}

func TestRemovedTicketKeepsRunningLobby(t *testing.T) {
	ctx := context.Background()
	e := newTicketEnv(t, "a")
	e.grant(t, "a", 100)
	ls, err := e.pool.Reserve(ctx, 100, "lob_running")
	if err != nil {
		t.Fatal(err)
	}

	e.grant(t, "a") // the admin took the ticket away
	if got := e.pool.Leases(); got["a"] != "lob_running" {
		t.Errorf("leases after the removal = %v, want a still hosting lob_running", got)
	}
	// Reserving again for the same lobby returns its lease untouched.
	if again, err := e.pool.Reserve(ctx, 100, "lob_running"); err != nil || again != ls {
		t.Errorf("Reserve for the running lobby = %+v, %v", again, err)
	}
	// A new lobby cannot have the bot, even once it is free.
	e.pool.Release(ls)
	if _, err := e.pool.Reserve(ctx, 100, "lob_new"); !errors.Is(err, ErrNoHolder) {
		t.Errorf("Reserve after the removal = %v, want ErrNoHolder", err)
	}
	// It still takes lobbies without a league.
	if ls, err := e.pool.Reserve(ctx, 0, "lob_open"); err != nil || ls.Bot != "a" {
		t.Errorf("Reserve(0) = %+v, %v", ls, err)
	}
}
