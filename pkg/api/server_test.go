package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// fakeBots is a scriptable status provider.
type fakeBots struct {
	mu       sync.Mutex
	statuses []bot.Status
	stalled  []string
	retried  []string
}

func (f *fakeBots) Retry(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, st := range f.statuses {
		if strings.EqualFold(st.Name, name) {
			f.retried = append(f.retried, name)
			return true
		}
	}
	return false
}

func (f *fakeBots) Statuses() []bot.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bot.Status(nil), f.statuses...)
}

func (f *fakeBots) Stalled(time.Duration) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stalled...)
}

func (f *fakeBots) set(sts ...bot.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = sts
}

var since = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func ready(name string) bot.Status {
	return bot.Status{Name: name, Username: name, State: bot.StateGCReady, Ready: true, Since: since, SteamID: 76561198000000001}
}

func backoff(name string) bot.Status {
	return bot.Status{
		Name: name, Username: name, State: bot.StateBackoff, Since: since,
		Reason: "logged_in_elsewhere", LastResult: "LoggedInElsewhere", LastError: "logon failed", Attempt: 2,
	}
}

const testKey = "s3cret-key"

func newTestServer(t *testing.T, key config.Secret, minReady int, bots *fakeBots) *Server {
	t.Helper()
	return New(Options{APIKey: key, Bots: bots, MinReadyBots: minReady, Version: "test", Now: func() time.Time { return since.Add(time.Minute) }})
}

type response struct {
	code int
	hdr  http.Header
	body map[string]any
}

func do(t *testing.T, s *Server, method, path string, hdr map[string]string) response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	out := response{code: rec.Code, hdr: rec.Header()}
	if method != http.MethodHead {
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: Content-Type %q", method, path, ct)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return out
}

func errCode(r response) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestAuth(t *testing.T) {
	bots := &fakeBots{}
	tests := []struct {
		name string
		key  config.Secret
		path string
		hdr  map[string]string
		want int
	}{
		{"no key configured", "", "/v1/bots", nil, 200},
		{"missing key", testKey, "/v1/bots", nil, 401},
		{"x-api-key", testKey, "/v1/bots", map[string]string{"X-API-Key": testKey}, 200},
		{"wrong x-api-key", testKey, "/v1/bots", map[string]string{"X-API-Key": "nope"}, 401},
		{"bearer", testKey, "/v1/bots", map[string]string{"Authorization": "Bearer " + testKey}, 200},
		{"bearer lowercase", testKey, "/v1/bots", map[string]string{"Authorization": "bearer " + testKey}, 200},
		{"basic scheme", testKey, "/v1/bots", map[string]string{"Authorization": "Basic " + testKey}, 401},
		{"bare bearer", testKey, "/v1/bots", map[string]string{"Authorization": "Bearer"}, 401},
		{"key prefix", testKey, "/v1/bots", map[string]string{"X-API-Key": testKey[:4]}, 401},
		{"livez open", testKey, "/livez", nil, 200},
		{"readyz open", testKey, "/readyz", nil, 200},
		{"health open", testKey, "/health", nil, 200},
		{"lobbies need key", testKey, "/v1/lobbies", nil, 401},
		{"old route needs key", testKey, "/bots", nil, 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, tt.key, 0, bots)
			r := do(t, s, http.MethodGet, tt.path, tt.hdr)
			if r.code != tt.want {
				t.Fatalf("status %d, want %d (%v)", r.code, tt.want, r.body)
			}
			if r.code == 401 {
				if errCode(r) != "unauthorized" || r.hdr.Get("WWW-Authenticate") == "" {
					t.Errorf("401 body %v, header %q", r.body, r.hdr.Get("WWW-Authenticate"))
				}
			}
		})
	}
}

func TestLivez(t *testing.T) {
	bots := &fakeBots{}
	s := newTestServer(t, "", 1, bots)
	if r := do(t, s, http.MethodGet, "/livez", nil); r.code != 200 || r.body["status"] != "ok" {
		t.Fatalf("livez = %d %v", r.code, r.body)
	}
	// Liveness ignores readiness and draining.
	bots.set(backoff("a"))
	s.SetDraining()
	if r := do(t, s, http.MethodGet, "/livez", nil); r.code != 200 {
		t.Fatalf("livez while draining with no ready bots = %d", r.code)
	}
	bots.mu.Lock()
	bots.stalled = []string{"a"}
	bots.mu.Unlock()
	r := do(t, s, http.MethodGet, "/livez", nil)
	if r.code != 503 || r.body["status"] != "stalled" {
		t.Fatalf("livez with a stalled bot = %d %v", r.code, r.body)
	}
	// The endpoint is open, so it gives a count, never bot names.
	if got := r.body["stalled_bots"]; got != float64(1) {
		t.Errorf("stalled_bots = %#v, want 1", got)
	}
}

