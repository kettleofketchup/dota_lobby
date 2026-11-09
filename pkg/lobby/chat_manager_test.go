package lobby

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

const chatAdmin = 50

func withChat(countdown time.Duration) func(*config.LobbyConfig) {
	return func(c *config.LobbyConfig) {
		c.Chat = config.ChatConfig{Enabled: true, Prefix: "!", SendInterval: 5 * time.Millisecond, StartCountdown: countdown}
		c.Admins = []uint32{chatAdmin}
	}
}

// chatWaiting creates a lobby, waits until it is waiting with its chat
// joined and greeted, and returns it with its bot.
func (h *harness) chatWaiting(s Spec) (Lobby, *testBot) {
	h.t.Helper()
	l, b := h.toWaiting(s)
	h.waitChat(b, "Type !help for commands.")
	return l, b
}

func (b *testBot) say(l Lobby, account uint32, text string) {
	b.fc.EmitChat(gc.ChatMessage{ChannelID: l.GCLobbyID + 1<<32, AccountID: account, PersonaName: "Player" + string(rune('A'+account%26)), Text: text})
}

func (h *harness) waitChat(b *testBot, substr string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, s := range b.fc.ChatSent() {
			if strings.Contains(s, substr) {
				return
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no chat message with %q; sent %q", substr, b.fc.ChatSent())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func chatCount(b *testBot, substr string) int {
	n := 0
	for _, s := range b.fc.ChatSent() {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

func (h *harness) eventData(id, typ string) []map[string]any {
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	var out []map[string]any
	for _, ev := range h.rec.events {
		if ev.LobbyID == id && ev.Type == typ {
			out = append(out, ev.Data)
		}
	}
	return out
}

func TestChatReadyCheckAndStart(t *testing.T) {
	h := newHarness(t, 1, withChat(50*time.Millisecond))
	s := spec("game-chat")
	s.ReadyCheck = true
	l, b := h.chatWaiting(s)
	if got := b.fc.Calls("JoinLobbyChat")[0].Args[0].(uint64); got != l.GCLobbyID {
		t.Errorf("joined the chat of lobby %d, want %d", got, l.GCLobbyID)
	}
	h.seatAll(b)
	time.Sleep(50 * time.Millisecond)
	if l := h.waitState(l.ID, StateWaiting); l.State != StateWaiting {
		t.Fatal("seated lobby went ready without a ready check")
	}

	// Only the account ID the GC set counts: a stranger naming himself
	// after a player, the bot itself and another channel are ignored.
	b.fc.EmitChat(gc.ChatMessage{ChannelID: l.GCLobbyID + 1<<32, AccountID: 99, PersonaName: "SpoofName", Text: "!ready"})
	b.fc.EmitChat(gc.ChatMessage{ChannelID: l.GCLobbyID + 1<<32, AccountID: gc.AccountID(b.host), Text: "!ready"})
	b.fc.EmitChat(gc.ChatMessage{ChannelID: 12345, AccountID: 11, Text: "!ready"})
	time.Sleep(50 * time.Millisecond)
	if got := h.eventData(l.ID, "lobby.player_ready"); len(got) != 0 {
		t.Fatalf("ready from a non-member: %v", got)
	}

	for _, a := range []uint32{11, 12, 21} {
		b.say(l, a, "!ready")
	}
	h.waitState(l.ID, StateReady)
	h.waitChat(b, "Everyone is set. Type !start to launch.")
	if got := h.eventData(l.ID, "lobby.player_ready"); len(got) != 3 {
		t.Errorf("player_ready events %v", got)
	}

	// A player who leaves their side loses the flag and the lobby is not
	// ready any more.
	b.fc.UpdateLobby(func(v *gc.LobbyView) {
		for i := range v.Members {
			if gc.AccountID(v.Members[i].SteamID) == 12 {
				v.Members[i].Team = gc.TeamPlayerPool
			}
		}
	})
	l = h.waitState(l.ID, StateWaiting)
	if l.Spec.Teams.Player(12).Ready || !l.Spec.Teams.Player(11).Ready {
		t.Fatalf("ready flags after a move: %+v", l.Spec.Teams)
	}
	b.fc.UpdateLobby(func(v *gc.LobbyView) {
		for i := range v.Members {
			if gc.AccountID(v.Members[i].SteamID) == 12 {
				v.Members[i].Team = gc.TeamRadiant
			}
		}
	})
	h.clock.advance(readyCooldown)
	b.say(l, 12, "!ready")
	h.waitState(l.ID, StateReady)

	b.say(l, 21, "!start")
	h.waitChat(b, "Starting in 0 s.")
	h.waitCalls(b, "Launch", 1)
	h.waitChat(b, "Launching.")
	launching := h.eventData(l.ID, "lobby.launching")
	if len(launching) != 1 || launching[0]["trigger"] != TriggerChat || launching[0]["by_account_id"] != uint32(21) {
		t.Errorf("lobby.launching %v", launching)
	}
	logs := h.logs.String()
	if strings.Contains(logs, "SpoofName") || strings.Contains(logs, "!ready") {
		t.Error("chat text or persona names reached the log")
	}
	if !strings.Contains(logs, `"msg":"chat command"`) {
		t.Error("commands are not logged")
	}
}

// The countdown runs on the manager's clock: it launches when the clock
// says so, never on wall time alone.
func TestChatAdminStartAndCancel(t *testing.T) {
	h := newHarness(t, 1, withChat(10*time.Minute))
	l, b := h.chatWaiting(spec("game-admin"))
	b.join(chatAdmin, gc.TeamSpectator)
	b.join(11, gc.TeamRadiant)
	time.Sleep(30 * time.Millisecond)

	b.say(l, 11, "!start")
	h.waitChat(b, "Not ready yet: 12 not seated, 21 not seated.")
	b.say(l, chatAdmin, "!start")
	h.waitChat(b, "Starting in 600 s.")
	b.say(l, 11, "!cancel")
	h.waitChat(b, "Only an admin can !cancel.")
	b.say(l, chatAdmin, "!cancel")
	h.waitChat(b, "Start cancelled.")
	h.clock.advance(11 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	if n := b.fc.CallCount("Launch"); n != 0 {
		t.Fatalf("a cancelled start launched %d times", n)
	}

	// The admin's start is a forced launch from waiting, once the
	// countdown is over.
	b.say(l, chatAdmin, "!start")
	h.waitFor(l.ID, "a second start", func(Lobby) bool { return chatCount(b, "Starting in 600 s.") == 2 })
	time.Sleep(50 * time.Millisecond)
	if n := b.fc.CallCount("Launch"); n != 0 {
		t.Fatalf("launched %d times before the countdown ended", n)
	}
	h.clock.advance(10 * time.Minute)
	h.waitCalls(b, "Launch", 1)
	h.waitState(l.ID, StateLaunching)

	// Each reply has its cooldown: of two !cancel from the player in the
	// same window only the first is answered. The clock has moved past
	// the earlier denial's cooldown.
	b.say(l, 11, "!cancel")
	b.say(l, 11, "!cancel")
	time.Sleep(50 * time.Millisecond)
	if n := chatCount(b, "Only an admin"); n != 2 {
		t.Errorf("denial sent %d times, want 2", n)
	}
}

// Ready and unready share a cooldown: a player toggling them gets one
// stored change per readyCooldown.
func TestChatReadyToggleCooldown(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Second))
	s := spec("game-toggle")
	s.ReadyCheck = true
	l, b := h.chatWaiting(s)
	h.seatAll(b)
	b.say(l, 11, "!ready")
	h.waitChat(b, "is ready (1/3 ready).")
	for range 3 {
		b.say(l, 11, "!unready")
		b.say(l, 11, "!ready")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(h.eventData(l.ID, "lobby.player_ready")) + len(h.eventData(l.ID, "lobby.player_unready")); n != 1 {
		t.Fatalf("%d ready changes inside the cooldown, want 1", n)
	}
	h.clock.advance(readyCooldown)
	b.say(l, 11, "!unready")
	h.waitChat(b, "is not ready (0/3 ready).")
}

// A join the GC never answers runs off the lobby's op lock: a DELETE
// meanwhile answers at once, and the lobby is closed without waiting for
// the join.
func TestChatJoinDoesNotHoldTheLobby(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Second))
	b := h.bots[0]
	b.fc.BlockUntilDone("JoinLobbyChat", true)
	l, _ := h.toWaiting(spec("game-hang"))
	h.waitCalls(b, "JoinLobbyChat", 1)
	start := time.Now()
	if _, _, err := h.m.Close(context.Background(), l.ID); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("close waited %v behind the chat join", d)
	}
	h.waitState(l.ID, StateClosed)
	if n := b.fc.CallCount("JoinLobbyChat"); n != 1 {
		t.Errorf("joined %d times", n)
	}
}

func TestChatCountdownStopsWhenUnready(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Hour))
	l, b := h.chatWaiting(spec("game-unready"))
	h.seatAll(b)
	h.waitState(l.ID, StateReady)
	b.say(l, 11, "!start")
	h.waitChat(b, "Starting in")
	b.fc.UpdateLobby(func(v *gc.LobbyView) {
		v.Members = slices.DeleteFunc(v.Members, func(m gc.Member) bool { return gc.AccountID(m.SteamID) == 21 })
	})
	h.waitChat(b, "Start cancelled: the lobby is not ready any more.")
	if n := b.fc.CallCount("Launch"); n != 0 {
		t.Errorf("launched %d times", n)
	}
}

