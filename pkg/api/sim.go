package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/sim"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

// Simulator is the simulated Steam and game coordinator the control
// operations drive. *sim.World implements it.
type Simulator interface {
	Lobbies() []sim.LobbyInfo
	Lobby(id uint64) (sim.LobbyInfo, error)
	Join(id uint64, account uint32, personaName string) (sim.LobbyInfo, error)
	Seat(id uint64, account uint32, team gc.Team, slot *uint32) (sim.LobbyInfo, error)
	Leave(id uint64, account uint32) (sim.LobbyInfo, error)
	Say(id uint64, account uint32, text string) (bool, error)
	Ready(id uint64, account uint32) (bool, error)
	StartMatch(id, matchID uint64) (sim.LobbyInfo, error)
	EndMatch(id uint64, outcome int32) (sim.LobbyInfo, error)
	Fail(id uint64, mode string) (sim.LobbyInfo, error)
	Settings() sim.Settings
	SetSettings(s sim.Settings)
	ReadyCommand() string
}

var simTags = []string{"Simulation"}

// --- views ---

type simMemberView struct {
	AccountID   uint32 `json:"account_id"`
	SteamID     string `json:"steam_id" doc:"The SteamID64, as a string"`
	Team        string `json:"team" enum:"radiant,dire,broadcaster,spectator,player_pool,no_team"`
	Slot        uint32 `json:"slot" doc:"The slot on radiant or dire (0-4); the channel for broadcaster; 0 otherwise"`
	PersonaName string `json:"persona_name"`
	Host        bool   `json:"host" doc:"True for the bot that hosts the lobby"`
}

type simChatView struct {
	AccountID   uint32    `json:"account_id"`
	PersonaName string    `json:"persona_name"`
	Text        string    `json:"text"`
	At          time.Time `json:"at"`
	Delivered   bool      `json:"delivered" doc:"False for a player's line said before the host joined the lobby chat; the host never saw it"`
}

type simLobbyView struct {
	GCLobbyID      string          `json:"gc_lobby_id" doc:"The simulated GC lobby ID, as the service's lobby shows it"`
	Bot            string          `json:"bot" doc:"The bot hosting the lobby"`
	HostAccountID  uint32          `json:"host_account_id"`
	State          string          `json:"state" enum:"ui,server_setup,run,postgame,ready_up,not_ready,server_assign" doc:"The GC lobby state: ui before the launch, server_assign after it, run once the match started, postgame once it ended"`
	Name           string          `json:"name"`
	Password       string          `json:"password"`
	LeagueID       uint32          `json:"league_id" doc:"The league read back; 0 when the bot does not administer the league asked for"`
	LeagueNodeID   uint32          `json:"league_node_id"`
	GameMode       uint32          `json:"game_mode"`
	ServerRegion   uint32          `json:"server_region"`
	TVDelaySeconds uint32          `json:"tv_delay_seconds"`
	MatchID        *string         `json:"match_id" nullable:"true" doc:"The simulated match ID once the match started"`
	Outcome        *int32          `json:"outcome" nullable:"true" doc:"The EMatchOutcome once the match ended"`
	Members        []simMemberView `json:"members" doc:"Every member, the host included, in join order"`
	PendingInvites []uint32        `json:"pending_invites" doc:"Invited account IDs that have not joined"`
	ChatJoined     bool            `json:"chat_joined" doc:"True while the host is in the lobby chat"`
	Chat           []simChatView   `json:"chat" doc:"The lobby chat, oldest first, the last 200 lines"`
	CreatedAt      time.Time       `json:"created_at"`
}

func newSimLobbyView(l sim.LobbyInfo) simLobbyView {
	v := simLobbyView{
		GCLobbyID:      strconv.FormatUint(l.LobbyID, 10),
		Bot:            l.Bot,
		HostAccountID:  l.HostAccountID,
		State:          l.State.String(),
		Name:           l.Details.GameName,
		Password:       l.Details.PassKey,
		LeagueID:       l.Details.LeagueID,
		LeagueNodeID:   l.Details.LeagueNodeID,
		GameMode:       l.Details.GameMode,
		ServerRegion:   l.Details.ServerRegion,
		TVDelaySeconds: l.Details.TVDelaySeconds,
		Members:        make([]simMemberView, len(l.Members)),
		PendingInvites: l.Pending,
		ChatJoined:     l.ChatJoined,
		Chat:           make([]simChatView, len(l.Chat)),
		CreatedAt:      l.CreatedAt.UTC(),
	}
	if l.MatchID != 0 {
		id := strconv.FormatUint(l.MatchID, 10)
		v.MatchID = &id
	}
	if l.Outcome != 0 {
		o := l.Outcome
		v.Outcome = &o
	}
	for i, m := range l.Members {
		v.Members[i] = simMemberView{
			AccountID: m.AccountID, SteamID: strconv.FormatUint(gc.SteamID64(m.AccountID), 10),
			Team: m.Team.String(), Slot: m.Slot, PersonaName: m.PersonaName, Host: m.Host,
		}
	}
	for i, c := range l.Chat {
		v.Chat[i] = simChatView{AccountID: c.AccountID, PersonaName: c.PersonaName, Text: c.Text, At: c.At.UTC(), Delivered: c.Delivered}
	}
	return v
}

