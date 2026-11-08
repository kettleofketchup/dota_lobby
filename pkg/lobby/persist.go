package lobby

import (
	"encoding/json"
	"fmt"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// specDoc is the stored form of a Spec: no password (its own column) and
// no players (lobby_players).
type specDoc struct {
	ExternalRef      string            `json:"external_ref"`
	Name             string            `json:"name"`
	LeagueID         uint32            `json:"league_id"`
	LeagueNodeID     uint32            `json:"league_node_id,omitempty"`
	GameMode         string            `json:"game_mode,omitempty"`
	Region           string            `json:"region,omitempty"`
	TVDelaySeconds   uint32            `json:"tv_delay_seconds,omitempty"`
	AllowCheats      bool              `json:"allow_cheats,omitempty"`
	Visibility       string            `json:"visibility"`
	Launch           string            `json:"launch"`
	ReadyCheck       bool              `json:"ready_check,omitempty"`
	AllowShortHanded bool              `json:"allow_short_handed,omitempty"`
	KickStrangers    bool              `json:"kick_strangers,omitempty"`
	Admins           []uint32          `json:"admins,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Radiant          teamDoc           `json:"radiant"`
	Dire             teamDoc           `json:"dire"`
}

type teamDoc struct {
	TeamRef string `json:"team_ref,omitempty"`
	Name    string `json:"name,omitempty"`
}

// detailsDoc is the stored detail set, without the password.
type detailsDoc struct {
	LobbyID         uint64        `json:"lobby_id,omitempty"`
	GameName        string        `json:"game_name"`
	ServerRegion    uint32        `json:"server_region"`
	GameMode        uint32        `json:"game_mode"`
	LeagueID        uint32        `json:"league_id"`
	LeagueNodeID    uint32        `json:"league_node_id,omitempty"`
	Visibility      gc.Visibility `json:"visibility"`
	AllowSpectating bool          `json:"allow_spectating"`
	AllowCheats     bool          `json:"allow_cheats"`
	TVDelaySeconds  uint32        `json:"tv_delay_seconds,omitempty"`
}

func toRow(l Lobby) (store.LobbyRow, []store.PlayerRow, error) {
	s := l.Spec
	spec, err := json.Marshal(specDoc{
		ExternalRef: s.ExternalRef, Name: s.Name, LeagueID: s.LeagueID, LeagueNodeID: s.LeagueNodeID,
		GameMode: s.GameMode, Region: s.Region, TVDelaySeconds: s.TVDelaySeconds, AllowCheats: s.AllowCheats,
		Visibility: s.Visibility, Launch: s.Launch, ReadyCheck: s.ReadyCheck, AllowShortHanded: s.AllowShortHanded,
		KickStrangers: s.KickStrangers, Admins: s.Admins, Metadata: s.Metadata,
		Radiant: teamDoc{TeamRef: s.Teams.Radiant.TeamRef, Name: s.Teams.Radiant.Name},
		Dire:    teamDoc{TeamRef: s.Teams.Dire.TeamRef, Name: s.Teams.Dire.Name},
	})
	if err != nil {
		return store.LobbyRow{}, nil, fmt.Errorf("encode spec: %w", err)
	}
	row := store.LobbyRow{
		ID: l.ID, ExternalRef: s.ExternalRef, State: string(l.State), StateReason: l.StateReason,
		StateSince: l.StateSince, Bot: l.Bot, GCLobbyID: l.GCLobbyID, LeagueID: s.LeagueID,
		LeagueVerified: l.LeagueVerified, Spec: spec, PassKey: s.Password.Reveal(),
		LaunchAttempts: l.LaunchAttempts, MatchID: l.Match.MatchID, Outcome: l.Match.Outcome,
		Seq: l.Seq, Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
		ExpiresAt: l.ExpiresAt, EndedAt: l.EndedAt,
	}
	if l.Details != (gc.LobbyDetails{}) {
		d := l.Details
		if row.Details, err = json.Marshal(detailsDoc{
			LobbyID: d.LobbyID, GameName: d.GameName, ServerRegion: d.ServerRegion, GameMode: d.GameMode,
			LeagueID: d.LeagueID, LeagueNodeID: d.LeagueNodeID, Visibility: d.Visibility,
			AllowSpectating: d.AllowSpectating, AllowCheats: d.AllowCheats, TVDelaySeconds: d.TVDelaySeconds,
		}); err != nil {
			return store.LobbyRow{}, nil, fmt.Errorf("encode details: %w", err)
		}
	}
	if row.HostSeat, err = json.Marshal(l.HostSeat); err != nil {
		return store.LobbyRow{}, nil, fmt.Errorf("encode host seat: %w", err)
	}
	if row.Match, err = json.Marshal(l.Match); err != nil {
		return store.LobbyRow{}, nil, fmt.Errorf("encode match: %w", err)
	}
	var players []store.PlayerRow
	for _, side := range []Side{Radiant, Dire} {
		t := s.Teams.Team(side)
		for i, p := range t.Players {
			players = append(players, store.PlayerRow{
				AccountID: p.AccountID, Side: string(side), Position: i, TeamRef: t.TeamRef,
				Captain: p.Captain, Ready: p.Ready, InvitesSent: p.InvitesSent,
				LastInviteAt: p.LastInviteAt, InviteConfirmed: p.InviteConfirmed,
			})
		}
	}
	if players == nil {
		players = []store.PlayerRow{} // not nil: Transition replaces the roster
	}
	return row, players, nil
}

func fromRow(row store.LobbyRow, players []store.PlayerRow) (Lobby, error) {
	var doc specDoc
	if err := json.Unmarshal(row.Spec, &doc); err != nil {
		return Lobby{}, fmt.Errorf("decode spec of %s: %w", row.ID, err)
	}
	l := Lobby{
		ID: row.ID, State: State(row.State), StateReason: row.StateReason, StateSince: row.StateSince,
		Seq: row.Seq, Version: row.Version, Bot: row.Bot, GCLobbyID: row.GCLobbyID,
		LeagueVerified: row.LeagueVerified, LaunchAttempts: row.LaunchAttempts,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ExpiresAt: row.ExpiresAt, EndedAt: row.EndedAt,
		Spec: Spec{
			ExternalRef: doc.ExternalRef, Name: doc.Name, Password: PassKey(row.PassKey),
			LeagueID: doc.LeagueID, LeagueNodeID: doc.LeagueNodeID, GameMode: doc.GameMode, Region: doc.Region,
			TVDelaySeconds: doc.TVDelaySeconds, AllowCheats: doc.AllowCheats, Visibility: doc.Visibility,
			Launch: doc.Launch, ReadyCheck: doc.ReadyCheck, AllowShortHanded: doc.AllowShortHanded,
			KickStrangers: doc.KickStrangers, Admins: doc.Admins, Metadata: doc.Metadata,
			Teams: Roster{
				Radiant: Team{TeamRef: doc.Radiant.TeamRef, Name: doc.Radiant.Name},
				Dire:    Team{TeamRef: doc.Dire.TeamRef, Name: doc.Dire.Name},
			},
		},
	}
	if row.Details != nil {
		var d detailsDoc
		if err := json.Unmarshal(row.Details, &d); err != nil {
			return Lobby{}, fmt.Errorf("decode details of %s: %w", row.ID, err)
		}
		l.Details = gc.LobbyDetails{
			LobbyID: d.LobbyID, GameName: d.GameName, PassKey: row.PassKey, ServerRegion: d.ServerRegion,
			GameMode: d.GameMode, LeagueID: d.LeagueID, LeagueNodeID: d.LeagueNodeID, Visibility: d.Visibility,
			AllowSpectating: d.AllowSpectating, AllowCheats: d.AllowCheats, TVDelaySeconds: d.TVDelaySeconds,
		}
	}
	if row.HostSeat != nil {
		if err := json.Unmarshal(row.HostSeat, &l.HostSeat); err != nil {
			return Lobby{}, fmt.Errorf("decode host seat of %s: %w", row.ID, err)
		}
	}
	if row.Match != nil {
		if err := json.Unmarshal(row.Match, &l.Match); err != nil {
			return Lobby{}, fmt.Errorf("decode match of %s: %w", row.ID, err)
		}
	}
	for _, p := range players {
		t := l.Spec.Teams.Team(Side(p.Side))
		t.Players = append(t.Players, Player{
			AccountID: p.AccountID, Captain: p.Captain, Ready: p.Ready, InvitesSent: p.InvitesSent,
			LastInviteAt: p.LastInviteAt, InviteConfirmed: p.InviteConfirmed,
		})
	}
	return l, nil
}

func eventRow(ev Event) (store.EventRow, error) {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return store.EventRow{}, fmt.Errorf("encode event %s: %w", ev.Type, err)
	}
	return store.EventRow{ID: ev.ID, LobbyID: ev.LobbyID, Seq: ev.Seq, Type: ev.Type, OccurredAt: ev.OccurredAt, Data: data}, nil
}
