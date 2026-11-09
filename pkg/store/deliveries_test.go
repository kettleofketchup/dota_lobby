package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

// queueRig stores lobbies whose events are queued for every URL in urls.
func queueRig(t *testing.T, urls ...string) (*Store, *Lobbies) {
	t.Helper()
	s, _ := openTemp(t)
	return s, s.Lobbies().WithWebhooks(func(string) []string { return urls })
}

func insertWithEvents(t *testing.T, repo *Lobbies, id string, n int) {
	t.Helper()
	evs := make([]EventRow, n)
	for i := range evs {
		evs[i] = event(id, int64(i+1), "lobby.state_changed")
		evs[i].Lobby = []byte(`{"id":"` + id + `"}`)
	}
	if err := repo.Insert(context.Background(), lobbyRow(id, "ref-"+id, "waiting"), nil, evs); err != nil {
		t.Fatal(err)
	}
}

func dueKeys(t *testing.T, d *Deliveries, now time.Time) []string {
	t.Helper()
	due, err := d.Due(context.Background(), now, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, dl := range due {
		out = append(out, fmt.Sprintf("%s#%d@%s", dl.Event.LobbyID, dl.Event.Seq, dl.URL))
	}
	return out
}

func TestDeliveriesQueuedWithEvents(t *testing.T) {
	ctx := context.Background()
	s, repo := queueRig(t, "https://a/h", "https://b/h")
	insertWithEvents(t, repo, "lob_1", 2)
	// The plain repository queues nothing.
	insertWithEvents(t, s.Lobbies(), "lob_2", 1)
	c, err := s.Deliveries().Counts(ctx)
	if err != nil || c != (DeliveryCounts{Pending: 4}) {
		t.Fatalf("counts = %+v, %v", c, err)
	}
	// A failed transaction queues nothing either.
	row := lobbyRow("lob_1", "ref-lob_1", "ready")
	if err := repo.Transition(ctx, 5, row, nil, []EventRow{event("lob_1", 3, "x")}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("transition = %v", err)
	}
	if c, _ := s.Deliveries().Counts(ctx); c.Pending != 4 {
		t.Errorf("a rolled back event was queued: %+v", c)
	}
	due, err := s.Deliveries().Due(ctx, t0, 0)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	if e := due[0].Event; e.Seq != 1 || string(e.Lobby) != `{"id":"lob_1"}` || string(e.Data) != `{}` || !e.OccurredAt.Equal(t0) || due[0].Attempts != 0 {
		t.Errorf("due[0] = %+v", due[0])
	}
}

func TestDeliveriesDueOrder(t *testing.T) {
	ctx := context.Background()
	s, repo := queueRig(t, "https://a/h")
	d := s.Deliveries()
	insertWithEvents(t, repo, "lob_1", 3)
	insertWithEvents(t, repo, "lob_2", 1)

	steps := []struct {
		name string
		do   func() error
		now  time.Time
		want string
	}{
		{"only the lowest seq of each lobby", nil, t0, "[lob_1#1@https://a/h lob_2#1@https://a/h]"},
		{"nothing is due before its time", nil, t0.Add(-time.Second), "[]"},
		{"a retry later holds the lobby's next event back", func() error {
			return d.MarkRetry(ctx, "evt_lob_1_1", "https://a/h", 1, 500, "500 Internal Server Error", t0.Add(time.Minute))
		}, t0, "[lob_2#1@https://a/h]"},
		{"the retry is due at its time", nil, t0.Add(time.Minute), "[lob_2#1@https://a/h lob_1#1@https://a/h]"},
		{"delivered lets the next go", func() error {
			return d.MarkDelivered(ctx, "evt_lob_1_1", "https://a/h", 2, 200, t0)
		}, t0.Add(time.Minute), "[lob_1#2@https://a/h lob_2#1@https://a/h]"},
		{"dead lets the next go too", func() error {
			return d.MarkDead(ctx, "evt_lob_1_2", "https://a/h", 1, 404, "404 Not Found")
		}, t0.Add(time.Minute), "[lob_1#3@https://a/h lob_2#1@https://a/h]"},
	}
	for _, st := range steps {
		if st.do != nil {
			if err := st.do(); err != nil {
				t.Fatalf("%s: %v", st.name, err)
			}
		}
		if got := fmt.Sprint(dueKeys(t, d, st.now)); got != st.want {
			t.Errorf("%s: due = %s, want %s", st.name, got, st.want)
		}
	}
	c, _ := d.Counts(ctx)
	if c != (DeliveryCounts{Pending: 2, Delivered: 1, Dead: 1}) {
		t.Errorf("counts = %+v", c)
	}
	var (
		attempts, status int
		lastErr          string
	)
	if err := s.DB().QueryRowContext(ctx, `SELECT attempts, last_status, last_error FROM webhook_deliveries
		WHERE event_id = 'evt_lob_1_2'`).Scan(&attempts, &status, &lastErr); err != nil || attempts != 1 || status != 404 || lastErr != "404 Not Found" {
		t.Errorf("dead row = %d %d %q, %v", attempts, status, lastErr, err)
	}
	if err := d.MarkDead(ctx, "evt_nope", "https://a/h", 1, 0, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("mark unknown = %v", err)
	}
}

// Each URL has its own order: a failing receiver does not hold back another.
func TestDeliveriesOrderPerURL(t *testing.T) {
	ctx := context.Background()
	s, repo := queueRig(t, "https://a/h", "https://b/h")
	d := s.Deliveries()
	insertWithEvents(t, repo, "lob_1", 2)
	if err := d.MarkRetry(ctx, "evt_lob_1_1", "https://a/h", 1, 0, "refused", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkDelivered(ctx, "evt_lob_1_1", "https://b/h", 1, 204, t0); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(dueKeys(t, d, t0)); got != "[lob_1#2@https://b/h]" {
		t.Errorf("due = %s", got)
	}
}

func TestSweepDeletesDeliveries(t *testing.T) {
	ctx := context.Background()
	s, repo := queueRig(t, "https://a/h")
	row := lobbyRow("lob_1", "ref", "closed")
	row.EndedAt = t0
	if err := repo.Insert(ctx, row, nil, []EventRow{event("lob_1", 1, "lobby.closed")}); err != nil {
		t.Fatal(err)
	}
	n, err := repo.Sweep(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if c, _ := s.Deliveries().Counts(ctx); c != (DeliveryCounts{}) {
		t.Errorf("deliveries left: %+v", c)
	}
}

// The limit applies per URL: a backlog for one receiver leaves room for
// the others.
func TestDeliveriesDueLimitPerURL(t *testing.T) {
	ctx := context.Background()
	s, repo := queueRig(t, "https://a/h", "https://b/h")
	d := s.Deliveries()
	for i := 1; i <= 3; i++ {
		insertWithEvents(t, repo, fmt.Sprintf("lob_%d", i), 1)
	}
	// b's rows are due later than all of a's, so a global limit would
	// return only a's.
	for i := 1; i <= 3; i++ {
		if err := d.MarkRetry(ctx, fmt.Sprintf("evt_lob_%d_1", i), "https://b/h", 1, 500, "x", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	due, err := d.Due(ctx, t0.Add(time.Minute), 2)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, dl := range due {
		got = append(got, fmt.Sprintf("%s#%d@%s", dl.Event.LobbyID, dl.Event.Seq, dl.URL))
	}
	want := "[lob_1#1@https://a/h lob_2#1@https://a/h lob_1#1@https://b/h lob_2#1@https://b/h]"
	if fmt.Sprint(got) != want {
		t.Errorf("due = %v, want %s", got, want)
	}
}

// Stored times sort as text in time order, also when the fraction has
// trailing zeros: a row due at 10.12 s is due at 10.123 s.
func TestDueWithTrailingZeroFraction(t *testing.T) {
	ctx := context.Background()
	s, repo := queueRig(t, "https://a/h")
	d := s.Deliveries()
	insertWithEvents(t, repo, "lob_1", 1)
	at := t0.Add(10*time.Second + 120*time.Millisecond)
	if err := d.MarkRetry(ctx, "evt_lob_1_1", "https://a/h", 1, 500, "x", at); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		now  time.Time
		want int
	}{
		{at.Add(-time.Nanosecond), 0},
		{at, 1},
		{at.Add(3 * time.Millisecond), 1},
		{at.Add(time.Second), 1},
	} {
		due, err := d.Due(ctx, tc.now, 0)
		if err != nil || len(due) != tc.want {
			t.Errorf("due at %s = %d rows, %v; want %d", tc.now.Format(time.RFC3339Nano), len(due), err, tc.want)
			continue
		}
		if tc.want == 1 && !due[0].NextAt.Equal(at) {
			t.Errorf("next_at = %s, want %s", due[0].NextAt, at)
		}
	}
}

func TestFormatTimeSortsAsText(t *testing.T) {
	times := []time.Time{
		t0,
		t0.Add(100 * time.Millisecond),
		t0.Add(100*time.Millisecond + 10*time.Microsecond),
		t0.Add(120 * time.Millisecond),
		t0.Add(123 * time.Millisecond),
		t0.Add(time.Second),
	}
	for i := 1; i < len(times); i++ {
		a, b := formatTime(times[i-1]), formatTime(times[i])
		if a >= b {
			t.Errorf("%q does not sort before %q", a, b)
		}
	}
	// A time written before the fixed width still reads back.
	old := t0.Add(100 * time.Millisecond).Format(time.RFC3339Nano)
	if got, err := parseTime(sql.NullString{String: old, Valid: true}); err != nil || !got.Equal(t0.Add(100*time.Millisecond)) {
		t.Errorf("parse %q = %v, %v", old, got, err)
	}
}
