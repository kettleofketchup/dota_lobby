package pool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/bot/bottest"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// holders is a static league -> holdings table.
type holders map[uint32][]league.Holding

func (h holders) Holders(l uint32) []league.Holding { return h[l] }

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) tick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Second)
}

func newBots(names ...string) (map[string]*bottest.Bot, *bottest.Source) {
	m := map[string]*bottest.Bot{}
	var list []*bottest.Bot
	for _, n := range names {
		b := bottest.NewBot(n, fake.New())
		b.SetReady(t0)
		m[n] = b
		list = append(list, b)
	}
	return m, bottest.NewSource(list...)
}

func TestReserve(t *testing.T) {
	ctx := context.Background()
	v := func(name string) league.Holding {
		return league.Holding{Bot: name, Verified: true, Source: league.SourceGC}
	}
	u := func(name string) league.Holding { return league.Holding{Bot: name, Source: league.SourceConfig} }
	tests := []struct {
		name    string
		holders []league.Holding
		setup   func(bots map[string]*bottest.Bot, p *Pool, clk *clock)
		want    string
		wantErr error
	}{
		{name: "no holder", wantErr: ErrNoHolder},
		{name: "holder not ready", holders: []league.Holding{v("a")},
			setup: func(b map[string]*bottest.Bot, _ *Pool, _ *clock) { b["a"].SetReady(time.Time{}) }, wantErr: ErrNoneFree},
		{name: "holder lobby stale", holders: []league.Holding{v("a")},
			setup: func(b map[string]*bottest.Bot, _ *Pool, _ *clock) { b["a"].SetLobbyStale(true) }, wantErr: ErrNoneFree},
		{name: "holder leased", holders: []league.Holding{v("a")},
			setup: func(_ map[string]*bottest.Bot, p *Pool, _ *clock) {
				if _, err := p.Restore("a", "lob_other"); err != nil {
					panic(err)
				}
			}, wantErr: ErrNoneFree},
		{name: "holder unknown to the bot source", holders: []league.Holding{v("ghost")}, wantErr: ErrNoneFree},
		{name: "verified preferred over unverified", holders: []league.Holding{u("a"), v("b")}, want: "b"},
		{name: "unverified when nothing else", holders: []league.Holding{u("a")}, want: "a"},
		{name: "never used before recently released", holders: []league.Holding{v("a"), v("b")},
			setup: func(_ map[string]*bottest.Bot, p *Pool, _ *clock) {
				ls, err := p.Restore("a", "lob_old")
				if err != nil {
					panic(err)
				}
				p.Release(ls)
			}, want: "b"},
		{name: "least recently released", holders: []league.Holding{v("a"), v("b"), v("c")},
			setup: func(_ map[string]*bottest.Bot, p *Pool, clk *clock) {
				for _, n := range []string{"c", "a", "b"} {
					ls, err := p.Restore(n, "lob_"+n)
					if err != nil {
						panic(err)
					}
					clk.tick()
					p.Release(ls)
				}
			}, want: "c"},
		{name: "name breaks ties", holders: []league.Holding{v("b"), v("a")}, want: "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bots, src := newBots("a", "b", "c")
			clk := &clock{now: t0}
			p := New(src, holders{17929: tt.holders}, Options{Now: clk.Now})
			if tt.setup != nil {
				tt.setup(bots, p, clk)
			}
			ls, err := p.Reserve(ctx, 17929, "lob_new")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Reserve = %v, %v; want %v", ls, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ls.Bot != tt.want || ls.Owner != "lob_new" || ls.LeagueID != 17929 {
				t.Errorf("lease = %+v, want bot %s", ls, tt.want)
			}
			if p.Leases()[tt.want] != "lob_new" {
				t.Errorf("Leases = %v", p.Leases())
			}
		})
	}
}

