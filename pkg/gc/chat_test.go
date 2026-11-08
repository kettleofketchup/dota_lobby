package gc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/paralin/go-dota2/protocol"
)

func TestJoinLobbyChat(t *testing.T) {
	const lobbyID = 27110012345678901
	name := "Lobby_27110012345678901"
	success := protocol.CMsgDOTAJoinChatChannelResponse_JOIN_SUCCESS
	full := protocol.CMsgDOTAJoinChatChannelResponse_USER_IN_TOO_MANY_CHANNELS
	type reply struct {
		job    bool // echo our source job instead of none
		name   string
		id     uint64
		result protocol.CMsgDOTAJoinChatChannelResponse_Result
	}
	tests := []struct {
		name    string
		replies []reply
		wantID  uint64
		wantErr string // "" ok, "refused", "timeout"
	}{
		// Seen live: the GC answers with no target job (research 09 §9).
		{"reply without our job id", []reply{{name: name, id: 555, result: success}}, 555, ""},
		{"reply with our job id", []reply{{job: true, name: name, id: 556, result: success}}, 556, ""},
		{"other channel ignored", []reply{{name: "Lobby_1", id: 1, result: success}, {name: name, id: 557, result: success}}, 557, ""},
		{"refused", []reply{{name: name, result: full}}, 0, "refused"},
		{"refused without a channel name", []reply{{name: "", result: full}}, 0, "refused"},
		{"other channel refused", []reply{{name: "Lobby_3", result: full}}, 0, "timeout"},
		{"success without a channel name ignored", []reply{{name: "", id: 9, result: success}}, 0, "timeout"},
		{"no reply", nil, 0, "timeout"},
		{"only other channels", []reply{{name: "Lobby_2", id: 2, result: success}}, 0, "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := newFakeTransport()
			a := newTestAdapter(t, ft)
			connectWelcomed(t, a, ft)

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			type out struct {
				id  uint64
				err error
			}
			done := make(chan out, 1)
			go func() {
				id, err := a.JoinLobbyChat(ctx, lobbyID)
				done <- out{id, err}
			}()
			sent := ft.waitWrite(t, msgJoinChatChannel)
			req := sent.Body.(*protocol.CMsgDOTAJoinChatChannel)
			if req.GetChannelName() != name || req.GetChannelType() != protocol.DOTAChatChannelTypeT_DOTAChannelType_Lobby {
				t.Fatalf("join request %+v", req)
			}
			job := uint64(sent.GetSourceJobId())
			if job <= tapJobBase {
				t.Errorf("source job %d not one of ours", job)
			}
			for _, r := range tt.replies {
				target := noJob
				if r.job {
					target = job
				}
				ft.deliver(packet(t, msgJoinChatResponse, target, &protocol.CMsgDOTAJoinChatChannelResponse{
					ChannelName: new(r.name), ChannelId: new(r.id), Result: new(r.result),
				}))
			}
			got := <-done
			switch tt.wantErr {
			case "":
				if got.err != nil || got.id != tt.wantID {
					t.Errorf("JoinLobbyChat = %d, %v; want %d", got.id, got.err, tt.wantID)
				}
			case "refused":
				var je *ChatJoinError
				if !errors.As(got.err, &je) || je.Result != "USER_IN_TOO_MANY_CHANNELS" || je.Channel != name {
					t.Errorf("err = %v, want a ChatJoinError", got.err)
				}
			case "timeout":
				if !errors.Is(got.err, context.DeadlineExceeded) {
					t.Errorf("err = %v, want deadline exceeded", got.err)
				}
			}
		})
	}
}

func TestChatNeedsSession(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	if _, err := a.JoinLobbyChat(t.Context(), 1); !errors.Is(err, ErrNotConnected) {
		t.Errorf("disconnected join = %v", err)
	}
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatal(err)
	}
	waitEvent[Connected](t, a)
	for name, call := range map[string]func() error{
		"join":  func() error { _, err := a.JoinLobbyChat(t.Context(), 1); return err },
		"send":  func() error { return a.SendChat(1, "hi") },
		"leave": func() error { return a.LeaveChat(1) },
	} {
		if err := call(); !errors.Is(err, ErrNotReady) {
			t.Errorf("%s before welcome = %v, want ErrNotReady", name, err)
		}
	}
}

func TestSendAndLeaveChat(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	connectWelcomed(t, a, ft)

	if err := a.SendChat(555, "!help: commands"); err != nil {
		t.Fatal(err)
	}
	m := ft.waitWrite(t, uint32(protocol.EDOTAGCMsg_k_EMsgGCChatMessage)).Body.(*protocol.CMsgDOTAChatMessage)
	if m.GetChannelId() != 555 || m.GetText() != "!help: commands" {
		t.Errorf("chat message %+v", m)
	}
	if err := a.SendChat(555, ""); err == nil {
		t.Error("empty text accepted")
	}
	if err := a.LeaveChat(555); err != nil {
		t.Fatal(err)
	}
	if l := ft.waitWrite(t, uint32(protocol.EDOTAGCMsg_k_EMsgGCLeaveChatChannel)).Body.(*protocol.CMsgDOTALeaveChatChannel); l.GetChannelId() != 555 {
		t.Errorf("leave %+v", l)
	}
}

func TestSubscribeChat(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	ch, stop := a.SubscribeChat()
	defer stop()
	connectWelcomed(t, a, ft)

	ft.deliver(packet(t, uint32(protocol.EDOTAGCMsg_k_EMsgGCChatMessage), noJob, &protocol.CMsgDOTAChatMessage{
		ChannelId: new(uint64(555)), AccountId: new(uint32(11111111)), PersonaName: new("Admin"), Text: new("!ready"),
	}))
	select {
	case m := <-ch:
		want := ChatMessage{ChannelID: 555, AccountID: 11111111, PersonaName: "Admin", Text: "!ready"}
		if m != want {
			t.Errorf("chat %+v, want %+v", m, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no chat message on the subscription")
	}
	// Chat stays off the supervisor's event stream.
	select {
	case ev := <-a.Events():
		t.Errorf("unexpected event %T", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLobbyChannelName(t *testing.T) {
	if got := LobbyChannelName(27110012345678901); got != "Lobby_27110012345678901" {
		t.Errorf("LobbyChannelName = %q", got)
	}
}
