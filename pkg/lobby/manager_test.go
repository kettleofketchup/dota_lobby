package lobby

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/bot/bottest"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
	"github.com/kettleofketchup/dota_lobby/pkg/pool"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

const testPassword = "k3tt1e-secret"

func testConfig() config.LobbyConfig {
	return config.LobbyConfig{
		HostSeatStrategy:       []string{SeatCaster, SeatSelfKick},
		HostSeatConfirmTimeout: 150 * time.Millisecond,
		MaxLaunchAttempts:      3,
		PendingTimeout:         time.Minute,
		AutoLaunchDelay:        50 * time.Millisecond,
		IdleTimeout:            time.Hour,
		LaunchSettleTimeout:    300 * time.Millisecond,
		ServerStallTimeout:     300 * time.Millisecond,
		IngameBackstop:         time.Hour,
		PostgameLinger:         100 * time.Millisecond,
		EnforceCooldown:        200 * time.Millisecond,
		ReadoptTimeout:         time.Second,
		DrainTimeout:           2 * time.Second,
		Invite:                 config.InviteConfig{MinResend: 30 * time.Second, MaxPerPlayer: 3},
		GameModes:              map[string]uint32{"ap": 1, "cm": 2, "turbo": 23},
		Regions:                map[string]uint32{"us_east": 2},
	}
}

// holders says which test bots hold which league; league 0 is every bot.
type holders struct {
	mu   sync.Mutex
	bots []string
	by   map[uint32][]league.Holding
}

func (h *holders) Holders(l uint32) []league.Holding {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l == 0 {
		var out []league.Holding
		for _, b := range h.bots {
			out = append(out, league.Holding{Bot: b, Verified: true})
		}
		return out
	}
	return slices.Clone(h.by[l])
}

// recorder keeps every event the hooks hear about.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) OnEvent(_ Lobby, ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) types(lobbyID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, ev := range r.events {
		if ev.LobbyID == lobbyID {
			out = append(out, ev.Type)
		}
	}
	return out
}

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testBot struct {
	name   string
	fc     *fake.Client
	handle *bottest.Bot
	host   uint64
}

type harness struct {
	t       *testing.T
	path    string
	db      *store.Store
	bots    []*testBot
	source  *bottest.Source
	holders *holders
	pool    *pool.Pool
	cfg     config.LobbyConfig
	rec     *recorder
	logs    *syncBuffer
	m       *Manager
}

// newHarness builds bots named bot1..botN, ready and holding league 17929,
// with GC reactions that play the host seat and kicks.
func newHarness(t *testing.T, n int, tweak func(*config.LobbyConfig)) *harness {
	t.Helper()
	h := &harness{t: t, path: filepath.Join(t.TempDir(), "dota_lobby.db"), cfg: testConfig(),
		holders: &holders{by: map[uint32][]league.Holding{}}}
	if tweak != nil {
		tweak(&h.cfg)
	}
	var handles []*bottest.Bot
	for i := range n {
		name := "bot" + string(rune('1'+i))
		fc := fake.New()
		host := gc.SteamID64(uint32(900 + i))
		fc.SetSteamID(host)
		b := bottest.NewBot(name, fc)
		b.SetSteamID(host)
		b.SetReady(time.Now().Add(-time.Minute))
		tb := &testBot{name: name, fc: fc, handle: b, host: host}
		react(tb)
		h.bots = append(h.bots, tb)
		handles = append(handles, b)
		h.holders.bots = append(h.holders.bots, name)
		h.holders.by[17929] = append(h.holders.by[17929], league.Holding{Bot: name, Verified: true, Source: league.SourceGC})
	}
	h.source = bottest.NewSource(handles...)
	h.pool = pool.New(h.source, h.holders, pool.Options{})
	h.open()
	h.start()
	return h
}

