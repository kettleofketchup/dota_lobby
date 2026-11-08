package lobby

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

var cfgChat = func() config.LobbyConfig {
	c := cfgPure
	c.Chat = config.ChatConfig{Enabled: true, Prefix: "!", SendInterval: time.Second, StartCountdown: 10 * time.Second}
	c.Admins = []uint32{50}
	return c
}()

func TestParseCommand(t *testing.T) {
	tests := []struct {
		prefix, text string
		cmd          string
		ok           bool
	}{
		{"!", "!ready", "ready", true},
		{"!", "  !READY  now please", "ready", true},
		{"!", "!Start", "start", true},
		{"!", "ready", "", false},
		{"!", "! ready", "", false},
		{"!", "!", "", false},
		{"!", "/ready", "", false},
		{"!!", "!!teams", "teams", true},
		{"!!", "!teams", "", false},
		{"", "!ready", "", false},
	}
	for _, tt := range tests {
		cmd, ok := parseCommand(tt.prefix, tt.text)
		if cmd != tt.cmd || ok != tt.ok {
			t.Errorf("parseCommand(%q, %q) = %q, %v; want %q, %v", tt.prefix, tt.text, cmd, ok, tt.cmd, tt.ok)
		}
	}
}

func TestRunCommand(t *testing.T) {
	readyCheck := func(l Lobby) Lobby { l.Spec.ReadyCheck = true; return l }
	isReady := func(l Lobby, accounts ...uint32) Lobby {
		for _, a := range accounts {
			l.Spec.Teams.Player(a).Ready = true
		}
		return l
	}
	exhausted := func(l Lobby) Lobby { l.LaunchAttempts = 3; return l }
	unseatedHost := func(l Lobby) Lobby { l.HostSeat.Confirmed = false; return l }
	lobbyAdmin := func(l Lobby) Lobby { l.Spec.Admins = []uint32{60}; return l }
	oneOff := view(gc.LobbyUI, hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant), mem(2, gc.TeamPlayerPool), mem(3, gc.TeamDire))

	tests := []struct {
		name      string
		cmd       string
		lobby     Lobby
		view      *gc.LobbyView
		account   uint32
		countdown bool
		chatOff   bool
		effect    chatEffect
		force     bool
		reply     string // substring
		cooldown  time.Duration
	}{
		{"help", cmdHelp, rostered(StateWaiting, t0), seatedView(gc.LobbyUI), 99, false, false, effNone, false, "Commands: !teams, !start.", helpCooldown},
		{"help with ready check", cmdHelp, readyCheck(rostered(StateWaiting, t0)), seatedView(gc.LobbyUI), 1, false, false, effNone, false, "!teams, !ready, !unready, !start.", helpCooldown},
		{"help for an admin", cmdHelp, rostered(StateWaiting, t0), seatedView(gc.LobbyUI), 50, false, false, effNone, false, "!start, !cancel.", helpCooldown},
		{"teams", cmdTeams, rostered(StateWaiting, t0), oneOff, 3, false, false, effNone, false, "Radiant: 1/2 seated, missing 2. Dire: 1/1 seated.", teamsCooldown},

		{"ready", cmdReady, readyCheck(rostered(StateWaiting, t0)), seatedView(gc.LobbyUI), 1, false, false, effReady, false, "1 is ready (1/3 ready).", 0},
		{"ready again", cmdReady, isReady(readyCheck(rostered(StateWaiting, t0)), 1), seatedView(gc.LobbyUI), 1, false, false, effNone, false, "already ready", denyCooldown},
		{"ready off its side", cmdReady, readyCheck(rostered(StateWaiting, t0)), oneOff, 2, false, false, effNone, false, "Take a Radiant slot first", denyCooldown},
		{"ready not rostered", cmdReady, readyCheck(rostered(StateWaiting, t0)), seatedView(gc.LobbyUI), 99, false, false, effNone, false, "Only rostered players", denyCooldown},
		{"ready without ready check", cmdReady, rostered(StateWaiting, t0), seatedView(gc.LobbyUI), 1, false, false, effNone, false, "no ready check", denyCooldown},
		{"ready with chat off", cmdReady, readyCheck(rostered(StateWaiting, t0)), seatedView(gc.LobbyUI), 1, false, true, effNone, false, "no ready check", denyCooldown},
		{"ready while launching", cmdReady, readyCheck(rostered(StateLaunching, t0)), seatedView(gc.LobbyUI), 1, false, false, effNone, false, "Too late", denyCooldown},
		{"unready", cmdUnready, isReady(readyCheck(rostered(StateReady, t0)), 1, 2), seatedView(gc.LobbyUI), 2, false, false, effUnready, false, "2 is not ready (1/3 ready).", 0},
		{"unready when not ready", cmdUnready, readyCheck(rostered(StateWaiting, t0)), seatedView(gc.LobbyUI), 2, false, false, effNone, false, "not marked ready", denyCooldown},

		{"start by a player when ready", cmdStart, rostered(StateReady, t0), seatedView(gc.LobbyUI), 3, false, false, effStart, false, "Starting in 10 s. An admin can !cancel.", 0},
		{"start by a player while waiting", cmdStart, rostered(StateWaiting, t0), oneOff, 3, false, false, effNone, false, "Not ready yet: 2 not seated.", denyCooldown},
		{"start waiting on ready flags", cmdStart, readyCheck(rostered(StateWaiting, t0)), seatedView(gc.LobbyUI), 3, false, false, effNone, false, "1 not ready, 2 not ready, 3 not ready", denyCooldown},
		{"start by a stranger", cmdStart, rostered(StateReady, t0), seatedView(gc.LobbyUI), 99, false, false, effNone, false, "Only rostered players and admins", denyCooldown},
		{"start by an admin while waiting", cmdStart, rostered(StateWaiting, t0), oneOff, 50, false, false, effStart, true, "Starting in 10 s.", 0},
		{"start by a lobby admin", cmdStart, lobbyAdmin(rostered(StateWaiting, t0)), oneOff, 60, false, false, effStart, true, "Starting", 0},
		{"start twice", cmdStart, rostered(StateReady, t0), seatedView(gc.LobbyUI), 3, true, false, effNone, false, "Already starting", denyCooldown},
		{"start with no attempts left", cmdStart, exhausted(rostered(StateReady, t0)), seatedView(gc.LobbyUI), 3, false, false, effNone, false, "No launch attempts left", denyCooldown},
		{"admin start with no attempts left", cmdStart, exhausted(rostered(StateReady, t0)), seatedView(gc.LobbyUI), 50, false, false, effStart, true, "Starting", 0},
		{"start with the host on a team", cmdStart, unseatedHost(rostered(StateReady, t0)), seatedView(gc.LobbyUI), 50, false, false, effNone, false, "still leaving its player slot", denyCooldown},
		{"start in game", cmdStart, rostered(StateInGame, t0), seatedView(gc.LobbyRun), 50, false, false, effNone, false, "cannot start now", denyCooldown},

		{"cancel by an admin", cmdCancel, rostered(StateReady, t0), seatedView(gc.LobbyUI), 50, true, false, effCancel, false, "Start cancelled.", 0},
		{"cancel by a player", cmdCancel, rostered(StateReady, t0), seatedView(gc.LobbyUI), 1, true, false, effNone, false, "Only an admin", denyCooldown},
		{"cancel with nothing running", cmdCancel, rostered(StateReady, t0), seatedView(gc.LobbyUI), 50, false, false, effNone, false, "No start to cancel", denyCooldown},

		{"unknown command", "dance", rostered(StateReady, t0), seatedView(gc.LobbyUI), 1, false, false, effNone, false, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgChat
			if tt.chatOff {
				cfg.Chat.Enabled = false
			}
			out := runCommand(tt.cmd, chatInput{
				Lobby: tt.lobby, View: tt.view, Host: hostID, Account: tt.account,
				Admin: isAdmin(tt.lobby, cfg, tt.account), Countdown: tt.countdown, Cfg: cfg,
			})
			if out.Effect != tt.effect || out.Force != tt.force || out.Cooldown != tt.cooldown {
				t.Errorf("outcome %+v, want effect %d force %v cooldown %s", out, tt.effect, tt.force, tt.cooldown)
			}
			if tt.reply == "" && out.Reply != "" || !strings.Contains(out.Reply, tt.reply) {
				t.Errorf("reply %q, want it to contain %q", out.Reply, tt.reply)
			}
		})
	}
}

