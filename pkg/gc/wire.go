package gc

import (
	"fmt"
	"strings"

	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-steam/protocol/steamlang"
)

// tvDelayWire is the current Valve numbering of LobbyDotaTVDelay
// (GameTracking-Dota2 c592bf50, 2026-08-12). The vendored protocol constants
// are the old numbering (their _120 is 1, which now means 60 s), so they must
// never be used to build or read field 24/53 (gap-04). The golden
// file testdata/lobby_tv_delay.golden pins this table.
var tvDelayWire = []struct {
	seconds uint32
	wire    int32
}{
	{10, 0},
	{60, 1},
	{120, 2},
	{300, 3},
	{900, 4},
}

// TVDelayToWire maps a delay in seconds to its wire value.
func TVDelayToWire(seconds uint32) (int32, bool) {
	for _, e := range tvDelayWire {
		if e.seconds == seconds {
			return e.wire, true
		}
	}
	return 0, false
}

// TVDelayFromWire maps a wire value (e.g. a CSODOTALobby read-back) to seconds.
func TVDelayFromWire(wire int32) (uint32, bool) {
	for _, e := range tvDelayWire {
		if e.wire == wire {
			return e.seconds, true
		}
	}
	return 0, false
}

// EResult is a Steam EResult as a plain number. String gives its name, so no
// steamlang type leaves this package.
type EResult int32

// EResults the supervisor and auth classifier act on (04 §5.1).
const (
	EResultOK                         EResult = 1
	EResultFail                       EResult = 2
	EResultNoConnection               EResult = 3
	EResultInvalidPassword            EResult = 5
	EResultLoggedInElsewhere          EResult = 6
	EResultAccessDenied               EResult = 15
	EResultTimeout                    EResult = 16
	EResultServiceUnavailable         EResult = 20
	EResultRevoked                    EResult = 26
	EResultExpired                    EResult = 27
	EResultLogonSessionReplaced       EResult = 34
	EResultTryAnotherCM               EResult = 48
	EResultAlreadyLoggedInElsewhere   EResult = 50
	EResultAccountLogonDenied         EResult = 63
	EResultRateLimitExceeded          EResult = 84
	EResultAccountLoginDeniedThrottle EResult = 87
	EResultTwoFactorCodeMismatch      EResult = 88
	// EResultInvalidSignature is missing from go-steam's table (gap-01).
	EResultInvalidSignature EResult = 121
)

// String returns the EResult name without the "EResult_" prefix, or the
// number when go-steam does not know it.
func (e EResult) String() string {
	if e == EResultInvalidSignature {
		return "InvalidSignature"
	}
	if name, ok := steamlang.EResult_name[steamlang.EResult(e)]; ok {
		return strings.TrimPrefix(name, "EResult_")
	}
	return fmt.Sprintf("EResult(%d)", int32(e))
}

// GCConnStatus is GCConnectionStatus as a plain number.
type GCConnStatus int32

const (
	GCHaveSession           GCConnStatus = 0
	GCGoingDown             GCConnStatus = 1
	GCNoSession             GCConnStatus = 2
	GCNoSessionInLogonQueue GCConnStatus = 3
	GCNoSteam               GCConnStatus = 4
	GCSuspended             GCConnStatus = 5
	GCSteamGoingDown        GCConnStatus = 6
)

func (s GCConnStatus) String() string {
	name := protocol.GCConnectionStatus(s).String()
	return strings.ToLower(strings.TrimPrefix(name, "GCConnectionStatus_"))
}

// Wire message IDs used outside go-dota2's own wrappers.
const (
	msgPracticeLobbyCreate   = uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyCreate)               // 7038
	msgJoinBroadcastChannel  = uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyJoinBroadcastChannel) // 7149
	msgPracticeLobbyResponse = uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyResponse)             // 7055
	msgPopup                 = uint32(protocol.EDOTAGCMsg_k_EMsgGCPopup)                             // 7102
	msgLeagueAdminsRequest   = uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest)     // 7633
	msgLeagueAdminsResponse  = uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsReponse)     // 7634
)

// decodeGenericResult decodes a 7055 body. It is CMsgGenericResult, not
// CMsgPracticeLobbyJoinResponse, which drops debug_message (gap-05 §5).
func decodeGenericResult(body []byte) (eresult uint32, debug string, err error) {
	var r protocol.CMsgGenericResult
	if err := r.UnmarshalVT(body); err != nil {
		return 0, "", fmt.Errorf("decode 7055 CMsgGenericResult: %w", err)
	}
	return r.GetEresult(), r.GetDebugMessage(), nil
}

// decodePopup decodes a 7102 body.
func decodePopup(body []byte) (Popup, error) {
	var p protocol.CMsgDOTAPopup
	if err := p.UnmarshalVT(body); err != nil {
		return Popup{}, fmt.Errorf("decode popup: %w", err)
	}
	return popupEvent(&p), nil
}

func popupEvent(p *protocol.CMsgDOTAPopup) Popup {
	return Popup{
		ID:         int32(p.GetId()),
		Name:       p.GetId().String(),
		CustomText: p.GetCustomText(),
		IntData:    p.GetIntData(),
	}
}

// decodeLeagueList decodes a 7634 body.
func decodeLeagueList(body []byte) ([]LeagueInfo, error) {
	var l protocol.CMsgDOTALeagueInfoList
	if err := l.UnmarshalVT(body); err != nil {
		return nil, fmt.Errorf("decode 7634 CMsgDOTALeagueInfoList: %w", err)
	}
	return leagueInfos(&l), nil
}

