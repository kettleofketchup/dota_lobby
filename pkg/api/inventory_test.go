package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

var (
	seenAt    = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	refreshAt = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
)

// spring is league 100 held four ways: bot1 verified and hosting a
// lobby, bot2 from config and free, bot3 not ready, bot4 ready but with
// a stale lobby cache.
func spring() league.League {
	gcBot := func(name string) league.LeagueBot {
		return league.LeagueBot{Name: name, Source: league.SourceGC, Verified: true, RefreshedAt: refreshAt, FirstSeenAt: seenAt}
	}
	return league.League{LeagueID: 100, Name: "Spring Cup", Bots: []league.LeagueBot{
		gcBot("bot1"), {Name: "bot2", Source: league.SourceConfig}, gcBot("bot3"), gcBot("bot4"),
	}}
}

func holderServer() *Server {
	bots := &fakeBots{}
	stale := ready("bot4")
	stale.LobbyStale = true
	bots.set(ready("bot1"), ready("bot2"), backoff("bot3"), stale)
	return New(Options{
		Bots:    bots,
		Leagues: &fakeLeagues{all: []league.League{spring()}},
		Leases:  fakeLeases{"BOT1": "lob_busy"},
	})
}

func TestLeagueHolders(t *testing.T) {
	s := holderServer()
	list := do(t, s, http.MethodGet, "/v1/leagues", nil)
	one := do(t, s, http.MethodGet, "/v1/leagues/100", nil)
	if one.code != 200 {
		t.Fatalf("GET /v1/leagues/100 = %d %v", one.code, one.body)
	}
	if got := list.body["leagues"].([]any)[0]; !reflect.DeepEqual(got, any(one.body)) {
		t.Errorf("the list and the single league differ:\n%v\n%v", got, one.body)
	}
	l := one.body
	if l["holders_total"] != float64(4) || l["holders_free"] != float64(1) {
		t.Errorf("holders_total %v, holders_free %v; want 4 and 1", l["holders_total"], l["holders_free"])
	}
	if bots := l["bots"].([]any); len(bots) != 4 || bots[0].(map[string]any)["name"] != "bot1" {
		t.Errorf("bots (kept for old clients) = %v", l["bots"])
	}
	holders := l["holders"].([]any)
	tests := []struct {
		i    int
		want map[string]any
	}{
		{0, map[string]any{
			"bot": "bot1", "source": "gc", "verified": true, "first_seen_at": "2026-11-01T09:00:00Z",
			"refreshed_at": "2026-11-08T12:00:00Z", "bot_state": "gc_ready", "ready": true, "lobby_id": "lob_busy",
		}},
		{1, map[string]any{
			"bot": "bot2", "source": "config", "verified": false, "first_seen_at": nil,
			"refreshed_at": nil, "bot_state": "gc_ready", "ready": true, "lobby_id": nil,
		}},
		{2, map[string]any{"bot": "bot3", "bot_state": "backoff", "ready": false, "lobby_id": nil}},
		{3, map[string]any{"bot": "bot4", "bot_state": "gc_ready", "ready": true, "lobby_id": nil}},
	}
	for _, tt := range tests {
		h := holders[tt.i].(map[string]any)
		for k, v := range tt.want {
			if h[k] != v {
				t.Errorf("holder %d %s = %#v, want %#v", tt.i, k, h[k], v)
			}
		}
	}
}

func TestGetLeagueErrors(t *testing.T) {
	s := holderServer()
	tests := []struct {
		path string
		code int
		err  string
	}{
		{"/v1/leagues/999", 404, "not_found"},
		{"/v1/leagues/0", 422, "invalid_request"},
		{"/v1/leagues/abc", 422, "invalid_request"},
	}
	for _, tt := range tests {
		if r := do(t, s, http.MethodGet, tt.path, nil); r.code != tt.code || errCode(r) != tt.err {
			t.Errorf("GET %s = %d %v, want %d %s", tt.path, r.code, r.body, tt.code, tt.err)
		}
	}
}

func inventory() league.Inventory {
	return league.Inventory{
		Bot: "bot1",
		Leagues: []league.BotLeague{
			{LeagueID: 100, Name: "Spring Cup", LeagueBot: league.LeagueBot{
				Name: "bot1", Source: league.SourceGC, Verified: true, RefreshedAt: refreshAt, FirstSeenAt: seenAt, Stale: true,
			}},
			{LeagueID: 300, LeagueBot: league.LeagueBot{Name: "bot1", Source: league.SourceConfig}},
		},
		OKAt: refreshAt, Error: "list admin leagues: gc timeout", ErrorAt: refreshAt.Add(time.Hour),
		Changes: []league.Change{
			{LeagueID: 200, Kind: store.ChangeRemoved, At: refreshAt},
			{LeagueID: 100, Kind: store.ChangeAdded, At: seenAt},
		},
	}
}

