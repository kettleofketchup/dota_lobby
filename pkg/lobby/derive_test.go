package lobby

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

var (
	t0      = time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	hostID  = gc.SteamID64(900)
	cfgPure = config.LobbyConfig{
		HostSeatStrategy:    []string{SeatCaster, SeatSelfKick},
		MaxLaunchAttempts:   3,
		PendingTimeout:      10 * time.Minute,
		AutoLaunchDelay:     10 * time.Second,
		IdleTimeout:         30 * time.Minute,
		LaunchSettleTimeout: 2 * time.Minute,
		ServerStallTimeout:  10 * time.Minute,
		IngameBackstop:      3 * time.Hour,
		PostgameLinger:      30 * time.Second,
		EnforceCooldown:     10 * time.Second,
	}
)

func mem(account uint32, team gc.Team) gc.Member {
	return gc.Member{SteamID: gc.SteamID64(account), Team: team, TeamKnown: true}
}

func hostAt(team gc.Team) gc.Member { return gc.Member{SteamID: hostID, Team: team, TeamKnown: true} }

func view(state gc.LobbyState, members ...gc.Member) *gc.LobbyView {
	return &gc.LobbyView{LobbyID: 77, LeaderID: hostID, State: state, Members: members}
}

// rostered is a lobby with 1 and 2 on Radiant, 3 on Dire.
func rostered(state State, since time.Time) Lobby {
	return Lobby{
		ID: "lob_x", State: state, StateSince: since, CreatedAt: t0, GCLobbyID: 77, Bot: "bot1",
		HostSeat: HostSeat{Strategy: SeatSelfKick, Confirmed: true, Next: 1},
		Spec: Spec{Name: "x", Launch: LaunchManual, Teams: Roster{
			Radiant: Team{TeamRef: "team-7", Players: []Player{{AccountID: 1}, {AccountID: 2}}},
			Dire:    Team{TeamRef: "team-9", Players: []Player{{AccountID: 3}}},
		}},
	}
}

func seatedView(state gc.LobbyState) *gc.LobbyView {
	return view(state, hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant), mem(2, gc.TeamRadiant), mem(3, gc.TeamDire))
}

func types(evs []newEvent) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func kinds(as []action) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, fmt.Sprintf("%s:%d", a.Kind, a.Account))
	}
	return out
}

