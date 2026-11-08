package events

import (
	"fmt"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		patterns []string
		typ      string
		want     bool
	}{
		{nil, "lobby.created", true},
		{[]string{"*"}, "match.ended", true},
		{[]string{"lobby.*"}, "lobby.created", true},
		{[]string{"lobby.*"}, "match.started", false},
		{[]string{"lobby.*", "match.*"}, "match.started", true},
		{[]string{"match.ended"}, "match.ended", true},
		{[]string{"match.ended"}, "match.started", false},
		{[]string{"lobby.["}, "lobby.created", false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.patterns, tc.typ), func(t *testing.T) {
			if got := Match(tc.patterns, tc.typ); got != tc.want {
				t.Errorf("Match = %v", got)
			}
		})
	}
}

func testLobby() lobby.Lobby {
	return lobby.Lobby{ID: "lob_1", State: lobby.StateInGame, Bot: "bot1",
		Spec: lobby.Spec{ExternalRef: "game-1", Password: "k3tt1e"}}
}

func TestBusFiltersAndBuildsEnvelopes(t *testing.T) {
	b := New(nil)
	all, stopAll := b.Subscribe(nil)
	defer stopAll()
	matches, stopMatches := b.Subscribe([]string{"match.*"})
	defer stopMatches()

	at := time.Date(2026, 11, 8, 18, 5, 0, 0, time.UTC)
	b.OnEvent(testLobby(), lobby.Event{ID: "evt_1", LobbyID: "lob_1", Seq: 1, Type: "lobby.created", OccurredAt: at})
	b.OnEvent(testLobby(), lobby.Event{ID: "evt_2", LobbyID: "lob_1", Seq: 2, Type: "match.started", OccurredAt: at,
		Data: map[string]any{"match_id": "8123456789"}})

	if e := <-all; e.ID != "evt_1" || string(e.Data) != "{}" || e.Lobby.ExternalRef != "game-1" || e.Lobby.State != lobby.StateInGame {
		t.Errorf("first = %+v", e)
	}
	if e := <-all; e.ID != "evt_2" {
		t.Errorf("second = %+v", e)
	}
	if e := <-matches; e.ID != "evt_2" || string(e.Data) != `{"match_id":"8123456789"}` {
		t.Errorf("match subscriber got %+v", e)
	}
	select {
	case e := <-matches:
		t.Errorf("match subscriber got a lobby event %+v", e)
	default:
	}
}

// A subscriber that does not read loses its oldest envelopes; publishing
// never blocks.
func TestBusDropsOldest(t *testing.T) {
	b := New(nil)
	ch, stop := b.Subscribe(nil)
	n := SubscriberBuffer + 10
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= n; i++ {
			b.Publish(lobby.Envelope{ID: fmt.Sprint(i), Seq: int64(i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	if first := <-ch; first.Seq != 11 {
		t.Errorf("oldest kept = %d, want 11", first.Seq)
	}
	stop()
	stop() // idempotent
	got := 1
	for range ch {
		got++
	}
	if got != SubscriberBuffer {
		t.Errorf("kept %d, want %d", got, SubscriberBuffer)
	}
	// Publishing after the subscriber left is fine.
	b.Publish(lobby.Envelope{ID: "late"})
}
