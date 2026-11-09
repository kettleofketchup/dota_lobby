package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

type hostSeatView struct {
	Strategy  *string `json:"strategy" nullable:"true" doc:"The host seat strategy that worked, or null"`
	Confirmed bool    `json:"confirmed" doc:"False when every strategy failed; launches then answer 409 host_not_seated"`
	Team      *string `json:"team" nullable:"true" doc:"Where the bot sits, or null"`
}

type playerView struct {
	AccountID       uint32     `json:"account_id"`
	SteamID         string     `json:"steam_id" doc:"SteamID64 as a string"`
	Captain         bool       `json:"captain"`
	Status          string     `json:"status" doc:"pending, invited, joined, seated or left"`
	Seat            *string    `json:"seat" nullable:"true" doc:"Where the player sits now, or null when not in the lobby"`
	Ready           bool       `json:"ready" doc:"Typed !ready in the lobby chat and has not left their side since"`
	InvitesSent     int        `json:"invites_sent"`
	LastInviteAt    *time.Time `json:"last_invite_at" nullable:"true"`
	InviteConfirmed bool       `json:"invite_confirmed"`
}

type teamView struct {
	TeamRef *string      `json:"team_ref" nullable:"true"`
	Name    *string      `json:"name" nullable:"true"`
	Players []playerView `json:"players"`
}

type teamsView struct {
	Radiant teamView `json:"radiant"`
	Dire    teamView `json:"dire"`
}

type memberView struct {
	AccountID uint32 `json:"account_id"`
	Seat      string `json:"seat"`
}

type matchView struct {
	MatchID         *string    `json:"match_id" nullable:"true" doc:"64-bit, as a string"`
	Outcome         *int32     `json:"outcome" nullable:"true" doc:"The raw EMatchOutcome"`
	OutcomeName     *string    `json:"outcome_name" nullable:"true"`
	WinnerSide      *string    `json:"winner_side" nullable:"true"`
	WinnerTeamRef   *string    `json:"winner_team_ref" nullable:"true"`
	Scored          *bool      `json:"scored" nullable:"true"`
	DurationSeconds *uint32    `json:"duration_seconds" nullable:"true"`
	StartedAt       *time.Time `json:"started_at" nullable:"true"`
	EndedAt         *time.Time `json:"ended_at" nullable:"true"`
}

// LobbyView is a lobby as the API shows it, without the password.
// 64-bit IDs are strings; unknown values are null.
type LobbyView struct {
	ID             string            `json:"id" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	ExternalRef    string            `json:"external_ref"`
	State          lobby.State       `json:"state"`
	StateReason    *string           `json:"state_reason" nullable:"true"`
	Seq            int64             `json:"seq" doc:"The number of the lobby's last event"`
	Bot            *string           `json:"bot" nullable:"true"`
	GCLobbyID      *string           `json:"gc_lobby_id" nullable:"true"`
	LeagueID       *uint32           `json:"league_id" nullable:"true"`
	LeagueNodeID   *uint32           `json:"league_node_id" nullable:"true"`
	LeagueVerified *bool             `json:"league_verified" nullable:"true"`
	Name           string            `json:"name"`
	GameMode       nameValue         `json:"game_mode"`
	Region         nameValue         `json:"region"`
	TVDelaySeconds *uint32           `json:"tv_delay_seconds" nullable:"true"`
	Visibility     string            `json:"visibility"`
	AllowCheats    bool              `json:"allow_cheats"`
	Launch         string            `json:"launch"`
	ReadyCheck     bool              `json:"ready_check"`
	ShortHanded    bool              `json:"allow_short_handed"`
	KickStrangers  bool              `json:"kick_strangers"`
	Admins         []uint32          `json:"admins"`
	HostSeat       hostSeatView      `json:"host_seat"`
	Teams          teamsView         `json:"teams"`
	Strangers      []memberView      `json:"strangers" doc:"Members who are neither rostered nor the bot"`
	PendingInvites []string          `json:"pending_invites" doc:"Outstanding invites, as SteamID64 strings"`
	Match          matchView         `json:"match"`
	LaunchAttempts int               `json:"launch_attempts"`
	Metadata       map[string]string `json:"metadata"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	ExpiresAt      *time.Time        `json:"expires_at" nullable:"true"`
	EndedAt        *time.Time        `json:"ended_at" nullable:"true"`
}

