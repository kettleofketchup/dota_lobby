package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

func TestSummary(t *testing.T) {
	tests := []struct {
		name string
		l    Lobby
		want string
	}{
		{"pending", Lobby{ID: "lob_1", State: StatePending, Spec: Spec{ExternalRef: "game-1", Password: "pw"}},
			`{"id":"lob_1","external_ref":"game-1","state":"pending","bot":null,"gc_lobby_id":null,"league_id":null,"metadata":{}}`},
		{"created in a league", Lobby{ID: "lob_1", State: StateWaiting, Bot: "bot1", GCLobbyID: 27110012345678901,
			Spec: Spec{ExternalRef: "game-1", LeagueID: 17929, Metadata: map[string]string{"round": "2"}}},
			`{"id":"lob_1","external_ref":"game-1","state":"waiting","bot":"bot1","gc_lobby_id":"27110012345678901","league_id":17929,"metadata":{"round":"2"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.l.Summary())
			if err != nil || string(got) != tc.want {
				t.Errorf("summary = %s, %v\nwant %s", got, err, tc.want)
			}
		})
	}
}

func TestEnvelopeFromRow(t *testing.T) {
	at := time.Date(2026, 11, 8, 18, 5, 0, 0, time.UTC)
	stored := Lobby{ID: "lob_1", State: StateInGame, Spec: Spec{ExternalRef: "game-1"}}.Summary()
	raw, _ := json.Marshal(stored)
	fallback := Lobby{ID: "lob_1", State: StateFinished, Spec: Spec{ExternalRef: "game-1"}}.Summary()
	tests := []struct {
		name      string
		row       store.EventRow
		fallback  *Summary
		wantState State
		wantData  string
		wantErr   bool
	}{
		{"stored summary wins", store.EventRow{ID: "evt_1", Data: []byte(`{"a":1}`), Lobby: raw}, &fallback, StateInGame, `{"a":1}`, false},
		{"old event uses the fallback", store.EventRow{ID: "evt_1", Data: []byte(`{}`)}, &fallback, StateFinished, `{}`, false},
		{"no summary and no fallback", store.EventRow{ID: "evt_1", Data: []byte(`{}`)}, nil, "", "", true},
		{"empty data is an object", store.EventRow{ID: "evt_1", Lobby: raw}, nil, StateInGame, `{}`, false},
		{"bad summary", store.EventRow{ID: "evt_1", Lobby: []byte(`{`)}, nil, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.row.Type, tc.row.Seq, tc.row.OccurredAt = "match.started", 3, at.In(time.FixedZone("x", 3600))
			env, err := EnvelopeFromRow(tc.row, tc.fallback)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				return
			}
			if env.Lobby.State != tc.wantState || string(env.Data) != tc.wantData || env.Seq != 3 ||
				env.Type != "match.started" || env.OccurredAt.Location() != time.UTC || env.Lobby.Metadata == nil {
				t.Errorf("envelope = %+v", env)
			}
		})
	}
}

// Each event carries the lobby as it was right after that change, not as
// it is when it is read.
func TestManagerEvents(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, _ := h.toWaiting(spec("game-1"))
	ctx := context.Background()
	all, err := h.m.Events(ctx, l.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 || all[0].Seq != 1 || all[0].Lobby.State != StatePending || all[0].Lobby.Bot != nil {
		t.Fatalf("events = %+v", all)
	}
	last := all[len(all)-1]
	if last.Lobby.State != StateWaiting || last.Lobby.GCLobbyID == nil || last.Lobby.ExternalRef != "game-1" {
		t.Errorf("last event lobby = %+v", last.Lobby)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq != all[i-1].Seq+1 {
			t.Errorf("seq gap at %d: %d after %d", i, all[i].Seq, all[i-1].Seq)
		}
	}
	page, err := h.m.Events(ctx, l.ID, 1, 1)
	if err != nil || len(page) != 1 || page[0].Seq != 2 {
		t.Errorf("page = %+v, %v", page, err)
	}
	if _, err := h.m.Events(ctx, "lob_missing", 0, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown lobby: %v", err)
	}
	for _, bad := range [][2]int{{-1, 10}, {0, MaxEventsPage + 1}, {0, -1}} {
		if _, err := h.m.Events(ctx, l.ID, int64(bad[0]), bad[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("after=%d limit=%d: %v", bad[0], bad[1], err)
		}
	}
}

// With webhooks, every event is queued for each matching URL in the same
// transaction that stores it.
func TestManagerQueuesWebhookDeliveries(t *testing.T) {
	h := newHarness(t, 1, nil)
	_ = h.m.Drain(context.Background())
	h.store = h.db.Lobbies().WithWebhooks(func(typ string) []string {
		if typ == "lobby.state_changed" {
			return []string{"https://a.example/hook", "https://b.example/hook"}
		}
		return []string{"https://a.example/hook"}
	})
	h.start()
	l, _ := h.toWaiting(spec("game-1"))
	ctx := context.Background()
	evs, err := h.db.Lobbies().Events(ctx, l.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, ev := range evs {
		want++
		if ev.Type == "lobby.state_changed" {
			want++
		}
	}
	var got int
	if err := h.db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE lobby_id = ? AND status = 'pending'`, l.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("queued %d deliveries for %d events, want %d", got, len(evs), want)
	}
}
