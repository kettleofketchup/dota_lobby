package api

import (
	"maps"
	"reflect"
	"slices"

	"github.com/danielgtaylor/huma/v2"
)

// The data of each event type, for the spec only: package lobby builds
// the data as maps. A test in this package reads the lobby sources and
// fails when an event type or a data key is missing here.

// LobbyStateChangedData is the data of lobby.state_changed.
type LobbyStateChangedData struct {
	From   *string `json:"from" nullable:"true" doc:"The state before; null for the first event of a lobby"`
	To     string  `json:"to" doc:"The state after, as in lobby.state"`
	Reason *string `json:"reason" nullable:"true" doc:"Why, e.g. accepted, created, api, idle_timeout; null when there is no particular reason"`
}

// LobbyCreatedData is the data of lobby.created.
type LobbyCreatedData struct {
	GCLobbyID      string `json:"gc_lobby_id" doc:"The game coordinator's lobby ID (64-bit, as a string)"`
	LeagueVerified bool   `json:"league_verified" doc:"Whether the game coordinator confirmed the league"`
}

// LobbyCreateFailedData is the data of lobby.create_failed.
type LobbyCreateFailedData struct {
	Code    string `json:"code" enum:"league_rejected,create_timeout,gc_error" doc:"Why the create failed; lobby.failed follows"`
	Message string `json:"message" doc:"The cause, for people"`
}

// LobbyUpdatedData is the data of lobby.updated.
type LobbyUpdatedData struct {
	Fields []string `json:"fields" doc:"The settings the PATCH changed"`
}

// LobbyTeamsData is the data of lobby.roster_changed and
// lobby.teams_flipped.
type LobbyTeamsData struct {
	RadiantTeamRef *string `json:"radiant_team_ref" nullable:"true" doc:"The Radiant team's reference after the change"`
	DireTeamRef    *string `json:"dire_team_ref" nullable:"true" doc:"The Dire team's reference after the change"`
}

// LobbyPlayerInvitedData is the data of lobby.player_invited.
type LobbyPlayerInvitedData struct {
	AccountID   uint32 `json:"account_id" doc:"The player's 32-bit account ID"`
	SteamID     string `json:"steam_id" doc:"The player's 64-bit Steam ID, as a string"`
	InvitesSent int    `json:"invites_sent" doc:"Invites sent to this player so far"`
	Confirmed   bool   `json:"confirmed" doc:"Always false: Steam does not confirm an invite"`
}

// LobbyPlayerPresenceData is the data of lobby.player_joined and
// lobby.player_left.
type LobbyPlayerPresenceData struct {
	AccountID uint32  `json:"account_id" doc:"The member's 32-bit account ID"`
	Rostered  bool    `json:"rostered" doc:"Whether the member is on the lobby's roster"`
	TeamRef   *string `json:"team_ref" nullable:"true" doc:"The rostered team's reference; null when not rostered or the team has none"`
}

// LobbyPlayerSeatedData is the data of lobby.player_seated.
type LobbyPlayerSeatedData struct {
	AccountID uint32 `json:"account_id" doc:"The member's 32-bit account ID"`
	Seat      string `json:"seat" enum:"radiant,dire" doc:"The side the member took"`
	Correct   bool   `json:"correct" doc:"Whether that is the member's rostered side (always true without a roster)"`
}

// LobbyPlayerKickedData is the data of lobby.player_kicked.
type LobbyPlayerKickedData struct {
	AccountID uint32 `json:"account_id" doc:"The member's 32-bit account ID"`
	From      string `json:"from" enum:"team,lobby" doc:"Moved off a team slot, or out of the lobby"`
	Reason    string `json:"reason" enum:"wrong_side,stranger,api,roster" doc:"Why"`
}

// LobbyEnforcementStoppedData is the data of lobby.enforcement_stopped.
type LobbyEnforcementStoppedData struct {
	AccountID uint32 `json:"account_id" doc:"The member the bot stopped moving"`
	Kicks     int    `json:"kicks" doc:"How often the bot moved the member"`
}

// LobbyPlayerReadyData is the data of lobby.player_ready.
type LobbyPlayerReadyData struct {
	AccountID uint32 `json:"account_id" doc:"The player's 32-bit account ID"`
}