func TestTeamsText(t *testing.T) {
	l := rostered(StateWaiting, t0)
	l.Spec.ReadyCheck = true
	l.Spec.Teams.Radiant.Name, l.Spec.Teams.Dire.Name = "Ravens", "Owls"
	l.Spec.Teams.Player(1).Ready = true
	v := view(gc.LobbyUI, hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant), mem(2, gc.TeamDire))
	got := teamsText(chatInput{Lobby: l, View: v, Host: hostID, Names: map[uint32]string{2: "Bob"}, Cfg: cfgChat})
	want := "Radiant Ravens: 1/2 seated, 1 ready, on the wrong side Bob (2). Dire Owls: 0/1 seated, 0 ready, missing 3."
	if got != want {
		t.Errorf("teams\n got %q\nwant %q", got, want)
	}
	open := rostered(StateWaiting, t0)
	open.Spec.Teams = Roster{}
	if got := teamsText(chatInput{Lobby: open, View: v, Host: hostID, Cfg: cfgChat}); got != "Open lobby: Radiant 1, Dire 1." {
		t.Errorf("open lobby teams %q", got)
	}
}

// The ready check holds a seated lobby in waiting until every rostered
// player said they are ready, and a player who moves loses the flag.
func TestDeriveReadyCheck(t *testing.T) {
	cfg := cfgChat
	l := rostered(StateWaiting, t0)
	l.Spec.ReadyCheck = true
	in := inputs{Now: t0.Add(time.Second), View: seatedView(gc.LobbyUI), Host: hostID, BotReady: true, Cfg: cfg}

	res := derive(l, in)
	if res.Lobby.State != StateWaiting {
		t.Fatalf("seated but not ready: %s", res.Lobby.State)
	}
	for _, a := range []uint32{1, 2, 3} {
		l.Spec.Teams.Player(a).Ready = true
	}
	res = derive(l, in)
	if res.Lobby.State != StateReady || !slices.Equal(types(res.Events), []string{"lobby.ready"}) {
		t.Fatalf("all ready: %s %v", res.Lobby.State, types(res.Events))
	}

	// 2 moves to the pool, 3 leaves.
	moved := view(gc.LobbyUI, hostAt(gc.TeamPlayerPool), mem(1, gc.TeamRadiant), mem(2, gc.TeamPlayerPool))
	in.View = moved
	res = derive(res.Lobby, in)
	if res.Lobby.State != StateWaiting || res.Lobby.Spec.Teams.Player(2).Ready || res.Lobby.Spec.Teams.Player(3).Ready || !res.Lobby.Spec.Teams.Player(1).Ready {
		t.Fatalf("after moves: %s %+v", res.Lobby.State, res.Lobby.Spec.Teams)
	}
	var reasons []string
	for _, ev := range res.Events {
		if ev.Type == "lobby.player_unready" {
			reasons = append(reasons, ev.Data["reason"].(string))
		}
	}
	if !slices.Equal(reasons, []string{"moved", "left"}) {
		t.Errorf("unready reasons %v, events %v", reasons, types(res.Events))
	}

	// A member whose team is not sent keeps the flag; so does everyone
	// while a flip is on its way.
	keep := l.clone()
	unknown := view(gc.LobbyUI, hostAt(gc.TeamPlayerPool), gc.Member{SteamID: gc.SteamID64(1)}, mem(2, gc.TeamDire), mem(3, gc.TeamRadiant))
	res = derive(keep, inputs{Now: t0.Add(time.Second), View: unknown, Host: hostID, BotReady: true, Flipping: true, Cfg: cfg})
	for _, a := range []uint32{1, 2, 3} {
		if !res.Lobby.Spec.Teams.Player(a).Ready {
			t.Errorf("player %d lost the flag during a flip", a)
		}
	}
	res = derive(keep, inputs{Now: t0.Add(time.Second), View: unknown, Host: hostID, BotReady: true, Cfg: cfg})
	if !res.Lobby.Spec.Teams.Player(1).Ready || res.Lobby.Spec.Teams.Player(2).Ready {
		t.Errorf("unknown team: %+v", res.Lobby.Spec.Teams)
	}

	// Without chat the flag is not needed.
	off := rostered(StateWaiting, t0)
	off.Spec.ReadyCheck = true
	in = inputs{Now: t0.Add(time.Second), View: seatedView(gc.LobbyUI), Host: hostID, BotReady: true, Cfg: cfgPure}
	if res := derive(off, in); res.Lobby.State != StateReady {
		t.Errorf("chat off: %s", res.Lobby.State)
	}
}