func (h *harness) open() {
	db, err := store.Open(context.Background(), h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	h.db = db
	h.t.Cleanup(func() { _ = db.Close() })
}

func (h *harness) start() {
	h.rec, h.logs = &recorder{}, &syncBuffer{}
	h.m = New(Options{
		Store: h.db.Lobbies(), Pool: h.pool, Holders: h.holders, Bots: h.source,
		Lobby: h.cfg, GC: config.GCConfig{RequestTimeout: time.Second, CreateTimeout: time.Second},
		Retention: 24 * time.Hour, Hooks: h.rec, Tick: 10 * time.Millisecond, LostGrace: 100 * time.Millisecond,
		Logger: slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err := h.m.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	m := h.m
	h.t.Cleanup(func() { _ = m.Drain(context.Background()) })
}

// restart drains the manager and starts a new one on the same database,
// with a fresh pool, as a process restart would.
func (h *harness) restart() {
	if err := h.m.Drain(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.pool = pool.New(h.source, h.holders, pool.Options{})
	h.start()
}

// react plays the GC: the host seat ops move the host, kicks move or drop
// members.
func react(b *testBot) {
	setTeam := func(steam uint64, team gc.Team) {
		b.fc.UpdateLobby(func(v *gc.LobbyView) {
			for i := range v.Members {
				if v.Members[i].SteamID == steam {
					v.Members[i].Team, v.Members[i].TeamKnown = team, true
				}
			}
		})
	}
	b.fc.OnCall("JoinBroadcastChannel", func([]any) { setTeam(b.host, gc.TeamBroadcaster) })
	b.fc.OnCall("JoinTeam", func(args []any) { setTeam(b.host, args[0].(gc.Team)) })
	b.fc.OnCall("KickFromTeam", func(args []any) { setTeam(gc.SteamID64(args[0].(uint32)), gc.TeamPlayerPool) })
	b.fc.OnCall("Kick", func(args []any) {
		b.fc.UpdateLobby(func(v *gc.LobbyView) {
			v.Members = slices.DeleteFunc(v.Members, func(m gc.Member) bool { return gc.AccountID(m.SteamID) == args[0].(uint32) })
		})
	})
}

func (b *testBot) join(account uint32, team gc.Team) {
	b.fc.UpdateLobby(func(v *gc.LobbyView) {
		v.Members = append(v.Members, gc.Member{SteamID: gc.SteamID64(account), Team: team, TeamKnown: true})
	})
}

func (b *testBot) setState(st gc.LobbyState, matchID uint64, outcome int32) {
	b.fc.UpdateLobby(func(v *gc.LobbyView) { v.State, v.MatchID, v.Outcome = st, matchID, outcome })
}

func (h *harness) bot(name string) *testBot {
	for _, b := range h.bots {
		if b.name == name {
			return b
		}
	}
	h.t.Fatalf("no bot %s", name)
	return nil
}

func spec(ref string) Spec {
	return Spec{
		ExternalRef: ref, Name: "Spring Cup " + ref, Password: testPassword, LeagueID: 17929,
		GameMode: "cm", Region: "us_east", TVDelaySeconds: 120,
		Teams: Roster{
			Radiant: Team{TeamRef: "team-7", Name: "Ravens", Players: []Player{{AccountID: 11, Captain: true}, {AccountID: 12}}},
			Dire:    Team{TeamRef: "team-9", Name: "Owls", Players: []Player{{AccountID: 21}}},
		},
		Metadata: map[string]string{"round": "2"},
	}
}

func (h *harness) create(s Spec) Lobby {
	h.t.Helper()
	l, created, err := h.m.Create(context.Background(), s)
	if err != nil || !created {
		h.t.Fatalf("Create = %v, %v", created, err)
	}
	return l
}

func (h *harness) waitFor(id string, what string, cond func(l Lobby) bool) Lobby {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, err := h.m.Get(context.Background(), id)
		if err == nil && cond(l) {
			return l
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("lobby %s never got %s: last %s (%s) err %v\nevents %v", id, what, l.State, l.StateReason, err, h.rec.types(id))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitEvent waits until the hooks heard an event: they run after the
// lobby is swapped in, so a state seen by Get may not have reached them.
func (h *harness) waitEvent(id, typ string) {
	h.t.Helper()
	h.waitFor(id, "event "+typ, func(Lobby) bool { return slices.Contains(h.rec.types(id), typ) })
}

func (h *harness) waitState(id string, s State) Lobby {
	h.t.Helper()
	return h.waitFor(id, string(s), func(l Lobby) bool { return l.State == s })
}

func (h *harness) waitCalls(b *testBot, method string, n int) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.fc.WaitForCalls(ctx, method, n); err != nil {
		h.t.Fatal(err)
	}
}

// toWaiting creates a lobby and waits until its host seat is confirmed.
func (h *harness) toWaiting(s Spec) (Lobby, *testBot) {
	h.t.Helper()
	l := h.create(s)
	l = h.waitFor(l.ID, "a confirmed host seat", func(l Lobby) bool { return l.State == StateWaiting && l.HostSeat.Confirmed })
	return l, h.bot(l.Bot)
}

func (h *harness) seatAll(b *testBot) {
	b.join(11, gc.TeamRadiant)
	b.join(12, gc.TeamRadiant)
	b.join(21, gc.TeamDire)
}

func TestCreateToWaiting(t *testing.T) {
	h := newHarness(t, 1, nil)
	l := h.create(spec("game-1"))
	if l.State != StatePending || !strings.HasPrefix(l.ID, "lob_") || len(l.ID) != 30 || l.Seq != 1 {
		t.Fatalf("accepted lobby = %+v", l)
	}
	l = h.waitFor(l.ID, "a confirmed host seat", func(l Lobby) bool { return l.State == StateWaiting && l.HostSeat.Confirmed })
	b := h.bot(l.Bot)
	if l.GCLobbyID == 0 || l.Bot != "bot1" || l.LeagueVerified == nil || !*l.LeagueVerified {
		t.Errorf("waiting lobby = %+v", l)
	}
	if l.HostSeat.Strategy != SeatCaster || l.HostSeat.Team != "broadcaster" {
		t.Errorf("host seat = %+v", l.HostSeat)
	}
	create := b.fc.Calls("CreateLobby")[0].Args[0].(gc.LobbyDetails)
	if create.GameMode != 2 || create.ServerRegion != 2 || create.LeagueID != 17929 || create.TVDelaySeconds != 120 || create.PassKey != testPassword {
		t.Errorf("create details = %+v", create)
	}
	// Each rostered player gets one invite, recorded before it was sent.
	h.waitCalls(b, "Invite", 3)
	l = h.waitFor(l.ID, "invites", func(l Lobby) bool { return l.Spec.Teams.Player(21).InvitesSent == 1 })
	if got := h.pool.Leases()["bot1"]; got != l.ID {
		t.Errorf("lease = %q", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := b.fc.CallCount("Invite"); n != 3 {
		t.Errorf("invites sent %d times, want 3", n)
	}
	wantEvents := []string{"lobby.state_changed", "lobby.state_changed", "lobby.created", "lobby.state_changed", "lobby.player_invited"}
	if got := h.rec.types(l.ID); !slices.Equal(got[:min(len(got), 5)], wantEvents) {
		t.Errorf("events = %v", got)
	}
	// Confirmation from the GC is recorded.
	b.fc.EmitInvite(gc.InvitationCreated{SteamID: gc.SteamID64(12)})
	h.waitFor(l.ID, "a confirmed invite", func(l Lobby) bool { return l.Spec.Teams.Player(12).InviteConfirmed })
}

func TestCreateFailures(t *testing.T) {
	tests := []struct {
		name   string
		script func(b *testBot)
		code   string
	}{
		{"league read back differs", func(b *testBot) {
			b.fc.SetCreateResult(&gc.LobbyView{LobbyID: 5, LeaderID: b.host, State: gc.LobbyUI, LeagueID: 0,
				Members: []gc.Member{{SteamID: b.host, Team: gc.TeamRadiant, TeamKnown: true}}})
		}, "league_rejected"},
		{"not league admin popup", func(b *testBot) {
			b.fc.FailNext("CreateLobby", &gc.CreateError{Reason: gc.CreateLeagueRejected, PopupID: 27})
		}, "league_rejected"},
		{"gc error", func(b *testBot) { b.fc.FailNext("CreateLobby", &gc.CreateError{Reason: gc.CreateFailed, EResult: 2}) }, "gc_error"},
		{"timeout", func(b *testBot) { b.fc.BlockUntilDone("CreateLobby", true) }, "create_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, 1, nil)
			b := h.bot("bot1")
			tt.script(b)
			l := h.create(spec("game-1"))
			l = h.waitState(l.ID, StateFailed)
			if l.StateReason != tt.code {
				t.Errorf("reason %q, want %q", l.StateReason, tt.code)
			}
			h.waitEvent(l.ID, "lobby.failed")
			if !slices.Contains(h.rec.types(l.ID), "lobby.create_failed") {
				t.Errorf("no create_failed event: %v", h.rec.types(l.ID))
			}
			h.waitFor(l.ID, "the lease back", func(Lobby) bool { return len(h.pool.Leases()) == 0 })
			if tt.code == "league_rejected" && tt.name == "league read back differs" {
				if _, ok := b.fc.CurrentLobby(); ok || b.fc.CallCount("Destroy") != 1 {
					t.Errorf("rejected lobby not destroyed (destroy calls %d)", b.fc.CallCount("Destroy"))
				}
			}
		})
	}
}

// A DELETE during a slow create answers at once; the lobby closes when
// the create returns.
func TestCloseDuringCreate(t *testing.T) {
	h := newHarness(t, 1, nil)
	b := h.bot("bot1")
	b.fc.BlockUntilDone("CreateLobby", true)
	l := h.create(spec("game-1"))
	h.waitCalls(b, "CreateLobby", 1)
	start := time.Now()
	closing, changed, err := h.m.Close(context.Background(), l.ID)
	if err != nil || !changed || closing.State != StateClosing {
		t.Fatalf("Close = %s %v %v", closing.State, changed, err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("Close waited %s for the create", d)
	}
	h.waitState(l.ID, StateClosed)
}

func TestBotNotReadyRequeues(t *testing.T) {
	h := newHarness(t, 1, nil)
	b := h.bot("bot1")
	b.fc.FailNext("CreateLobby", gc.ErrNotReady)
	l := h.create(spec("game-1"))
	h.waitFor(l.ID, "a retried create", func(Lobby) bool { return b.fc.CallCount("CreateLobby") >= 2 })
	h.waitState(l.ID, StateWaiting)
}

func TestHostSeatFallsThrough(t *testing.T) {
	h := newHarness(t, 1, nil)
	b := h.bot("bot1")
	// The GC ignores the caster request.
	b.fc.OnCall("JoinBroadcastChannel", nil)
	l, _ := h.toWaiting(spec("game-1"))
	if l.HostSeat.Strategy != SeatSelfKick || l.HostSeat.Next != 1 || l.HostSeat.Team != "player_pool" {
		t.Errorf("host seat = %+v", l.HostSeat)
	}
	if b.fc.CallCount("JoinBroadcastChannel") != 1 {
		t.Errorf("caster tried %d times", b.fc.CallCount("JoinBroadcastChannel"))
	}
	kick := b.fc.Calls("KickFromTeam")[0].Args[0].(uint32)
	if kick != gc.AccountID(b.host) {
		t.Errorf("self kick used account %d", kick)
	}
}

func TestHostSeatAllFail(t *testing.T) {
	h := newHarness(t, 1, nil)
	b := h.bot("bot1")
	b.fc.OnCall("JoinBroadcastChannel", nil)
	b.fc.OnCall("KickFromTeam", nil)
	l := h.create(spec("game-1"))
	l = h.waitFor(l.ID, "both strategies tried", func(Lobby) bool { return b.fc.CallCount("KickFromTeam") >= 1 })
	l = h.waitFor(l.ID, "an unconfirmed seat", func(l Lobby) bool { return l.HostSeat.Strategy == SeatSelfKick })
	if l.HostSeat.Confirmed {
		t.Fatal("seat confirmed")
	}
	h.seatAll(b)
	l = h.waitState(l.ID, StateReady)
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); !errors.Is(err, ErrHostNotSeated) {
		t.Errorf("Launch = %v, want ErrHostNotSeated", err)
	}
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{Force: true}); !errors.Is(err, ErrHostNotSeated) {
		t.Errorf("forced Launch = %v; force must not skip the host gate", err)
	}
}

func TestSeatingEnforcement(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	b.fc.OnCall("KickFromTeam", nil) // the player does not move: kicks repeat on the cooldown only
	b.join(21, gc.TeamRadiant)       // a Dire player on Radiant
	b.join(99, gc.TeamDire)          // a stranger on Dire
	h.waitCalls(b, "KickFromTeam", 2)
	time.Sleep(100 * time.Millisecond)
	if n := b.fc.CallCount("KickFromTeam"); n != 2 {
		t.Errorf("kicks within the cooldown: %d", n)
	}
	h.waitCalls(b, "KickFromTeam", 4)
	got := map[uint32]bool{}
	for _, c := range b.fc.Calls("KickFromTeam") {
		got[c.Args[0].(uint32)] = true
	}
	if !got[21] || !got[99] || len(got) != 2 {
		t.Errorf("kicked %v", got)
	}
	h.waitFor(l.ID, "kick events", func(Lobby) bool { return slices.Contains(h.rec.types(l.ID), "lobby.player_kicked") })
	// After five kicks enforcement gives up on a member.
	h.waitFor(l.ID, "enforcement to stop", func(Lobby) bool {
		return slices.Contains(h.rec.types(l.ID), "lobby.enforcement_stopped")
	})
}

func TestLaunchToFinished(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); !errors.Is(err, ErrNotReady) {
		t.Errorf("Launch while waiting = %v", err)
	}
	h.seatAll(b)
	h.waitState(l.ID, StateReady)
	l, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{By: 11})
	if err != nil || l.State != StateLaunching {
		t.Fatalf("Launch = %+v, %v", l.State, err)
	}
	h.waitCalls(b, "Launch", 1)
	l = h.waitFor(l.ID, "a counted attempt", func(l Lobby) bool { return l.LaunchAttempts == 1 })
	b.setState(gc.LobbyServerAssign, 0, 0)
	b.setState(gc.LobbyRun, 8123456789, 0)
	l = h.waitState(l.ID, StateInGame)
	if l.Match.MatchID != 8123456789 || l.Match.HostSeatStrategy != SeatCaster || l.Match.RadiantTeamRef != "team-7" {
		t.Errorf("match = %+v", l.Match)
	}
	b.setState(gc.LobbyPostGame, 8123456789, 2)
	l = h.waitState(l.ID, StateFinished)
	h.waitEvent(l.ID, "lobby.finished")
	h.waitCalls(b, "Leave", 1)
	if l.Match.WinnerSide() != Radiant || l.Match.WinnerTeamRef() != "team-7" || l.EndedAt.IsZero() {
		t.Errorf("finished lobby match %+v", l.Match)
	}
	h.waitFor(l.ID, "the lease back", func(Lobby) bool { return len(h.pool.Leases()) == 0 })
	types := h.rec.types(l.ID)
	for _, want := range []string{"lobby.ready", "lobby.launching", "match.started", "match.ended", "lobby.finished"} {
		if !slices.Contains(types, want) {
			t.Errorf("no %s in %v", want, types)
		}
	}
	// Events are numbered without gaps, and the stored ones match.
	evs, err := h.db.Lobbies().Events(context.Background(), l.ID, 0, 1000)
	if err != nil || int64(len(evs)) != l.Seq {
		t.Fatalf("%d stored events, seq %d, %v", len(evs), l.Seq, err)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, ev.Seq)
		}
	}
}

func TestLaunchIgnoredRetriesNextStrategy(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	h.seatAll(b)
	h.waitState(l.ID, StateReady)
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); err != nil {
		t.Fatal(err)
	}
	// The GC never moves: after the settle timeout it is a failed launch,
	// and the next attempt uses the next strategy.
	l = h.waitFor(l.ID, "a failed launch", func(l Lobby) bool { return l.LaunchAttempts == 1 && l.State != StateLaunching })
	if l.StateReason != "ignored" && l.State != StateReady {
		t.Errorf("after an ignored launch: %s (%s)", l.State, l.StateReason)
	}
	l = h.waitFor(l.ID, "the next strategy", func(l Lobby) bool { return l.HostSeat.Strategy == SeatSelfKick && l.HostSeat.Confirmed })
	if !slices.Contains(h.rec.types(l.ID), "lobby.launch_failed") {
		t.Errorf("no launch_failed in %v", h.rec.types(l.ID))
	}
}

