package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

type fakeLeagues struct {
	all      []league.League
	perBot   map[string][]uint32
	nodesBot string
	nodes    []gc.LeagueNode
	nodesErr error
	asked    []uint32
	deadline time.Duration // time left on the ctx Nodes got; -1 without one
}

func (f *fakeLeagues) All() []league.League            { return f.all }
func (f *fakeLeagues) BotLeagues(name string) []uint32 { return f.perBot[name] }
func (f *fakeLeagues) Nodes(ctx context.Context, id uint32) (string, []gc.LeagueNode, error) {
	f.asked = append(f.asked, id)
	f.deadline = -1
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return f.nodesBot, f.nodes, f.nodesErr
}

type fakeLeases map[string]string

func (f fakeLeases) Leases() map[string]string { return f }

func TestLeaguesList(t *testing.T) {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.FixedZone("x", 3600))
	lg := &fakeLeagues{all: []league.League{
		{LeagueID: 17929, Name: "Spring Cup", Bots: []league.LeagueBot{
			{Name: "bot1", Source: league.SourceGC, Verified: true, RefreshedAt: at},
			{Name: "bot2", Source: league.SourceConfig},
		}},
		{LeagueID: 20004, Bots: []league.LeagueBot{{Name: "bot1", Source: league.SourceGC, Verified: true, RefreshedAt: at, Stale: true}}},
	}}
	s := New(Options{APIKey: testKey, Leagues: lg})
	auth := map[string]string{"X-API-Key": testKey}

	if r := do(t, s, http.MethodGet, "/v1/leagues", nil); r.code != 401 {
		t.Errorf("leagues without key = %d", r.code)
	}
	r := do(t, s, http.MethodGet, "/v1/leagues", auth)
	list, _ := r.body["leagues"].([]any)
	if r.code != 200 || len(list) != 2 {
		t.Fatalf("leagues = %d %v", r.code, r.body)
	}
	first := list[0].(map[string]any)
	if first["league_id"] != float64(17929) || first["name"] != "Spring Cup" {
		t.Errorf("first league = %v", first)
	}
	bots := first["bots"].([]any)
	b1, b2 := bots[0].(map[string]any), bots[1].(map[string]any)
	if b1["name"] != "bot1" || b1["source"] != "gc" || b1["verified"] != true || b1["refreshed_at"] != "2026-11-08T11:00:00Z" || b1["stale"] != false {
		t.Errorf("gc holder = %v", b1)
	}
	if b2["source"] != "config" || b2["verified"] != false || b2["refreshed_at"] != nil {
		t.Errorf("config holder = %v", b2)
	}
	second := list[1].(map[string]any)
	if second["name"] != nil || second["bots"].([]any)[0].(map[string]any)["stale"] != true {
		t.Errorf("second league = %v", second)
	}

	// No registry wired: an empty list, not null.
	r = do(t, New(Options{}), http.MethodGet, "/v1/leagues", nil)
	if l, ok := r.body["leagues"].([]any); r.code != 200 || !ok || len(l) != 0 {
		t.Errorf("leagues without a registry = %d %v", r.code, r.body)
	}
}