type simLobbyOutput struct{ Body simLobbyView }

type simLobbyList struct {
	Lobbies []simLobbyView `json:"lobbies" doc:"Every simulated lobby, oldest first"`
}

type simLobbiesOutput struct{ Body simLobbyList }

type simChatResult struct {
	Delivered bool         `json:"delivered" doc:"True when the host was in the lobby chat and got the line"`
	Lobby     simLobbyView `json:"lobby"`
}

type simChatOutput struct{ Body simChatResult }

type simSettingsView struct {
	AutoJoin         bool   `json:"auto_join" doc:"Invited players join and take a slot on their rostered side"`
	AutoJoinDelayMS  int64  `json:"auto_join_delay_ms" doc:"How long after the invite a player joins"`
	AutoReady        bool   `json:"auto_ready" doc:"Auto-joined players say the ready command once seated"`
	AutoStart        bool   `json:"auto_start" doc:"A launched lobby starts its match on its own"`
	AutoStartDelayMS int64  `json:"auto_start_delay_ms" doc:"How long after the launch the match starts"`
	ReadyCommand     string `json:"ready_command" doc:"What a ready player says: lobby.chat.prefix and ready"`
}

type simSettingsOutput struct{ Body simSettingsView }

// --- inputs ---

type simLobbyInput struct {
	ID string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
}

type simPlayerInput struct {
	ID        string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	AccountID uint32 `path:"account_id" minimum:"1" doc:"The player's 32-bit account ID" example:"11111111"`
}

type simJoinRequest struct {
	AccountID   uint32 `json:"account_id" minimum:"1" doc:"The joining player's 32-bit account ID; an invite is not needed" example:"11111111"`
	PersonaName string `json:"persona_name,omitempty" maxLength:"64" doc:"The name the player shows in chat; default \"Player <account_id>\""`
}

type simJoinInput struct {
	ID   string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	Body simJoinRequest
}

type simSeatRequest struct {
	Team string  `json:"team" enum:"radiant,dire,player_pool,spectator,broadcaster" doc:"Where the player moves; player_pool is the unassigned pool"`
	Slot *uint32 `json:"slot,omitempty" maximum:"4" doc:"The radiant or dire slot, 0-4; default the first free one"`
}

type simSeatInput struct {
	ID        string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	AccountID uint32 `path:"account_id" minimum:"1" doc:"The player's 32-bit account ID" example:"11111111"`
	Body      simSeatRequest
}

type simSayRequest struct {
	AccountID uint32 `json:"account_id" minimum:"1" doc:"The member who says it" example:"11111111"`
	Text      string `json:"text" minLength:"1" maxLength:"300" example:"!ready"`
}

type simSayInput struct {
	ID   string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	Body simSayRequest
}

type simStartRequest struct {
	MatchID string `json:"match_id,omitempty" pattern:"^[1-9][0-9]{0,18}$" doc:"The match ID to report, as a string; default the next simulated one"`
}

type simStartInput struct {
	ID   string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	Body *simStartRequest
}

type simEndRequest struct {
	Result string `json:"result" enum:"radiant,dire,not_scored" doc:"radiant or dire wins, or not_scored (not_scored_canceled)"`
}

type simEndInput struct {
	ID   string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	Body simEndRequest
}

type simFailRequest struct {
	Mode string `json:"mode" enum:"lost,load_failed" doc:"lost: the lobby disappears from the bot's cache; load_failed: a launched lobby goes back to the UI state without a game"`
}

type simFailInput struct {
	ID   string `path:"id" doc:"The simulated GC lobby ID, or the service's lobby ID" example:"900000000000000001"`
	Body simFailRequest
}

type simSettingsRequest struct {
	AutoJoin         *bool  `json:"auto_join,omitempty"`
	AutoJoinDelayMS  *int64 `json:"auto_join_delay_ms,omitempty" minimum:"0" maximum:"3600000"`
	AutoReady        *bool  `json:"auto_ready,omitempty"`
	AutoStart        *bool  `json:"auto_start,omitempty"`
	AutoStartDelayMS *int64 `json:"auto_start_delay_ms,omitempty" minimum:"0" maximum:"3600000"`
}