func TestLaunchAttemptsExhausted(t *testing.T) {
	h := newHarness(t, 1, func(c *config.LobbyConfig) { c.MaxLaunchAttempts = 1 })
	l, b := h.toWaiting(spec("game-1"))
	h.seatAll(b)
	h.waitState(l.ID, StateReady)
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); err != nil {
		t.Fatal(err)
	}
	h.waitFor(l.ID, "an ignored launch", func(l Lobby) bool { return l.LaunchAttempts == 1 && l.State != StateLaunching })
	l = h.waitFor(l.ID, "a seated host", func(l Lobby) bool { return l.HostSeat.Confirmed && l.State == StateReady })
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Launch after the limit = %v", err)
	}
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{Force: true}); err != nil {
		t.Errorf("forced Launch after the limit = %v", err)
	}
}

func TestAutoLaunch(t *testing.T) {
	h := newHarness(t, 1, nil)
	s := spec("game-1")
	s.Launch = LaunchAuto
	l, b := h.toWaiting(s)
	h.seatAll(b)
	h.waitCalls(b, "Launch", 1)
	l = h.waitState(l.ID, StateLaunching)
	if l.StateReason != TriggerAuto {
		t.Errorf("reason %q", l.StateReason)
	}
}

func TestServerStall(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	h.seatAll(b)
	h.waitState(l.ID, StateReady)
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); err != nil {
		t.Fatal(err)
	}
	h.waitCalls(b, "Launch", 1)
	b.setState(gc.LobbyServerAssign, 0, 0)
	l = h.waitState(l.ID, StateTimedOut)
	if l.StateReason != "server_stall" {
		t.Errorf("reason %q", l.StateReason)
	}
}