func TestReserveSameOwnerTwice(t *testing.T) {
	_, src := newBots("a", "b")
	p := New(src, holders{0: {{Bot: "a", Verified: true}, {Bot: "b", Verified: true}}}, Options{})
	first, err := p.Reserve(context.Background(), 0, "lob_1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.Reserve(context.Background(), 0, "lob_1")
	if err != nil || again != first {
		t.Errorf("second Reserve = %+v, %v; want the first lease", again, err)
	}
	if len(p.Leases()) != 1 {
		t.Errorf("Leases = %v", p.Leases())
	}
}

func TestReserveCancelledCtx(t *testing.T) {
	_, src := newBots("a")
	p := New(src, holders{0: {{Bot: "a", Verified: true}}}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Reserve(ctx, 0, "lob_1"); !errors.Is(err, context.Canceled) {
		t.Errorf("Reserve on a cancelled ctx = %v", err)
	}
}

func TestReleaseAndRestore(t *testing.T) {
	_, src := newBots("a")
	p := New(src, holders{0: {{Bot: "a", Verified: true}}}, Options{})
	ctx := context.Background()

	ls, err := p.Reserve(ctx, 0, "lob_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Restore("A", "lob_2"); !errors.Is(err, ErrLeased) {
		t.Errorf("Restore over another owner = %v, want ErrLeased", err)
	}
	if got, err := p.Restore("a", "lob_1"); err != nil || got != ls {
		t.Errorf("Restore by the same owner = %+v, %v", got, err)
	}
	p.Release(ls)
	p.Release(ls) // twice is harmless
	p.Release(nil)
	if len(p.Leases()) != 0 {
		t.Fatalf("Leases after release = %v", p.Leases())
	}

	// A stale lease must not release its successor.
	next, err := p.Reserve(ctx, 0, "lob_3")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(ls)
	if p.Leases()["a"] != "lob_3" {
		t.Errorf("old lease released the new one: %v", p.Leases())
	}
	p.Release(next)

	if _, err := p.Restore("nobody", "lob_4"); !errors.Is(err, bot.ErrUnknownBot) {
		t.Errorf("Restore unknown bot = %v", err)
	}
	// Restore does not need a ready bot.
	_, src2 := newBots("x")
	b, _ := src2.Bot("x")
	b.(*bottest.Bot).SetReady(time.Time{})
	p2 := New(src2, holders{}, Options{})
	if ls, err := p2.Restore("x", "lob_5"); err != nil || ls.Bot != "x" || ls.Verified {
		t.Errorf("Restore not-ready bot = %+v, %v", ls, err)
	}
}

// Concurrent reservers never share a bot; run with -race.
func TestReserveConcurrent(t *testing.T) {
	names := make([]string, 8)
	var hs []league.Holding
	for i := range names {
		names[i] = fmt.Sprintf("bot%d", i)
		hs = append(hs, league.Holding{Bot: names[i], Verified: true})
	}
	_, src := newBots(names...)
	p := New(src, holders{5: hs}, Options{})

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		got    = map[string]string{}
		none   int
		leases []*Lease
	)
	for i := range 32 {
		wg.Go(func() {
			ls, err := p.Reserve(context.Background(), 5, fmt.Sprintf("lob_%d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrNoneFree):
				none++
			case err != nil:
				t.Error(err)
			default:
				if prev, dup := got[ls.Bot]; dup {
					t.Errorf("%s leased to %s and %s", ls.Bot, prev, ls.Owner)
				}
				got[ls.Bot] = ls.Owner
				leases = append(leases, ls)
			}
		})
	}
	wg.Wait()
	if len(got) != 8 || none != 24 {
		t.Errorf("leased %d bots, %d turned away; want 8 and 24", len(got), none)
	}
	for _, ls := range leases {
		wg.Go(func() { p.Release(ls) })
	}
	wg.Wait()
	if len(p.Leases()) != 0 {
		t.Errorf("leases left: %v", p.Leases())
	}
	for bot, owner := range got {
		if !strings.HasPrefix(owner, "lob_") || !strings.HasPrefix(bot, "bot") {
			t.Errorf("odd lease %s -> %s", bot, owner)
		}
	}
}
