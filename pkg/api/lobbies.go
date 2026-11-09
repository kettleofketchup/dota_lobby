package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

// Lobbies is what the API asks of the lobby layer. *lobby.Manager
// implements it.
type Lobbies interface {
	Create(ctx context.Context, spec lobby.Spec) (lobby.Lobby, bool, error)
	Get(ctx context.Context, id string) (lobby.Lobby, error)
	List(ctx context.Context, f lobby.Filter) ([]lobby.Lobby, string, error)
	Update(ctx context.Context, id string, p lobby.Patch) (lobby.Lobby, error)
	Close(ctx context.Context, id string) (lobby.Lobby, bool, error)
	Invite(ctx context.Context, id string, req lobby.InviteRequest) (lobby.InviteResult, error)
	Kick(ctx context.Context, id string, account uint32, from string) (lobby.Lobby, error)
	SetRoster(ctx context.Context, id string, r lobby.Roster) (lobby.Lobby, error)
	Launch(ctx context.Context, id string, req lobby.LaunchRequest) (lobby.Lobby, error)
	FlipTeams(ctx context.Context, id string) (lobby.Lobby, error)
	Shuffle(ctx context.Context, id string) (lobby.Lobby, error)
	Events(ctx context.Context, id string, after int64, limit int) ([]lobby.Envelope, error)
}

// --- requests ---

type playerReq struct {
	AccountID *uint32 `json:"account_id,omitempty" doc:"The 32-bit account ID; give this or steam_id" example:"11111111"`
	SteamID   *string `json:"steam_id,omitempty" doc:"An individual account's SteamID64 as a string; give this or account_id" example:"76561197971376839"`
	Captain   bool    `json:"captain,omitempty"`
}

type teamReq struct {
	TeamRef string      `json:"team_ref,omitempty" doc:"Your reference for the team"`
	Name    string      `json:"name,omitempty"`
	Players []playerReq `json:"players,omitempty" doc:"Up to 5 players; empty for an open lobby"`
}

type teamsReq struct {
	Radiant teamReq `json:"radiant,omitempty"`
	Dire    teamReq `json:"dire,omitempty"`
}

type createLobbyRequest struct {
	ExternalRef      string            `json:"external_ref" doc:"Your key for the lobby, 1-128 letters, digits, '.', '_', ':' or '-'; unique among lobbies that have not ended" example:"game-42"`
	Name             string            `json:"name" doc:"The lobby name, 1-64 characters" example:"Spring Cup R2 Ravens vs Owls"`
	Password         string            `json:"password,omitempty" doc:"Up to 32 characters; empty means none"`
	LeagueID         uint32            `json:"league_id,omitempty" doc:"A league one of the bots administers; 0 for none"`
	LeagueNodeID     uint32            `json:"league_node_id,omitempty"`
	GameMode         nameOrNumber      `json:"game_mode,omitempty"`
	Region           nameOrNumber      `json:"region,omitempty"`
	TVDelaySeconds   uint32            `json:"tv_delay_seconds,omitempty" doc:"10, 60, 120, 300 or 900"`
	AllowCheats      bool              `json:"allow_cheats,omitempty"`
	Visibility       string            `json:"visibility,omitempty" doc:"public (default), friends or unlisted"`
	Launch           string            `json:"launch,omitempty" doc:"manual (default) or auto"`
	ReadyCheck       bool              `json:"ready_check,omitempty"`
	AllowShortHanded bool              `json:"allow_short_handed,omitempty"`
	KickStrangers    bool              `json:"kick_strangers,omitempty"`
	Admins           []uint32          `json:"admins,omitempty" doc:"Account IDs allowed admin commands in this lobby's chat"`
	Teams            teamsReq          `json:"teams"`
	Metadata         map[string]string `json:"metadata,omitempty" doc:"Up to 16 string pairs, returned in every response"`
}