func leagueInfos(l *protocol.CMsgDOTALeagueInfoList) []LeagueInfo {
	out := make([]LeagueInfo, 0, len(l.GetInfos()))
	for _, i := range l.GetInfos() {
		out = append(out, LeagueInfo{
			LeagueID: i.GetLeagueId(),
			Name:     i.GetName(),
			Tier:     int32(i.GetTier()),
			Region:   int32(i.GetRegion()),
			Status:   int32(i.GetStatus()),
			StartTS:  i.GetStartTimestamp(),
			EndTS:    i.GetEndTimestamp(),
		})
	}
	return out
}

func leagueNodes(n *protocol.CMsgDOTALeagueAvailableLobbyNodes) []LeagueNode {
	out := make([]LeagueNode, 0, len(n.GetNodeInfos()))
	for _, i := range n.GetNodeInfos() {
		out = append(out, LeagueNode{
			NodeID:    i.GetNodeId(),
			Name:      i.GetNodeName(),
			GroupName: i.GetNodeGroupName(),
			TeamID1:   i.GetTeamId_1(),
			TeamID2:   i.GetTeamId_2(),
		})
	}
	return out
}

// lobbyView converts the lobby SO. Members are filtered by member_indices
// when present: all_members keeps entries for people who left (Q15).
func lobbyView(l *protocol.CSODOTALobby) *LobbyView {
	if l == nil {
		return nil
	}
	v := &LobbyView{
		LobbyID:      l.GetLobbyId(),
		LeaderID:     l.GetLeaderId(),
		MatchID:      l.GetMatchId(),
		ServerID:     l.GetServerId(),
		State:        LobbyState(l.GetState()),
		GameState:    int32(l.GetGameState()),
		Outcome:      int32(l.GetMatchOutcome()),
		LeagueID:     l.GetLeagueid(),
		LeagueNodeID: l.GetLeagueNodeId(),
		ServerRegion: l.GetServerRegion(),
		GameMode:     l.GetGameMode(),
		GameName:     l.GetGameName(),
		Visibility:   Visibility(l.GetVisibility()),
		TVDelayWire:  int32(l.GetDotaTvDelay()),
		Pending:      append([]uint64(nil), l.GetPendingInvites()...),
		Raw:          l,
	}
	v.TVDelaySeconds, _ = TVDelayFromWire(v.TVDelayWire)

	all := l.GetAllMembers()
	if idx := l.GetMemberIndices(); len(idx) > 0 {
		for _, i := range idx {
			if int(i) < len(all) {
				v.Members = append(v.Members, member(all[i]))
			}
		}
	} else {
		for _, m := range all {
			v.Members = append(v.Members, member(m))
		}
	}
	return v
}

func member(m *protocol.CSODOTALobbyMember) Member {
	out := Member{
		SteamID:   m.GetId(),
		Team:      Team(m.GetTeam()),
		TeamKnown: m.Team != nil,
		Slot:      m.GetSlot(),
		Leaver:    int32(m.GetLeaverStatus()),
		HeroID:    m.GetHeroId(),
	}
	if m.CoachTeam != nil {
		ct := Team(m.GetCoachTeam())
		out.CoachTeam = &ct
	}
	return out
}

// detailsProto builds CMsgPracticeLobbySetDetails. TV delay goes through the
// TV-delay table; an unknown value is an error rather than a silent default.
func detailsProto(d LobbyDetails) (*protocol.CMsgPracticeLobbySetDetails, error) {
	vis := protocol.DOTALobbyVisibility(d.Visibility)
	p := &protocol.CMsgPracticeLobbySetDetails{
		GameName:          new(d.GameName),
		ServerRegion:      new(d.ServerRegion),
		GameMode:          new(d.GameMode),
		Leagueid:          new(d.LeagueID),
		SeriesType:        new(d.SeriesType),
		RadiantSeriesWins: new(d.RadiantWins),
		DireSeriesWins:    new(d.DireWins),
		Visibility:        &vis,
		AllowSpectating:   new(d.AllowSpectating),
		AllowCheats:       new(d.AllowCheats),
		FillWithBots:      new(d.FillWithBots),
		Allchat:           new(d.AllChat),
	}
	if d.LobbyID != 0 {
		p.LobbyId = new(d.LobbyID)
	}
	if d.PassKey != "" {
		p.PassKey = new(d.PassKey)
	}
	if d.LeagueNodeID != 0 {
		p.LeagueNodeId = new(d.LeagueNodeID)
	}
	if d.TVDelaySeconds != 0 {
		w, ok := TVDelayToWire(d.TVDelaySeconds)
		if !ok {
			return nil, fmt.Errorf("unsupported dota_tv_delay %ds (want 10, 60, 120, 300 or 900)", d.TVDelaySeconds)
		}
		tv := protocol.LobbyDotaTVDelay(w)
		p.DotaTvDelay = &tv
	}
	return p, nil
}

func matchResult(r *protocol.CMsgGCMatchDetailsResponse) *MatchResult {
	m := r.GetMatch()
	out := &MatchResult{
		GCResult:     r.GetResult(),
		MatchID:      m.GetMatchId(),
		Outcome:      int32(m.GetMatchOutcome()),
		DurationSec:  m.GetDuration(),
		StartTime:    m.GetStarttime(),
		LeagueID:     m.GetLeagueid(),
		LobbyType:    m.GetLobbyType(),
		GameMode:     int32(m.GetGameMode()),
		RadiantScore: m.GetRadiantTeamScore(),
		DireScore:    m.GetDireTeamScore(),
	}
	for _, p := range m.GetPlayers() {
		out.Players = append(out.Players, MatchPlayer{
			AccountID:  p.GetAccountId(),
			PlayerSlot: p.GetPlayerSlot(),
			HeroID:     p.GetHeroId(),
			Kills:      p.GetKills(),
			Deaths:     p.GetDeaths(),
			Assists:    p.GetAssists(),
			Leaver:     p.GetLeaverStatus(),
		})
	}
	return out
}