func TestReadyzTransitions(t *testing.T) {
	bots := &fakeBots{}
	s := newTestServer(t, "", 2, bots)
	steps := []struct {
		name     string
		statuses []bot.Status
		drain    bool
		code     int
		status   string
		ready    float64
	}{
		{"no bots", nil, false, 503, "not_ready", 0},
		{"one of two", []bot.Status{ready("a"), backoff("b")}, false, 503, "not_ready", 1},
		{"two ready", []bot.Status{ready("a"), ready("b")}, false, 200, "ready", 2},
		{"one drops", []bot.Status{ready("a"), backoff("b")}, false, 503, "not_ready", 1},
		{"recovers", []bot.Status{ready("a"), ready("b"), backoff("c")}, false, 200, "ready", 2},
		{"draining", []bot.Status{ready("a"), ready("b")}, true, 503, "draining", 2},
	}
	for _, st := range steps {
		bots.set(st.statuses...)
		if st.drain {
			s.SetDraining()
		}
		r := do(t, s, http.MethodGet, "/readyz", nil)
		if r.code != st.code || r.body["status"] != st.status || r.body["bots_ready"] != st.ready {
			t.Errorf("%s: readyz = %d %v", st.name, r.code, r.body)
		}
		if r.body["min_ready_bots"] != float64(2) || r.body["bots_total"] != float64(len(st.statuses)) {
			t.Errorf("%s: counts %v", st.name, r.body)
		}
	}
}

func TestReadyzZeroMinimum(t *testing.T) {
	s := newTestServer(t, "", 0, &fakeBots{})
	if r := do(t, s, http.MethodGet, "/readyz", nil); r.code != 200 {
		t.Fatalf("readyz with min 0 and no bots = %d", r.code)
	}
}

func TestHealth(t *testing.T) {
	bots := &fakeBots{}
	bots.set(ready("a"), backoff("b"), backoff("c"))
	s := newTestServer(t, "", 2, bots)
	r := do(t, s, http.MethodGet, "/health", nil)
	if r.code != 200 || r.body["status"] != "healthy" || r.body["ready"] != false {
		t.Fatalf("health = %d %v", r.code, r.body)
	}
	if r.body["bots_ready"] != float64(1) || r.body["bots_total"] != float64(3) || r.body["version"] != "test" {
		t.Errorf("health counts %v", r.body)
	}
	states, _ := r.body["states"].(map[string]any)
	if states["gc_ready"] != float64(1) || states["backoff"] != float64(2) {
		t.Errorf("states = %v", states)
	}
	if r.body["uptime_seconds"] != float64(0) {
		t.Errorf("uptime = %v", r.body["uptime_seconds"])
	}
	// Health stays 200 while draining.
	s.SetDraining()
	if r := do(t, s, http.MethodGet, "/health", nil); r.code != 200 || r.body["draining"] != true {
		t.Errorf("draining health = %d %v", r.code, r.body)
	}
}

func TestBots(t *testing.T) {
	bots := &fakeBots{}
	exp := time.Date(2027, 5, 21, 0, 0, 0, 0, time.UTC)
	a := ready("league_bot_1")
	a.TokenExpiresAt = exp
	q := bot.Status{Name: "league_bot_2", Username: "lb2", State: bot.StateGCQueued, Since: since, QueuePosition: 7}
	bots.set(a, backoff("league_bot_3"), q)
	s := newTestServer(t, testKey, 1, bots)
	auth := map[string]string{"X-API-Key": testKey}

	r := do(t, s, http.MethodGet, "/v1/bots", auth)
	list, _ := r.body["bots"].([]any)
	if r.code != 200 || len(list) != 3 {
		t.Fatalf("bots = %d %v", r.code, r.body)
	}
	tests := []struct {
		i    int
		want map[string]any
	}{
		{0, map[string]any{
			"name": "league_bot_1", "state": "gc_ready", "ready": true, "steam_id": "76561198000000001",
			"token_expires_at": "2027-05-21T00:00:00Z", "last_error": nil, "reason": nil,
			"since": "2026-11-07T12:00:00Z", "limited": false, "gc_queue_position": nil,
		}},
		{1, map[string]any{
			"name": "league_bot_3", "state": "backoff", "ready": false, "steam_id": nil,
			"token_expires_at": nil, "last_error": "LoggedInElsewhere", "reason": "logged_in_elsewhere",
			"last_error_message": "logon failed", "attempt": float64(2),
		}},
		{2, map[string]any{"name": "league_bot_2", "state": "gc_queued", "gc_queue_position": float64(7)}},
	}
	for _, tt := range tests {
		got, _ := list[tt.i].(map[string]any)
		for k, want := range tt.want {
			if v, ok := got[k]; !ok || v != want {
				t.Errorf("bot %d: %s = %#v (present %v), want %#v", tt.i, k, v, ok, want)
			}
		}
	}

	r = do(t, s, http.MethodGet, "/v1/bots/LEAGUE_BOT_3", auth)
	if r.code != 200 || r.body["name"] != "league_bot_3" {
		t.Errorf("one bot = %d %v", r.code, r.body)
	}
	r = do(t, s, http.MethodGet, "/v1/bots/nobody", auth)
	if r.code != 404 || errCode(r) != "not_found" {
		t.Errorf("missing bot = %d %v", r.code, r.body)
	}
}