func TestGetBotLeagues(t *testing.T) {
	s := New(Options{Leagues: &fakeLeagues{inv: map[string]league.Inventory{"bot1": inventory()}}})
	if r := do(t, s, http.MethodGet, "/v1/bots/nobody/leagues", nil); r.code != 404 || errCode(r) != "not_found" {
		t.Errorf("unknown bot = %d %v", r.code, r.body)
	}
	r := do(t, s, http.MethodGet, "/v1/bots/bot1/leagues", nil)
	if r.code != 200 || r.body["bot"] != "bot1" {
		t.Fatalf("bot leagues = %d %v", r.code, r.body)
	}
	ls := r.body["leagues"].([]any)
	want := []map[string]any{
		{"league_id": float64(100), "name": "Spring Cup", "source": "gc", "verified": true,
			"first_seen_at": "2026-11-01T09:00:00Z", "refreshed_at": "2026-11-08T12:00:00Z", "stale": true},
		{"league_id": float64(300), "name": nil, "source": "config", "verified": false,
			"first_seen_at": nil, "refreshed_at": nil, "stale": false},
	}
	if len(ls) != len(want) {
		t.Fatalf("leagues = %v", ls)
	}
	for i, w := range want {
		if got := ls[i].(map[string]any); !reflect.DeepEqual(got, w) {
			t.Errorf("league %d = %v, want %v", i, got, w)
		}
	}
	wantRefresh := map[string]any{"ok_at": "2026-11-08T12:00:00Z", "error": "list admin leagues: gc timeout", "error_at": "2026-11-08T13:00:00Z"}
	if !reflect.DeepEqual(r.body["refresh"], any(wantRefresh)) {
		t.Errorf("refresh = %v", r.body["refresh"])
	}
	wantChanges := []any{
		map[string]any{"league_id": float64(200), "change": "removed", "at": "2026-11-08T12:00:00Z"},
		map[string]any{"league_id": float64(100), "change": "added", "at": "2026-11-01T09:00:00Z"},
	}
	if !reflect.DeepEqual(r.body["changes"], any(wantChanges)) {
		t.Errorf("changes = %v", r.body["changes"])
	}

	// A bot that was never refreshed: empty arrays and null status, never
	// missing fields.
	s = New(Options{Leagues: &fakeLeagues{inv: map[string]league.Inventory{"bot2": {Bot: "bot2"}}}})
	r = do(t, s, http.MethodGet, "/v1/bots/bot2/leagues", nil)
	if l, ok := r.body["leagues"].([]any); !ok || len(l) != 0 {
		t.Errorf("leagues = %#v", r.body["leagues"])
	}
	if c, ok := r.body["changes"].([]any); !ok || len(c) != 0 {
		t.Errorf("changes = %#v", r.body["changes"])
	}
	if want := map[string]any{"ok_at": nil, "error": nil, "error_at": nil}; !reflect.DeepEqual(r.body["refresh"], any(want)) {
		t.Errorf("refresh = %v", r.body["refresh"])
	}
}

func TestRefreshBotLeagues(t *testing.T) {
	tests := []struct {
		name    string
		bot     string
		err     error
		code    int
		errCode string
	}{
		{"ok", "bot1", nil, 200, ""},
		{"unknown bot", "nobody", nil, 404, "not_found"},
		{"not ready", "bot1", fmt.Errorf("x: %w", bot.ErrNotReady), 503, "unavailable"},
		{"stopped", "bot1", fmt.Errorf("x: %w", bot.ErrStopped), 503, "unavailable"},
		{"gc source disabled", "bot1", fmt.Errorf("x: %w", league.ErrGCSourceDisabled), 409, "invalid_state"},
		{"gc timeout", "bot1", fmt.Errorf("x: %w", context.DeadlineExceeded), 504, "gc_timeout"},
		{"gc error", "bot1", errors.New("decode failed"), 503, "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			after := inventory()
			lg := &fakeLeagues{
				inv:          map[string]league.Inventory{"bot1": {Bot: "bot1"}},
				afterRefresh: map[string]league.Inventory{"bot1": after},
				refreshErr:   tt.err,
			}
			s := New(Options{Leagues: lg, GCRequestTimeout: 3 * time.Second})
			r := do(t, s, http.MethodPost, "/v1/bots/"+tt.bot+"/leagues/refresh", nil)
			if r.code != tt.code || errCode(r) != tt.errCode {
				t.Fatalf("refresh = %d %v, want %d %s", r.code, r.body, tt.code, tt.errCode)
			}
			if tt.bot == "bot1" && (lg.deadline <= 0 || lg.deadline > 3*time.Second) {
				t.Errorf("Refresh ctx had %v left, want about 3s", lg.deadline)
			}
			if tt.code != 200 {
				return
			}
			if ls := r.body["leagues"].([]any); len(ls) != 2 || len(lg.refreshed) != 1 {
				t.Errorf("refresh answered %v after %v refreshes", r.body, lg.refreshed)
			}
		})
	}
}