func TestDerive(t *testing.T) {
	outcome := func(v *gc.LobbyView, o int32, match uint64) *gc.LobbyView {
		v.Outcome, v.MatchID = o, match
		return v
	}
	withServer := func(l Lobby, at time.Time) Lobby { l.Match.ServerAt = at; return l }
	attempts := func(l Lobby, n int) Lobby { l.LaunchAttempts = n; return l }
	stale := func(l Lobby, id uint64) Lobby { l.Match.StaleMatchID = id; return l }
	auto := func(l Lobby) Lobby { l.Spec.Launch = LaunchAuto; return l }
	ready := func(in *inputs) { in.BotReady = true }

	tests := []struct {
		name    string
		lobby   Lobby
		now     time.Duration // after t0
		view    *gc.LobbyView
		in      func(in *inputs)
		state   State
		reason  string
		events  []string
		actions []string
	}{
		{"pending waits", Lobby{State: StatePending, CreatedAt: t0}, time.Minute, nil, nil, StatePending, "", []string{}, []string{}},
		{"pending times out", Lobby{State: StatePending, CreatedAt: t0}, 10 * time.Minute, nil, nil, StateTimedOut, "pending_timeout", []string{}, []string{}},
		{"creating is left to the create", rostered(StateCreating, t0), time.Hour, nil, ready, StateCreating, "", []string{}, []string{}},
		{"terminal stays", rostered(StateFinished, t0), time.Hour, nil, ready, StateFinished, "", []string{}, []string{}},
		{"bot lost", rostered(StateWaiting, t0), time.Second, seatedView(gc.LobbyUI), func(in *inputs) { in.BotLost = true }, StateFailed, "bot_lost", []string{}, []string{}},

		{"waiting to ready", rostered(StateWaiting, t0), time.Second, seatedView(gc.LobbyUI), ready, StateReady, "", []string{"lobby.ready"}, []string{}},
		{"waiting stays without the roster", rostered(StateWaiting, t0), time.Second, view(gc.LobbyUI, hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant)), ready, StateWaiting, "", []string{}, []string{}},
		{"ready to waiting on leave", rostered(StateReady, t0), time.Second, view(gc.LobbyUI, hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant), mem(3, gc.TeamDire)), ready, StateWaiting, "", []string{"lobby.unready"}, []string{}},
		{"stale bot: no view transitions", rostered(StateWaiting, t0), time.Second, seatedView(gc.LobbyUI), nil, StateWaiting, "", []string{}, []string{}},
		{"idle timeout destroys", rostered(StateWaiting, t0), 30 * time.Minute, seatedView(gc.LobbyUI), ready, StateTimedOut, "idle_timeout", []string{}, []string{"destroy:0"}},
		{"idle timeout while stale sends nothing", rostered(StateReady, t0), 31 * time.Minute, nil, nil, StateTimedOut, "idle_timeout", []string{}, []string{}},
		{"auto launch after the delay", auto(rostered(StateReady, t0)), 10 * time.Second, seatedView(gc.LobbyUI), ready, StateLaunching, TriggerAuto, []string{"lobby.launching"}, []string{}},
		{"auto launch waits for the delay", auto(rostered(StateReady, t0)), 9 * time.Second, seatedView(gc.LobbyUI), ready, StateReady, "", []string{}, []string{}},
		{"auto launch stops when exhausted", attempts(auto(rostered(StateReady, t0)), 3), time.Minute, seatedView(gc.LobbyUI), ready, StateReady, "", []string{}, []string{}},
		{"auto launch needs the host off the teams", auto(rostered(StateReady, t0)), time.Minute,
			view(gc.LobbyUI, hostAt(gc.TeamRadiant), mem(1, gc.TeamRadiant), mem(2, gc.TeamRadiant), mem(3, gc.TeamDire)), ready, StateReady, "", []string{}, []string{}},
		{"lobby lost after the grace", rostered(StateWaiting, t0), time.Minute, nil, func(in *inputs) { in.BotReady, in.MissingFor = true, DefaultLostGrace }, StateFailed, "lobby_lost", []string{}, []string{}},
		{"lobby missing within the grace", rostered(StateWaiting, t0), time.Minute, nil, func(in *inputs) { in.BotReady, in.MissingFor = true, time.Second }, StateWaiting, "", []string{}, []string{}},
		{"launched elsewhere", rostered(StateWaiting, t0), time.Second, seatedView(gc.LobbyServerAssign), ready, StateLaunching, "launched_elsewhere", []string{}, []string{}},

		{"launching to in game", rostered(StateLaunching, t0), time.Second, outcome(seatedView(gc.LobbyRun), 0, 8123456789), ready, StateInGame, "", []string{"match.started"}, []string{}},
		{"launching waits in server assign", rostered(StateLaunching, t0), time.Second, seatedView(gc.LobbyServerAssign), ready, StateLaunching, "", []string{}, []string{}},
		{"launch ignored", attempts(rostered(StateLaunching, t0), 1), 2 * time.Minute, seatedView(gc.LobbyUI), ready, StateWaiting, "ignored", []string{"lobby.launch_failed"}, []string{}},
		{"launch ignored, attempts exhausted", attempts(rostered(StateLaunching, t0), 3), 2 * time.Minute, seatedView(gc.LobbyUI), ready, StateWaiting, "launch_attempts_exhausted", []string{"lobby.launch_failed"}, []string{}},
		{"launch settling", rostered(StateLaunching, t0), time.Minute, seatedView(gc.LobbyUI), ready, StateLaunching, "", []string{}, []string{}},
		{"run back to ui is a failed load", withServer(rostered(StateLaunching, t0), t0), time.Second, seatedView(gc.LobbyUI), ready, StateWaiting, "load_failed", []string{"lobby.launch_failed"}, []string{}},
		{"a stale match id is not a failed load", stale(rostered(StateLaunching, t0), 9), time.Second, outcome(seatedView(gc.LobbyUI), 0, 9), ready, StateLaunching, "", []string{}, []string{}},
		{"a stale match id still settles", stale(rostered(StateLaunching, t0), 9), 2 * time.Minute, outcome(seatedView(gc.LobbyUI), 0, 9), ready, StateWaiting, "ignored", []string{"lobby.launch_failed"}, []string{}},
		{"a new match id in ui is a failed load", stale(rostered(StateLaunching, t0), 9), time.Second, outcome(seatedView(gc.LobbyUI), 0, 10), ready, StateWaiting, "load_failed", []string{"lobby.launch_failed"}, []string{}},
		{"a stale match id waits in server assign", stale(rostered(StateLaunching, t0), 9), time.Second, outcome(seatedView(gc.LobbyServerAssign), 0, 9), ready, StateLaunching, "", []string{}, []string{}},
		{"server stall", withServer(rostered(StateLaunching, t0), t0), 10 * time.Minute, seatedView(gc.LobbyServerSetup), ready, StateTimedOut, "server_stall", []string{"lobby.launch_failed"}, []string{}},
		{"straight to postgame", rostered(StateLaunching, t0), time.Second, outcome(seatedView(gc.LobbyPostGame), 3, 9), ready, StatePostGame, "", []string{"match.started", "match.ended"}, []string{}},

		{"in game to post game", rostered(StateInGame, t0), time.Hour, outcome(seatedView(gc.LobbyPostGame), 2, 9), ready, StatePostGame, "", []string{"match.ended"}, []string{}},
		{"run with a scored outcome ends", rostered(StateInGame, t0), time.Hour, outcome(seatedView(gc.LobbyRun), 3, 9), ready, StatePostGame, "", []string{"match.ended"}, []string{}},
		{"run with an unscored outcome waits for postgame", rostered(StateInGame, t0), time.Hour, outcome(seatedView(gc.LobbyRun), 66, 9), ready, StateInGame, "", []string{}, []string{}},
		{"postgame with an unscored outcome ends", rostered(StateInGame, t0), time.Hour, outcome(seatedView(gc.LobbyPostGame), 68, 9), ready, StatePostGame, "", []string{"match.ended"}, []string{}},
		{"in game back to ui", rostered(StateInGame, t0), time.Minute, outcome(seatedView(gc.LobbyUI), 0, 9), ready, StateWaiting, "load_failed", []string{"lobby.launch_failed"}, []string{}},
		{"in game back to ui with a scored outcome ends", rostered(StateInGame, t0), time.Hour, outcome(seatedView(gc.LobbyUI), 2, 9), ready, StatePostGame, "", []string{"match.ended"}, []string{}},
		{"in game backstop", rostered(StateInGame, t0), 3 * time.Hour, outcome(seatedView(gc.LobbyRun), 0, 9), ready, StateTimedOut, "ingame_backstop", []string{}, []string{}},
		{"in game stall", withServer(rostered(StateInGame, t0), t0), 10 * time.Minute, outcome(seatedView(gc.LobbyServerSetup), 0, 9), ready, StateTimedOut, "server_stall", []string{}, []string{}},
		{"in game lobby gone", rostered(StateInGame, t0), time.Hour, nil, func(in *inputs) { in.BotReady, in.MissingFor = true, time.Minute }, StateFailed, "lost_in_game", []string{}, []string{}},

		{"post game lingers", rostered(StatePostGame, t0), 10 * time.Second, outcome(seatedView(gc.LobbyPostGame), 2, 9), ready, StatePostGame, "", []string{}, []string{}},
		{"post game linger then leave", rostered(StatePostGame, t0), 30 * time.Second, outcome(seatedView(gc.LobbyPostGame), 2, 9), ready, StateFinished, "postgame_linger", []string{}, []string{"leave:0"}},
		{"post game lobby gone", rostered(StatePostGame, t0), time.Second, nil, ready, StateFinished, "lobby_gone", []string{}, []string{}},

		{"closing waits for the destroy", rostered(StateClosing, t0), time.Second, seatedView(gc.LobbyUI), ready, StateClosing, "", []string{}, []string{}},
		{"closing done", rostered(StateClosing, t0), time.Second, nil, ready, StateClosed, "destroyed", []string{}, []string{}},
		{"closing unconfirmed", rostered(StateClosing, t0), 30 * time.Second, seatedView(gc.LobbyUI), ready, StateClosed, "destroy_unconfirmed", []string{}, []string{"leave:0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := inputs{Now: t0.Add(tt.now), View: tt.view, Host: hostID, LostGrace: DefaultLostGrace, Cfg: cfgPure}
			if tt.in != nil {
				tt.in(&in)
			}
			res := derive(tt.lobby, in)
			if res.Lobby.State != tt.state || (tt.reason != "" && res.Lobby.StateReason != tt.reason) {
				t.Errorf("state %s (%s), want %s (%s)", res.Lobby.State, res.Lobby.StateReason, tt.state, tt.reason)
			}
			if got := types(res.Events); !slices.Equal(got, tt.events) {
				t.Errorf("events %v, want %v", got, tt.events)
			}
			if got := kinds(res.Actions); !slices.Equal(got, tt.actions) {
				t.Errorf("actions %v, want %v", got, tt.actions)
			}
		})
	}
}