func TestBotRetry(t *testing.T) {
	bots := &fakeBots{}
	bots.set(backoff("league_bot_3"))
	s := newTestServer(t, testKey, 0, bots)
	auth := map[string]string{"X-API-Key": testKey}

	if r := do(t, s, http.MethodPost, "/v1/bots/league_bot_3/retry", nil); r.code != 401 {
		t.Errorf("retry without key = %d", r.code)
	}
	if r := do(t, s, http.MethodPost, "/v1/bots/league_bot_3/retry", auth); r.code != 202 || r.body["status"] != "retry_requested" {
		t.Errorf("retry = %d %v", r.code, r.body)
	}
	if r := do(t, s, http.MethodPost, "/v1/bots/nobody/retry", auth); r.code != 404 || errCode(r) != "not_found" {
		t.Errorf("retry missing bot = %d %v", r.code, r.body)
	}
	if r := do(t, s, http.MethodGet, "/v1/bots/league_bot_3/retry", auth); r.code != 405 {
		t.Errorf("GET retry = %d", r.code)
	}
	if len(bots.retried) != 1 || bots.retried[0] != "league_bot_3" {
		t.Errorf("retried = %v", bots.retried)
	}
}

func TestBotsNeverLeakCredentials(t *testing.T) {
	bots := &fakeBots{}
	bots.set(ready("a"))
	s := newTestServer(t, "", 0, bots)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/bots", nil))
	for _, bad := range []string{"password", "shared_secret", "refresh_token"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Errorf("response mentions %s: %s", bad, rec.Body.String())
		}
	}
}

func TestRouting(t *testing.T) {
	s := newTestServer(t, "", 0, &fakeBots{})
	tests := []struct {
		method, path string
		code         int
		errCode      string
	}{
		{"GET", "/nope", 404, "not_found"},
		{"POST", "/livez", 405, "method_not_allowed"},
		{"DELETE", "/v1/bots", 405, "method_not_allowed"},
		{"POST", "/v1/lobbies", 501, "not_implemented"},
		{"GET", "/v1/lobbies/lby_1", 501, "not_implemented"},
		{"POST", "/lobby/create", 410, "gone"},
		{"GET", "/lobby/info", 410, "gone"},
		{"GET", "/bots", 410, "gone"},
	}
	for _, tt := range tests {
		r := do(t, s, tt.method, tt.path, nil)
		if r.code != tt.code || errCode(r) != tt.errCode {
			t.Errorf("%s %s = %d %v, want %d %s", tt.method, tt.path, r.code, r.body, tt.code, tt.errCode)
		}
	}
	if r := do(t, s, "POST", "/livez", nil); r.hdr.Get("Allow") != "GET, HEAD" {
		t.Errorf("Allow = %q", r.hdr.Get("Allow"))
	}
	if r := do(t, s, http.MethodHead, "/livez", nil); r.code != 200 {
		t.Errorf("HEAD /livez = %d", r.code)
	}
}

// A wrong method on a protected path must not reveal the Allow list to a
// caller without the key; the open probe paths still do.
func TestMethodFallbackNeedsKey(t *testing.T) {
	s := newTestServer(t, testKey, 0, &fakeBots{})
	tests := []struct {
		name, method, path string
		hdr                map[string]string
		code               int
		allow              string
	}{
		{"protected without key", "DELETE", "/v1/bots", nil, 401, ""},
		{"protected retry without key", "GET", "/v1/bots/a/retry", nil, 401, ""},
		{"protected with wrong key", "DELETE", "/v1/bots", map[string]string{"X-API-Key": "nope"}, 401, ""},
		{"protected with key", "DELETE", "/v1/bots", map[string]string{"X-API-Key": testKey}, 405, "GET, HEAD"},
		{"open probe without key", "POST", "/livez", nil, 405, "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := do(t, s, tt.method, tt.path, tt.hdr)
			if r.code != tt.code || r.hdr.Get("Allow") != tt.allow {
				t.Errorf("%s %s = %d Allow=%q, want %d Allow=%q", tt.method, tt.path, r.code, r.hdr.Get("Allow"), tt.code, tt.allow)
			}
		})
	}
}

func TestServeAndShutdown(t *testing.T) {
	s := newTestServer(t, "", 0, &fakeBots{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/livez")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("livez = %d", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("Serve after Shutdown = %v", err)
	}
	if resp, err := http.Get("http://" + ln.Addr().String() + "/livez"); err == nil {
		_ = resp.Body.Close()
		t.Error("server still answering after Shutdown")
	}
}