func TestRefreshAllLeagues(t *testing.T) {
	lg := &fakeLeagues{
		perBot: map[string][]uint32{"bot1": {100}, "bot2": {300}},
		refreshAll: []league.RefreshResult{
			{Bot: "bot1"},
			{Bot: "bot2", Err: fmt.Errorf("refresh leagues of bot2: %w", context.DeadlineExceeded)},
			{Bot: "bot3", Err: errors.New("refresh leagues of bot3: gc said no")},
			{Bot: "bot4", NotReady: true},
		},
	}
	s := New(Options{Leagues: lg, GCRequestTimeout: 3 * time.Second})
	r := do(t, s, http.MethodPost, "/v1/leagues/refresh", nil)
	if r.code != 200 {
		t.Fatalf("refresh all = %d %v", r.code, r.body)
	}
	if lg.deadline <= 0 || lg.deadline > 3*time.Second {
		t.Errorf("RefreshAll ctx had %v left, want about 3s", lg.deadline)
	}
	want := []any{
		map[string]any{"bot": "bot1", "status": "ok", "error": nil, "leagues": []any{float64(100)}},
		map[string]any{"bot": "bot2", "status": "failed", "error": "the game coordinator did not answer in time", "leagues": []any{float64(300)}},
		map[string]any{"bot": "bot3", "status": "failed", "error": "refresh leagues of bot3: gc said no", "leagues": []any{}},
		map[string]any{"bot": "bot4", "status": "not_ready", "error": nil, "leagues": []any{}},
	}
	if !reflect.DeepEqual(r.body["results"], any(want)) {
		t.Errorf("results =\n%v\nwant\n%v", r.body["results"], want)
	}

	lg.refreshAllEr = fmt.Errorf("x: %w", league.ErrGCSourceDisabled)
	if r := do(t, s, http.MethodPost, "/v1/leagues/refresh", nil); r.code != 409 || errCode(r) != "invalid_state" {
		t.Errorf("refresh all without the gc source = %d %v", r.code, r.body)
	}
}

// /v1/leagues/refresh and /v1/leagues/{id} overlap: each still answers
// its own methods and a 405 for the others, behind the key.
func TestOverlappingLeagueRoutes(t *testing.T) {
	s := New(Options{APIKey: testKey, Leagues: &fakeLeagues{all: []league.League{spring()}}})
	auth := map[string]string{"X-API-Key": testKey}
	tests := []struct {
		method, path string
		hdr          map[string]string
		code         int
		allow        string
	}{
		{"GET", "/v1/leagues/100", auth, 200, ""},
		{"POST", "/v1/leagues/refresh", auth, 200, ""},
		{"GET", "/v1/leagues/refresh", auth, 405, "POST"},
		{"DELETE", "/v1/leagues/refresh", auth, 405, "POST"},
		{"POST", "/v1/leagues/100", auth, 405, "GET, HEAD"},
		{"GET", "/v1/leagues/refresh", nil, 401, ""},
		{"POST", "/v1/leagues/100", nil, 401, ""},
		{"POST", "/v1/leagues/refresh", nil, 401, ""},
		{"GET", "/v1/bots/bot1/leagues/refresh", auth, 405, "POST"},
		{"DELETE", "/v1/bots/bot1/leagues", nil, 401, ""},
	}
	for _, tt := range tests {
		r := do(t, s, tt.method, tt.path, tt.hdr)
		if r.code != tt.code || r.hdr.Get("Allow") != tt.allow {
			t.Errorf("%s %s = %d (Allow %q), want %d (Allow %q)", tt.method, tt.path, r.code, r.hdr.Get("Allow"), tt.code, tt.allow)
		}
	}
}