func TestDeriveMatchData(t *testing.T) {
	l := rostered(StateLaunching, t0)
	v := seatedView(gc.LobbyRun)
	v.MatchID = 8123456789
	res := derive(l, inputs{Now: t0.Add(time.Second), View: v, Host: hostID, BotReady: true, Cfg: cfgPure})
	started := res.Events[0].Data
	if started["match_id"] != "8123456789" || started["radiant_team_ref"] != "team-7" || started["host_seat_strategy"] != SeatSelfKick {
		t.Errorf("match.started = %v", started)
	}
	if res.Lobby.Match.RunAt.IsZero() || res.Lobby.Match.MatchID != 8123456789 {
		t.Errorf("match = %+v", res.Lobby.Match)
	}

	v2 := seatedView(gc.LobbyPostGame)
	v2.MatchID, v2.Outcome = 8123456789, 3
	ended := derive(res.Lobby, inputs{Now: t0.Add(41 * time.Minute), View: v2, Host: hostID, BotReady: true, Cfg: cfgPure})
	d := ended.Events[0].Data
	if d["winner_side"] != "dire" || d["winner_team_ref"] != "team-9" || d["scored"] != true || d["outcome_name"] != "dire_victory" ||
		d["provisional"] != true || d["source"] != "lobby" || d["duration_seconds"] != uint32(41*60-1) {
		t.Errorf("match.ended = %v", d)
	}
}