func TestReadinessReadyCheck(t *testing.T) {
	l := rostered(StateWaiting, t0)
	l.Spec.Teams.Player(1).Ready = true
	ready, missing := readiness(l, seatedView(gc.LobbyUI), hostID, true)
	if ready || !slices.Equal(missing, []string{"unready:2", "unready:3"}) {
		t.Errorf("readiness = %v %v", ready, missing)
	}
}

func TestSendQueue(t *testing.T) {
	q := newSendQueue()
	for i := range chatQueueSize + 5 {
		q.push(chatLine{Channel: 1, Text: string(rune('a' + i))})
	}
	var (
		mu    sync.Mutex
		sent  []string
		times []time.Time
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.run(ctx, 20*time.Millisecond, func(l chatLine) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, l.Text)
			times = append(times, time.Now())
			return nil
		}, nil)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n == chatQueueSize {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sent %d lines", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// One more after the queue went idle.
	q.push(chatLine{Channel: 1, Text: "late"})
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if want := strings.Split("f g h i j k l m n o late", " "); !slices.Equal(sent, want) {
		t.Errorf("sent %v, want the newest %d then late: %v", sent, chatQueueSize, want)
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 18*time.Millisecond {
			t.Errorf("lines %d and %d only %s apart", i-1, i, gap)
		}
	}
}

func TestChatTextHelpers(t *testing.T) {
	long := strings.Repeat("é", maxChatText)
	if got := truncateChat(long); len(got) > maxChatText || !strings.HasSuffix(got, "...") || !strings.HasPrefix(got, "é") {
		t.Errorf("truncateChat: %d bytes", len(got))
	}
	if got := truncateChat("short"); got != "short" {
		t.Errorf("truncateChat(short) = %q", got)
	}
	if got := cleanName(" Bob\x00\n" + strings.Repeat("x", 40)); got != "Bob"+strings.Repeat("x", 29) {
		t.Errorf("cleanName = %q", got)
	}
	if got := missingText([]string{"2", "unready:3", "stranger:8", "dire"}, map[uint32]string{3: "Cat"}); got != "2 not seated, Cat (3) not ready, 8 is not rostered, nobody on dire" {
		t.Errorf("missingText = %q", got)
	}
}