type updateLobbyRequest struct {
	Name             *string            `json:"name,omitempty"`
	Password         *string            `json:"password,omitempty" doc:"Empty removes the password"`
	GameMode         *nameOrNumber      `json:"game_mode,omitempty"`
	Region           *nameOrNumber      `json:"region,omitempty"`
	TVDelaySeconds   *uint32            `json:"tv_delay_seconds,omitempty"`
	Visibility       *string            `json:"visibility,omitempty"`
	AllowCheats      *bool              `json:"allow_cheats,omitempty"`
	Launch           *string            `json:"launch,omitempty"`
	ReadyCheck       *bool              `json:"ready_check,omitempty"`
	AllowShortHanded *bool              `json:"allow_short_handed,omitempty"`
	KickStrangers    *bool              `json:"kick_strangers,omitempty"`
	Admins           *[]uint32          `json:"admins,omitempty"`
	Metadata         *map[string]string `json:"metadata,omitempty"`
}

type inviteRequest struct {
	Players []uint32 `json:"players,omitempty" doc:"Rostered players to invite again; give this or missing"`
	Missing bool     `json:"missing,omitempty" doc:"Invite every rostered player not in the lobby"`
}

type kickRequest struct {
	AccountID uint32 `json:"account_id" minimum:"1" example:"99999999"`
	From      string `json:"from,omitempty" enum:"team,lobby" doc:"team (default): back to the unassigned pool; lobby: out of the lobby"`
}

type rosterRequest struct {
	Teams teamsReq `json:"teams"`
}

type launchRequest struct {
	Force bool `json:"force,omitempty" doc:"Launch from waiting, past the roster check and the attempt limit; never past the host seat check"`
}

// individualSteamID reports whether a SteamID64 is an individual account
// in the public universe, the only kind a player can be.
func individualSteamID(id uint64) bool { return id>>32 == gc.SteamID64(0)>>32 }

// roster converts the request's teams; a bad player is a 422 naming it.
func (t teamsReq) roster() (lobby.Roster, error) {
	var r lobby.Roster
	for _, side := range []lobby.Side{lobby.Radiant, lobby.Dire} {
		in := t.Radiant
		if side == lobby.Dire {
			in = t.Dire
		}
		out := r.Team(side)
		out.TeamRef, out.Name = in.TeamRef, in.Name
		for i, p := range in.Players {
			field := fmt.Sprintf("body.teams.%s.players[%d]", side, i)
			var account uint32
			switch {
			case p.AccountID != nil && p.SteamID != nil:
				return r, invalid(field, "give account_id or steam_id, not both")
			case p.AccountID != nil:
				account = *p.AccountID
			case p.SteamID != nil:
				id, err := strconv.ParseUint(*p.SteamID, 10, 64)
				if err != nil || !individualSteamID(id) {
					return r, invalid(field+".steam_id", "must be an individual account's SteamID64 as a string")
				}
				account = gc.AccountID(id)
			default:
				return r, invalid(field, "account_id or steam_id is required")
			}
			if account == 0 {
				return r, invalid(field, "account 0 is not a player")
			}
			out.Players = append(out.Players, lobby.Player{AccountID: account, Captain: p.Captain})
		}
	}
	return r, nil
}

func (c createLobbyRequest) spec() (lobby.Spec, error) {
	roster, err := c.Teams.roster()
	if err != nil {
		return lobby.Spec{}, err
	}
	return lobby.Spec{
		ExternalRef: c.ExternalRef, Name: c.Name, Password: lobby.PassKey(c.Password),
		LeagueID: c.LeagueID, LeagueNodeID: c.LeagueNodeID, GameMode: string(c.GameMode), Region: string(c.Region),
		TVDelaySeconds: c.TVDelaySeconds, AllowCheats: c.AllowCheats, Visibility: c.Visibility, Launch: c.Launch,
		ReadyCheck: c.ReadyCheck, AllowShortHanded: c.AllowShortHanded, KickStrangers: c.KickStrangers,
		Admins: c.Admins, Metadata: c.Metadata, Teams: roster,
	}, nil
}