func TestLeagueNodes(t *testing.T) {
	nodes := []gc.LeagueNode{{NodeID: 3, Name: "Grand final", GroupName: "Playoffs", TeamID1: 11}}
	tests := []struct {
		name     string
		path     string
		err      error
		code     int
		errCode  string
		askedFor uint32
	}{
		{"ok", "/v1/leagues/17929/nodes", nil, 200, "", 17929},
		{"not a number", "/v1/leagues/abc/nodes", nil, 400, "invalid_request", 0},
		{"zero", "/v1/leagues/0/nodes", nil, 400, "invalid_request", 0},
		{"too big", "/v1/leagues/4294967296/nodes", nil, 400, "invalid_request", 0},
		{"unknown", "/v1/leagues/5/nodes", fmt.Errorf("x: %w", league.ErrUnknownLeague), 404, "not_found", 5},
		{"none ready", "/v1/leagues/5/nodes", fmt.Errorf("x: %w", league.ErrNoReadyHolder), 503, "unavailable", 5},
		{"bot dropped", "/v1/leagues/5/nodes", fmt.Errorf("x: %w", bot.ErrNotReady), 503, "unavailable", 5},
		{"gc timeout", "/v1/leagues/5/nodes", fmt.Errorf("x: %w", context.DeadlineExceeded), 504, "gc_timeout", 5},
		{"gc error", "/v1/leagues/5/nodes", errors.New("decode failed"), 503, "unavailable", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lg := &fakeLeagues{nodesBot: "bot1", nodes: nodes, nodesErr: tt.err}
			s := New(Options{Leagues: lg})
			r := do(t, s, http.MethodGet, tt.path, nil)
			if r.code != tt.code || errCode(r) != tt.errCode {
				t.Fatalf("%s = %d %v, want %d %s", tt.path, r.code, r.body, tt.code, tt.errCode)
			}
			if tt.askedFor == 0 && len(lg.asked) != 0 {
				t.Errorf("registry asked for %v on a bad id", lg.asked)
			}
			if tt.code != 200 {
				if tt.code >= 503 && r.body["error"].(map[string]any)["retryable"] != true {
					t.Errorf("%d not retryable", tt.code)
				}
				return
			}
			if r.body["league_id"] != float64(17929) || r.body["bot"] != "bot1" {
				t.Errorf("body = %v", r.body)
			}
			n := r.body["nodes"].([]any)[0].(map[string]any)
			want := map[string]any{"node_id": float64(3), "name": "Grand final", "group_name": "Playoffs", "team_id_1": float64(11), "team_id_2": nil}
			for k, v := range want {
				if n[k] != v {
					t.Errorf("node %s = %#v, want %#v", k, n[k], v)
				}
			}
		})
	}
}

func TestLeagueNodesDeadline(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"default", 0, bot.DefaultRequestTimeout},
		{"configured", 3 * time.Second, 3 * time.Second},
		{"capped below the write timeout", time.Minute, MaxSyncGCWait},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lg := &fakeLeagues{nodesBot: "bot1"}
			s := New(Options{Leagues: lg, GCRequestTimeout: tt.timeout})
			if r := do(t, s, http.MethodGet, "/v1/leagues/1/nodes", nil); r.code != 200 {
				t.Fatalf("code = %d", r.code)
			}
			if lg.deadline <= 0 || lg.deadline > tt.want || lg.deadline < tt.want-time.Second {
				t.Errorf("Nodes ctx had %v left, want about %v", lg.deadline, tt.want)
			}
		})
	}
}

func TestLeagueRoutesNeedKey(t *testing.T) {
	s := New(Options{APIKey: testKey, Leagues: &fakeLeagues{}})
	for _, path := range []string{"/v1/leagues", "/v1/leagues/1/nodes"} {
		if r := do(t, s, http.MethodGet, path, nil); r.code != 401 {
			t.Errorf("%s without key = %d", path, r.code)
		}
		if r := do(t, s, http.MethodPost, path, nil); r.code != 401 {
			t.Errorf("POST %s without key = %d, want 401 before 405", path, r.code)
		}
		if r := do(t, s, http.MethodPost, path, map[string]string{"X-API-Key": testKey}); r.code != 405 {
			t.Errorf("POST %s with key = %d", path, r.code)
		}
	}
}

func TestBotsLeaguesAndLobby(t *testing.T) {
	bots := &fakeBots{}
	a := ready("bot1")
	a.ReadySince = since.Add(time.Second)
	bots.set(a, backoff("bot2"))
	s := New(Options{
		Bots:    bots,
		Leagues: &fakeLeagues{perBot: map[string][]uint32{"bot1": {100, 17929}}},
		Leases:  fakeLeases{"BOT1": "lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"},
	})
	r := do(t, s, http.MethodGet, "/v1/bots", nil)
	list := r.body["bots"].([]any)
	b1, b2 := list[0].(map[string]any), list[1].(map[string]any)
	if l := b1["leagues"].([]any); len(l) != 2 || l[0] != float64(100) || l[1] != float64(17929) {
		t.Errorf("bot1 leagues = %v", b1["leagues"])
	}
	if b1["lobby"] != "lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V" || b1["ready_since"] != "2026-11-07T12:00:01Z" {
		t.Errorf("bot1 = %v", b1)
	}
	if l, ok := b2["leagues"].([]any); !ok || len(l) != 0 {
		t.Errorf("bot2 leagues = %#v, want []", b2["leagues"])
	}
	if b2["lobby"] != nil || b2["ready_since"] != nil {
		t.Errorf("bot2 = %v", b2)
	}
	r = do(t, s, http.MethodGet, "/v1/bots/bot1", nil)
	if r.body["lobby"] != "lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V" {
		t.Errorf("one bot = %v", r.body)
	}
}