func TestChatHelpTeamsAndSeatNotice(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Second))
	l, b := h.chatWaiting(spec("game-help"))
	b.join(11, gc.TeamRadiant)
	b.say(l, 11, "!HELP")
	h.waitChat(b, "Commands: !teams, !start.")
	b.say(l, 11, "!help")
	b.say(l, 11, "!teams")
	h.waitChat(b, "Radiant Ravens: 1/2 seated, missing 12. Dire Owls: 0/1 seated, missing 21.")
	if n := chatCount(b, "Commands:"); n != 1 {
		t.Errorf("help answered %d times inside its cooldown", n)
	}

	// A rostered player on the other side is moved and told why.
	b.join(21, gc.TeamRadiant)
	h.waitChat(b, "21: your seat is Dire.")
}

func TestChatRejoinAndLeave(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Second))
	l, b := h.chatWaiting(spec("game-rejoin"))

	// A new GC session has left every channel: join again, without a
	// second greeting.
	b.handle.SetReady(time.Now())
	h.waitCalls(b, "JoinLobbyChat", 2)
	b.join(11, gc.TeamRadiant)
	b.say(l, 11, "!teams")
	h.waitChat(b, "Radiant Ravens")
	if n := chatCount(b, "Type !help"); n != 1 {
		t.Errorf("greeted %d times", n)
	}

	if _, _, err := h.m.Close(context.Background(), l.ID); err != nil {
		t.Fatal(err)
	}
	h.waitState(l.ID, StateClosed)
	h.waitCalls(b, "LeaveChat", 1)
	if got := b.fc.Calls("LeaveChat")[0].Args[0].(uint64); got != l.GCLobbyID+1<<32 {
		t.Errorf("left channel %d", got)
	}
}