func (p updateLobbyRequest) patch() lobby.Patch {
	out := lobby.Patch{
		Name: p.Name, TVDelaySeconds: p.TVDelaySeconds, Visibility: p.Visibility, AllowCheats: p.AllowCheats,
		Launch: p.Launch, ReadyCheck: p.ReadyCheck, AllowShortHanded: p.AllowShortHanded,
		KickStrangers: p.KickStrangers, Admins: p.Admins, Metadata: p.Metadata,
	}
	if p.Password != nil {
		pk := lobby.PassKey(*p.Password)
		out.Password = &pk
	}
	if p.GameMode != nil {
		s := string(*p.GameMode)
		out.GameMode = &s
	}
	if p.Region != nil {
		s := string(*p.Region)
		out.Region = &s
	}
	return out
}

// --- inputs and outputs ---

type lobbyIDInput struct {
	ID string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
}

type createLobbyInput struct {
	Body createLobbyRequest
}

type createLobbyOutput struct {
	Status   int
	Location string `header:"Location" doc:"The lobby's URL"`
	Body     LobbyWithPassword
}

type listLobbiesInput struct {
	State       []string `query:"state" enum:"pending,creating,waiting,ready,launching,in_game,post_game,finished,failed,closing,closed,timed_out" doc:"Only lobbies in these states, comma-separated; spaces around the commas are ignored"`
	ExternalRef string   `query:"external_ref" doc:"Only lobbies with this external_ref, ended ones included"`
	Active      bool     `query:"active" doc:"Only lobbies that have not ended"`
	Limit       int      `query:"limit" minimum:"1" maximum:"200" doc:"Page size, default 50"`
	Cursor      string   `query:"cursor" doc:"The next_cursor of the previous page"`
}

type lobbyList struct {
	Lobbies    []LobbyView `json:"lobbies" doc:"Newest first"`
	NextCursor *string     `json:"next_cursor" nullable:"true" doc:"Pass as cursor for the next page; null on the last page"`
}

type lobbyListOutput struct{ Body lobbyList }

type lobbyOutput struct{ Body LobbyView }

type lobbyWithPasswordOutput struct{ Body LobbyWithPassword }

type lobbyStatusOutput struct {
	Status int
	Body   LobbyView
}

type updateLobbyInput struct {
	ID   string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	Body updateLobbyRequest
}

type inviteInput struct {
	ID   string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	Body inviteRequest
}

type inviteSkip struct {
	AccountID uint32 `json:"account_id"`
	Reason    string `json:"reason" doc:"in_lobby, pending_cooldown, cap or not_rostered"`
}

type inviteResult struct {
	Invited []uint32     `json:"invited"`
	Skipped []inviteSkip `json:"skipped"`
}

type inviteOutput struct{ Body inviteResult }

type kickInput struct {
	ID   string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	Body kickRequest
}

type rosterInput struct {
	ID   string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	Body rosterRequest
}

type launchInput struct {
	ID   string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	Body *launchRequest
}

