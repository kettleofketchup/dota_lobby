package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

const testSecret = "whsec_test"

// clock is a settable time for the deliverer; the poll loop runs on real
// time, so moving the clock makes retries due at once.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// received is one request the test receiver saw.
type received struct {
	path   string
	header http.Header
	body   []byte
	env    lobby.Envelope
}

// rig is a database with a delivery queue, a receiver and a deliverer.
type rig struct {
	t       *testing.T
	path    string
	db      *store.Store
	clock   *clock
	srv     *httptest.Server
	hooks   []config.WebhookConfig
	secrets config.Secrets
	// queue, when set, wraps the database queue the deliverer uses.
	queue func(Queue) Queue

	mu       sync.Mutex
	reply    func(n int, r received) (status int, hdr map[string]string)
	got      []received
	timeout  time.Duration
	cancel   context.CancelFunc
	runDone  chan struct{}
	lobbySeq map[string]int64
}

func newRig(t *testing.T, reply func(n int, r received) (int, map[string]string)) *rig {
	t.Helper()
	r := &rig{t: t, path: filepath.Join(t.TempDir(), "dota_lobby.db"), reply: reply,
		clock: &clock{t: time.Now().UTC().Truncate(time.Second)}, timeout: time.Second, lobbySeq: map[string]int64{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	r.hooks = []config.WebhookConfig{{URL: r.srv.URL + "/hook?k=v", Events: []string{"*"}, TokenHeader: "X-Internal-Token"}}
	r.secrets = config.Secrets{
		WebhookSecrets: map[string]config.Secret{strings.ToLower(r.hooks[0].URL): testSecret},
		WebhookTokens:  map[string]config.Secret{strings.ToLower(r.hooks[0].URL): "internal-token"},
	}
	r.open()
	return r
}

func (r *rig) open() {
	db, err := store.Open(context.Background(), r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	r.db = db
	r.t.Cleanup(func() { _ = db.Close() })
}

func (r *rig) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	rec := received{path: req.URL.Path, header: req.Header.Clone(), body: body}
	_ = json.Unmarshal(body, &rec.env)
	r.mu.Lock()
	r.got = append(r.got, rec)
	n := len(r.got)
	reply := r.reply
	r.mu.Unlock()
	status, hdr := reply(n, rec)
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	if status == 0 { // hang until the attempt times out
		<-req.Context().Done()
		return
	}
	w.WriteHeader(status)
}

func (r *rig) received() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

// addEvents stores a lobby with n events, each queued for the rig's
// webhooks, occurring at the rig's current time plus age.
func (r *rig) addEvents(lobbyID string, n int, age time.Duration) {
	r.t.Helper()
	at := r.clock.now().Add(age)
	l := lobby.Lobby{ID: lobbyID, State: lobby.StateWaiting, Bot: "bot1",
		Spec: lobby.Spec{ExternalRef: "ref-" + lobbyID, Password: "k3tt1e", LeagueID: 17929}}
	summary, _ := json.Marshal(l.Summary())
	evs := make([]store.EventRow, n)
	for i := range evs {
		evs[i] = store.EventRow{ID: fmt.Sprintf("evt_%s_%d", lobbyID, i+1), LobbyID: lobbyID, Seq: int64(i + 1),
			Type: "lobby.state_changed", OccurredAt: at, Data: []byte(`{"to":"waiting"}`), Lobby: summary}
	}
	row := store.LobbyRow{ID: lobbyID, ExternalRef: "ref-" + lobbyID, State: "waiting", StateSince: at,
		Spec: []byte(`{}`), PassKey: "k3tt1e", CreatedAt: at, UpdatedAt: at, Seq: int64(n)}
	repo := r.db.Lobbies().WithWebhooks(Route(r.hooks))
	if err := repo.Insert(context.Background(), row, nil, evs); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) start() {
	r.t.Helper()
	var q Queue = r.db.Deliveries()
	if r.queue != nil {
		q = r.queue(q)
	}
	d := New(Options{
		Queue: q, Webhooks: r.hooks, Secrets: r.secrets,
		Delivery: config.WebhookDeliveryConfig{Timeout: r.timeout, MaxAge: 24 * time.Hour},
		Poll:     5 * time.Millisecond, Version: "test", Now: r.clock.now, Jitter: func() float64 { return 0.5 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.runDone = cancel, make(chan struct{})
	go func() { defer close(r.runDone); d.Run(ctx) }()
	r.t.Cleanup(r.stop)
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.runDone
	r.cancel = nil
}

type row struct {
	status   string
	attempts int
	last     sql.NullInt64
	lastErr  sql.NullString
	next     time.Time
}

func (r *rig) row(eventID string) row {
	r.t.Helper()
	var (
		out  row
		next string
	)
	if err := r.db.DB().QueryRowContext(context.Background(), `SELECT status, attempts, last_status, last_error, next_at
		FROM webhook_deliveries WHERE event_id = ?`, eventID).Scan(&out.status, &out.attempts, &out.last, &out.lastErr, &next); err != nil {
		r.t.Fatal(err)
	}
	out.next, _ = time.Parse(time.RFC3339Nano, next)
	return out
}

// waitFor polls cond for up to 5 s.
func (r *rig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *rig) waitStatus(eventID, status string, attempts int) row {
	r.t.Helper()
	var got row
	r.waitFor(fmt.Sprintf("%s to be %s after %d attempts", eventID, status, attempts), func() bool {
		got = r.row(eventID)
		return got.status == status && got.attempts == attempts
	})
	return got
}

func always(status int, hdr map[string]string) func(int, received) (int, map[string]string) {
	return func(int, received) (int, map[string]string) { return status, hdr }
}

func TestDeliverySucceeds(t *testing.T) {
	r := newRig(t, always(http.StatusNoContent, nil))
	r.addEvents("lob_1", 1, 0)
	r.start()
	got := r.waitStatus("evt_lob_1_1", store.DeliveryDelivered, 1)
	if got.last.Int64 != 204 {
		t.Errorf("last status = %v", got.last)
	}
	rec := r.received()[0]
	h := rec.header
	if h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "dota_lobby/test" ||
		h.Get(HeaderEventID) != "evt_lob_1_1" || h.Get(HeaderEventType) != "lobby.state_changed" ||
		h.Get("X-Internal-Token") != "internal-token" {
		t.Errorf("headers = %v", h)
	}
	if err := Verify([]byte(testSecret), h.Get(HeaderTimestamp), h.Get(HeaderSignature), rec.body, r.clock.now(), 0); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
	if e := rec.env; e.ID != "evt_lob_1_1" || e.Seq != 1 || e.Lobby.ExternalRef != "ref-lob_1" || e.Lobby.LeagueID == nil ||
		*e.Lobby.LeagueID != 17929 || string(e.Data) != `{"to":"waiting"}` {
		t.Errorf("envelope = %+v", e)
	}
	if strings.Contains(string(rec.body), "k3tt1e") {
		t.Errorf("body carries the password: %s", rec.body)
	}
}

// Failures that may pass are retried after the backoff (or Retry-After);
// any other answer gives the delivery up at once.
func TestDeliveryFailures(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		hdr       map[string]string
		wantState string
		wantWait  time.Duration
		wantErr   string
	}{
		{"server error retries after 10 s", 500, nil, store.DeliveryPending, 10 * time.Second, "500"},
		{"bad gateway retries", 502, nil, store.DeliveryPending, 10 * time.Second, "502"},
		{"request timeout retries", 408, nil, store.DeliveryPending, 10 * time.Second, "408"},
		{"too early retries", 425, nil, store.DeliveryPending, 10 * time.Second, "425"},
		{"429 honours Retry-After seconds", 429, map[string]string{"Retry-After": "120"}, store.DeliveryPending, 2 * time.Minute, "429"},
		{"429 without Retry-After backs off", 429, nil, store.DeliveryPending, 10 * time.Second, "429"},
		{"503 honours a Retry-After date", 503, map[string]string{"Retry-After": "@+90s"}, store.DeliveryPending, 90 * time.Second, "503"},
		{"no answer in time retries", 0, nil, store.DeliveryPending, 10 * time.Second, "timed out"},
		{"not found is dead", 404, nil, store.DeliveryDead, 0, "404"},
		{"unauthorized is dead", 401, nil, store.DeliveryDead, 0, "401"},
		{"bad request is dead", 400, nil, store.DeliveryDead, 0, "400"},
		{"redirect is dead and not followed", 302, map[string]string{"Location": "/elsewhere"}, store.DeliveryDead, 0, "redirect"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r *rig
			r = newRig(t, func(int, received) (int, map[string]string) {
				hdr := map[string]string{}
				for k, v := range tc.hdr {
					if strings.HasPrefix(v, "@+") {
						d, _ := time.ParseDuration(v[2:])
						v = r.clock.now().Add(d).UTC().Format(http.TimeFormat)
					}
					hdr[k] = v
				}
				return tc.status, hdr
			})
			r.timeout = 50 * time.Millisecond
			r.addEvents("lob_1", 1, 0)
			start := r.clock.now()
			r.start()
			got := r.waitStatus("evt_lob_1_1", tc.wantState, 1)
			if tc.status != 0 && got.last.Int64 != int64(tc.status) {
				t.Errorf("last status = %v", got.last)
			}
			if !strings.Contains(got.lastErr.String, tc.wantErr) {
				t.Errorf("last error = %q, want %q", got.lastErr.String, tc.wantErr)
			}
			if tc.wantState == store.DeliveryPending {
				if wait := got.next.Sub(start); wait != tc.wantWait {
					t.Errorf("next attempt in %s, want %s", wait, tc.wantWait)
				}
			}
			r.stop()
			if n := len(r.received()); n != 1 {
				t.Errorf("receiver saw %d requests, want 1 (no early retry, no redirect followed)", n)
			}
		})
	}
}

func TestBackoffSchedule(t *testing.T) {
	d := New(Options{Jitter: func() float64 { return 0.5 }})
	want := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, time.Hour}
	for i, w := range want {
		if got := d.backoff(i + 1); got != w {
			t.Errorf("after attempt %d: %s, want %s", i+1, got, w)
		}
	}
	for _, j := range []struct {
		jitter float64
		want   time.Duration
	}{{0, 8 * time.Second}, {0.999999, 12 * time.Second}} {
		d := New(Options{Jitter: func() float64 { return j.jitter }})
		if got := d.backoff(1); got.Round(time.Millisecond) != j.want {
			t.Errorf("jitter %v: %s, want %s", j.jitter, got, j.want)
		}
	}
}

// A retried delivery goes out again once its time comes, and succeeds.
func TestDeliveryRetriesThenSucceeds(t *testing.T) {
	r := newRig(t, func(n int, _ received) (int, map[string]string) {
		if n == 1 {
			return 500, nil
		}
		return 200, nil
	})
	r.addEvents("lob_1", 1, 0)
	r.start()
	r.waitStatus("evt_lob_1_1", store.DeliveryPending, 1)
	time.Sleep(30 * time.Millisecond)
	if n := len(r.received()); n != 1 {
		t.Fatalf("retried before the backoff: %d requests", n)
	}
	r.clock.advance(10 * time.Second)
	r.waitStatus("evt_lob_1_1", store.DeliveryDelivered, 2)
	recs := r.received()
	if recs[0].header.Get(HeaderEventID) != recs[1].header.Get(HeaderEventID) ||
		recs[0].header.Get(HeaderTimestamp) == recs[1].header.Get(HeaderTimestamp) {
		t.Error("a retry should carry the same event ID and a fresh timestamp")
	}
}

// A lobby's events arrive in seq order: the next one waits while the
// previous is being retried, and goes once it is delivered or dead.
func TestDeliveryOrderPerLobby(t *testing.T) {
	var (
		mu    sync.Mutex
		first int
	)
	r := newRig(t, func(_ int, rec received) (int, map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		if rec.env.Lobby.ID == "lob_1" && rec.env.Seq == 1 {
			first++
		}
		switch {
		case rec.env.Lobby.ID == "lob_1" && rec.env.Seq == 1 && first <= 2:
			return 503, nil
		case rec.env.Lobby.ID == "lob_1" && rec.env.Seq == 2:
			return 410, nil // dead; must not hold seq 3 back
		}
		return 200, nil
	})
	r.addEvents("lob_1", 3, 0)
	r.addEvents("lob_2", 1, 0)
	r.start()
	// lob_2 is not held back by lob_1's failing event.
	r.waitStatus("evt_lob_2_1", store.DeliveryDelivered, 1)
	r.waitStatus("evt_lob_1_1", store.DeliveryPending, 1)
	r.clock.advance(10 * time.Second)
	r.waitStatus("evt_lob_1_1", store.DeliveryPending, 2)
	r.clock.advance(30 * time.Second)
	r.waitStatus("evt_lob_1_1", store.DeliveryDelivered, 3)
	r.waitStatus("evt_lob_1_2", store.DeliveryDead, 1)
	r.waitStatus("evt_lob_1_3", store.DeliveryDelivered, 1)
	var order []string
	for _, rec := range r.received() {
		if rec.env.Lobby.ID == "lob_1" {
			order = append(order, fmt.Sprint(rec.env.Seq))
		}
	}
	if got := strings.Join(order, ","); got != "1,1,1,2,3" {
		t.Errorf("lob_1 order = %s, want 1,1,1,2,3", got)
	}
}

// A delivery that keeps failing is given up once the next try would fall
// past webhook_delivery.max_age after the event.
func TestDeliveryGivesUpAfterMaxAge(t *testing.T) {
	r := newRig(t, always(500, nil))
	r.addEvents("lob_1", 1, -24*time.Hour+5*time.Second)
	r.start()
	got := r.waitStatus("evt_lob_1_1", store.DeliveryDead, 1)
	if !strings.Contains(got.lastErr.String, "max_age") {
		t.Errorf("last error = %q", got.lastErr.String)
	}
}

// Pending deliveries survive a restart; an attempt cut off by shutdown is
// not counted.
func TestDeliveryResumesAfterRestart(t *testing.T) {
	var (
		mu   sync.Mutex
		hang = true
	)
	r := newRig(t, func(int, received) (int, map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		if hang {
			return 0, nil
		}
		return 200, nil
	})
	r.timeout = time.Minute
	r.addEvents("lob_1", 1, 0)
	r.start()
	r.waitFor("the first attempt", func() bool { return len(r.received()) == 1 })
	r.stop()
	if got := r.row("evt_lob_1_1"); got.status != store.DeliveryPending || got.attempts != 0 {
		t.Fatalf("after shutdown = %+v", got)
	}
	mu.Lock()
	hang = false
	mu.Unlock()
	_ = r.db.Close()
	r.open()
	r.start()
	r.waitStatus("evt_lob_1_1", store.DeliveryDelivered, 1)
}

// Rows queued for a URL that is no longer configured are given up.
func TestDeliveryToRemovedWebhook(t *testing.T) {
	r := newRig(t, always(200, nil))
	r.addEvents("lob_1", 1, 0)
	r.hooks = []config.WebhookConfig{{URL: r.srv.URL + "/other", Events: []string{"*"}}}
	r.start()
	got := r.waitStatus("evt_lob_1_1", store.DeliveryDead, 1)
	if !strings.Contains(got.lastErr.String, "no longer configured") || len(r.received()) != 0 {
		t.Errorf("row = %+v, requests %d", got, len(r.received()))
	}
}

// Without a token header no token is sent, even when one is stored.
func TestDeliveryWithoutTokenHeader(t *testing.T) {
	r := newRig(t, always(200, nil))
	r.hooks[0].TokenHeader = ""
	r.addEvents("lob_1", 1, 0)
	r.start()
	r.waitStatus("evt_lob_1_1", store.DeliveryDelivered, 1)
	for k, v := range r.received()[0].header {
		if strings.Contains(strings.Join(v, ","), "internal-token") {
			t.Errorf("token sent in %s", k)
		}
	}
}

func TestRoute(t *testing.T) {
	if Route(nil) != nil {
		t.Error("no webhooks should route nothing")
	}
	route := Route([]config.WebhookConfig{
		{URL: "https://a/h", Events: []string{"lobby.*"}},
		{URL: "https://b/h", Events: []string{"match.*", "lobby.created"}},
		{URL: "https://c/h"}, // no events: every event
	})
	tests := map[string]string{
		"lobby.created":       "[https://a/h https://b/h https://c/h]",
		"lobby.state_changed": "[https://a/h https://c/h]",
		"match.ended":         "[https://b/h https://c/h]",
		"other":               "[https://c/h]",
	}
	for typ, want := range tests {
		if got := fmt.Sprint(route(typ)); got != want {
			t.Errorf("%s -> %s, want %s", typ, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"":    0,
		"30":  30 * time.Second,
		"0":   0,
		"-5":  0,
		"abc": 0,
		now.Add(time.Minute).Format(http.TimeFormat):  time.Minute,
		now.Add(-time.Minute).Format(http.TimeFormat): 0,
	}
	for in, want := range tests {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q -> %s, want %s", in, got, want)
		}
	}
}

func TestLogNameDropsCredentials(t *testing.T) {
	if got := logName("https://user:pw@hooks.example/dota?token=s3cret#x"); got != "https://hooks.example/dota" {
		t.Errorf("logName = %s", got)
	}
}

// flakyQueue fails every queue update while fail is set, as a full or
// read-only database does; reads keep working.
type flakyQueue struct {
	Queue
	mu   sync.Mutex
	fail bool
}

func (q *flakyQueue) setFail(v bool) {
	q.mu.Lock()
	q.fail = v
	q.mu.Unlock()
}

func (q *flakyQueue) err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail {
		return errors.New("database or disk is full")
	}
	return nil
}

func (q *flakyQueue) MarkDelivered(ctx context.Context, eventID, url string, attempts, status int, at time.Time) error {
	if err := q.err(); err != nil {
		return err
	}
	return q.Queue.MarkDelivered(ctx, eventID, url, attempts, status, at)
}

func (q *flakyQueue) MarkRetry(ctx context.Context, eventID, url string, attempts, status int, errMsg string, next time.Time) error {
	if err := q.err(); err != nil {
		return err
	}
	return q.Queue.MarkRetry(ctx, eventID, url, attempts, status, errMsg, next)
}

func (q *flakyQueue) MarkDead(ctx context.Context, eventID, url string, attempts, status int, errMsg string) error {
	if err := q.err(); err != nil {
		return err
	}
	return q.Queue.MarkDead(ctx, eventID, url, attempts, status, errMsg)
}

// A delivery whose queue update fails stays pending and due; it is not
// sent again until storeFailHold has passed, whatever the receiver said.
func TestQueueUpdateFailureHoldsDelivery(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"delivered", 200, store.DeliveryDelivered},
		{"retry", 503, store.DeliveryPending},
		{"dead", 410, store.DeliveryDead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, always(tc.status, nil))
			fq := &flakyQueue{fail: true}
			r.queue = func(q Queue) Queue { fq.Queue = q; return fq }
			r.addEvents("lob_1", 1, 0)
			r.start()
			r.waitFor("the first attempt", func() bool { return len(r.received()) == 1 })
			// Dozens of polls run meanwhile; none may resend.
			time.Sleep(150 * time.Millisecond)
			if n := len(r.received()); n != 1 {
				t.Fatalf("sent %d times while the queue could not be updated, want 1", n)
			}
			if got := r.row("evt_lob_1_1"); got.status != store.DeliveryPending || got.attempts != 0 {
				t.Fatalf("row = %+v", got)
			}
			fq.setFail(false)
			r.clock.advance(storeFailHold - time.Second)
			time.Sleep(50 * time.Millisecond)
			if n := len(r.received()); n != 1 {
				t.Fatalf("sent %d times before the hold ended, want 1", n)
			}
			r.clock.advance(time.Second)
			r.waitStatus("evt_lob_1_1", tc.want, 1)
			if n := len(r.received()); n != 2 {
				t.Errorf("sent %d times, want 2", n)
			}
		})
	}
}

