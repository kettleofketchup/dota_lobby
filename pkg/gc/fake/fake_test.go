package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

func drain(c *Client) []gc.Event {
	var out []gc.Event
	for {
		select {
		case ev := <-c.Events():
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestScriptedLogin(t *testing.T) {
	f := New()
	f.SetSteamID(76561198000000001)
	f.Script("Connect", gc.Connected{}, gc.LoggedOn{SteamID: 76561198000000001})
	f.Script("SayHello")                  // first hello is lost
	f.Script("SayHello", gc.GCWelcomed{}) // second is answered

	if err := f.Connect(t.Context(), gc.Credentials{Username: "bot"}); err != nil {
		t.Fatal(err)
	}
	if got := drain(f); len(got) != 2 {
		t.Fatalf("Connect emitted %v", got)
	}
	f.SayHello()
	if got := drain(f); len(got) != 0 {
		t.Fatalf("first hello emitted %v", got)
	}
	f.SayHello()
	if got := drain(f); len(got) != 1 || got[0] != (gc.GCWelcomed{}) {
		t.Fatalf("second hello emitted %v", got)
	}
	f.SayHello()
	if got := drain(f); len(got) != 0 {
		t.Fatalf("unscripted hello emitted %v", got)
	}
	if n := f.CallCount("SayHello"); n != 3 {
		t.Errorf("SayHello calls = %d", n)
	}
	if err := f.Connect(t.Context(), gc.Credentials{}); !errors.Is(err, gc.ErrAlreadyConnected) {
		t.Errorf("second Connect = %v", err)
	}
}

func TestScriptEvery(t *testing.T) {
	f := New()
	f.Script("SayHello", gc.GCStatus{New: gc.GCNoSessionInLogonQueue})
	f.ScriptEvery("SayHello", gc.GCWelcomed{})
	f.SayHello()
	f.SayHello()
	f.SayHello()
	got := drain(f)
	if len(got) != 3 || got[0] != (gc.GCStatus{New: gc.GCNoSessionInLogonQueue}) || got[2] != (gc.GCWelcomed{}) {
		t.Fatalf("got %v", got)
	}
}

func TestDisconnectEmits(t *testing.T) {
	tests := []struct {
		name    string
		connect bool
		script  bool
		want    int
	}{
		{name: "connected", connect: true, want: 1},
		{name: "not connected", want: 0},
		{name: "scripted override", connect: true, script: true, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := New()
			if tt.connect {
				if err := f.Connect(t.Context(), gc.Credentials{}); err != nil {
					t.Fatal(err)
				}
			}
			if tt.script {
				f.Script("Disconnect", gc.Fatal{Err: errors.New("eof")}, gc.Disconnected{})
			}
			f.Disconnect()
			if got := drain(f); len(got) != tt.want {
				t.Errorf("got %v, want %d events", got, tt.want)
			}
			if f.Connected() {
				t.Error("still connected")
			}
		})
	}
}

func TestFailures(t *testing.T) {
	f := New()
	boom := errors.New("boom")
	f.FailNext("Connect", boom)
	if err := f.Connect(t.Context(), gc.Credentials{}); !errors.Is(err, boom) {
		t.Fatalf("Connect = %v", err)
	}
	if f.Connected() {
		t.Fatal("connected after a failed Connect")
	}

	f.FailAlways("ListAdminLeagues", gc.ErrNotReady)
	f.FailNext("ListAdminLeagues", boom)
	if _, err := f.ListAdminLeagues(t.Context()); !errors.Is(err, boom) {
		t.Errorf("first = %v, want queued error", err)
	}
	if _, err := f.ListAdminLeagues(t.Context()); !errors.Is(err, gc.ErrNotReady) {
		t.Errorf("second = %v, want sticky error", err)
	}
	f.FailAlways("ListAdminLeagues", nil)
	f.SetLeagues([]gc.LeagueInfo{{LeagueID: 1}})
	if l, err := f.ListAdminLeagues(t.Context()); err != nil || len(l) != 1 {
		t.Errorf("third = %v, %v", l, err)
	}

	f.BlockUntilDone("MatchDetails", true)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := f.MatchDetails(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("blocked MatchDetails = %v", err)
	}
}

func TestLobbyLifecycle(t *testing.T) {
	f := New()
	f.SetSteamID(5)
	sub, cancel := f.SubscribeLobby()
	defer cancel()

	v, err := f.CreateLobby(t.Context(), gc.LobbyDetails{GameName: "g", LeagueID: 9, TVDelaySeconds: 300})
	if err != nil || v.LeaderID != 5 || v.LeagueID != 9 || v.TVDelaySeconds != 300 || v.TVDelayWire != 3 {
		t.Fatalf("create = %+v, %v", v, err)
	}
	if cur, ok := f.CurrentLobby(); !ok || cur != v {
		t.Error("created lobby is not current")
	}
	f.Invite(76561198000000002)
	f.KickFromTeam(2)
	if err := f.Destroy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.CurrentLobby(); ok {
		t.Error("lobby survived Destroy")
	}

	for _, want := range []gc.LobbyEventKind{gc.LobbyCreated, gc.LobbyDestroyed} {
		select {
		case ev := <-sub:
			if ev.Kind != want {
				t.Errorf("lobby event %s, want %s", ev.Kind, want)
			}
		default:
			t.Fatalf("missing %s event", want)
		}
	}
	calls := f.Calls("Invite", "KickFromTeam")
	if len(calls) != 2 || calls[0].Args[0] != uint64(76561198000000002) || calls[1].Args[0] != uint32(2) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestWaitForCalls(t *testing.T) {
	f := New()
	go func() {
		for range 3 {
			f.SetPlaying(true)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := f.WaitForCalls(ctx, "SetPlaying", 3); err != nil {
		t.Fatal(err)
	}
	short, cancel2 := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel2()
	if err := f.WaitForCalls(short, "SetPlaying", 4); err == nil {
		t.Error("waited for a call that never came")
	}
}

func TestDisconnectedEventAllowsReconnect(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *Client)
	}{
		{"scripted dial failure", func(f *Client) {
			f.Script("Connect", gc.Fatal{Err: errors.New("dial")}, gc.Disconnected{})
		}},
		{"server drop", func(f *Client) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := New()
			tt.setup(f)
			if err := f.Connect(t.Context(), gc.Credentials{}); err != nil {
				t.Fatal(err)
			}
			drain(f)
			f.Emit(gc.Disconnected{})
			if f.Connected() {
				t.Fatal("still connected after Disconnected")
			}
			if err := f.Connect(t.Context(), gc.Credentials{}); err != nil {
				t.Fatalf("reconnect: %v", err)
			}
		})
	}
}
