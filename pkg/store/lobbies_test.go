package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

var t0 = time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)

func lobbyRow(id, ref, state string) LobbyRow {
	return LobbyRow{
		ID: id, ExternalRef: ref, State: state, StateSince: t0, LeagueID: 17929,
		Spec: []byte(`{"name":"x"}`), CreatedAt: t0, UpdatedAt: t0,
	}
}

func event(lobby string, seq int64, typ string) EventRow {
	return EventRow{ID: fmt.Sprintf("evt_%s_%d", lobby, seq), LobbyID: lobby, Seq: seq, Type: typ, OccurredAt: t0, Data: []byte(`{}`)}
}

func TestLobbiesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	repo := s.Lobbies()

	verified := true
	outcome := int32(2)
	row := lobbyRow("lob_01", "game-1", "waiting")
	row.Bot, row.GCLobbyID, row.LeagueVerified, row.PassKey = "bot1", 27110012345678901, &verified, "k3tt1e"
	row.Details, row.HostSeat, row.LaunchAttempts = []byte(`{"GameName":"x"}`), []byte(`{"strategy":"caster"}`), 1
	row.MatchID, row.Outcome, row.Match = 8123456789, &outcome, []byte(`{}`)
	row.Seq, row.ExpiresAt = 1, t0.Add(30*time.Minute)
	players := []PlayerRow{
		{AccountID: 22222222, Side: "radiant", Position: 1, TeamRef: "team-7"},
		{AccountID: 11111111, Side: "radiant", Position: 0, TeamRef: "team-7", Captain: true, InvitesSent: 1, LastInviteAt: t0},
		{AccountID: 33333333, Side: "dire", Position: 0},
	}
	if err := repo.Insert(ctx, row, players, []EventRow{event("lob_01", 1, "lobby.state_changed")}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "lob_01")
	if err != nil {
		t.Fatal(err)
	}
	if got.GCLobbyID != row.GCLobbyID || got.PassKey != "k3tt1e" || got.LeagueVerified == nil || !*got.LeagueVerified ||
		got.Outcome == nil || *got.Outcome != 2 || got.MatchID != 8123456789 || !got.ExpiresAt.Equal(row.ExpiresAt) ||
		!got.EndedAt.IsZero() || string(got.HostSeat) != `{"strategy":"caster"}` || got.LaunchAttempts != 1 {
		t.Errorf("round trip = %+v", got)
	}
	ps, err := repo.Players(ctx, "lob_01")
	if err != nil {
		t.Fatal(err)
	}
	order := fmt.Sprint(ps[0].AccountID, ps[1].AccountID, ps[2].AccountID)
	if order != "11111111 22222222 33333333" || !ps[0].Captain || ps[0].InvitesSent != 1 || !ps[0].LastInviteAt.Equal(t0) || ps[2].TeamRef != "" {
		t.Errorf("players %s = %+v", order, ps)
	}

	if _, err := repo.Get(ctx, "lob_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing Get = %v", err)
	}
}

func TestLobbiesActiveRef(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	repo := s.Lobbies()
	if err := repo.Insert(ctx, lobbyRow("lob_01", "game-1", "pending"), nil, nil); err != nil {
		t.Fatal(err)
	}
	// Same ref while the first is active: refused.
	if err := repo.Insert(ctx, lobbyRow("lob_02", "game-1", "pending"), nil, nil); !errors.Is(err, ErrDuplicateRef) {
		t.Fatalf("duplicate active ref = %v", err)
	}
	if got, err := repo.GetActiveByRef(ctx, "game-1"); err != nil || got.ID != "lob_01" {
		t.Fatalf("active by ref = %+v, %v", got, err)
	}
	// Once it has ended, the ref is free again.
	ended := lobbyRow("lob_01", "game-1", "closed")
	ended.Version, ended.EndedAt = 1, t0
	if err := repo.Transition(ctx, 0, ended, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetActiveByRef(ctx, "game-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ended lobby still active: %v", err)
	}
	if err := repo.Insert(ctx, lobbyRow("lob_02", "game-1", "pending"), nil, nil); err != nil {
		t.Fatalf("reuse after end = %v", err)
	}
}

