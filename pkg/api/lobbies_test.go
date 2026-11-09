package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

// fakeLobbies records what the handlers pass and returns err (or the
// lobby) for every call.
type fakeLobbies struct {
	mu      sync.Mutex
	err     error
	lobby   lobby.Lobby
	created bool
	changed bool
	list    []lobby.Lobby
	next    string
	invite  lobby.InviteResult

	spec    lobby.Spec
	patch   lobby.Patch
	filter  lobby.Filter
	invited lobby.InviteRequest
	kicked  [2]any
	roster  lobby.Roster
	launch  lobby.LaunchRequest
	calls   []string
}

func (f *fakeLobbies) call(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeLobbies) Create(_ context.Context, s lobby.Spec) (lobby.Lobby, bool, error) {
	f.call("create")
	f.spec = s
	return f.lobby, f.created, f.err
}
func (f *fakeLobbies) Get(context.Context, string) (lobby.Lobby, error) {
	f.call("get")
	return f.lobby, f.err
}
func (f *fakeLobbies) List(_ context.Context, fl lobby.Filter) ([]lobby.Lobby, string, error) {
	f.call("list")
	f.filter = fl
	return f.list, f.next, f.err
}
func (f *fakeLobbies) Update(_ context.Context, _ string, p lobby.Patch) (lobby.Lobby, error) {
	f.call("update")
	f.patch = p
	return f.lobby, f.err
}
func (f *fakeLobbies) Close(context.Context, string) (lobby.Lobby, bool, error) {
	f.call("close")
	return f.lobby, f.changed, f.err
}
func (f *fakeLobbies) Invite(_ context.Context, _ string, r lobby.InviteRequest) (lobby.InviteResult, error) {
	f.call("invite")
	f.invited = r
	return f.invite, f.err
}
func (f *fakeLobbies) Kick(_ context.Context, _ string, a uint32, from string) (lobby.Lobby, error) {
	f.call("kick")
	f.kicked = [2]any{a, from}
	return f.lobby, f.err
}
func (f *fakeLobbies) SetRoster(_ context.Context, _ string, r lobby.Roster) (lobby.Lobby, error) {
	f.call("roster")
	f.roster = r
	return f.lobby, f.err
}
func (f *fakeLobbies) Launch(_ context.Context, _ string, r lobby.LaunchRequest) (lobby.Lobby, error) {
	f.call("launch")
	f.launch = r
	return f.lobby, f.err
}
func (f *fakeLobbies) FlipTeams(context.Context, string) (lobby.Lobby, error) {
	f.call("flip")
	return f.lobby, f.err
}
func (f *fakeLobbies) Shuffle(context.Context, string) (lobby.Lobby, error) {
	f.call("shuffle")
	return f.lobby, f.err
}

func sampleLobby() lobby.Lobby {
	verified := true
	o := int32(2)
	at := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	return lobby.Lobby{
		ID: "lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V", State: lobby.StateWaiting, Seq: 7, Bot: "bot1",
		GCLobbyID: 27110012345678901, LeagueVerified: &verified,
		Spec: lobby.Spec{
			ExternalRef: "game-42", Name: "Spring Cup", Password: "k3tt1e", LeagueID: 17929, GameMode: "cm",
			Region: "2", TVDelaySeconds: 120, Visibility: "public", Launch: "manual",
			Metadata: map[string]string{"round": "2"},
			Teams: lobby.Roster{
				Radiant: lobby.Team{TeamRef: "team-7", Name: "Ravens", Players: []lobby.Player{{AccountID: 11111111, Captain: true, InvitesSent: 1, LastInviteAt: at}}},
				Dire:    lobby.Team{TeamRef: "team-9", Players: []lobby.Player{{AccountID: 22222222}}},
			},
		},
		HostSeat: lobby.HostSeat{Strategy: "self_kick", Confirmed: true, Team: "player_pool"},
		Match:    lobby.Match{MatchID: 8123456789, Outcome: &o, RadiantTeamRef: "team-7"},
		Live: &lobby.Live{
			Members: []lobby.Member{{AccountID: 900, Seat: "player_pool"}, {AccountID: 11111111, Seat: "radiant"}, {AccountID: 99999999, Seat: "player_pool"}},
			Pending: []uint64{gc.SteamID64(22222222)},
		},
		CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(30 * time.Minute),
	}
}

func lobbyServer(t *testing.T, f *fakeLobbies) *Server {
	t.Helper()
	bots := &fakeBots{statuses: []bot.Status{{Name: "bot1", SteamID: gc.SteamID64(900)}}}
	return New(Options{APIKey: testKey, Bots: bots, Lobbies: f})
}

type bodyResponse struct {
	response
	raw string
}

