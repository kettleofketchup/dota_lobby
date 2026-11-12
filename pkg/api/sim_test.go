package api

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/sim"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

// simServer is a server in simulation mode with one bot hosting one
// simulated lobby, which the service's lobby f.lobby points at.
func simServer(t *testing.T) (*Server, *sim.World, *sim.Client, *fakeLobbies, uint64) {
	t.Helper()
	w := sim.New(sim.Options{ReadyCommand: "!ready"})
	t.Cleanup(w.Close)
	c := w.Client(sim.Bot{Name: "bot1", AccountID: 4_000_000_001})
	if err := c.Connect(t.Context(), gc.Credentials{}); err != nil {
		t.Fatal(err)
	}
	c.SetPlaying(true)
	c.SayHello() // welcomed at once
	v, err := c.CreateLobby(t.Context(), gc.LobbyDetails{GameName: "Cup", PassKey: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	l := sampleLobby()
	l.GCLobbyID = v.LobbyID
	f := &fakeLobbies{lobby: l}
	s := New(Options{
		APIKey: testKey, Bots: &fakeBots{statuses: []bot.Status{ready("bot1")}}, Lobbies: f, Simulator: w,
	})
	return s, w, c, f, v.LobbyID
}

func TestSimRoutesOnlyInSimulation(t *testing.T) {
	s := lobbyServer(t, &fakeLobbies{lobby: sampleLobby()})
	for _, p := range []string{"/v1/sim/lobbies", "/v1/sim/settings"} {
		if r := send(t, s, "GET", p, "", nil); r.code != http.StatusNotFound || errCode(r.response) != "not_found" {
			t.Errorf("GET %s without simulation = %d %v", p, r.code, r.body)
		}
	}
	// The spec documents them all the same.
	if s.OpenAPI().Paths["/v1/sim/lobbies"] == nil {
		t.Error("sim operations missing from the spec")
	}
	if r := send(t, s, "GET", "/v1/bots", "", nil); r.body["simulation"] != false {
		t.Errorf("bots simulation = %v", r.body["simulation"])
	}

	simSrv, _, _, _, _ := simServer(t)
	if r := send(t, simSrv, "GET", "/v1/bots", "", nil); r.body["simulation"] != true {
		t.Errorf("bots simulation = %v", r.body["simulation"])
	}
	if r := do(t, simSrv, "GET", "/health", nil); r.body["simulation"] != true {
		t.Errorf("health simulation = %v", r.body["simulation"])
	}
}

func TestSimLobbyFlow(t *testing.T) {
	s, w, c, _, id := simServer(t)
	gcID := strconv.FormatUint(id, 10)
	base := "/v1/sim/lobbies/" + gcID

	r := send(t, s, "GET", "/v1/sim/lobbies", "", nil)
	lobbies, _ := r.body["lobbies"].([]any)
	if r.code != 200 || len(lobbies) != 1 || lobbies[0].(map[string]any)["gc_lobby_id"] != gcID {
		t.Fatalf("list = %d %v", r.code, r.body)
	}
	// The service's lobby ID finds the same lobby.
	r = send(t, s, "GET", "/v1/sim/lobbies/lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V", "", nil)
	if r.code != 200 || r.body["gc_lobby_id"] != gcID || r.body["password"] != "pw" || r.body["bot"] != "bot1" || r.body["state"] != "ui" {
		t.Fatalf("get by service id = %d %v", r.code, r.body)
	}

	steps := []struct {
		method, path, body string
		code               int
		errCode            string
	}{
		{"POST", base + "/players", `{"account_id": 11111111, "persona_name": "Alice"}`, 200, ""},
		{"POST", base + "/players", `{"account_id": 11111111}`, 409, "already_member"},
		{"POST", base + "/players", `{"account_id": 4000000001}`, 422, "invalid_request"},
		{"POST", base + "/players", `{"account_id": 0}`, 422, ""},
		{"POST", base + "/players/11111111/seat", `{"team": "dire", "slot": 3}`, 200, ""},
		{"POST", base + "/players/22222222/seat", `{"team": "dire"}`, 409, "not_member"},
		{"POST", base + "/players/11111111/seat", `{"team": "dire", "slot": 9}`, 422, ""},
		{"POST", base + "/start", "", 409, "invalid_state"},
		{"POST", base + "/end", `{"result": "radiant"}`, 409, "invalid_state"},
		{"POST", base + "/fail", `{"mode": "load_failed"}`, 409, "invalid_state"},
		{"POST", "/v1/sim/lobbies/42/players", `{"account_id": 1}`, 404, "not_found"},
	}
	for _, st := range steps {
		r := send(t, s, st.method, st.path, st.body, nil)
		if r.code != st.code || (st.errCode != "" && errCode(r.response) != st.errCode) {
			t.Errorf("%s %s %s = %d %v, want %d %s", st.method, st.path, st.body, r.code, r.body, st.code, st.errCode)
		}
	}
	info, _ := w.Lobby(id)
	if m := info.Members[1]; m.AccountID != 11111111 || m.Team != gc.TeamDire || m.Slot != 3 || m.PersonaName != "Alice" {
		t.Errorf("member = %+v", m)
	}

	// Chat before and after the host joins the lobby chat.
	r = send(t, s, "POST", base+"/players/11111111/ready", "", nil)
	if r.code != 200 || r.body["delivered"] != false {
		t.Errorf("ready before chat join = %d %v", r.code, r.body)
	}
	if _, err := c.JoinLobbyChat(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	r = send(t, s, "POST", base+"/chat", `{"account_id": 11111111, "text": "!start"}`, nil)
	if r.code != 200 || r.body["delivered"] != true {
		t.Errorf("say = %d %v", r.code, r.body)
	}
	chat, _ := r.body["lobby"].(map[string]any)["chat"].([]any)
	if len(chat) != 2 || chat[1].(map[string]any)["text"] != "!start" {
		t.Errorf("chat = %v", chat)
	}

	// Launch, start with a chosen match ID, end.
	if err := c.Launch(); err != nil {
		t.Fatal(err)
	}
	r = send(t, s, "POST", base+"/start", `{"match_id": "8000000001"}`, nil)
	if r.code != 200 || r.body["state"] != "run" || r.body["match_id"] != "8000000001" {
		t.Fatalf("start = %d %v", r.code, r.body)
	}
	r = send(t, s, "POST", base+"/end", `{"result": "dire"}`, nil)
	if r.code != 200 || r.body["state"] != "postgame" || r.body["outcome"] != float64(sim.OutcomeDireVictory) {
		t.Fatalf("end = %d %v", r.code, r.body)
	}
	r = send(t, s, "DELETE", base+"/players/11111111", "", nil)
	if r.code != 200 || len(r.body["members"].([]any)) != 1 {
		t.Errorf("leave = %d %v", r.code, r.body)
	}
	r = send(t, s, "POST", base+"/fail", `{"mode": "lost"}`, nil)
	if r.code != 200 {
		t.Errorf("fail = %d %v", r.code, r.body)
	}
	if _, ok := c.CurrentLobby(); ok {
		t.Error("lost lobby still in the bot's cache")
	}
}

func TestSimLobbyByServiceID(t *testing.T) {
	s, _, _, f, _ := simServer(t)
	f.lobby.GCLobbyID = 0
	if r := send(t, s, "GET", "/v1/sim/lobbies/lob_x", "", nil); r.code != 409 || errCode(r.response) != "invalid_state" {
		t.Errorf("no gc lobby yet = %d %v", r.code, r.body)
	}
	f.err = lobby.ErrNotFound
	if r := send(t, s, "GET", "/v1/sim/lobbies/lob_x", "", nil); r.code != 404 {
		t.Errorf("unknown lobby = %d %v", r.code, r.body)
	}
}

func TestSimSettings(t *testing.T) {
	s, w, _, _, _ := simServer(t)
	r := send(t, s, "GET", "/v1/sim/settings", "", nil)
	if r.code != 200 || r.body["auto_join"] != false || r.body["ready_command"] != "!ready" {
		t.Fatalf("settings = %d %v", r.code, r.body)
	}
	r = send(t, s, "PATCH", "/v1/sim/settings", `{"auto_join": true, "auto_join_delay_ms": 250, "auto_start": true}`, nil)
	if r.code != 200 || r.body["auto_join"] != true || r.body["auto_join_delay_ms"] != float64(250) || r.body["auto_ready"] != false {
		t.Fatalf("patch = %d %v", r.code, r.body)
	}
	if st := w.Settings(); !st.AutoJoin || st.AutoJoinDelay != 250*time.Millisecond || !st.AutoStart || st.AutoStartDelay != 0 {
		t.Errorf("world settings = %+v", st)
	}
	if r := send(t, s, "PATCH", "/v1/sim/settings", `{"auto_join_delay_ms": -1}`, nil); r.code != 422 {
		t.Errorf("negative delay = %d", r.code)
	}
}

// PATCHes of different fields sent together all apply.
func TestSimSettingsConcurrentPatches(t *testing.T) {
	s, w, _, _, _ := simServer(t)
	bodies := []string{`{"auto_join": true}`, `{"auto_ready": true}`, `{"auto_start": true}`}
	for run := range 50 {
		w.SetSettings(sim.Settings{})
		var wg sync.WaitGroup
		for _, b := range bodies {
			wg.Go(func() {
				if r := send(t, s, "PATCH", "/v1/sim/settings", b, nil); r.code != 200 {
					t.Errorf("patch %s = %d", b, r.code)
				}
			})
		}
		wg.Wait()
		if st := w.Settings(); !st.AutoJoin || !st.AutoReady || !st.AutoStart {
			t.Fatalf("run %d: settings %+v lost a change", run, st)
		}
	}
}

var _ Simulator = (*sim.World)(nil)