// LobbyPlayerUnreadyData is the data of lobby.player_unready.
type LobbyPlayerUnreadyData struct {
	AccountID uint32 `json:"account_id" doc:"The player's 32-bit account ID"`
	Reason    string `json:"reason" enum:"chat,moved,left" doc:"Said so in chat, moved off the side, or left the lobby"`
}

// LobbyReadyData is the data of lobby.ready: always empty.
type LobbyReadyData struct{}

// LobbyUnreadyData is the data of lobby.unready.
type LobbyUnreadyData struct {
	Missing []string `json:"missing" doc:"What the lobby is waiting for"`
}

// LobbyLaunchingData is the data of lobby.launching.
type LobbyLaunchingData struct {
	Trigger          string  `json:"trigger" enum:"api,chat,auto" doc:"What started the launch"`
	ByAccountID      *uint32 `json:"by_account_id" nullable:"true" doc:"Who started it in chat; null otherwise"`
	Attempt          int     `json:"attempt" doc:"The launch attempt, from 1"`
	HostSeatStrategy string  `json:"host_seat_strategy" doc:"How the bot left the teams for this launch (lobby.host_seat_strategy)"`
}

// LobbyLaunchFailedData is the data of lobby.launch_failed.
type LobbyLaunchFailedData struct {
	Reason       string  `json:"reason" doc:"Why, e.g. host_not_seated, server_stall, load_failed, ignored"`
	Attempt      int     `json:"attempt" doc:"The launch attempt that failed"`
	NextStrategy *string `json:"next_strategy" nullable:"true" doc:"The host seat strategy the next launch tries; null when none is left"`
}

// MatchStartedData is the data of match.started.
type MatchStartedData struct {
	MatchID          string  `json:"match_id" doc:"The match ID (64-bit, as a string)"`
	HostSeatStrategy string  `json:"host_seat_strategy" doc:"The host seat strategy of the launch"`
	RadiantTeamRef   *string `json:"radiant_team_ref" nullable:"true" doc:"The Radiant team's reference"`
	DireTeamRef      *string `json:"dire_team_ref" nullable:"true" doc:"The Dire team's reference"`
}

// MatchEndedData is the data of match.ended. It is what the lobby
// reported, not the Steam Web API's match record.
type MatchEndedData struct {
	MatchID         string  `json:"match_id" doc:"The match ID (64-bit, as a string)"`
	Outcome         int32   `json:"outcome" doc:"The raw EMatchOutcome: 2 Radiant won, 3 Dire won"`
	OutcomeName     string  `json:"outcome_name" doc:"The outcome's name"`
	WinnerSide      *string `json:"winner_side" nullable:"true" doc:"radiant or dire; null when nobody won"`
	WinnerTeamRef   *string `json:"winner_team_ref" nullable:"true" doc:"The winning team's reference"`
	Scored          *bool   `json:"scored" nullable:"true" doc:"Whether the match counts; null when unknown"`
	DurationSeconds *uint32 `json:"duration_seconds" nullable:"true" doc:"The game's length; null when unknown"`
	Provisional     bool    `json:"provisional" enum:"true" doc:"Always true"`
	Source          string  `json:"source" enum:"lobby" doc:"Always lobby"`
}

// LobbyEndedData is the data of lobby.finished, lobby.closed, lobby.failed
// and lobby.timed_out.
type LobbyEndedData struct {
	Reason *string `json:"reason" nullable:"true" doc:"Why the lobby ended, as in lobby.state_changed"`
}

// eventKind is one schema of the LobbyEvent union: the event types that
// share a data shape.
type eventKind struct {
	name  string // the event's schema name
	doc   string
	types []string
	data  reflect.Type
}