func TestLobbiesTransition(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	repo := s.Lobbies()
	if err := repo.Insert(ctx, lobbyRow("lob_01", "game-1", "pending"), []PlayerRow{{AccountID: 1, Side: "radiant"}},
		[]EventRow{event("lob_01", 1, "a")}); err != nil {
		t.Fatal(err)
	}
	next := lobbyRow("lob_01", "game-1", "creating")
	next.Version, next.Seq = 1, 2

	tests := []struct {
		name     string
		expected int64
		row      LobbyRow
		players  []PlayerRow
		events   []EventRow
		want     error
	}{
		{"stale version", 5, next, nil, nil, ErrVersionConflict},
		{"unknown lobby", 0, lobbyRow("lob_x", "r", "pending"), nil, nil, ErrNotFound},
		{"duplicate seq rolls back", 0, next, nil, []EventRow{event("lob_01", 1, "dup")}, nil},
		{"applies", 0, next, []PlayerRow{{AccountID: 2, Side: "dire"}}, []EventRow{event("lob_01", 2, "b")}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.Transition(ctx, tt.expected, tt.row, tt.players, tt.events)
			if tt.name == "duplicate seq rolls back" {
				if err == nil {
					t.Fatal("duplicate seq accepted")
				}
				if got, _ := repo.Get(ctx, "lob_01"); got.State != "pending" || got.Version != 0 {
					t.Errorf("state %s version %d after a failed transition", got.State, got.Version)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Transition = %v, want %v", err, tt.want)
			}
		})
	}
	got, _ := repo.Get(ctx, "lob_01")
	ps, _ := repo.Players(ctx, "lob_01")
	evs, _ := repo.Events(ctx, "lob_01", 0, 0)
	if got.State != "creating" || got.Version != 1 || len(ps) != 1 || ps[0].AccountID != 2 || len(evs) != 2 || evs[1].Type != "b" {
		t.Errorf("after transition: %+v players %+v events %+v", got, ps, evs)
	}
	if after, _ := repo.Events(ctx, "lob_01", 1, 10); len(after) != 1 || after[0].Seq != 2 {
		t.Errorf("events after 1 = %+v", after)
	}
}

func TestLobbiesList(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	repo := s.Lobbies()
	for i, st := range []string{"pending", "waiting", "closed", "waiting", "finished"} {
		ref := fmt.Sprintf("game-%d", i)
		if i == 2 {
			ref = "game-1" // lob_01 and lob_02 share a ref; lob_02 has ended
		}
		row := lobbyRow(fmt.Sprintf("lob_%02d", i), ref, st)
		if err := repo.Insert(ctx, row, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(rows []LobbyRow) string {
		out := ""
		for _, r := range rows {
			out += r.ID + " "
		}
		return out
	}
	tests := []struct {
		name string
		f    LobbyFilter
		want string
	}{
		{"all newest first", LobbyFilter{}, "lob_04 lob_03 lob_02 lob_01 lob_00 "},
		{"active", LobbyFilter{Active: true}, "lob_03 lob_01 lob_00 "},
		{"states", LobbyFilter{States: []string{"waiting", "closed"}}, "lob_03 lob_02 lob_01 "},
		{"ref", LobbyFilter{ExternalRef: "game-1"}, "lob_02 lob_01 "},
		{"page", LobbyFilter{Limit: 2, Before: "lob_03"}, "lob_02 lob_01 "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := repo.List(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(rows); got != tt.want {
				t.Errorf("List = %q, want %q", got, tt.want)
			}
		})
	}
	active, err := repo.ActiveLobbies(ctx)
	if err != nil || ids(active) != "lob_00 lob_01 lob_03 " {
		t.Errorf("ActiveLobbies = %q, %v", ids(active), err)
	}
}

func TestLobbiesSweep(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	repo := s.Lobbies()
	old := lobbyRow("lob_old", "a", "finished")
	old.EndedAt = t0.Add(-40 * 24 * time.Hour)
	recent := lobbyRow("lob_new", "b", "failed")
	recent.EndedAt = t0.Add(-time.Hour)
	active := lobbyRow("lob_act", "c", "waiting")
	for _, r := range []LobbyRow{old, recent, active} {
		if err := repo.Insert(ctx, r, []PlayerRow{{AccountID: 1, Side: "radiant"}}, []EventRow{event(r.ID, 1, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := repo.Sweep(ctx, t0.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if _, err := repo.Get(ctx, "lob_old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old lobby kept: %v", err)
	}
	if evs, _ := repo.Events(ctx, "lob_old", 0, 0); len(evs) != 0 {
		t.Errorf("old events kept: %v", evs)
	}
	for _, id := range []string{"lob_new", "lob_act"} {
		if _, err := repo.Get(ctx, id); err != nil {
			t.Errorf("%s swept: %v", id, err)
		}
	}
}

// A database at the first schema version upgrades in place and keeps its
// league lists.
func TestMigrateFromLeaguesOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dota_lobby.db")
	all := mustSub(t)
	first := fstest.MapFS{"0001_bot_leagues.sql": all["0001_bot_leagues.sql"]}
	s, err := Open(ctx, path, withMigrations(first))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Leagues().ReplaceBotLeagues(ctx, "bot1", "gc", []uint32{17929}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if v, _ := s2.SchemaVersion(ctx); v != len(mustMigrations(t)) {
		t.Errorf("schema version %d", v)
	}
	if err := s2.Lobbies().Insert(ctx, lobbyRow("lob_01", "game-1", "pending"), nil, nil); err != nil {
		t.Errorf("lobbies table missing after upgrade: %v", err)
	}
	if sets, _ := s2.Leagues().BotLeagues(ctx); len(sets["bot1"].Sources["gc"].IDs) != 1 {
		t.Errorf("league lists lost: %+v", sets)
	}
}
