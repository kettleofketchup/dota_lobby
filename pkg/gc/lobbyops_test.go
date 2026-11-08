package gc

import (
	"errors"
	"testing"
	"time"

	"github.com/paralin/go-dota2/protocol"
)

func TestLobbyOpsWrite(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	connectWelcomed(t, a, ft)
	ft.deliver(lobbySO(t, "add", &protocol.CSODOTALobby{LobbyId: new(uint64(77))}))
	waitFor(t, func() bool { _, ok := a.CurrentLobby(); return ok })

	tests := []struct {
		name  string
		call  func() error
		msg   protocol.EDOTAGCMsg
		check func(t *testing.T, body any)
	}{
		{"kick from team", func() error { return a.KickFromTeam(42) }, protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyKickFromTeam, func(t *testing.T, b any) {
			if got := b.(*protocol.CMsgPracticeLobbyKickFromTeam).GetAccountId(); got != 42 {
				t.Errorf("account %d", got)
			}
		}},
		{"kick", func() error { return a.Kick(43) }, protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyKick, func(t *testing.T, b any) {
			if got := b.(*protocol.CMsgPracticeLobbyKick).GetAccountId(); got != 43 {
				t.Errorf("account %d", got)
			}
		}},
		{"join team", func() error { return a.JoinTeam(TeamPlayerPool, 0) }, protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbySetTeamSlot, func(t *testing.T, b any) {
			m := b.(*protocol.CMsgPracticeLobbySetTeamSlot)
			if m.GetTeam() != protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_PLAYER_POOL || m.GetSlot() != 0 {
				t.Errorf("team slot %+v", m)
			}
		}},
		{"broadcast channel", func() error { return a.JoinBroadcastChannel(0) }, protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyJoinBroadcastChannel, func(t *testing.T, b any) {
			m := b.(*protocol.CMsgPracticeLobbyJoinBroadcastChannel)
			if m.Channel == nil || m.GetChannel() != 0 || m.PreferredDescription != nil {
				t.Errorf("broadcast %+v", m)
			}
		}},
		{"launch", a.Launch, protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyLaunch, nil},
		{"flip", a.FlipTeams, protocol.EDOTAGCMsg_k_EMsgGCFlipLobbyTeams, nil},
		{"shuffle", a.BalancedShuffle, protocol.EDOTAGCMsg_k_EMsgGCBalancedShuffleLobby, nil},
		{"details", func() error { return a.SetLobbyDetails(LobbyDetails{GameName: "x", TVDelaySeconds: 300}) }, protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbySetDetails, func(t *testing.T, b any) {
			m := b.(*protocol.CMsgPracticeLobbySetDetails)
			if m.GetLobbyId() != 77 || m.GetGameName() != "x" || int32(m.GetDotaTvDelay()) != 3 {
				t.Errorf("details %+v", m)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatal(err)
			}
			msg := ft.waitWrite(t, uint32(tt.msg))
			if tt.check != nil {
				tt.check(t, msg.Body)
			}
		})
	}

	if err := a.Invite(SteamID64(9)); err != nil {
		t.Fatal(err)
	}
	if err := a.SetLobbyDetails(LobbyDetails{TVDelaySeconds: 42}); err == nil {
		t.Error("unsupported TV delay accepted")
	}
}

func TestLobbyOpsNeedSession(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	if err := a.Launch(); !errors.Is(err, ErrNotConnected) {
		t.Errorf("disconnected Launch = %v", err)
	}
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatal(err)
	}
	waitEvent[Connected](t, a)
	// Connected, but no GC welcome yet.
	for name, call := range map[string]func() error{
		"invite":  func() error { return a.Invite(1) },
		"kick":    func() error { return a.Kick(1) },
		"details": func() error { return a.SetLobbyDetails(LobbyDetails{}) },
		"caster":  func() error { return a.JoinBroadcastChannel(0) },
	} {
		if err := call(); !errors.Is(err, ErrNotReady) {
			t.Errorf("%s before welcome = %v, want ErrNotReady", name, err)
		}
	}
}

func TestSubscribeInvites(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	ch, stop := a.SubscribeInvites()
	defer stop()
	connectWelcomed(t, a, ft)

	ft.deliver(packet(t, uint32(protocol.EGCBaseMsg_k_EMsgGCInvitationCreated), noJob,
		&protocol.CMsgInvitationCreated{SteamId: new(uint64(SteamID64(5))), UserOffline: new(true)}))
	select {
	case ev := <-ch:
		if ev.SteamID != SteamID64(5) || !ev.Offline {
			t.Errorf("invite %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no invite on the subscription")
	}
	// The event stream still gets it too.
	if ev := waitEvent[InvitationCreated](t, a); ev.SteamID != SteamID64(5) {
		t.Errorf("event %+v", ev)
	}
}

func TestMemberCoachTeam(t *testing.T) {
	dire := protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_BAD_GUYS
	none := protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_NOTEAM
	tests := []struct {
		name      string
		m         *protocol.CSODOTALobbyMember
		coach     bool
		coachTeam *Team
	}{
		{"absent", &protocol.CSODOTALobbyMember{Id: new(uint64(1))}, false, nil},
		{"no team", &protocol.CSODOTALobbyMember{Id: new(uint64(1)), CoachTeam: &none}, false, new(TeamNoTeam)},
		{"dire coach", &protocol.CSODOTALobbyMember{Id: new(uint64(1)), CoachTeam: &dire}, true, new(TeamDire)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := member(tt.m)
			if got.Coach() != tt.coach {
				t.Errorf("Coach() = %v", got.Coach())
			}
			if (got.CoachTeam == nil) != (tt.coachTeam == nil) || (got.CoachTeam != nil && *got.CoachTeam != *tt.coachTeam) {
				t.Errorf("CoachTeam = %v, want %v", got.CoachTeam, tt.coachTeam)
			}
		})
	}
}

func TestSteamIDConversions(t *testing.T) {
	tests := []struct {
		account uint32
		steam   uint64
	}{
		{0, 76561197960265728},
		{11111111, 76561197971376839},
		{4294967295, 76561202255233023},
	}
	for _, tt := range tests {
		if got := SteamID64(tt.account); got != tt.steam {
			t.Errorf("SteamID64(%d) = %d, want %d", tt.account, got, tt.steam)
		}
		if got := AccountID(tt.steam); got != tt.account {
			t.Errorf("AccountID(%d) = %d, want %d", tt.steam, got, tt.account)
		}
	}
}

// waitFor polls cond for up to 2 s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
