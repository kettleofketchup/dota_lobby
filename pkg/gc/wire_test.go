package gc

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/paralin/go-dota2/protocol"
)

// readTVGolden parses testdata/lobby_tv_delay.golden into seconds -> wire.
func readTVGolden(t *testing.T) map[uint32]int32 {
	t.Helper()
	f, err := os.Open("testdata/lobby_tv_delay.golden")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	re := regexp.MustCompile(`^\s*LobbyDotaTV_(\d+)\s*=\s*(\d+);`)
	out := map[uint32]int32{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := re.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		secs, _ := strconv.ParseUint(m[1], 10, 32)
		wire, _ := strconv.ParseInt(m[2], 10, 32)
		out[uint32(secs)] = int32(wire)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("golden file has no enum values")
	}
	return out
}

func TestTVDelayMatchesGolden(t *testing.T) {
	golden := readTVGolden(t)
	if len(golden) != len(tvDelayWire) {
		t.Fatalf("golden has %d values, table has %d", len(golden), len(tvDelayWire))
	}
	for secs, wire := range golden {
		got, ok := TVDelayToWire(secs)
		if !ok || got != wire {
			t.Errorf("TVDelayToWire(%d) = %d, %v; golden %d", secs, got, ok, wire)
		}
		back, ok := TVDelayFromWire(wire)
		if !ok || back != secs {
			t.Errorf("TVDelayFromWire(%d) = %d, %v; golden %d", wire, back, ok, secs)
		}
	}
}

func TestTVDelayRejectsUnknown(t *testing.T) {
	for _, secs := range []uint32{0, 5, 30, 90, 600, 1800} {
		if w, ok := TVDelayToWire(secs); ok {
			t.Errorf("TVDelayToWire(%d) = %d, want unsupported", secs, w)
		}
	}
	for _, wire := range []int32{-1, 5, 99} {
		if s, ok := TVDelayFromWire(wire); ok {
			t.Errorf("TVDelayFromWire(%d) = %d, want unknown", wire, s)
		}
	}
}

// The vendored enum is the stale numbering. If go-dota2 regenerates its
// protos this fails, and the D6 table can be checked against the new enum.
func TestVendoredTVEnumIsStale(t *testing.T) {
	w120, _ := TVDelayToWire(120)
	if int32(protocol.LobbyDotaTVDelay_LobbyDotaTV_120) == w120 {
		t.Fatal("vendored LobbyDotaTV_120 now matches the current numbering; re-check the TV-delay table in wire.go")
	}
}

func TestEResultString(t *testing.T) {
	tests := []struct {
		in   EResult
		want string
	}{
		{EResultOK, "OK"},
		{EResultLoggedInElsewhere, "LoggedInElsewhere"},
		{EResultRateLimitExceeded, "RateLimitExceeded"},
		{EResultInvalidSignature, "InvalidSignature"},
		{EResult(9999), "EResult(9999)"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("EResult(%d).String() = %q, want %q", int32(tt.in), got, tt.want)
		}
	}
}

func TestGCConnStatusString(t *testing.T) {
	tests := []struct {
		in   GCConnStatus
		want string
	}{
		{GCHaveSession, "have_session"},
		{GCNoSessionInLogonQueue, "no_session_in_logon_queue"},
		{GCSuspended, "suspended"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", int32(tt.in), got, tt.want)
		}
	}
}

func TestLobbyView(t *testing.T) {
	dire := protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_BAD_GUYS
	tv := protocol.LobbyDotaTVDelay(3) // 300 s in the current numbering
	state := protocol.CSODOTALobby_RUN
	l := &protocol.CSODOTALobby{
		LobbyId:     new(uint64(11)),
		LeaderId:    new(uint64(22)),
		Leagueid:    new(uint32(33)),
		GameName:    new("inhouse"),
		State:       &state,
		DotaTvDelay: &tv,
		AllMembers: []*protocol.CSODOTALobbyMember{
			{Id: new(uint64(1)), Team: &dire, Slot: new(uint32(2))},
			{Id: new(uint64(2))}, // team omitted on the wire
			{Id: new(uint64(3))}, // left: not in member_indices
		},
		MemberIndices:  []uint32{0, 1, 7}, // 7 is out of range and skipped
		PendingInvites: []uint64{44},
	}
	v := lobbyView(l)
	if v.LobbyID != 11 || v.LeaderID != 22 || v.LeagueID != 33 || v.GameName != "inhouse" || v.State != LobbyRun {
		t.Fatalf("scalar fields wrong: %+v", v)
	}
	if v.TVDelaySeconds != 300 || v.TVDelayWire != 3 {
		t.Errorf("tv delay = %ds wire %d, want 300s wire 3", v.TVDelaySeconds, v.TVDelayWire)
	}
	if len(v.Members) != 2 {
		t.Fatalf("members = %+v, want 2 filtered by member_indices", v.Members)
	}
	if m := v.Members[0]; m.SteamID != 1 || m.Team != TeamDire || !m.TeamKnown || m.Slot != 2 {
		t.Errorf("member 0 = %+v", m)
	}
	if m := v.Members[1]; m.TeamKnown || m.Team != TeamRadiant {
		t.Errorf("member 1 = %+v, want TeamKnown false (decodes as radiant)", m)
	}
	if len(v.Pending) != 1 || v.Pending[0] != 44 {
		t.Errorf("pending = %v", v.Pending)
	}

	l.MemberIndices = nil
	if got := len(lobbyView(l).Members); got != 3 {
		t.Errorf("without member_indices got %d members, want all 3", got)
	}
	if lobbyView(nil) != nil {
		t.Error("lobbyView(nil) != nil")
	}
}