func send(t *testing.T, s *Server, method, path, body string, hdr map[string]string) bodyResponse {
	t.Helper()
	var rd *bytes.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("X-API-Key", testKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	out := bodyResponse{response: response{code: rec.Code, hdr: rec.Header()}, raw: rec.Body.String()}
	if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
		t.Fatalf("%s %s: body %q: %v", method, path, rec.Body.String(), err)
	}
	return out
}

const createBody = `{
  "external_ref": "game-42", "name": "Spring Cup", "password": "k3tt1e", "league_id": 17929,
  "game_mode": "cm", "region": 2, "tv_delay_seconds": 120, "launch": "auto", "admins": [12345678],
  "teams": {
    "radiant": {"team_ref": "team-7", "name": "Ravens", "players": [{"account_id": 11111111, "captain": true}]},
    "dire": {"team_ref": "team-9", "players": [{"steam_id": "76561197982487950"}]}
  },
  "metadata": {"round": "2"}
}`

func TestLobbyCreate(t *testing.T) {
	f := &fakeLobbies{lobby: sampleLobby(), created: true}
	s := lobbyServer(t, f)
	r := send(t, s, "POST", "/v1/lobbies", createBody, nil)
	if r.code != http.StatusAccepted || r.hdr.Get("Location") != "/v1/lobbies/lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V" {
		t.Fatalf("create = %d %s %v", r.code, r.hdr.Get("Location"), r.body)
	}
	sp := f.spec
	if sp.ExternalRef != "game-42" || sp.Password.Reveal() != "k3tt1e" || sp.GameMode != "cm" || sp.Region != "2" ||
		sp.Launch != "auto" || sp.Teams.Radiant.Players[0].AccountID != 11111111 || !sp.Teams.Radiant.Players[0].Captain ||
		sp.Teams.Dire.Players[0].AccountID != 22222222 || sp.Metadata["round"] != "2" || sp.Admins[0] != 12345678 {
		t.Errorf("spec = %+v", sp)
	}
	if r.body["password"] != "k3tt1e" || r.body["gc_lobby_id"] != "27110012345678901" || r.body["game_mode"] != "cm" || r.body["region"] != float64(2) {
		t.Errorf("body = %v", r.body)
	}
	f.created = false
	if r := send(t, s, "POST", "/v1/lobbies", createBody, nil); r.code != http.StatusOK {
		t.Errorf("replay = %d", r.code)
	}
}

func TestLobbyCreateRejects(t *testing.T) {
	s := lobbyServer(t, &fakeLobbies{lobby: sampleLobby(), created: true})
	tests := []struct {
		name string
		body string
		ct   string
		code int
		err  string
		msg  string
	}{
		{"not json content type", createBody, "text/plain", 415, "unsupported_media_type", ""},
		{"unknown field", `{"external_ref":"a","nmae":"x","teams":{}}`, "", 400, "invalid_request", "nmae"},
		{"truncated", `{"external_ref":`, "", 400, "invalid_request", "invalid JSON"},
		{"syntax", `{"external_ref" 1}`, "", 400, "invalid_request", "syntax error"},
		{"two values", `{"teams":{}} {}`, "", 400, "invalid_request", "more than one"},
		{"no teams", `{"external_ref":"a","name":"x"}`, "", 400, "invalid_request", "teams is required"},
		{"both ids", `{"teams":{"radiant":{"players":[{"account_id":1,"steam_id":"76561197960265729"}]}}}`, "", 400, "invalid_request", "not both"},
		{"no id", `{"teams":{"radiant":{"players":[{"captain":true}]}}}`, "", 400, "invalid_request", "required"},
		{"group steam id", `{"teams":{"dire":{"players":[{"steam_id":"103582791429521412"}]}}}`, "", 400, "invalid_request", "steam_id"},
		{"steam id as number", `{"teams":{"dire":{"players":[{"steam_id":76561197960265729}]}}}`, "", 400, "invalid_request", "steam_id"},
		{"negative account", `{"teams":{"dire":{"players":[{"account_id":-1}]}}}`, "", 400, "invalid_request", "account_id"},
		{"bad game mode type", `{"game_mode":true,"teams":{}}`, "", 400, "invalid_request", "name or a non-negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tt.ct != "" {
				hdr["Content-Type"] = tt.ct
			}
			r := send(t, s, "POST", "/v1/lobbies", tt.body, hdr)
			if r.code != tt.code || errCode(r.response) != tt.err {
				t.Fatalf("= %d %v, want %d %s", r.code, r.body, tt.code, tt.err)
			}
			if msg := r.body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, tt.msg) {
				t.Errorf("message %q does not mention %q", msg, tt.msg)
			}
		})
	}
	big := `{"name":"` + strings.Repeat("x", DefaultMaxBodyBytes) + `"}`
	if r := send(t, s, "POST", "/v1/lobbies", big, nil); r.code != 413 || errCode(r.response) != "payload_too_large" {
		t.Errorf("big body = %d %v", r.code, r.body)
	}
}

