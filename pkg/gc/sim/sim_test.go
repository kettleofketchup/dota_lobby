package sim

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

const (
	botAccount uint32 = 4_000_000_001
	alice      uint32 = 11111111
	bob        uint32 = 22222222
	league     uint32 = 17000
)

func newBot(t *testing.T, w *World) *Client {
	t.Helper()
	return w.Client(Bot{Name: "bot1", AccountID: botAccount, Leagues: []uint32{league}})
}

// next reads one event or fails.
func next(t *testing.T, c *Client) gc.Event {
	t.Helper()
	select {
	case ev := <-c.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return nil
	}
}

// ready logs c on and waits for the welcome, as the supervisor does.
func ready(t *testing.T, c *Client) {
	t.Helper()
	if err := c.Connect(t.Context(), gc.Credentials{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"connected", "logged_on", "account_info"} {
		ev := next(t, c)
		got := map[string]bool{
			"connected":    is[gc.Connected](ev),
			"logged_on":    is[gc.LoggedOn](ev),
			"account_info": is[gc.AccountInfo](ev),
		}
		if !got[want] {
			t.Fatalf("event %T, want %s", ev, want)
		}
	}
	c.SetPlaying(true)
	c.SayHello()
	if ev := next(t, c); !is[gc.GCWelcomed](ev) {
		t.Fatalf("event %T, want GCWelcomed", ev)
	}
}

func is[T gc.Event](ev gc.Event) bool {
	_, ok := ev.(T)
	return ok
}

// waitLobby waits until the host's lobby satisfies cond.
func waitLobby(t *testing.T, c *Client, cond func(v *gc.LobbyView) bool) *gc.LobbyView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := c.CurrentLobby(); ok && cond(v) {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	v, _ := c.CurrentLobby()
	t.Fatalf("lobby never matched; last %+v", v)
	return nil
}

func memberOf(v *gc.LobbyView, account uint32) (gc.Member, bool) {
	for _, m := range v.Members {
		if gc.AccountID(m.SteamID) == account {
			return m, true
		}
	}
	return gc.Member{}, false
}

func TestLogonAndDisconnect(t *testing.T) {
	w := New(Options{})
	c := newBot(t, w)
	if c.SteamID() != 0 {
		t.Error("SteamID before logon")
	}
	if _, err := c.CreateLobby(t.Context(), gc.LobbyDetails{}); !errors.Is(err, gc.ErrNotConnected) {
		t.Errorf("create before connect = %v", err)
	}
	ready(t, c)
	if c.SteamID() != gc.SteamID64(botAccount) {
		t.Errorf("SteamID = %d", c.SteamID())
	}
	if err := c.Connect(t.Context(), gc.Credentials{}); !errors.Is(err, gc.ErrAlreadyConnected) {
		t.Errorf("second connect = %v", err)
	}
	if _, err := c.Authenticate(t.Context(), gc.Credentials{}); !errors.Is(err, gc.ErrNoCredentials) {
		t.Errorf("authenticate = %v", err)
	}
	c.Disconnect()
	if ev := next(t, c); ev != (gc.Disconnected{Conn: 1}) {
		t.Errorf("event %#v, want Disconnected{1}", ev)
	}
	if err := c.Invite(1); !errors.Is(err, gc.ErrNotConnected) {
		t.Errorf("invite while disconnected = %v", err)
	}

	// Cancelling the connect ctx disconnects too.
	ctx, cancel := context.WithCancel(t.Context())
	if err := c.Connect(ctx, gc.Credentials{}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		next(t, c)
	}
	cancel()
	if ev := next(t, c); ev != (gc.Disconnected{Conn: 2}) {
		t.Errorf("event %#v, want Disconnected{2}", ev)
	}
}

func TestHelloNeedsPlaying(t *testing.T) {
	w := New(Options{})
	c := newBot(t, w)
	if err := c.Connect(t.Context(), gc.Credentials{}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		next(t, c)
	}
	c.SayHello()
	if _, err := c.ListAdminLeagues(t.Context()); !errors.Is(err, gc.ErrNotReady) {
		t.Errorf("leagues before welcome = %v", err)
	}
	select {
	case ev := <-c.Events():
		t.Errorf("welcomed without SetPlaying: %T", ev)
	default:
	}
}

func TestCreateLobby(t *testing.T) {
	w := New(Options{})
	c := newBot(t, w)
	ready(t, c)
	lobbies, stop := c.SubscribeLobby()
	defer stop()

	v, err := c.CreateLobby(t.Context(), gc.LobbyDetails{
		GameName: "Cup R1", PassKey: "pw", LeagueID: league, GameMode: 2, ServerRegion: 3, TVDelaySeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.LeagueID != league || v.GameName != "Cup R1" || v.GameMode != 2 || v.ServerRegion != 3 ||
		v.TVDelaySeconds != 120 || v.State != gc.LobbyUI || v.LeaderID != gc.SteamID64(botAccount) {
		t.Errorf("view = %+v", v)
	}
	if m, ok := memberOf(v, botAccount); !ok || m.Team != gc.TeamRadiant || !m.TeamKnown {
		t.Errorf("host = %+v %v", m, ok)
	}
	if ev := <-lobbies; ev.Kind != gc.LobbyCreated || ev.Lobby.LobbyID != v.LobbyID {
		t.Errorf("event = %v %+v", ev.Kind, ev.Lobby)
	}
	info, err := w.Lobby(v.LobbyID)
	if err != nil || info.Details.PassKey != "pw" || info.Bot != "bot1" {
		t.Errorf("info = %+v %v", info, err)
	}

	// A league the bot does not administer reads back as 0, with the
	// NOT_LEAGUE_ADMIN answer; the new lobby replaced the old one.
	_, err = c.CreateLobby(t.Context(), gc.LobbyDetails{GameName: "x", LeagueID: 999})
	var ce *gc.CreateError
	if !errors.As(err, &ce) || ce.Reason != gc.CreateLeagueRejected || ce.PopupID != gc.PopupNotLeagueAdmin {
		t.Fatalf("err = %v", err)
	}
	cur, ok := c.CurrentLobby()
	if !ok || cur.LeagueID != 0 || cur.LobbyID == v.LobbyID || cur.TVDelaySeconds != 10 {
		t.Errorf("current = %+v", cur)
	}
	if _, err := w.Lobby(v.LobbyID); !errors.Is(err, ErrNoLobby) {
		t.Errorf("old lobby still there: %v", err)
	}
	if ev := next(t, c); !is[gc.Popup](ev) {
		t.Errorf("event %T, want Popup", ev)
	}
	if err := c.Destroy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.CurrentLobby(); ok {
		t.Error("lobby left after destroy")
	}
	if err := c.Destroy(t.Context()); err == nil {
		t.Error("destroy without a lobby succeeded")
	}
	if _, err := c.CreateLobby(t.Context(), gc.LobbyDetails{TVDelaySeconds: 7}); err == nil {
		t.Error("unsupported tv delay accepted")
	}

	// Without a delay, a league lobby reads back 120 s, as live, and any
	// other 10 s.
	for _, tc := range []struct {
		league uint32
		want   uint32
	}{{league, 120}, {0, 10}} {
		v, err := c.CreateLobby(t.Context(), gc.LobbyDetails{LeagueID: tc.league})
		if err != nil {
			t.Fatal(err)
		}
		if v.TVDelaySeconds != tc.want {
			t.Errorf("league %d: tv delay %d, want %d", tc.league, v.TVDelaySeconds, tc.want)
		}
	}
}

func TestManualLifecycle(t *testing.T) {
	w := New(Options{ReadyCommand: "!ready"})
	c := newBot(t, w)
	ready(t, c)
	chats, stopChat := c.SubscribeChat()
	defer stopChat()
	invites, stopInv := c.SubscribeInvites()
	defer stopInv()

	v, err := c.CreateLobby(t.Context(), gc.LobbyDetails{GameName: "g"})
	if err != nil {
		t.Fatal(err)
	}
	id := v.LobbyID
	if err := c.JoinBroadcastChannel(0); err != nil {
		t.Fatal(err)
	}
	if err := c.Invite(gc.SteamID64(alice)); err != nil {
		t.Fatal(err)
	}
	if inv := <-invites; inv.SteamID != gc.SteamID64(alice) {
		t.Errorf("invite = %+v", inv)
	}
	v, _ = c.CurrentLobby()
	if m, _ := memberOf(v, botAccount); m.Team != gc.TeamBroadcaster || !slices.Contains(v.Pending, gc.SteamID64(alice)) {
		t.Errorf("view = %+v", v)
	}

	if _, err := w.Join(id, alice, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Join(id, alice, ""); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("second join = %v", err)
	}
	if _, err := w.Join(id, botAccount, ""); !errors.Is(err, ErrBadAccount) {
		t.Errorf("bot join = %v", err)
	}
	slot := uint32(2)
	if _, err := w.Seat(id, alice, gc.TeamDire, &slot); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Join(id, bob, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Seat(id, bob, gc.TeamDire, &slot); !errors.Is(err, ErrSeatTaken) {
		t.Errorf("taken slot = %v", err)
	}
	if _, err := w.Seat(id, bob, gc.TeamRadiant, nil); err != nil {
		t.Fatal(err)
	}
	v, _ = c.CurrentLobby()
	if m, _ := memberOf(v, alice); m.Team != gc.TeamDire || m.Slot != 2 || len(v.Pending) != 0 {
		t.Errorf("view = %+v", v)
	}

	// Chat: unseen until the host joins, then delivered.
	if delivered, err := w.Ready(id, alice); err != nil || delivered {
		t.Errorf("ready before chat join = %v %v", delivered, err)
	}
	ch, err := c.JoinLobbyChat(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.JoinLobbyChat(t.Context(), id+1); err == nil {
		t.Error("joined another lobby's chat")
	}
	if delivered, err := w.Ready(id, alice); err != nil || !delivered {
		t.Errorf("ready = %v %v", delivered, err)
	}
	if m := <-chats; m.ChannelID != ch || m.AccountID != alice || m.Text != "!ready" || m.PersonaName != "Alice" {
		t.Errorf("chat = %+v", m)
	}
	if err := c.SendChat(ch, "hello"); err != nil {
		t.Fatal(err)
	}
	info, _ := w.Lobby(id)
	if n := len(info.Chat); n != 3 || info.Chat[2].AccountID != botAccount || info.Chat[2].Text != "hello" {
		t.Errorf("chat log = %+v", info.Chat)
	}
	if _, err := w.Say(id, 999, "hi"); !errors.Is(err, ErrNotMember) {
		t.Errorf("say by stranger = %v", err)
	}

	// Kicks, flip and shuffle as the GC does them.
	if err := c.KickFromTeam(bob); err != nil {
		t.Fatal(err)
	}
	if err := c.FlipTeams(); err != nil {
		t.Fatal(err)
	}
	v, _ = c.CurrentLobby()
	if a, _ := memberOf(v, alice); a.Team != gc.TeamRadiant {
		t.Errorf("alice after flip = %+v", a)
	}
	if b, _ := memberOf(v, bob); b.Team != gc.TeamPlayerPool {
		t.Errorf("bob after kick from team = %+v", b)
	}
	if err := c.Kick(bob); err != nil {
		t.Fatal(err)
	}
	if v, _ = c.CurrentLobby(); len(v.Members) != 2 {
		t.Errorf("members after kick = %+v", v.Members)
	}

	// Launch, start, end.
	if _, err := w.StartMatch(id, 0); !errors.Is(err, ErrWrongState) {
		t.Errorf("start before launch = %v", err)
	}
	if err := c.Launch(); err != nil {
		t.Fatal(err)
	}
	if v, _ = c.CurrentLobby(); v.State != gc.LobbyServerAssign || v.MatchID != 0 {
		t.Errorf("after launch = %+v", v)
	}
	info, err = w.StartMatch(id, 0)
	if err != nil || info.MatchID == 0 || info.State != gc.LobbyRun {
		t.Fatalf("start = %+v %v", info, err)
	}
	if _, err := c.MatchDetails(t.Context(), info.MatchID); err == nil {
		t.Error("match details before the end")
	}
	info, err = w.EndMatch(id, OutcomeDireVictory)
	if err != nil || info.State != gc.LobbyPostGame || info.Outcome != OutcomeDireVictory {
		t.Fatalf("end = %+v %v", info, err)
	}
	if _, err := w.EndMatch(id, OutcomeDireVictory); !errors.Is(err, ErrWrongState) {
		t.Errorf("second end = %v", err)
	}
	res, err := c.MatchDetails(t.Context(), info.MatchID)
	if err != nil || res.Outcome != OutcomeDireVictory || len(res.Players) != 1 || res.Players[0].AccountID != alice {
		t.Errorf("match details = %+v %v", res, err)
	}
	if _, err := w.Leave(id, alice); err != nil {
		t.Fatal(err)
	}
	c.Leave()
	if _, err := w.Lobby(id); !errors.Is(err, ErrNoLobby) {
		t.Errorf("lobby after the host left = %v", err)
	}
}

// staticRoster puts alice on Radiant and bob on Dire.
func staticRoster(_ uint64, account uint32) (gc.Team, bool) {
	switch account {
	case alice:
		return gc.TeamRadiant, true
	case bob:
		return gc.TeamDire, true
	}
	return 0, false
}

func TestAutoMode(t *testing.T) {
	w := New(Options{ReadyCommand: "!ready", Settings: Settings{
		AutoJoin: true, AutoJoinDelay: 10 * time.Millisecond, AutoReady: true,
		AutoStart: true, AutoStartDelay: 10 * time.Millisecond,
	}})
	defer w.Close()
	w.SetRoster(staticRoster)
	c := newBot(t, w)
	ready(t, c)
	chats, stop := c.SubscribeChat()
	defer stop()

	if _, err := c.CreateLobby(t.Context(), gc.LobbyDetails{GameName: "auto"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []uint32{alice, bob, 33333333} {
		if err := c.Invite(gc.SteamID64(a)); err != nil {
			t.Fatal(err)
		}
	}
	v := waitLobby(t, c, func(v *gc.LobbyView) bool {
		a, okA := memberOf(v, alice)
		b, okB := memberOf(v, bob)
		_, okS := memberOf(v, 33333333)
		return okA && okB && okS && a.Team == gc.TeamRadiant && b.Team == gc.TeamDire
	})
	if s, _ := memberOf(v, 33333333); s.Team != gc.TeamPlayerPool {
		t.Errorf("unrostered player = %+v", s)
	}

	// The ready lines wait for the host to join the chat.
	ch, err := c.JoinLobbyChat(t.Context(), v.LobbyID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32]bool{}
	for len(got) < 2 {
		select {
		case m := <-chats:
			if m.ChannelID != ch || m.Text != "!ready" {
				t.Errorf("chat = %+v", m)
			}
			got[m.AccountID] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("ready lines = %v", got)
		}
	}

	if err := c.Launch(); err != nil {
		t.Fatal(err)
	}
	v = waitLobby(t, c, func(v *gc.LobbyView) bool { return v.State == gc.LobbyRun })
	if v.MatchID <= matchIDBase || v.MatchID > math.MaxInt64 {
		t.Errorf("match id = %d", v.MatchID)
	}

	// A failed load goes back to the UI and keeps the stale match ID.
	if _, err := w.Fail(v.LobbyID, FailLoadFailed); err != nil {
		t.Fatal(err)
	}
	if v, _ = c.CurrentLobby(); v.State != gc.LobbyUI {
		t.Errorf("after load failure = %+v", v)
	}
	if _, err := w.Fail(v.LobbyID, FailLost); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.CurrentLobby(); ok || len(w.Lobbies()) != 0 {
		t.Error("lost lobby still there")
	}
	if _, err := w.Fail(v.LobbyID, FailLost); !errors.Is(err, ErrNoLobby) {
		t.Errorf("fail of a gone lobby = %v", err)
	}
}

// Players invited together join, sit down and say ready in invite order,
// whatever order their timers fire in.
func TestAutoJoinOrder(t *testing.T) {
	invites := []uint32{105, 103, 101, 104, 102}
	radiant := func(_ uint64, account uint32) (gc.Team, bool) { return gc.TeamRadiant, account > 100 }
	for run := range 50 {
		w := New(Options{ReadyCommand: "!ready", Settings: Settings{AutoJoin: true, AutoReady: true}})
		w.SetRoster(radiant)
		c := newBot(t, w)
		ready(t, c)
		v, err := c.CreateLobby(t.Context(), gc.LobbyDetails{})
		if err != nil {
			t.Fatal(err)
		}
		// The host leaves Radiant so all five fit.
		if err := c.JoinTeam(gc.TeamPlayerPool, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := c.JoinLobbyChat(t.Context(), v.LobbyID); err != nil {
			t.Fatal(err)
		}
		for _, a := range invites {
			if err := c.Invite(gc.SteamID64(a)); err != nil {
				t.Fatal(err)
			}
		}
		var info LobbyInfo
		deadline := time.Now().Add(3 * time.Second)
		for {
			info, _ = w.Lobby(v.LobbyID)
			if len(info.Chat) == len(invites) || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		w.Close()
		var joined, said []uint32
		for _, m := range info.Members[1:] {
			joined = append(joined, m.AccountID)
			if m.Team != gc.TeamRadiant || int(m.Slot) != len(joined)-1 {
				t.Fatalf("run %d: %d sits %s %d", run, m.AccountID, m.Team, m.Slot)
			}
		}
		for _, l := range info.Chat {
			said = append(said, l.AccountID)
		}
		if !slices.Equal(joined, invites) || !slices.Equal(said, invites) {
			t.Fatalf("run %d: joined %v, said ready %v, invited %v", run, joined, said, invites)
		}
	}
}

func TestSettingsAndClose(t *testing.T) {
	w := New(Options{})
	w.SetSettings(Settings{AutoJoin: true, AutoJoinDelay: time.Hour})
	if s := w.Settings(); !s.AutoJoin || s.AutoJoinDelay != time.Hour {
		t.Errorf("settings = %+v", s)
	}
	c := newBot(t, w)
	ready(t, c)
	if _, err := c.CreateLobby(t.Context(), gc.LobbyDetails{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Invite(gc.SteamID64(alice)); err != nil {
		t.Fatal(err)
	}
	w.Close() // stops the hour-long join
	w.mu.Lock()
	n := len(w.timers)
	w.mu.Unlock()
	if n != 0 {
		t.Errorf("%d timers left", n)
	}
	if _, err := w.Ready(1, alice); !errors.Is(err, ErrNoReadyCommand) {
		t.Errorf("ready without a command = %v", err)
	}
}

func TestTeamFromString(t *testing.T) {
	for _, s := range []string{"radiant", "DIRE", "player_pool", "spectator", "broadcaster", "no_team"} {
		if _, ok := TeamFromString(s); !ok {
			t.Errorf("%q not parsed", s)
		}
	}
	if _, ok := TeamFromString("pool"); ok {
		t.Error("pool parsed")
	}
}

// Control calls and bot calls race on the same lobby; -race checks the
// locking.
func TestConcurrentUse(t *testing.T) {
	w := New(Options{ReadyCommand: "!ready", Settings: Settings{AutoJoin: true, AutoStart: true}})
	defer w.Close()
	w.SetRoster(staticRoster)
	c := newBot(t, w)
	ready(t, c)
	v, err := c.CreateLobby(t.Context(), gc.LobbyDetails{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.JoinLobbyChat(t.Context(), v.LobbyID); err != nil {
		t.Fatal(err)
	}
	sub, stop := c.SubscribeLobby()
	defer stop()
	var wg sync.WaitGroup
	for i := range 8 {
		account := uint32(50_000_000 + i)
		wg.Go(func() {
			_ = c.Invite(gc.SteamID64(account))
			_, _ = w.Join(v.LobbyID, account, "")
			_, _ = w.Seat(v.LobbyID, account, gc.TeamRadiant, nil)
			_, _ = w.Say(v.LobbyID, account, "hi")
			_ = c.KickFromTeam(account)
			_ = c.FlipTeams()
			_, _ = c.CurrentLobby()
			_ = w.Lobbies()
		})
	}
	wg.Go(func() {
		for range 50 {
			select {
			case <-sub:
			default:
			}
		}
	})
	wg.Wait()
}