// LobbyWithPassword is the lobby with its password: only the create and
// GET /v1/lobbies/{id} responses carry it.
type LobbyWithPassword struct {
	LobbyView
	Password *string `json:"password" nullable:"true" doc:"The lobby password, null for none"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func idString(v uint64) *string {
	if v == 0 {
		return nil
	}
	s := strconv.FormatUint(v, 10)
	return &s
}

// hostAccount is the account ID of the bot hosting l, or 0.
func (s *Server) hostAccount(l lobby.Lobby) uint32 {
	if l.Bot == "" {
		return 0
	}
	for _, st := range s.opts.Bots.Statuses() {
		if strings.EqualFold(st.Name, l.Bot) {
			return gc.AccountID(st.SteamID)
		}
	}
	return 0
}

// lobbyWithPassword is the create and GET form of a lobby.
func (s *Server) lobbyWithPassword(l lobby.Lobby) LobbyWithPassword {
	v := LobbyWithPassword{LobbyView: s.lobbyView(l)}
	if l.Spec.Password.IsSet() {
		p := l.Spec.Password.Reveal()
		v.Password = &p
	}
	return v
}

// lobbyView builds the API form of a lobby, without the password.
func (s *Server) lobbyView(l lobby.Lobby) LobbyView {
	sp := l.Spec
	v := LobbyView{
		ID: l.ID, ExternalRef: sp.ExternalRef, State: l.State, StateReason: nonEmpty(l.StateReason), Seq: l.Seq,
		Bot: nonEmpty(l.Bot), GCLobbyID: idString(l.GCLobbyID), LeagueID: nonZero(sp.LeagueID),
		LeagueNodeID: nonZero(sp.LeagueNodeID), LeagueVerified: l.LeagueVerified, Name: sp.Name,
		GameMode: nameValue(sp.GameMode), Region: nameValue(sp.Region), TVDelaySeconds: nonZero(sp.TVDelaySeconds),
		Visibility: sp.Visibility, AllowCheats: sp.AllowCheats, Launch: sp.Launch, ReadyCheck: sp.ReadyCheck,
		ShortHanded: sp.AllowShortHanded, KickStrangers: sp.KickStrangers, Admins: sp.Admins,
		HostSeat:       hostSeatView{Strategy: nonEmpty(l.HostSeat.Strategy), Confirmed: l.HostSeat.Confirmed, Team: nonEmpty(l.HostSeat.Team)},
		Strangers:      []memberView{},
		PendingInvites: []string{},
		LaunchAttempts: l.LaunchAttempts, Metadata: sp.Metadata,
		CreatedAt: l.CreatedAt.UTC(), UpdatedAt: l.UpdatedAt.UTC(), ExpiresAt: timePtr(l.ExpiresAt), EndedAt: timePtr(l.EndedAt),
	}
	if v.TVDelaySeconds == nil && l.Details.TVDelaySeconds != 0 {
		v.TVDelaySeconds = nonZero(l.Details.TVDelaySeconds)
	}
	if v.Admins == nil {
		v.Admins = []uint32{}
	}
	if v.Metadata == nil {
		v.Metadata = map[string]string{}
	}
	for _, side := range []lobby.Side{lobby.Radiant, lobby.Dire} {
		t := sp.Teams.Team(side)
		tv := teamView{TeamRef: nonEmpty(t.TeamRef), Name: nonEmpty(t.Name), Players: []playerView{}}
		for _, p := range t.Players {
			status, seat := l.PlayerStatus(lobby.RosterEntry{Side: side, Player: p})
			tv.Players = append(tv.Players, playerView{
				AccountID: p.AccountID, SteamID: strconv.FormatUint(gc.SteamID64(p.AccountID), 10), Captain: p.Captain,
				Status: status, Seat: nonEmpty(seat), Ready: p.Ready, InvitesSent: p.InvitesSent,
				LastInviteAt: timePtr(p.LastInviteAt), InviteConfirmed: p.InviteConfirmed,
			})
		}
		if side == lobby.Radiant {
			v.Teams.Radiant = tv
		} else {
			v.Teams.Dire = tv
		}
	}
	if l.Live != nil {
		for _, m := range l.Strangers(s.hostAccount(l)) {
			v.Strangers = append(v.Strangers, memberView{AccountID: m.AccountID, Seat: m.Seat})
		}
		for _, id := range l.Live.Pending {
			v.PendingInvites = append(v.PendingInvites, strconv.FormatUint(id, 10))
		}
	}
	m := l.Match
	v.Match = matchView{
		MatchID: idString(m.MatchID), Outcome: m.Outcome, WinnerSide: nonEmpty(string(m.WinnerSide())),
		WinnerTeamRef: nonEmpty(m.WinnerTeamRef()), Scored: m.Scored(), DurationSeconds: m.DurationSeconds(),
		StartedAt: timePtr(m.StartedAt), EndedAt: timePtr(m.EndedAt),
	}
	if m.Outcome != nil {
		name := lobby.OutcomeName(*m.Outcome)
		v.Match.OutcomeName = &name
	}
	return v
}
