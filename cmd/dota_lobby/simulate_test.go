package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/webhook"
)

// hookReceiver records the webhook deliveries it got, after checking each
// signature the way a consumer would.
type hookReceiver struct {
	t      *testing.T
	secret string

	mu     sync.Mutex
	events []delivered
}

type delivered struct {
	ID, Type   string
	Seq        int64
	LobbyID    string
	LobbyState string
	Data       map[string]any
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := webhook.Verify([]byte(h.secret), r.Header.Get(webhook.HeaderTimestamp), r.Header.Get(webhook.HeaderSignature), body, time.Now(), 0); err != nil {
		h.t.Errorf("delivery with a bad signature: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var env struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Seq   int64  `json:"seq"`
		Lobby struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"lobby"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		h.t.Errorf("delivery body %q: %v", body, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if env.ID != r.Header.Get(webhook.HeaderEventID) || env.Type != r.Header.Get(webhook.HeaderEventType) {
		h.t.Errorf("headers %v do not match the body %s", r.Header, body)
	}
	h.mu.Lock()
	h.events = append(h.events, delivered{ID: env.ID, Type: env.Type, Seq: env.Seq, LobbyID: env.Lobby.ID, LobbyState: env.Lobby.State, Data: env.Data})
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *hookReceiver) received() []delivered {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}

// waitEvent waits for a delivered event of the given type and returns it.
func (h *hookReceiver) waitEvent(t *testing.T, typ string, logs fmt.Stringer) delivered {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range h.received() {
			if e.Type == typ {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	var types []string
	for _, e := range h.received() {
		types = append(types, e.Type)
	}
	t.Fatalf("no %s webhook; got %v\n%s", typ, types, logs)
	return delivered{}
}

type apiClient struct {
	t    *testing.T
	base string
}

func (c apiClient) call(method, path, body string, want int) map[string]any {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("X-API-Key", "k")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, raw, want)
	}
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("%s %s: %q: %v", method, path, raw, err)
		}
	}
	return out
}

// A whole lobby against the simulator, through the real service: create,
// invites, auto-join, chat ready, launch, match start and end, with every
// event delivered as a signed webhook.
func TestServeSimulatedLobbyLifecycle(t *testing.T) {
	hooks := &hookReceiver{t: t, secret: "hook-secret"}
	receiver := httptest.NewServer(hooks)
	defer receiver.Close()

	addr := freeAddr(t)
	_, port, _ := net.SplitHostPort(addr)
	bots := `bots:
  - name: sim1
    leagues: [17000]
lobby:
  auto_launch_delay: 100ms
  postgame_linger: 1h
  chat: { enabled: true, send_interval: 10ms, start_countdown: 100ms }
simulation:
  auto_join: true
  auto_join_delay: 20ms
  auto_ready: true
  auto_start: true
  auto_start_delay: 20ms
webhooks:
  - url: ` + receiver.URL + `/hooks
`
	secrets := "secrets:\n  api_key: k\n  webhook_secrets:\n    \"" + receiver.URL + "/hooks\": \"hook-secret\"\n"
	flags, stateDir := writeConfig(t, port, bots, secrets)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _, stderr := newCLI()
	done := make(chan int, 1)
	go func() { done <- c.run(ctx, append([]string{"serve", "--simulate"}, flags...)) }()
	api := apiClient{t: t, base: "http://" + addr}

	// The bot reaches gc_ready without credentials, and says it is
	// simulated.
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(api.base + "/health")
		if err == nil {
			var h map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&h)
			_ = resp.Body.Close()
			if h["bots_ready"] == float64(1) {
				if h["simulation"] != true {
					t.Fatalf("health = %v", h)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("bot never ready: %v\n%s", err, stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "SIMULATION MODE") {
		t.Errorf("no simulation warning:\n%s", stderr)
	}
	if b := api.call("GET", "/v1/bots", "", 200); b["simulation"] != true {
		t.Errorf("bots = %v", b)
	}
	// The league list comes from the simulated GC.
	deadline = time.Now().Add(10 * time.Second)
	for {
		inv := api.call("GET", "/v1/bots/sim1/leagues", "", 200)
		if ls, _ := inv["leagues"].([]any); len(ls) == 1 && ls[0].(map[string]any)["verified"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("league never verified: %v", inv)
		}
		time.Sleep(20 * time.Millisecond)
	}

	l := api.call("POST", "/v1/lobbies", `{
	  "external_ref": "sim-1", "name": "Sim Cup", "password": "pw", "league_id": 17000, "game_mode": "cm",
	  "ready_check": true,
	  "teams": {
	    "radiant": {"team_ref": "team-r", "players": [{"account_id": 11111111}]},
	    "dire": {"team_ref": "team-d", "players": [{"account_id": 22222222}]}
	  }
	}`, http.StatusAccepted)
	id, _ := l["id"].(string)

	created := hooks.waitEvent(t, "lobby.created", stderr)
	if created.LobbyID != id || created.Data["league_verified"] != true {
		t.Errorf("lobby.created = %+v", created)
	}
	hooks.waitEvent(t, "lobby.ready", stderr)

	// The simulated lobby shows what the service asked for, and the
	// players' ready lines in its chat.
	s := api.call("GET", "/v1/sim/lobbies/"+id, "", 200)
	if s["gc_lobby_id"] != created.Data["gc_lobby_id"] || s["league_id"] != float64(17000) || s["password"] != "pw" ||
		s["game_mode"] != float64(2) || s["name"] != "Sim Cup" {
		t.Errorf("sim lobby = %v", s)
	}
	var said []string
	for _, line := range s["chat"].([]any) {
		m := line.(map[string]any)
		if m["text"] == "!ready" && m["delivered"] == true {
			said = append(said, fmt.Sprint(m["account_id"]))
		}
	}
	if len(said) != 2 {
		t.Errorf("ready lines = %v; chat %v", said, s["chat"])
	}

	api.call("POST", "/v1/lobbies/"+id+"/launch", "", http.StatusAccepted)
	started := hooks.waitEvent(t, "match.started", stderr)
	matchID, _ := started.Data["match_id"].(string)
	if matchID == "" || started.Data["radiant_team_ref"] != "team-r" {
		t.Errorf("match.started = %+v", started)
	}
	if got := api.call("GET", "/v1/lobbies/"+id, "", 200); got["state"] != "in_game" {
		t.Errorf("lobby after start = %v", got["state"])
	}

	api.call("POST", "/v1/sim/lobbies/"+id+"/end", `{"result": "dire"}`, 200)
	ended := hooks.waitEvent(t, "match.ended", stderr)
	if ended.Data["match_id"] != matchID || ended.Data["winner_side"] != "dire" ||
		ended.Data["winner_team_ref"] != "team-d" || ended.Data["outcome_name"] != "dire_victory" || ended.LobbyState != "post_game" {
		t.Errorf("match.ended = %+v", ended)
	}

	// Every event of the lobby arrived once, in order, signed.
	var types []string
	seq := int64(0)
	seen := map[string]bool{}
	for _, e := range hooks.received() {
		if e.LobbyID != id || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		if e.Seq <= seq {
			t.Errorf("%s seq %d after %d", e.Type, e.Seq, seq)
		}
		seq = e.Seq
		types = append(types, e.Type)
	}
	for _, want := range []string{
		"lobby.created", "lobby.player_invited", "lobby.player_joined", "lobby.player_seated", "lobby.player_ready",
		"lobby.ready", "lobby.launching", "match.started", "match.ended",
	} {
		if !slices.Contains(types, want) {
			t.Errorf("no %s webhook in %v", want, types)
		}
	}
	if i, j := slices.Index(types, "lobby.ready"), slices.Index(types, "match.started"); i < 0 || j < i {
		t.Errorf("events out of order: %v", types)
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("serve exit %d\n%s", code, stderr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop")
	}
	// The simulated run kept its own database.
	if !fileExists(stateDir+"/dota_lobby-simulation.db") || fileExists(stateDir+"/dota_lobby.db") {
		t.Errorf("databases in %s are not the simulation's", stateDir)
	}
}

// --simulate refuses a config with real credentials.
func TestServeSimulateRefusesCredentials(t *testing.T) {
	_, port, _ := net.SplitHostPort(freeAddr(t))
	flags, _ := writeConfig(t, port, "bots: [{ name: real1 }]\n", "secrets:\n  bots:\n    real1: { password: \"hunter2\" }\n")
	c, _, stderr := newCLI()
	if got := c.run(context.Background(), append([]string{"serve", "--simulate"}, flags...)); got != exitError {
		t.Fatalf("exit %d, want %d\n%s", got, exitError, stderr)
	}
	if !strings.Contains(stderr.String(), "simulation mode refuses bot real1") || strings.Contains(stderr.String(), "hunter2") {
		t.Errorf("stderr = %s", stderr)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