type eventsInput struct {
	ID    string `path:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	After int64  `query:"after" minimum:"0" doc:"Only events with a higher seq"`
	Limit int    `query:"limit" default:"100" minimum:"1" maximum:"500" doc:"Page size"`
}

type eventList struct {
	Events    []LobbyEvent `json:"events" doc:"Oldest first"`
	NextAfter int64        `json:"next_after" doc:"The last seq returned, or after when there is nothing new"`
}

type eventsOutput struct{ Body eventList }

// --- operations ---

// Error statuses the lobby operations document, besides the 422 and 500
// huma adds.
var (
	// lobbyErrors: an action on one lobby.
	lobbyErrors = []int{
		http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	}
	// bodyErrors: a request body that cannot be read.
	bodyErrors = []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType}
)

func lobbyOp(id, method, path, summary, desc string, status int, errs ...[]int) huma.Operation {
	if len(errs) == 0 {
		errs = [][]int{lobbyErrors}
	}
	return huma.Operation{
		OperationID: id, Method: method, Path: path, Summary: summary, Description: desc,
		Tags: []string{"Lobbies"}, Security: keyAuth, DefaultStatus: status, Errors: slices.Concat(errs...),
	}
}

func (s *Server) registerLobbies(r *router) {
	register(s, r, lobbyOp("create-lobby", http.MethodPost, "/v1/lobbies", "Ask for a lobby",
		"Accepts the request (202) and creates the lobby in the background; follow it with get-lobby. "+
			"Sending the same request again while the lobby is active returns it with 200. "+
			"The response carries the password.", http.StatusAccepted,
		[]int{http.StatusUnauthorized, http.StatusConflict, http.StatusServiceUnavailable}, bodyErrors), s.createLobby)
	alsoAnswers(s.api, http.MethodPost, "/v1/lobbies", http.StatusOK,
		"An active lobby with this external_ref and the same request exists; it is returned.")
	register(s, r, lobbyOp("list-lobbies", http.MethodGet, "/v1/lobbies", "List lobbies",
		"Newest first, without passwords.", http.StatusOK,
		[]int{http.StatusUnauthorized, http.StatusServiceUnavailable}), s.listLobbies)
	register(s, r, lobbyOp("get-lobby", http.MethodGet, "/v1/lobbies/{id}", "Get a lobby",
		"The lobby with its password.", http.StatusOK), s.getLobby)
	register(s, r, lobbyOp("update-lobby", http.MethodPatch, "/v1/lobbies/{id}", "Change a lobby's settings",
		"Allowed in pending, waiting and ready.", http.StatusOK, lobbyErrors, bodyErrors), s.updateLobby)
	register(s, r, lobbyOp("close-lobby", http.MethodDelete, "/v1/lobbies/{id}", "Close a lobby that has not launched",
		"202 when the lobby closes or starts closing; 200 when it was already closing or ended.", http.StatusAccepted), s.closeLobby)
	alsoAnswers(s.api, http.MethodDelete, "/v1/lobbies/{id}", http.StatusOK, "The lobby was already closing or ended; nothing changed.")
	invite := lobbyOp("invite-lobby-players", http.MethodPost, "/v1/lobbies/{id}/invite", "Send invites again",
		"Only rostered players, in waiting and ready. 429 when every requested invite is rate-limited.", http.StatusOK,
		lobbyErrors, bodyErrors, []int{http.StatusTooManyRequests})
	register(s, r, invite, s.inviteLobbyPlayers)
	register(s, r, lobbyOp("kick-lobby-member", http.MethodPost, "/v1/lobbies/{id}/kick", "Move a member to the pool or out of the lobby",
		"The lobby shows the result once the game coordinator applies it.", http.StatusAccepted, lobbyErrors, bodyErrors), s.kickLobbyMember)
	register(s, r, lobbyOp("set-lobby-roster", http.MethodPost, "/v1/lobbies/{id}/roster", "Replace both rosters",
		"In waiting and ready.", http.StatusOK, lobbyErrors, bodyErrors), s.setLobbyRoster)
	register(s, r, lobbyOp("launch-lobby", http.MethodPost, "/v1/lobbies/{id}/launch", "Start the match",
		"Allowed in ready, and in waiting with force. The body is optional.", http.StatusAccepted, lobbyErrors, bodyErrors), s.launchLobby)
	register(s, r, lobbyOp("flip-lobby-teams", http.MethodPost, "/v1/lobbies/{id}/teams/flip", "Swap the sides",
		"Swaps Radiant and Dire in the lobby and in the rosters, in waiting and ready.", http.StatusAccepted), s.flipLobbyTeams)
	register(s, r, lobbyOp("shuffle-lobby-teams", http.MethodPost, "/v1/lobbies/{id}/teams/shuffle", "Balanced shuffle of an open lobby",
		"Only when both rosters are empty.", http.StatusAccepted), s.shuffleLobbyTeams)
	register(s, r, lobbyOp("list-lobby-events", http.MethodGet, "/v1/lobbies/{id}/events", "List a lobby's events",
		"Events with a seq above after, oldest first, in the envelope webhooks deliver.", http.StatusOK), s.listLobbyEvents)
}

// gcContext bounds a request that waits on GC calls, so it answers before
// the server's write timeout.
func (s *Server) gcContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.opts.GCRequestTimeout)
}

func (s *Server) createLobby(ctx context.Context, in *createLobbyInput) (*createLobbyOutput, error) {
	spec, err := in.Body.spec()
	if err != nil {
		return nil, err
	}
	l, created, err := s.opts.Lobbies.Create(ctx, spec)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "create-lobby", err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	return &createLobbyOutput{Status: status, Location: "/v1/lobbies/" + l.ID, Body: s.lobbyWithPassword(l)}, nil
}

// trimStateFilter drops the spaces around each comma-separated value of
// GET /v1/lobbies?state=, so "waiting, ready" still filters: huma checks
// each raw value against the state enum.
func trimStateFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/lobbies" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		states, ok := q["state"]
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		changed := false
		for i, v := range states {
			parts := strings.Split(v, ",")
			for j, p := range parts {
				parts[j] = strings.TrimSpace(p)
			}
			if t := strings.Join(parts, ","); t != v {
				states[i], changed = t, true
			}
		}
		if !changed {
			next.ServeHTTP(w, r)
			return
		}
		u := *r.URL
		u.RawQuery = q.Encode()
		r = r.WithContext(r.Context())
		r.URL = &u
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listLobbies(ctx context.Context, in *listLobbiesInput) (*lobbyListOutput, error) {
	f := lobby.Filter{ExternalRef: in.ExternalRef, Cursor: in.Cursor, Active: in.Active, Limit: in.Limit}
	for _, st := range in.State {
		f.States = append(f.States, lobby.State(st))
	}
	ls, next, err := s.opts.Lobbies.List(ctx, f)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "list-lobbies", err)
	}
	out := make([]LobbyView, len(ls))
	for i, l := range ls {
		out[i] = s.lobbyView(l)
	}
	return &lobbyListOutput{Body: lobbyList{Lobbies: out, NextCursor: nonEmpty(next)}}, nil
}

func (s *Server) getLobby(ctx context.Context, in *lobbyIDInput) (*lobbyWithPasswordOutput, error) {
	l, err := s.opts.Lobbies.Get(ctx, in.ID)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "get-lobby", err)
	}
	return &lobbyWithPasswordOutput{Body: s.lobbyWithPassword(l)}, nil
}

func (s *Server) updateLobby(ctx context.Context, in *updateLobbyInput) (*lobbyOutput, error) {
	ctx, cancel := s.gcContext(ctx)
	defer cancel()
	l, err := s.opts.Lobbies.Update(ctx, in.ID, in.Body.patch())
	if err != nil {
		return nil, s.lobbyProblem(ctx, "update-lobby", err)
	}
	return &lobbyOutput{Body: s.lobbyView(l)}, nil
}

func (s *Server) closeLobby(ctx context.Context, in *lobbyIDInput) (*lobbyStatusOutput, error) {
	l, changed, err := s.opts.Lobbies.Close(ctx, in.ID)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "close-lobby", err)
	}
	status := http.StatusOK
	if changed {
		status = http.StatusAccepted
	}
	return &lobbyStatusOutput{Status: status, Body: s.lobbyView(l)}, nil
}

func (s *Server) inviteLobbyPlayers(ctx context.Context, in *inviteInput) (*inviteOutput, error) {
	if in.Body.Missing == (len(in.Body.Players) > 0) {
		return nil, invalid("body", `give either "players" or "missing": true`)
	}
	ctx, cancel := s.gcContext(ctx)
	defer cancel()
	res, err := s.opts.Lobbies.Invite(ctx, in.ID, lobby.InviteRequest{Players: in.Body.Players, Missing: in.Body.Missing})
	if err != nil {
		return nil, s.lobbyProblem(ctx, "invite-lobby-players", err)
	}
	out := inviteResult{Invited: res.Invited, Skipped: make([]inviteSkip, len(res.Skipped))}
	if out.Invited == nil {
		out.Invited = []uint32{}
	}
	for i, sk := range res.Skipped {
		out.Skipped[i] = inviteSkip{AccountID: sk.AccountID, Reason: sk.Reason}
	}
	return &inviteOutput{Body: out}, nil
}

func (s *Server) kickLobbyMember(ctx context.Context, in *kickInput) (*lobbyOutput, error) {
	from := in.Body.From
	if from == "" {
		from = "team"
	}
	ctx, cancel := s.gcContext(ctx)
	defer cancel()
	l, err := s.opts.Lobbies.Kick(ctx, in.ID, in.Body.AccountID, from)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "kick-lobby-member", err)
	}
	return &lobbyOutput{Body: s.lobbyView(l)}, nil
}

func (s *Server) setLobbyRoster(ctx context.Context, in *rosterInput) (*lobbyOutput, error) {
	roster, err := in.Body.Teams.roster()
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.gcContext(ctx)
	defer cancel()
	l, err := s.opts.Lobbies.SetRoster(ctx, in.ID, roster)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "set-lobby-roster", err)
	}
	return &lobbyOutput{Body: s.lobbyView(l)}, nil
}

func (s *Server) launchLobby(ctx context.Context, in *launchInput) (*lobbyOutput, error) {
	req := lobby.LaunchRequest{Trigger: lobby.TriggerAPI}
	if in.Body != nil {
		req.Force = in.Body.Force
	}
	l, err := s.opts.Lobbies.Launch(ctx, in.ID, req)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "launch-lobby", err)
	}
	return &lobbyOutput{Body: s.lobbyView(l)}, nil
}

func (s *Server) flipLobbyTeams(ctx context.Context, in *lobbyIDInput) (*lobbyOutput, error) {
	ctx, cancel := s.gcContext(ctx)
	defer cancel()
	l, err := s.opts.Lobbies.FlipTeams(ctx, in.ID)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "flip-lobby-teams", err)
	}
	return &lobbyOutput{Body: s.lobbyView(l)}, nil
}

func (s *Server) shuffleLobbyTeams(ctx context.Context, in *lobbyIDInput) (*lobbyOutput, error) {
	ctx, cancel := s.gcContext(ctx)
	defer cancel()
	l, err := s.opts.Lobbies.Shuffle(ctx, in.ID)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "shuffle-lobby-teams", err)
	}
	return &lobbyOutput{Body: s.lobbyView(l)}, nil
}

func (s *Server) listLobbyEvents(ctx context.Context, in *eventsInput) (*eventsOutput, error) {
	evs, err := s.opts.Lobbies.Events(ctx, in.ID, in.After, in.Limit)
	if err != nil {
		return nil, s.lobbyProblem(ctx, "list-lobby-events", err)
	}
	out := eventList{Events: make([]LobbyEvent, len(evs)), NextAfter: in.After}
	for i, e := range evs {
		out.Events[i] = newLobbyEvent(e)
	}
	if len(evs) > 0 {
		out.NextAfter = evs[len(evs)-1].Seq
	}
	return &eventsOutput{Body: out}, nil
}

// --- errors ---

// lobbyProblem maps a lobby error to a problem. The text of an unexpected
// error goes to the log; the client gets a generic 503.
func (s *Server) lobbyProblem(ctx context.Context, op string, err error) error {
	var unknown *lobby.UnknownNameError
	var limited *lobby.InviteLimitedError
	msg := strings.TrimPrefix(err.Error(), "lobby: ")
	switch {
	case errors.As(err, &unknown):
		return coded(http.StatusUnprocessableEntity, "unknown_"+unknown.Field,
			fmt.Sprintf("%s %q is not configured (lobby.%ss) and is not a number", unknown.Field, unknown.Value, unknown.Field))
	case errors.As(err, &limited):
		p := coded(http.StatusTooManyRequests, "invite_limited", msg)
		if limited.RetryAfter <= 0 {
			return p
		}
		return huma.ErrorWithHeaders(p, http.Header{"Retry-After": {strconv.Itoa(int(math.Ceil(limited.RetryAfter.Seconds())))}})
	case errors.Is(err, lobby.ErrInvalid):
		return coded(http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(msg, "invalid request: "))
	case errors.Is(err, lobby.ErrNotFound):
		return coded(http.StatusNotFound, "not_found", "no lobby with that id")
	case errors.Is(err, lobby.ErrConflict):
		return coded(http.StatusConflict, "conflict", "external_ref is used by an active lobby with a different request")
	case errors.Is(err, lobby.ErrInvalidState):
		return coded(http.StatusConflict, "invalid_state", strings.TrimPrefix(msg, "not allowed in the lobby's current state: "))
	case errors.Is(err, lobby.ErrNotReady):
		return coded(http.StatusConflict, "not_ready", strings.TrimPrefix(msg, "roster not complete or not seated: "))
	case errors.Is(err, lobby.ErrHostNotSeated):
		return coded(http.StatusConflict, "host_not_seated", "the host seat could not be confirmed; the bot is still on a team")
	case errors.Is(err, lobby.ErrLeagueNotHeld):
		return coded(http.StatusUnprocessableEntity, "league_not_held", strings.TrimPrefix(msg, "no configured bot administers the league: "))
	case errors.Is(err, lobby.ErrGCTimeout):
		s.log.WarnContext(ctx, "lobby request timed out", "op", op, "err", err)
		return coded(http.StatusGatewayTimeout, "gc_timeout", "the game coordinator did not answer in time")
	default:
		s.log.ErrorContext(ctx, "lobby request failed", "op", op, "err", err)
		return coded(http.StatusServiceUnavailable, "unavailable", "the lobby service cannot do that right now; try again shortly")
	}
}

// noLobbies stands in when no lobby manager is wired.
type noLobbies struct{}

var errNoLobbies = fmt.Errorf("%w: no lobby manager", lobby.ErrUnavailable)

func (noLobbies) Create(context.Context, lobby.Spec) (lobby.Lobby, bool, error) {
	return lobby.Lobby{}, false, errNoLobbies
}
func (noLobbies) Get(context.Context, string) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) List(context.Context, lobby.Filter) ([]lobby.Lobby, string, error) {
	return nil, "", errNoLobbies
}
func (noLobbies) Update(context.Context, string, lobby.Patch) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) Close(context.Context, string) (lobby.Lobby, bool, error) {
	return lobby.Lobby{}, false, errNoLobbies
}
func (noLobbies) Invite(context.Context, string, lobby.InviteRequest) (lobby.InviteResult, error) {
	return lobby.InviteResult{}, errNoLobbies
}
func (noLobbies) Kick(context.Context, string, uint32, string) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) SetRoster(context.Context, string, lobby.Roster) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) Launch(context.Context, string, lobby.LaunchRequest) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) FlipTeams(context.Context, string) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) Shuffle(context.Context, string) (lobby.Lobby, error) {
	return lobby.Lobby{}, errNoLobbies
}
func (noLobbies) Events(context.Context, string, int64, int) ([]lobby.Envelope, error) {
	return nil, errNoLobbies
}