func TestLobbyErrorMapping(t *testing.T) {
	tests := []struct {
		err   error
		code  int
		ecode string
	}{
		{fmt.Errorf("%w: name must be 1-64 characters", lobby.ErrInvalid), 400, "invalid_request"},
		{fmt.Errorf("%w: lob_x", lobby.ErrNotFound), 404, "not_found"},
		{lobby.ErrConflict, 409, "conflict"},
		{fmt.Errorf("%w: lobby is in_game", lobby.ErrInvalidState), 409, "invalid_state"},
		{fmt.Errorf("%w: missing [22222222]", lobby.ErrNotReady), 409, "not_ready"},
		{lobby.ErrHostNotSeated, 409, "host_not_seated"},
		{lobby.ErrLeagueNotHeld, 422, "league_not_held"},
		{&lobby.UnknownNameError{Field: "game_mode", Value: "blitz"}, 422, "unknown_game_mode"},
		{&lobby.UnknownNameError{Field: "region", Value: "mars"}, 422, "unknown_region"},
		{&lobby.InviteLimitedError{RetryAfter: 1500 * time.Millisecond}, 429, "invite_limited"},
		{lobby.ErrGCTimeout, 504, "gc_timeout"},
		{lobby.ErrUnavailable, 503, "unavailable"},
		{errors.New("disk on fire"), 503, "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.ecode, func(t *testing.T) {
			s := lobbyServer(t, &fakeLobbies{err: tt.err})
			r := send(t, s, "GET", "/v1/lobbies/lob_x", "", nil)
			if r.code != tt.code || errCode(r.response) != tt.ecode {
				t.Errorf("= %d %v, want %d %s", r.code, r.body, tt.code, tt.ecode)
			}
			if tt.code == 429 && r.hdr.Get("Retry-After") != "2" {
				t.Errorf("Retry-After %q", r.hdr.Get("Retry-After"))
			}
			if strings.Contains(r.raw, "disk on fire") {
				t.Error("internal error text leaked")
			}
		})
	}
}

func TestLobbyView(t *testing.T) {
	f := &fakeLobbies{lobby: sampleLobby()}
	s := lobbyServer(t, f)
	r := send(t, s, "GET", "/v1/lobbies/lob_x", "", nil)
	if r.code != 200 {
		t.Fatalf("get = %d %v", r.code, r.body)
	}
	b := r.body
	teams := b["teams"].(map[string]any)
	rad := teams["radiant"].(map[string]any)["players"].([]any)[0].(map[string]any)
	dire := teams["dire"].(map[string]any)
	direP := dire["players"].([]any)[0].(map[string]any)
	if rad["status"] != "seated" || rad["seat"] != "radiant" || rad["steam_id"] != "76561197971376839" || rad["invites_sent"] != float64(1) {
		t.Errorf("radiant player %v", rad)
	}
	if direP["status"] != "invited" || direP["seat"] != nil || dire["name"] != nil {
		t.Errorf("dire %v", dire)
	}
	strangers := b["strangers"].([]any)
	if len(strangers) != 1 || strangers[0].(map[string]any)["account_id"] != float64(99999999) {
		t.Errorf("strangers %v (the host must not be one)", strangers)
	}
	if p := b["pending_invites"].([]any); len(p) != 1 || p[0] != "76561197982487950" {
		t.Errorf("pending %v", p)
	}
	m := b["match"].(map[string]any)
	if m["match_id"] != "8123456789" || m["winner_side"] != "radiant" || m["winner_team_ref"] != "team-7" || m["scored"] != true || m["outcome_name"] != "radiant_victory" {
		t.Errorf("match %v", m)
	}
	if hs := b["host_seat"].(map[string]any); hs["strategy"] != "self_kick" || hs["team"] != "player_pool" {
		t.Errorf("host seat %v", hs)
	}
	if b["league_id"] != float64(17929) || b["state_reason"] != nil || b["ended_at"] != nil || b["password"] != "k3tt1e" {
		t.Errorf("lobby %v", b)
	}
}