// Every webhook has its own Concurrency slots: a receiver that hangs on
// many lobbies' deliveries does not hold up another receiver.
func TestSlowWebhookDoesNotHoldOthers(t *testing.T) {
	r := newRig(t, func(_ int, rec received) (int, map[string]string) {
		if rec.path == "/slow" {
			return 0, nil // hang until the attempt is cut off
		}
		return 200, nil
	})
	r.timeout = time.Minute
	slow := config.WebhookConfig{URL: r.srv.URL + "/slow", Events: []string{"*"}}
	fast := config.WebhookConfig{URL: r.srv.URL + "/fast", Events: []string{"*"}}
	r.secrets.WebhookSecrets[strings.ToLower(slow.URL)] = testSecret
	r.secrets.WebhookSecrets[strings.ToLower(fast.URL)] = testSecret
	// The slow receiver's deliveries are older, so they come first.
	r.hooks = []config.WebhookConfig{slow}
	for i := 1; i <= 2*DefaultConcurrency; i++ {
		r.addEvents(fmt.Sprintf("lob_s%d", i), 1, -time.Minute)
	}
	r.hooks = []config.WebhookConfig{slow, fast}
	r.addEvents("lob_f", 1, 0)
	r.start()
	r.waitFor("the fast delivery", func() bool {
		for _, rec := range r.received() {
			if rec.path == "/fast" {
				return true
			}
		}
		return false
	})
	var hung int
	for _, rec := range r.received() {
		if rec.path == "/slow" {
			hung++
		}
	}
	if hung > DefaultConcurrency {
		t.Errorf("%d attempts in flight to one webhook, want at most %d", hung, DefaultConcurrency)
	}
}