func TestDeriveLaunchFailedMovesHostSeat(t *testing.T) {
	l := rostered(StateLaunching, t0)
	l.HostSeat = HostSeat{Strategy: SeatCaster, Confirmed: true, Next: 0}
	l.LaunchAttempts = 1
	res := derive(l, inputs{Now: t0.Add(3 * time.Minute), View: seatedView(gc.LobbyUI), Host: hostID, BotReady: true, Cfg: cfgPure})
	if res.Lobby.HostSeat.Next != 1 || res.Lobby.HostSeat.Confirmed {
		t.Errorf("host seat %+v; want next strategy, unconfirmed", res.Lobby.HostSeat)
	}
	if res.Events[0].Data["next_strategy"] != SeatSelfKick || res.Events[0].Data["attempt"] != 1 {
		t.Errorf("launch_failed = %v", res.Events[0].Data)
	}
	// On the last strategy it stays there.
	res = derive(res.Lobby, inputs{Now: t0.Add(4 * time.Minute), View: seatedView(gc.LobbyUI), Host: hostID, BotReady: true, Cfg: cfgPure})
	res.Lobby.State, res.Lobby.StateSince = StateLaunching, t0.Add(4*time.Minute)
	res = derive(res.Lobby, inputs{Now: t0.Add(7 * time.Minute), View: seatedView(gc.LobbyUI), Host: hostID, BotReady: true, Cfg: cfgPure})
	if res.Lobby.HostSeat.Next != 1 {
		t.Errorf("host seat moved past the last strategy: %+v", res.Lobby.HostSeat)
	}
}