func TestLobbyListNeverHasPassword(t *testing.T) {
	f := &fakeLobbies{list: []lobby.Lobby{sampleLobby()}, next: "lob_00"}
	s := lobbyServer(t, f)
	r := send(t, s, "GET", "/v1/lobbies?state=waiting,ready&external_ref=game-42&active=true&limit=10&cursor=lob_99", "", nil)
	if r.code != 200 || r.body["next_cursor"] != "lob_00" {
		t.Fatalf("list = %d %v", r.code, r.body)
	}
	if strings.Contains(r.raw, "k3tt1e") || strings.Contains(r.raw, `"password"`) {
		t.Errorf("list carries the password: %s", r.raw)
	}
	fl := f.filter
	if len(fl.States) != 2 || fl.States[1] != lobby.StateReady || fl.ExternalRef != "game-42" || !fl.Active || fl.Limit != 10 || fl.Cursor != "lob_99" {
		t.Errorf("filter = %+v", fl)
	}
	for _, q := range []string{"state=bogus", "active=maybe", "limit=0", "limit=201"} {
		if r := send(t, s, "GET", "/v1/lobbies?"+q, "", nil); r.code != 400 {
			t.Errorf("%s = %d", q, r.code)
		}
	}
}

func TestLobbyActions(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		code                     int
		check                    func(t *testing.T, f *fakeLobbies)
	}{
		{"patch", "PATCH", "/v1/lobbies/lob_x", `{"name":"New","game_mode":23,"password":""}`, 200, func(t *testing.T, f *fakeLobbies) {
			p := f.patch
			if *p.Name != "New" || *p.GameMode != "23" || p.Password == nil || p.Password.IsSet() || p.Region != nil {
				t.Errorf("patch %+v", p)
			}
		}},
		{"delete", "DELETE", "/v1/lobbies/lob_x", "", 202, nil},
		{"invite players", "POST", "/v1/lobbies/lob_x/invite", `{"players":[11111111]}`, 200, func(t *testing.T, f *fakeLobbies) {
			if len(f.invited.Players) != 1 || f.invited.Missing {
				t.Errorf("invite %+v", f.invited)
			}
		}},
		{"invite missing", "POST", "/v1/lobbies/lob_x/invite", `{"missing":true}`, 200, nil},
		{"invite neither", "POST", "/v1/lobbies/lob_x/invite", `{}`, 400, nil},
		{"invite both", "POST", "/v1/lobbies/lob_x/invite", `{"players":[1],"missing":true}`, 400, nil},
		{"kick", "POST", "/v1/lobbies/lob_x/kick", `{"account_id":99999999}`, 202, func(t *testing.T, f *fakeLobbies) {
			if f.kicked != [2]any{uint32(99999999), "team"} {
				t.Errorf("kick %v", f.kicked)
			}
		}},
		{"kick needs an account", "POST", "/v1/lobbies/lob_x/kick", `{"from":"lobby"}`, 400, nil},
		{"roster", "POST", "/v1/lobbies/lob_x/roster", `{"teams":{"radiant":{"players":[{"account_id":5}]},"dire":{}}}`, 200, func(t *testing.T, f *fakeLobbies) {
			if f.roster.Radiant.Players[0].AccountID != 5 {
				t.Errorf("roster %+v", f.roster)
			}
		}},
		{"roster needs teams", "POST", "/v1/lobbies/lob_x/roster", `{}`, 400, nil},
		{"launch without a body", "POST", "/v1/lobbies/lob_x/launch", "", 202, func(t *testing.T, f *fakeLobbies) {
			if f.launch.Force || f.launch.Trigger != lobby.TriggerAPI {
				t.Errorf("launch %+v", f.launch)
			}
		}},
		{"launch forced", "POST", "/v1/lobbies/lob_x/launch", `{"force":true}`, 202, func(t *testing.T, f *fakeLobbies) {
			if !f.launch.Force {
				t.Errorf("launch %+v", f.launch)
			}
		}},
		{"flip", "POST", "/v1/lobbies/lob_x/teams/flip", "", 202, nil},
		{"shuffle", "POST", "/v1/lobbies/lob_x/teams/shuffle", "", 202, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLobbies{lobby: sampleLobby(), changed: true, invite: lobby.InviteResult{Invited: []uint32{1}, Skipped: []lobby.InviteSkip{{AccountID: 2, Reason: lobby.SkipCap}}}}
			r := send(t, lobbyServer(t, f), tt.method, tt.path, tt.body, nil)
			if r.code != tt.code {
				t.Fatalf("= %d %v, want %d", r.code, r.body, tt.code)
			}
			if tt.check != nil {
				tt.check(t, f)
			}
			// Only the create and GET responses carry the password.
			if strings.Contains(r.raw, "k3tt1e") || strings.Contains(r.raw, `"password"`) {
				t.Errorf("response carries the password: %s", r.raw)
			}
		})
	}
}

func TestLobbyRoutesNeedKey(t *testing.T) {
	s := lobbyServer(t, &fakeLobbies{lobby: sampleLobby()})
	for _, p := range []string{"/v1/lobbies", "/v1/lobbies/lob_x", "/v1/lobbies/lob_x/launch"} {
		if r := do(t, s, http.MethodGet, p, nil); r.code != 401 {
			t.Errorf("GET %s without key = %d", p, r.code)
		}
	}
}