func TestIdempotentCreate(t *testing.T) {
	h := newHarness(t, 0, nil) // no bots: lobbies stay pending
	h.holders.by[17929] = []league.Holding{{Bot: "elsewhere"}}
	first := h.create(spec("game-1"))

	same, created, err := h.m.Create(context.Background(), spec("game-1"))
	if err != nil || created || same.ID != first.ID {
		t.Errorf("same request = %s, %v, %v", same.ID, created, err)
	}
	other := spec("game-1")
	other.Name = "something else"
	if _, _, err := h.m.Create(context.Background(), other); !errors.Is(err, ErrConflict) {
		t.Errorf("different request = %v, want ErrConflict", err)
	}
	pw := spec("game-1")
	pw.Password = "other"
	if _, _, err := h.m.Create(context.Background(), pw); !errors.Is(err, ErrConflict) {
		t.Errorf("different password = %v, want ErrConflict", err)
	}
	if _, _, err := h.m.Close(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	h.waitState(first.ID, StateClosed)
	again, created, err := h.m.Create(context.Background(), other)
	if err != nil || !created || again.ID == first.ID {
		t.Errorf("reuse after close = %s, %v, %v", again.ID, created, err)
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t, 1, nil)
	tests := []struct {
		name   string
		change func(s *Spec)
		want   error
	}{
		{"bad ref", func(s *Spec) { s.ExternalRef = "game 1" }, ErrInvalid},
		{"no name", func(s *Spec) { s.Name = "" }, ErrInvalid},
		{"long password", func(s *Spec) { s.Password = PassKey(strings.Repeat("x", 33)) }, ErrInvalid},
		{"tv delay", func(s *Spec) { s.TVDelaySeconds = 30 }, ErrInvalid},
		{"game mode", func(s *Spec) { s.GameMode = "blitz" }, ErrUnknownName},
		{"region", func(s *Spec) { s.Region = "mars" }, ErrUnknownName},
		{"numeric game mode", func(s *Spec) { s.GameMode = "18" }, nil},
		{"duplicate player", func(s *Spec) { s.Teams.Dire.Players = append(s.Teams.Dire.Players, Player{AccountID: 11}) }, ErrInvalid},
		{"six a side", func(s *Spec) {
			s.Teams.Radiant.Players = []Player{{AccountID: 1}, {AccountID: 2}, {AccountID: 3}, {AccountID: 4}, {AccountID: 5}, {AccountID: 6}}
		}, ErrInvalid},
		{"visibility", func(s *Spec) { s.Visibility = "hidden" }, ErrInvalid},
		{"league not held", func(s *Spec) { s.LeagueID = 4242 }, ErrLeagueNotHeld},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := spec("v-" + string(rune('a'+i)))
			tt.change(&s)
			_, _, err := h.m.Create(context.Background(), s)
			if !errors.Is(err, tt.want) {
				t.Errorf("Create = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPendingQueue(t *testing.T) {
	h := newHarness(t, 1, func(c *config.LobbyConfig) { c.PendingTimeout = 300 * time.Millisecond })
	first, _ := h.toWaiting(spec("game-1"))
	second := h.create(spec("game-2"))
	// The only bot is busy: the second lobby waits, then times out.
	time.Sleep(50 * time.Millisecond)
	if l, _ := h.m.Get(context.Background(), second.ID); l.State != StatePending {
		t.Errorf("second lobby %s", l.State)
	}
	h.waitState(second.ID, StateTimedOut)

	third := h.create(spec("game-3"))
	if _, _, err := h.m.Close(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	h.waitState(first.ID, StateClosed)
	// The freed bot goes to the waiting lobby.
	if l := h.waitState(third.ID, StateWaiting); l.Bot != "bot1" {
		t.Errorf("third lobby on %q", l.Bot)
	}
}

// A lobby whose tracker is busy does not hold up the pending queue.
func TestPendingQueueSkipsBusyLobbies(t *testing.T) {
	h := newHarness(t, 2, nil)
	busy, _ := h.toWaiting(spec("game-1"))
	e, ok := h.m.entry(busy.ID)
	if !ok {
		t.Fatal("no entry")
	}
	e.op.Lock()
	defer e.op.Unlock()
	next := h.create(spec("game-2"))
	if l := h.waitState(next.ID, StateWaiting); l.Bot == busy.Bot {
		t.Errorf("both lobbies on %s", l.Bot)
	}
}

func TestInvitePolicy(t *testing.T) {
	h := newHarness(t, 1, func(c *config.LobbyConfig) { c.Invite.MinResend = 200 * time.Millisecond })
	l, b := h.toWaiting(spec("game-1"))
	h.waitCalls(b, "Invite", 3)
	b.join(11, gc.TeamRadiant)

	// Within min_resend: limited.
	_, err := h.m.Invite(context.Background(), l.ID, InviteRequest{Players: []uint32{12}})
	var limited *InviteLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 {
		t.Fatalf("resend too soon = %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	res, err := h.m.Invite(context.Background(), l.ID, InviteRequest{Players: []uint32{11, 12, 77}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Invited, []uint32{12}) || len(res.Skipped) != 2 || res.Skipped[0].Reason != SkipInLobby || res.Skipped[1].Reason != SkipNotRostered {
		t.Errorf("invite result = %+v", res)
	}
	// A repeated account is one invite, within the cooldown like any other.
	time.Sleep(250 * time.Millisecond)
	before := b.fc.CallCount("Invite")
	res, err = h.m.Invite(context.Background(), l.ID, InviteRequest{Players: []uint32{21, 21, 21, 21, 21}})
	if err != nil || !slices.Equal(res.Invited, []uint32{21}) || len(res.Skipped) != 0 {
		t.Errorf("repeated account = %+v, %v", res, err)
	}
	if n := b.fc.CallCount("Invite") - before; n != 1 {
		t.Errorf("repeated account sent %d invites", n)
	}
	// max_per_player (3) counts the automatic invite.
	time.Sleep(250 * time.Millisecond)
	if _, err := h.m.Invite(context.Background(), l.ID, InviteRequest{Missing: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	_, err = h.m.Invite(context.Background(), l.ID, InviteRequest{Players: []uint32{12}})
	if !errors.As(err, &limited) || limited.RetryAfter != 0 {
		t.Errorf("past the cap = %v", err)
	}
	cur, _ := h.m.Get(context.Background(), l.ID)
	if got := cur.Spec.Teams.Player(12).InvitesSent; got != 3 {
		t.Errorf("invites to 12 = %d", got)
	}
}

func TestCloseAndKickAndFlip(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	b.join(11, gc.TeamRadiant)
	if _, err := h.m.Kick(context.Background(), l.ID, gc.AccountID(b.host), "team"); !errors.Is(err, ErrInvalid) {
		t.Errorf("kicking the host = %v", err)
	}
	if _, err := h.m.Kick(context.Background(), l.ID, 11, "lobby"); err != nil {
		t.Fatal(err)
	}
	if c := b.fc.Calls("Kick"); len(c) != 1 || c[0].Args[0] != uint32(11) {
		t.Errorf("kick calls %v", c)
	}
	flipped, err := h.m.FlipTeams(context.Background(), l.ID)
	if err != nil || flipped.Spec.Teams.Radiant.TeamRef != "team-9" || flipped.Spec.Teams.Dire.Players[0].AccountID != 11 {
		t.Fatalf("flip = %+v, %v", flipped.Spec.Teams, err)
	}
	if _, err := h.m.Shuffle(context.Background(), l.ID); !errors.Is(err, ErrInvalidState) {
		t.Errorf("shuffle with a roster = %v", err)
	}
	closed, changed, err := h.m.Close(context.Background(), l.ID)
	if err != nil || !changed || closed.State != StateClosing {
		t.Fatalf("Close = %s %v %v", closed.State, changed, err)
	}
	l = h.waitState(l.ID, StateClosed)
	if l.StateReason != "destroyed" || b.fc.CallCount("Destroy") != 1 {
		t.Errorf("closed %q, destroys %d", l.StateReason, b.fc.CallCount("Destroy"))
	}
	if _, changed, err := h.m.Close(context.Background(), l.ID); err != nil || changed {
		t.Errorf("second Close = %v, %v", changed, err)
	}
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Launch after close = %v", err)
	}
	if _, err := h.m.Get(context.Background(), "lob_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown lobby = %v", err)
	}
}

func TestUpdateResendsMergedDetails(t *testing.T) {
	h := newHarness(t, 1, nil)
	s := spec("game-1")
	s.TVDelaySeconds = 0 // left to the GC: the read-back value is kept
	l, b := h.toWaiting(s)
	name, mode := "Renamed", "turbo"
	l, err := h.m.Update(context.Background(), l.ID, Patch{Name: &name, GameMode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	d := b.fc.Calls("SetLobbyDetails")[0].Args[0].(gc.LobbyDetails)
	if d.LobbyID != l.GCLobbyID || d.GameName != "Renamed" || d.GameMode != 23 || d.LeagueID != 17929 ||
		d.PassKey != testPassword || d.TVDelaySeconds != 10 || d.ServerRegion != 2 {
		t.Errorf("details = %+v", d)
	}
	bad := "blitz"
	if _, err := h.m.Update(context.Background(), l.ID, Patch{GameMode: &bad}); !errors.Is(err, ErrUnknownName) {
		t.Errorf("bad mode = %v", err)
	}
}

func TestSetRoster(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	h.waitCalls(b, "Invite", 3)
	b.join(12, gc.TeamRadiant)
	h.waitFor(l.ID, "12 seen", func(l Lobby) bool { _, ok := l.Member(12); return ok })
	r := spec("x").Teams
	r.Radiant.Players = []Player{{AccountID: 11}, {AccountID: 13}} // 12 dropped, 13 new
	l, err := h.m.SetRoster(context.Background(), l.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	if p := l.Spec.Teams.Player(11); p == nil || p.InvitesSent != 1 {
		t.Errorf("11 lost its history: %+v", p)
	}
	if p := l.Spec.Teams.Player(13); p == nil || p.InvitesSent != 1 {
		t.Errorf("13 not invited: %+v", p)
	}
	if c := b.fc.Calls("KickFromTeam"); len(c) == 0 || c[len(c)-1].Args[0] != uint32(12) {
		t.Errorf("dropped 12 not unseated: %v", c)
	}
}

func TestReadoption(t *testing.T) {
	t.Run("same lobby resumes", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		l, b := h.toWaiting(spec("game-1"))
		h.waitCalls(b, "Invite", 3)
		h.restart()
		h.seatAll(b)
		l = h.waitState(l.ID, StateReady)
		if l.Bot != "bot1" || h.pool.Leases()["bot1"] != l.ID {
			t.Errorf("lease after restart: %v", h.pool.Leases())
		}
		time.Sleep(50 * time.Millisecond)
		if n := b.fc.CallCount("Invite"); n != 3 {
			t.Errorf("invites repeated after restart: %d", n)
		}
	})
	t.Run("lobby gone fails it", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		l, b := h.toWaiting(spec("game-1"))
		h.restart()
		b.fc.SetLobby(nil)
		l = h.waitState(l.ID, StateFailed)
		h.waitFor(l.ID, "the lease back", func(Lobby) bool { return len(h.pool.Leases()) == 0 })
		if l.StateReason != "lobby_lost" {
			t.Errorf("reason %q", l.StateReason)
		}
	})
	t.Run("in game lobby gone keeps the match id", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		l, b := h.toWaiting(spec("game-1"))
		h.seatAll(b)
		h.waitState(l.ID, StateReady)
		if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); err != nil {
			t.Fatal(err)
		}
		b.setState(gc.LobbyRun, 4242, 0)
		h.waitState(l.ID, StateInGame)
		h.restart()
		b.fc.SetLobby(nil)
		l = h.waitState(l.ID, StateFailed)
		if l.StateReason != "lost_in_game" || l.Match.MatchID != 4242 {
			t.Errorf("reason %q match %d", l.StateReason, l.Match.MatchID)
		}
	})
	t.Run("bot never comes back", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		l, b := h.toWaiting(spec("game-1"))
		b.handle.SetReady(time.Time{})
		h.restart()
		l = h.waitState(l.ID, StateFailed)
		if l.StateReason != "lobby_lost" {
			t.Errorf("reason %q", l.StateReason)
		}
	})
	t.Run("interrupted create is adopted", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		b := h.bot("bot1")
		b.fc.BlockUntilDone("CreateLobby", true)
		l := h.create(spec("game-1"))
		h.waitState(l.ID, StateCreating)
		h.waitCalls(b, "CreateLobby", 1)
		h.restart()
		// The create had gone through: the lobby is in the bot's cache.
		b.fc.SetLobby(&gc.LobbyView{LobbyID: 555, LeaderID: b.host, State: gc.LobbyUI, GameName: l.Spec.Name, LeagueID: 17929,
			Members: []gc.Member{{SteamID: b.host, Team: gc.TeamRadiant, TeamKnown: true}}})
		l = h.waitState(l.ID, StateWaiting)
		if l.GCLobbyID != 555 || l.StateReason != "readopted" {
			t.Errorf("adopted %d (%s)", l.GCLobbyID, l.StateReason)
		}
	})
	t.Run("interrupted create without a lobby", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		b := h.bot("bot1")
		b.fc.BlockUntilDone("CreateLobby", true)
		l := h.create(spec("game-1"))
		h.waitCalls(b, "CreateLobby", 1)
		h.restart()
		l = h.waitState(l.ID, StateFailed)
		if l.StateReason != "create_interrupted" {
			t.Errorf("reason %q", l.StateReason)
		}
	})
}

func TestDrain(t *testing.T) {
	tests := []struct {
		name     string
		destroy  bool
		launched bool
		want     State
	}{
		{"default leaves lobbies for re-adoption", false, false, StateWaiting},
		{"destroy_on_shutdown closes waiting lobbies", true, false, StateClosed},
		{"launched lobbies are never touched", true, true, StateInGame},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, 1, func(c *config.LobbyConfig) { c.DestroyOnShutdown = tt.destroy })
			l, b := h.toWaiting(spec("game-1"))
			if tt.launched {
				h.seatAll(b)
				h.waitState(l.ID, StateReady)
				if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); err != nil {
					t.Fatal(err)
				}
				b.setState(gc.LobbyRun, 1, 0)
				h.waitState(l.ID, StateInGame)
			}
			if err := h.m.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := h.m.Get(context.Background(), l.ID)
			if err != nil || got.State != tt.want {
				t.Errorf("after drain %s, %v; want %s", got.State, err, tt.want)
			}
			if tt.launched && b.fc.CallCount("Destroy") != 0 {
				t.Error("an in-game lobby was destroyed")
			}
			if _, _, err := h.m.Create(context.Background(), spec("game-2")); !errors.Is(err, ErrUnavailable) {
				t.Errorf("Create while draining = %v", err)
			}
		})
	}
}