// A failed launch keeps the match_id the lobby still shows, and the next
// launch does not count it as its own.
func TestDeriveStaleMatchID(t *testing.T) {
	l := rostered(StateInGame, t0)
	l.Match.MatchID = 9
	v := seatedView(gc.LobbyUI)
	v.MatchID = 9
	res := derive(l, inputs{Now: t0.Add(time.Minute), View: v, Host: hostID, BotReady: true, Cfg: cfgPure})
	if res.Lobby.State != StateWaiting || res.Lobby.Match.StaleMatchID != 9 || res.Lobby.Match.MatchID != 0 {
		t.Fatalf("after a failed load: %s, match %+v", res.Lobby.State, res.Lobby.Match)
	}
	next := res.Lobby
	next.State, next.StateSince = StateLaunching, t0.Add(2*time.Minute)
	res = derive(next, inputs{Now: t0.Add(2*time.Minute + time.Second), View: v, Host: hostID, BotReady: true, Cfg: cfgPure})
	if res.Lobby.State != StateLaunching || len(res.Events) != 0 {
		t.Errorf("relaunch with the stale id: %s %v", res.Lobby.State, types(res.Events))
	}
}

func TestReadiness(t *testing.T) {
	coach := mem(4, gc.TeamPlayerPool)
	dire := gc.TeamDire
	coach.CoachTeam = &dire
	unknown := gc.Member{SteamID: gc.SteamID64(1)} // team absent: decodes as Radiant
	open := func() Lobby { l := rostered(StateWaiting, t0); l.Spec.Teams = Roster{}; return l }
	short := func() Lobby { l := rostered(StateWaiting, t0); l.Spec.AllowShortHanded = true; return l }

	tests := []struct {
		name    string
		lobby   Lobby
		members []gc.Member
		ready   bool
		missing []string
	}{
		{"all seated", rostered(StateWaiting, t0), seatedView(gc.LobbyUI).Members, true, []string{}},
		{"one absent", rostered(StateWaiting, t0), []gc.Member{hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant), mem(3, gc.TeamDire)}, false, []string{"2"}},
		{"wrong side", rostered(StateWaiting, t0), []gc.Member{mem(1, gc.TeamRadiant), mem(2, gc.TeamDire), mem(3, gc.TeamDire)}, false, []string{"2"}},
		{"absent team is not seated", rostered(StateWaiting, t0), []gc.Member{unknown, mem(2, gc.TeamRadiant), mem(3, gc.TeamDire)}, false, []string{"1"}},
		{"stranger on a side", rostered(StateWaiting, t0), append(seatedView(gc.LobbyUI).Members, mem(8, gc.TeamDire)), false, []string{"stranger:8"}},
		{"stranger in the pool is fine", rostered(StateWaiting, t0), append(seatedView(gc.LobbyUI).Members, mem(8, gc.TeamPlayerPool)), true, []string{}},
		{"coach does not count", rostered(StateWaiting, t0), append(seatedView(gc.LobbyUI).Members, coach), true, []string{}},
		{"open lobby, one per side", open(), []gc.Member{hostAt(gc.TeamRadiant), mem(8, gc.TeamRadiant), mem(9, gc.TeamDire)}, true, []string{}},
		{"open lobby, host does not count", open(), []gc.Member{hostAt(gc.TeamRadiant), mem(9, gc.TeamDire)}, false, []string{"radiant"}},
		{"short-handed allows absent", short(), []gc.Member{mem(1, gc.TeamRadiant), mem(3, gc.TeamDire)}, true, []string{}},
		{"short-handed still needs a side", short(), []gc.Member{mem(1, gc.TeamRadiant)}, false, []string{"dire"}},
		{"short-handed present but unseated", short(), []gc.Member{mem(1, gc.TeamRadiant), mem(2, gc.TeamPlayerPool), mem(3, gc.TeamDire)}, false, []string{"2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, missing := readiness(tt.lobby, view(gc.LobbyUI, tt.members...), hostID)
			if ready != tt.ready || !slices.Equal(missing, tt.missing) {
				t.Errorf("readiness = %v %v, want %v %v", ready, missing, tt.ready, tt.missing)
			}
		})
	}
}