func TestDetailsProto(t *testing.T) {
	tests := []struct {
		name    string
		in      LobbyDetails
		wantErr bool
		check   func(t *testing.T, p *protocol.CMsgPracticeLobbySetDetails)
	}{
		{
			name: "tv delay uses current numbering",
			in:   LobbyDetails{GameName: "g", LeagueID: 7, TVDelaySeconds: 120, Visibility: VisibilityUnlisted},
			check: func(t *testing.T, p *protocol.CMsgPracticeLobbySetDetails) {
				if p.DotaTvDelay == nil || int32(*p.DotaTvDelay) != 2 {
					t.Errorf("dota_tv_delay = %v, want wire 2", p.DotaTvDelay)
				}
				if p.GetLeagueid() != 7 || p.GetGameName() != "g" || p.GetVisibility() != protocol.DOTALobbyVisibility_DOTALobbyVisibility_Unlisted {
					t.Errorf("fields wrong: %+v", p)
				}
				if p.LobbyId != nil || p.PassKey != nil || p.LeagueNodeId != nil {
					t.Error("unset optional fields were sent")
				}
			},
		},
		{
			name: "zero tv delay is left unset",
			in:   LobbyDetails{LobbyID: 9, PassKey: "pw", LeagueNodeID: 3},
			check: func(t *testing.T, p *protocol.CMsgPracticeLobbySetDetails) {
				if p.DotaTvDelay != nil {
					t.Errorf("dota_tv_delay sent: %v", *p.DotaTvDelay)
				}
				if p.GetLobbyId() != 9 || p.GetPassKey() != "pw" || p.GetLeagueNodeId() != 3 {
					t.Errorf("fields wrong: %+v", p)
				}
			},
		},
		{name: "unknown tv delay", in: LobbyDetails{TVDelaySeconds: 45}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := detailsProto(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}

func TestDecodeLeagueList(t *testing.T) {
	tier := protocol.ELeagueTier(2)
	body := mustMarshal(t, &protocol.CMsgDOTALeagueInfoList{Infos: []*protocol.CMsgDOTALeagueInfo{
		{LeagueId: new(uint32(100)), Name: new("Inhouse League"), Tier: &tier, Status: new(uint32(3)), StartTimestamp: new(uint32(5))},
		{LeagueId: new(uint32(200))},
	}})
	got, err := decodeLeagueList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].LeagueID != 100 || got[0].Name != "Inhouse League" || got[0].Tier != 2 || got[0].Status != 3 || got[0].StartTS != 5 || got[1].LeagueID != 200 {
		t.Errorf("got %+v", got)
	}
	if _, err := decodeLeagueList([]byte{0xff}); err == nil {
		t.Error("garbage decoded without error")
	}
}

func TestMatchResult(t *testing.T) {
	outcome := protocol.EMatchOutcome(2)
	r := matchResult(&protocol.CMsgGCMatchDetailsResponse{
		Result: new(uint32(1)),
		Match: &protocol.CMsgDOTAMatch{
			MatchId: new(uint64(123)), MatchOutcome: &outcome, Duration: new(uint32(1800)), Leagueid: new(uint32(7)),
			Players: []*protocol.CMsgDOTAMatch_Player{{AccountId: new(uint32(5)), HeroId: new(int32(8)), Kills: new(uint32(9))}},
		},
	})
	if r.GCResult != 1 || r.MatchID != 123 || r.Outcome != 2 || r.DurationSec != 1800 || r.LeagueID != 7 {
		t.Errorf("got %+v", r)
	}
	if len(r.Players) != 1 || r.Players[0].AccountID != 5 || r.Players[0].HeroID != 8 || r.Players[0].Kills != 9 {
		t.Errorf("players %+v", r.Players)
	}
}