type simSettingsInput struct {
	Body simSettingsRequest
}

// --- registration ---

// registerSim adds the simulation control operations. They are always in
// the spec, so it is the same for every deployment, but routed only in
// simulation mode: otherwise their paths answer 404 like any unknown path.
func (s *Server) registerSim(r *router) {
	errs := []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict}
	simRegister(s, r, huma.Operation{
		OperationID: "list-sim-lobbies", Method: http.MethodGet, Path: "/v1/sim/lobbies",
		Summary: "List the simulated lobbies", Tags: simTags, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized},
	}, s.listSimLobbies)
	simRegister(s, r, huma.Operation{
		OperationID: "get-sim-lobby", Method: http.MethodGet, Path: "/v1/sim/lobbies/{id}",
		Summary: "Get a simulated lobby", Description: "The lobby as the simulated game coordinator holds it, chat included.",
		Tags: simTags, Security: keyAuth, Errors: errs,
	}, s.getSimLobby)
	simRegister(s, r, huma.Operation{
		OperationID: "join-sim-lobby", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/players",
		Summary:     "Make a player join",
		Description: "The player joins the unassigned pool, as when accepting an invite. 409 already_member for a member.",
		Tags:        simTags, Security: keyAuth, Errors: errs,
	}, s.joinSimLobby)
	simRegister(s, r, huma.Operation{
		OperationID: "seat-sim-player", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/players/{account_id}/seat",
		Summary: "Make a player take a seat", Description: "409 seat_taken for a taken slot or a full team, not_member for a player not in the lobby.",
		Tags: simTags, Security: keyAuth, Errors: errs,
	}, s.seatSimPlayer)
	simRegister(s, r, huma.Operation{
		OperationID: "ready-sim-player", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/players/{account_id}/ready",
		Summary:     "Make a player say the ready command",
		Description: "Says lobby.chat.prefix and ready in the lobby chat as the player, which marks it ready when the lobby has a ready check.",
		Tags:        simTags, Security: keyAuth, Errors: errs,
	}, s.readySimPlayer)
	simRegister(s, r, huma.Operation{
		OperationID: "leave-sim-lobby", Method: http.MethodDelete, Path: "/v1/sim/lobbies/{id}/players/{account_id}",
		Summary: "Make a player leave", Tags: simTags, Security: keyAuth, Errors: errs,
	}, s.leaveSimLobby)
	simRegister(s, r, huma.Operation{
		OperationID: "say-sim-chat", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/chat",
		Summary:     "Post a chat line as a player",
		Description: "The line reaches the bot when it is in the lobby chat, so chat commands such as !ready and !start can be tested.",
		Tags:        simTags, Security: keyAuth, Errors: errs,
	}, s.saySimChat)
	simRegister(s, r, huma.Operation{
		OperationID: "start-sim-match", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/start",
		Summary:     "Start the match of a launched lobby",
		Description: "The lobby gets a match ID and goes to run, which the service reports as match.started. 409 invalid_state before the launch.",
		Tags:        simTags, Security: keyAuth, Errors: errs,
	}, s.startSimMatch)
	simRegister(s, r, huma.Operation{
		OperationID: "end-sim-match", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/end",
		Summary:     "End the running match",
		Description: "The lobby goes to postgame with the outcome, which the service reports as match.ended. 409 invalid_state when no match is running.",
		Tags:        simTags, Security: keyAuth, Errors: errs,
	}, s.endSimMatch)
	simRegister(s, r, huma.Operation{
		OperationID: "fail-sim-lobby", Method: http.MethodPost, Path: "/v1/sim/lobbies/{id}/fail",
		Summary:     "Make a lobby fail",
		Description: "lost drops the lobby from the bot's cache: after lobby lost grace (10 s) the service fails it as lobby_lost or lost_in_game. load_failed returns a launched lobby to the UI state: the launch failed.",
		Tags:        simTags, Security: keyAuth, Errors: errs,
	}, s.failSimLobby)
	simRegister(s, r, huma.Operation{
		OperationID: "get-sim-settings", Method: http.MethodGet, Path: "/v1/sim/settings",
		Summary: "Get the auto-mode settings", Tags: simTags, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized},
	}, s.getSimSettings)
	simRegister(s, r, huma.Operation{
		OperationID: "update-sim-settings", Method: http.MethodPatch, Path: "/v1/sim/settings",
		Summary:     "Change the auto-mode settings",
		Description: "Changes the fields given; the others keep their value. Scheduled joins and starts keep their delay.",
		Tags:        simTags, Security: keyAuth, Errors: []int{http.StatusUnauthorized},
	}, s.updateSimSettings)
}