func TestHostGate(t *testing.T) {
	tests := []struct {
		name     string
		host     *gc.Member
		strategy string
		want     bool
	}{
		{"not in lobby", nil, SeatSelfKick, false},
		{"absent team decodes as radiant", &gc.Member{SteamID: hostID, Team: gc.TeamRadiant}, SeatSelfKick, false},
		{"on radiant", new(hostAt(gc.TeamRadiant)), SeatSelfKick, false},
		{"on dire", new(hostAt(gc.TeamDire)), SeatSelfKick, false},
		{"in the pool", new(hostAt(gc.TeamPlayerPool)), SeatSelfKick, true},
		{"spectator", new(hostAt(gc.TeamSpectator)), SeatSpectator, true},
		{"caster needs the broadcaster slot", new(hostAt(gc.TeamPlayerPool)), SeatCaster, false},
		{"caster in the broadcaster slot", new(hostAt(gc.TeamBroadcaster)), SeatCaster, true},
		{"self kick leaves the broadcaster slot", new(hostAt(gc.TeamBroadcaster)), SeatSelfKick, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := view(gc.LobbyUI)
			if tt.host != nil {
				v.Members = []gc.Member{*tt.host}
			}
			if got := hostGate(v, hostID, HostSeat{Strategy: tt.strategy, Confirmed: true}); got != tt.want {
				t.Errorf("hostGate = %v", got)
			}
			if hostGate(v, hostID, HostSeat{Strategy: tt.strategy}) {
				t.Error("hostGate passed an unconfirmed seat")
			}
		})
	}
}

func TestSeatSatisfied(t *testing.T) {
	teams := []gc.Team{gc.TeamRadiant, gc.TeamDire, gc.TeamBroadcaster, gc.TeamSpectator, gc.TeamPlayerPool, gc.TeamNoTeam}
	want := map[string][]bool{
		SeatCaster:     {false, false, true, false, false, false},
		SeatSelfKick:   {false, false, false, true, true, true},
		SeatPlayerPool: {false, false, false, false, true, false},
		SeatSpectator:  {false, false, false, true, false, false},
		SeatNoTeam:     {false, false, false, false, false, true},
	}
	for s, row := range want {
		for i, team := range teams {
			if got := seatSatisfied(s, hostAt(team)); got != row[i] {
				t.Errorf("%s on %s = %v", s, team, got)
			}
		}
		if seatSatisfied(s, gc.Member{SteamID: hostID, Team: gc.TeamPlayerPool}) {
			t.Errorf("%s satisfied without a known team", s)
		}
	}
}