func TestBotLost(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	b.handle.SetReady(time.Time{})
	b.handle.SetState(bot.StateNeedsAttention)
	l = h.waitState(l.ID, StateFailed)
	if l.StateReason != "bot_lost" {
		t.Errorf("reason %q", l.StateReason)
	}
}

func TestListAndRetention(t *testing.T) {
	h := newHarness(t, 0, nil)
	h.holders.by[17929] = []league.Holding{{Bot: "elsewhere"}}
	var ids []string
	for _, ref := range []string{"a", "b", "c"} {
		ids = append(ids, h.create(spec(ref)).ID)
	}
	page, next, err := h.m.List(context.Background(), Filter{Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID != ids[2] || next != ids[1] {
		t.Fatalf("page 1 = %d %q %v", len(page), next, err)
	}
	page, next, err = h.m.List(context.Background(), Filter{Limit: 2, Cursor: next})
	if err != nil || len(page) != 1 || page[0].ID != ids[0] || next != "" {
		t.Fatalf("page 2 = %d %q %v", len(page), next, err)
	}
	byRef, _, _ := h.m.List(context.Background(), Filter{ExternalRef: "b"})
	if len(byRef) != 1 || byRef[0].ID != ids[1] {
		t.Errorf("by ref = %v", byRef)
	}
}

// TestLobbiesNeverLeakPassKey: the password is in the database and the
// lobby, but never in an event, the stored event data or a log line.
func TestLobbiesNeverLeakPassKey(t *testing.T) {
	h := newHarness(t, 1, nil)
	l, b := h.toWaiting(spec("game-1"))
	h.seatAll(b)
	h.waitState(l.ID, StateReady)
	pw := PassKey("n3w-secret")
	if _, err := h.m.Update(context.Background(), l.ID, Patch{Password: &pw}); err != nil {
		t.Fatal(err)
	}
	h.m.log.Info("a lobby", "lobby", l, "spec", l.Spec, "details", l.Spec.Password)
	if _, err := h.m.Launch(context.Background(), l.ID, LaunchRequest{}); err != nil {
		t.Fatal(err)
	}
	b.setState(gc.LobbyRun, 1, 0)
	h.waitState(l.ID, StateInGame)

	evs, err := h.db.Lobbies().Events(context.Background(), l.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	h.rec.mu.Lock()
	hooked, _ := json.Marshal(h.rec.events)
	h.rec.mu.Unlock()
	row, _ := h.db.Lobbies().Get(context.Background(), l.ID)
	for _, secret := range []string{testPassword, "n3w-secret"} {
		for _, ev := range evs {
			if strings.Contains(string(ev.Data), secret) {
				t.Errorf("stored event %s has the password", ev.Type)
			}
		}
		for name, s := range map[string]string{"hooks": string(hooked), "logs": h.logs.String(),
			"spec column": string(row.Spec), "details column": string(row.Details)} {
			if strings.Contains(s, secret) {
				t.Errorf("%s contain the password", name)
			}
		}
	}
	got, _ := h.m.Get(context.Background(), l.ID)
	if got.Spec.Password.Reveal() != "n3w-secret" || row.PassKey != "n3w-secret" {
		t.Errorf("password not kept: %q / %q", got.Spec.Password.Reveal(), row.PassKey)
	}
}