// simRegister registers a control operation: routed in simulation mode,
// only documented otherwise.
func simRegister[I, O any](s *Server, r *router, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	if s.opts.Simulator != nil {
		register(s, r, op, h)
		return
	}
	if op.MaxBodyBytes == 0 {
		op.MaxBodyBytes = s.opts.MaxBodyBytes
	}
	huma.Register(docOnly{s.api}, op, h)
}

// docOnly is an API whose operations go into the spec but are not routed.
type docOnly struct{ huma.API }

func (d docOnly) Adapter() huma.Adapter { return unrouted{d.API.Adapter()} }

type unrouted struct{ huma.Adapter }

func (unrouted) Handle(*huma.Operation, func(huma.Context)) {}

// --- handlers ---

// simLobbyID resolves a path ID: a GC lobby ID, or a service lobby ID
// whose GC lobby is looked up.
func (s *Server) simLobbyID(ctx context.Context, id string) (uint64, error) {
	if n, err := strconv.ParseUint(id, 10, 64); err == nil {
		return n, nil
	}
	l, err := s.opts.Lobbies.Get(ctx, id)
	switch {
	case errors.Is(err, lobby.ErrNotFound):
		return 0, coded(http.StatusNotFound, "not_found", "no lobby with that id")
	case err != nil:
		return 0, s.lobbyProblem(ctx, "get lobby", err)
	case l.GCLobbyID == 0:
		return 0, coded(http.StatusConflict, "invalid_state", "the lobby has no game coordinator lobby yet")
	}
	return l.GCLobbyID, nil
}

func simProblem(err error) error {
	msg := strings.TrimPrefix(err.Error(), "sim: ")
	switch {
	case errors.Is(err, sim.ErrNoLobby):
		return coded(http.StatusNotFound, "not_found", "no simulated lobby with that id")
	case errors.Is(err, sim.ErrNotMember):
		return coded(http.StatusConflict, "not_member", msg)
	case errors.Is(err, sim.ErrAlreadyMember):
		return coded(http.StatusConflict, "already_member", msg)
	case errors.Is(err, sim.ErrSeatTaken):
		return coded(http.StatusConflict, "seat_taken", msg)
	case errors.Is(err, sim.ErrWrongState):
		return coded(http.StatusConflict, "invalid_state", msg)
	case errors.Is(err, sim.ErrNoReadyCommand):
		return coded(http.StatusConflict, "no_ready_command", msg)
	case errors.Is(err, sim.ErrBadAccount):
		return invalid("account_id", msg)
	}
	return err
}

// simLobbyCall resolves the lobby and runs one control call on it.
func (s *Server) simLobbyCall(ctx context.Context, id string, fn func(gcID uint64) (sim.LobbyInfo, error)) (*simLobbyOutput, error) {
	gcID, err := s.simLobbyID(ctx, id)
	if err != nil {
		return nil, err
	}
	info, err := fn(gcID)
	if err != nil {
		return nil, simProblem(err)
	}
	return &simLobbyOutput{Body: newSimLobbyView(info)}, nil
}

func (s *Server) listSimLobbies(context.Context, *struct{}) (*simLobbiesOutput, error) {
	ls := s.opts.Simulator.Lobbies()
	out := simLobbyList{Lobbies: make([]simLobbyView, len(ls))}
	for i, l := range ls {
		out.Lobbies[i] = newSimLobbyView(l)
	}
	return &simLobbiesOutput{Body: out}, nil
}

func (s *Server) getSimLobby(ctx context.Context, in *simLobbyInput) (*simLobbyOutput, error) {
	return s.simLobbyCall(ctx, in.ID, s.opts.Simulator.Lobby)
}

func (s *Server) joinSimLobby(ctx context.Context, in *simJoinInput) (*simLobbyOutput, error) {
	return s.simLobbyCall(ctx, in.ID, func(id uint64) (sim.LobbyInfo, error) {
		return s.opts.Simulator.Join(id, in.Body.AccountID, in.Body.PersonaName)
	})
}

func (s *Server) seatSimPlayer(ctx context.Context, in *simSeatInput) (*simLobbyOutput, error) {
	team, _ := sim.TeamFromString(in.Body.Team)
	return s.simLobbyCall(ctx, in.ID, func(id uint64) (sim.LobbyInfo, error) {
		return s.opts.Simulator.Seat(id, in.AccountID, team, in.Body.Slot)
	})
}