func TestChatJoinFailureAndChatOff(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Second))
	b := h.bots[0]
	b.fc.FailAlways("JoinLobbyChat", &gc.ChatJoinError{Channel: "Lobby_1", Result: "USER_IN_TOO_MANY_CHANNELS"})
	l, _ := h.toWaiting(spec("game-nochat"))
	h.waitCalls(b, "JoinLobbyChat", 1)
	h.seatAll(b)
	// The lobby works without chat; the join waits for its retry.
	h.waitState(l.ID, StateReady)
	if n := b.fc.CallCount("JoinLobbyChat"); n != 1 || b.fc.CallCount("SendChat") != 0 {
		t.Errorf("joins %d, sends %d", n, b.fc.CallCount("SendChat"))
	}
	// Each failure doubles the wait before the next join.
	failed := func(n int) func(Lobby) bool {
		return func(Lobby) bool { return strings.Count(h.logs.String(), "could not join the lobby chat") >= n }
	}
	h.waitFor(l.ID, "the first failure", failed(1))
	h.clock.advance(chatRetry)
	h.waitCalls(b, "JoinLobbyChat", 2)
	h.waitFor(l.ID, "the second failure", failed(2))
	h.clock.advance(chatRetry)
	time.Sleep(50 * time.Millisecond)
	if n := b.fc.CallCount("JoinLobbyChat"); n != 2 {
		t.Errorf("joined %d times %v after the second failure", n, chatRetry)
	}
	h.clock.advance(chatRetry)
	h.waitCalls(b, "JoinLobbyChat", 3)
	if !strings.Contains(h.logs.String(), "USER_IN_TOO_MANY_CHANNELS") {
		t.Error("the refusal is not in the log")
	}

	off := newHarness(t, 1, nil)
	l, ob := off.toWaiting(spec("game-off"))
	off.seatAll(ob)
	off.waitState(l.ID, StateReady)
	if n := ob.fc.CallCount("JoinLobbyChat"); n != 0 {
		t.Errorf("chat off, but joined %d times", n)
	}
}

// A create that never left the queue hands the lobby to a new tracker
// while the old one is still unwinding; each keeps to its own chat
// session.
func TestChatRequeuedLobby(t *testing.T) {
	h := newHarness(t, 1, withChat(time.Second))
	b := h.bot("bot1")
	b.fc.FailNext("CreateLobby", gc.ErrNotReady)
	l := h.create(spec("game-requeue"))
	h.waitFor(l.ID, "a retried create", func(Lobby) bool { return b.fc.CallCount("CreateLobby") >= 2 })
	h.waitState(l.ID, StateWaiting)
	h.waitChat(b, "Type !help for commands.")
}