func TestPlanEnforcement(t *testing.T) {
	coach := mem(4, gc.TeamRadiant)
	radiant := gc.TeamRadiant
	coach.CoachTeam = &radiant
	unknown := gc.Member{SteamID: gc.SteamID64(2)}
	kickStrangers := rostered(StateWaiting, t0)
	kickStrangers.Spec.KickStrangers = true
	open := rostered(StateWaiting, t0)
	open.Spec.Teams = Roster{}
	now := t0.Add(time.Minute)

	tests := []struct {
		name    string
		lobby   Lobby
		members []gc.Member
		history map[uint32]enforceState
		want    []string
	}{
		{"all correct", rostered(StateWaiting, t0), seatedView(gc.LobbyUI).Members, nil, []string{}},
		{"wrong side kicked to the pool", rostered(StateWaiting, t0), []gc.Member{hostAt(gc.TeamRadiant), mem(1, gc.TeamDire), mem(3, gc.TeamRadiant)}, nil, []string{"kick_from_team:1", "kick_from_team:3"}},
		{"host never kicked", rostered(StateWaiting, t0), []gc.Member{hostAt(gc.TeamDire)}, nil, []string{}},
		{"absent team never kicked", rostered(StateWaiting, t0), []gc.Member{unknown}, nil, []string{}},
		{"coach never kicked", rostered(StateWaiting, t0), []gc.Member{coach}, nil, []string{}},
		{"stranger on a side", rostered(StateWaiting, t0), []gc.Member{mem(8, gc.TeamDire), mem(9, gc.TeamPlayerPool)}, nil, []string{"kick_from_team:8"}},
		{"kick_strangers removes them", kickStrangers, []gc.Member{mem(8, gc.TeamDire), mem(9, gc.TeamPlayerPool)}, nil, []string{"kick:8", "kick:9"}},
		{"kick_strangers spares casters and spectators", kickStrangers, []gc.Member{mem(8, gc.TeamBroadcaster), mem(9, gc.TeamSpectator), mem(10, gc.TeamNoTeam)}, nil, []string{}},
		{"open lobby has no strangers", open, []gc.Member{mem(8, gc.TeamDire)}, nil, []string{}},
		{"cooldown", rostered(StateWaiting, t0), []gc.Member{mem(1, gc.TeamDire)}, map[uint32]enforceState{1: {Last: now.Add(-5 * time.Second), Count: 1}}, []string{}},
		{"after the cooldown", rostered(StateWaiting, t0), []gc.Member{mem(1, gc.TeamDire)}, map[uint32]enforceState{1: {Last: now.Add(-11 * time.Second), Count: 1}}, []string{"kick_from_team:1"}},
		{"gives up after five", rostered(StateWaiting, t0), []gc.Member{mem(1, gc.TeamDire)}, map[uint32]enforceState{1: {Last: t0, Count: 5}}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kinds(planEnforcement(tt.lobby, view(gc.LobbyUI, tt.members...), hostID, now, tt.history, cfgPure))
			if !slices.Equal(got, tt.want) {
				t.Errorf("plan %v, want %v", got, tt.want)
			}
		})
	}
	if got := planEnforcement(rostered(StateWaiting, t0), view(gc.LobbyRun, mem(1, gc.TeamDire)), hostID, now, nil, cfgPure); len(got) != 0 {
		t.Errorf("enforcement outside UI: %v", kinds(got))
	}
}

func TestMatchHelpers(t *testing.T) {
	tests := []struct {
		outcome int32
		side    Side
		scored  *bool
		name    string
	}{
		{0, "", nil, "unknown"},
		{2, Radiant, new(true), "radiant_victory"},
		{3, Dire, new(true), "dire_victory"},
		{5, "", new(true), "no_team_wins"},
		{13, "", new(true), "custom8_victory"},
		{64, "", new(false), "not_scored_poor_network"},
		{69, "", new(false), "not_scored_suspicious"},
		{70, "", nil, "outcome_70"},
	}
	for _, tt := range tests {
		m := Match{Outcome: &tt.outcome, RadiantTeamRef: "a", DireTeamRef: "b"}
		if m.WinnerSide() != tt.side || OutcomeName(tt.outcome) != tt.name {
			t.Errorf("outcome %d: side %q name %q", tt.outcome, m.WinnerSide(), OutcomeName(tt.outcome))
		}
		got := m.Scored()
		if (got == nil) != (tt.scored == nil) || (got != nil && *got != *tt.scored) {
			t.Errorf("outcome %d: scored %v", tt.outcome, got)
		}
	}
	if (Match{}).DurationSeconds() != nil {
		t.Error("duration without times")
	}
}

func TestPassKeyRedacts(t *testing.T) {
	p := PassKey("k3tt1e")
	for _, s := range []string{p.String(), fmt.Sprintf("%v %s %q %x %#v", p, p, p, p, p)} {
		if contains(s, "k3tt1e") {
			t.Errorf("formatted %q", s)
		}
	}
	b, _ := p.MarshalJSON()
	if contains(string(b), "k3tt1e") || p.Reveal() != "k3tt1e" {
		t.Errorf("json %s", b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestIDsSortByTime(t *testing.T) {
	var g idGen
	prev := ""
	for i := range 1000 {
		id := g.next(t0.Add(time.Duration(i/10) * time.Millisecond))
		if len(id) != 26 || id <= prev {
			t.Fatalf("id %d %q after %q", i, id, prev)
		}
		prev = id
	}
}