func (s *Server) leaveSimLobby(ctx context.Context, in *simPlayerInput) (*simLobbyOutput, error) {
	return s.simLobbyCall(ctx, in.ID, func(id uint64) (sim.LobbyInfo, error) {
		return s.opts.Simulator.Leave(id, in.AccountID)
	})
}

// simChat runs a chat call and answers whether the host got the line.
func (s *Server) simChat(ctx context.Context, id string, say func(gcID uint64) (bool, error)) (*simChatOutput, error) {
	gcID, err := s.simLobbyID(ctx, id)
	if err != nil {
		return nil, err
	}
	delivered, err := say(gcID)
	if err != nil {
		return nil, simProblem(err)
	}
	info, err := s.opts.Simulator.Lobby(gcID)
	if err != nil {
		return nil, simProblem(err)
	}
	return &simChatOutput{Body: simChatResult{Delivered: delivered, Lobby: newSimLobbyView(info)}}, nil
}

func (s *Server) readySimPlayer(ctx context.Context, in *simPlayerInput) (*simChatOutput, error) {
	return s.simChat(ctx, in.ID, func(id uint64) (bool, error) { return s.opts.Simulator.Ready(id, in.AccountID) })
}

func (s *Server) saySimChat(ctx context.Context, in *simSayInput) (*simChatOutput, error) {
	return s.simChat(ctx, in.ID, func(id uint64) (bool, error) {
		return s.opts.Simulator.Say(id, in.Body.AccountID, in.Body.Text)
	})
}

func (s *Server) startSimMatch(ctx context.Context, in *simStartInput) (*simLobbyOutput, error) {
	var matchID uint64
	if in.Body != nil && in.Body.MatchID != "" {
		var err error
		if matchID, err = strconv.ParseUint(in.Body.MatchID, 10, 64); err != nil {
			return nil, invalid("body.match_id", "must be a 64-bit match ID")
		}
	}
	return s.simLobbyCall(ctx, in.ID, func(id uint64) (sim.LobbyInfo, error) {
		return s.opts.Simulator.StartMatch(id, matchID)
	})
}

func (s *Server) endSimMatch(ctx context.Context, in *simEndInput) (*simLobbyOutput, error) {
	outcome := sim.OutcomeNotScored
	switch in.Body.Result {
	case "radiant":
		outcome = sim.OutcomeRadiantVictory
	case "dire":
		outcome = sim.OutcomeDireVictory
	}
	return s.simLobbyCall(ctx, in.ID, func(id uint64) (sim.LobbyInfo, error) {
		return s.opts.Simulator.EndMatch(id, outcome)
	})
}

func (s *Server) failSimLobby(ctx context.Context, in *simFailInput) (*simLobbyOutput, error) {
	return s.simLobbyCall(ctx, in.ID, func(id uint64) (sim.LobbyInfo, error) {
		return s.opts.Simulator.Fail(id, in.Body.Mode)
	})
}

func (s *Server) simSettings() simSettingsView {
	st := s.opts.Simulator.Settings()
	return simSettingsView{
		AutoJoin: st.AutoJoin, AutoJoinDelayMS: st.AutoJoinDelay.Milliseconds(), AutoReady: st.AutoReady,
		AutoStart: st.AutoStart, AutoStartDelayMS: st.AutoStartDelay.Milliseconds(),
		ReadyCommand: s.opts.Simulator.ReadyCommand(),
	}
}

func (s *Server) getSimSettings(context.Context, *struct{}) (*simSettingsOutput, error) {
	return &simSettingsOutput{Body: s.simSettings()}, nil
}

func (s *Server) updateSimSettings(_ context.Context, in *simSettingsInput) (*simSettingsOutput, error) {
	st := s.opts.Simulator.Settings()
	b := in.Body
	if b.AutoJoin != nil {
		st.AutoJoin = *b.AutoJoin
	}
	if b.AutoJoinDelayMS != nil {
		st.AutoJoinDelay = time.Duration(*b.AutoJoinDelayMS) * time.Millisecond
	}
	if b.AutoReady != nil {
		st.AutoReady = *b.AutoReady
	}
	if b.AutoStart != nil {
		st.AutoStart = *b.AutoStart
	}
	if b.AutoStartDelayMS != nil {
		st.AutoStartDelay = time.Duration(*b.AutoStartDelayMS) * time.Millisecond
	}
	s.opts.Simulator.SetSettings(st)
	return &simSettingsOutput{Body: s.simSettings()}, nil
}