var eventKinds = []eventKind{
	{"LobbyStateChangedEvent", "Sent with every state change, next to the specific event.", []string{"lobby.state_changed"}, reflect.TypeFor[LobbyStateChangedData]()},
	{"LobbyCreatedEvent", "The game coordinator created the lobby.", []string{"lobby.created"}, reflect.TypeFor[LobbyCreatedData]()},
	{"LobbyCreateFailedEvent", "The lobby could not be created.", []string{"lobby.create_failed"}, reflect.TypeFor[LobbyCreateFailedData]()},
	{"LobbyUpdatedEvent", "A PATCH changed the lobby's settings.", []string{"lobby.updated"}, reflect.TypeFor[LobbyUpdatedData]()},
	{"LobbyTeamsEvent", "The roster was replaced or the sides flipped.", []string{"lobby.roster_changed", "lobby.teams_flipped"}, reflect.TypeFor[LobbyTeamsData]()},
	{"LobbyPlayerInvitedEvent", "The bot invited a rostered player.", []string{"lobby.player_invited"}, reflect.TypeFor[LobbyPlayerInvitedData]()},
	{"LobbyPlayerPresenceEvent", "A member joined or left the lobby.", []string{"lobby.player_joined", "lobby.player_left"}, reflect.TypeFor[LobbyPlayerPresenceData]()},
	{"LobbyPlayerSeatedEvent", "A member took a team slot.", []string{"lobby.player_seated"}, reflect.TypeFor[LobbyPlayerSeatedData]()},
	{"LobbyPlayerKickedEvent", "The bot moved a member off a team or out of the lobby.", []string{"lobby.player_kicked"}, reflect.TypeFor[LobbyPlayerKickedData]()},
	{"LobbyEnforcementStoppedEvent", "The bot stopped moving a member.", []string{"lobby.enforcement_stopped"}, reflect.TypeFor[LobbyEnforcementStoppedData]()},
	{"LobbyPlayerReadyEvent", "A player said ready in chat.", []string{"lobby.player_ready"}, reflect.TypeFor[LobbyPlayerReadyData]()},
	{"LobbyPlayerUnreadyEvent", "A player is no longer ready.", []string{"lobby.player_unready"}, reflect.TypeFor[LobbyPlayerUnreadyData]()},
	{"LobbyReadyEvent", "The lobby is ready to launch.", []string{"lobby.ready"}, reflect.TypeFor[LobbyReadyData]()},
	{"LobbyUnreadyEvent", "The lobby is no longer ready.", []string{"lobby.unready"}, reflect.TypeFor[LobbyUnreadyData]()},
	{"LobbyLaunchingEvent", "A launch started.", []string{"lobby.launching"}, reflect.TypeFor[LobbyLaunchingData]()},
	{"LobbyLaunchFailedEvent", "A launch did not reach a game.", []string{"lobby.launch_failed"}, reflect.TypeFor[LobbyLaunchFailedData]()},
	{"MatchStartedEvent", "The match started.", []string{"match.started"}, reflect.TypeFor[MatchStartedData]()},
	{"MatchEndedEvent", "The match ended. Provisional: what the lobby reported.", []string{"match.ended"}, reflect.TypeFor[MatchEndedData]()},
	{"LobbyEndedEvent", "The lobby ended.", []string{"lobby.finished", "lobby.closed", "lobby.failed", "lobby.timed_out"}, reflect.TypeFor[LobbyEndedData]()},
}

// TransformSchema makes LobbyEvent a union over the event type: one
// schema per data shape, each with its types as the enum of `type`, picked
// by the discriminator on `type`.
func (LobbyEvent) TransformSchema(r huma.Registry, envelope *huma.Schema) *huma.Schema {
	const prefix = "#/components/schemas/"
	union := &huma.Schema{
		Description:   "One lobby event. `type` names the event and decides the shape of `data`.",
		Discriminator: &huma.Discriminator{PropertyName: "type", Mapping: map[string]string{}},
	}
	for _, k := range eventKinds {
		props := maps.Clone(envelope.Properties)
		typ := *props["type"]
		typ.Enum = make([]any, len(k.types))
		for i, t := range k.types {
			typ.Enum[i] = t
		}
		typ.Examples = []any{k.types[0]}
		props["type"] = &typ
		props["data"] = r.Schema(k.data, true, "")
		s := &huma.Schema{
			Type:                 huma.TypeObject,
			Description:          k.doc,
			Properties:           props,
			Required:             slices.Clone(envelope.Required),
			AdditionalProperties: envelope.AdditionalProperties,
		}
		s.PrecomputeMessages()
		r.Map()[k.name] = s
		union.OneOf = append(union.OneOf, &huma.Schema{Ref: prefix + k.name})
		for _, t := range k.types {
			union.Discriminator.Mapping[t] = prefix + k.name
		}
	}
	return union
}
