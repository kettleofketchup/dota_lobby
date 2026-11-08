package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

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
}

// maxListLimit caps GET /v1/lobbies?limit=.
const maxListLimit = 200

// --- requests ---

// nameOrNumber is a game mode or region: a configured name or a wire
// number, given as a JSON string or number.
type nameOrNumber string

func (n *nameOrNumber) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*n = nameOrNumber(s)
		return nil
	}
	var v uint32
	if err := json.Unmarshal(b, &v); err != nil {
		return errors.New("must be a name or a non-negative 32-bit number")
	}
	*n = nameOrNumber(strconv.FormatUint(uint64(v), 10))
	return nil
}

type playerReq struct {
	AccountID *uint32 `json:"account_id"`
	SteamID   *string `json:"steam_id"`
	Captain   bool    `json:"captain"`
}

type teamReq struct {
	TeamRef string      `json:"team_ref"`
	Name    string      `json:"name"`
	Players []playerReq `json:"players"`
}

type teamsReq struct {
	Radiant teamReq `json:"radiant"`
	Dire    teamReq `json:"dire"`
}

type createReq struct {
	ExternalRef      string            `json:"external_ref"`
	Name             string            `json:"name"`
	Password         string            `json:"password"`
	LeagueID         uint32            `json:"league_id"`
	LeagueNodeID     uint32            `json:"league_node_id"`
	GameMode         nameOrNumber      `json:"game_mode"`
	Region           nameOrNumber      `json:"region"`
	TVDelaySeconds   uint32            `json:"tv_delay_seconds"`
	AllowCheats      bool              `json:"allow_cheats"`
	Visibility       string            `json:"visibility"`
	Launch           string            `json:"launch"`
	ReadyCheck       bool              `json:"ready_check"`
	AllowShortHanded bool              `json:"allow_short_handed"`
	KickStrangers    bool              `json:"kick_strangers"`
	Admins           []uint32          `json:"admins"`
	Teams            *teamsReq         `json:"teams"`
	Metadata         map[string]string `json:"metadata"`
}

type patchReq struct {
	Name             *string            `json:"name"`
	Password         *string            `json:"password"`
	GameMode         *nameOrNumber      `json:"game_mode"`
	Region           *nameOrNumber      `json:"region"`
	TVDelaySeconds   *uint32            `json:"tv_delay_seconds"`
	Visibility       *string            `json:"visibility"`
	AllowCheats      *bool              `json:"allow_cheats"`
	Launch           *string            `json:"launch"`
	ReadyCheck       *bool              `json:"ready_check"`
	AllowShortHanded *bool              `json:"allow_short_handed"`
	KickStrangers    *bool              `json:"kick_strangers"`
	Admins           *[]uint32          `json:"admins"`
	Metadata         *map[string]string `json:"metadata"`
}

type inviteReq struct {
	Players []uint32 `json:"players"`
	Missing bool     `json:"missing"`
}

type kickReq struct {
	AccountID uint32 `json:"account_id"`
	From      string `json:"from"`
}

type rosterReq struct {
	Teams *teamsReq `json:"teams"`
}

type launchReq struct {
	Force bool `json:"force"`
}

// requestError is a 4xx found while reading a request.
type requestError struct {
	status int
	code   string
	msg    string
}

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{http.StatusBadRequest, "invalid_request", fmt.Sprintf(format, args...)}
}

// decodeBody reads a JSON body into v: application/json only, no unknown
// fields, one value. With optional, an empty body leaves v as it is.
func decodeBody(r *http.Request, v any, optional bool) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &requestError{http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("the body is larger than %d bytes", tooBig.Limit)}
		}
		return badRequest("could not read the body")
	}
	if optional && len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &requestError{http.StatusUnsupportedMediaType, "unsupported_media_type", "send the body as Content-Type: application/json"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON: %s", jsonProblem(err))
	}
	if dec.More() {
		return badRequest("invalid JSON: more than one value")
	}
	return nil
}

// jsonProblem describes a decode error without echoing the body.
func jsonProblem(err error) string {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &typeErr):
		return fmt.Sprintf("%s has the wrong type (want %s)", typeErr.Field, typeErr.Type)
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("syntax error at byte %d", syntaxErr.Offset)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	case errors.Is(err, io.EOF):
		return "empty body"
	}
	// Errors from nameOrNumber come without a field name.
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && strings.HasPrefix(msg, "json: ") {
		msg = msg[i+2:]
	}
	return msg
}

// individualSteamID reports whether a SteamID64 is an individual account
// in the public universe, the only kind a player can be.
func individualSteamID(id uint64) bool { return id>>32 == gc.SteamID64(0)>>32 }

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
			field := fmt.Sprintf("teams.%s.players[%d]", side, i)
			var account uint32
			switch {
			case p.AccountID != nil && p.SteamID != nil:
				return r, badRequest("%s: give account_id or steam_id, not both", field)
			case p.AccountID != nil:
				account = *p.AccountID
			case p.SteamID != nil:
				id, err := strconv.ParseUint(*p.SteamID, 10, 64)
				if err != nil || !individualSteamID(id) {
					return r, badRequest("%s.steam_id must be an individual account's SteamID64 as a string", field)
				}
				account = gc.AccountID(id)
			default:
				return r, badRequest("%s: account_id or steam_id is required", field)
			}
			if account == 0 {
				return r, badRequest("%s: account 0 is not a player", field)
			}
			out.Players = append(out.Players, lobby.Player{AccountID: account, Captain: p.Captain})
		}
	}
	return r, nil
}

func (c createReq) spec() (lobby.Spec, error) {
	if c.Teams == nil {
		return lobby.Spec{}, badRequest("teams is required (players may be empty for an open lobby)")
	}
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

func (p patchReq) patch() lobby.Patch {
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

// --- responses ---

type hostSeatView struct {
	Strategy  *string `json:"strategy"`
	Confirmed bool    `json:"confirmed"`
	Team      *string `json:"team"`
}

type playerView struct {
	AccountID       uint32     `json:"account_id"`
	SteamID         string     `json:"steam_id"`
	Captain         bool       `json:"captain"`
	Status          string     `json:"status"`
	Seat            *string    `json:"seat"`
	Ready           bool       `json:"ready"`
	InvitesSent     int        `json:"invites_sent"`
	LastInviteAt    *time.Time `json:"last_invite_at"`
	InviteConfirmed bool       `json:"invite_confirmed"`
}

type teamView struct {
	TeamRef *string      `json:"team_ref"`
	Name    *string      `json:"name"`
	Players []playerView `json:"players"`
}

type memberView struct {
	AccountID uint32 `json:"account_id"`
	Seat      string `json:"seat"`
}

type matchView struct {
	MatchID         *string    `json:"match_id"`
	Outcome         *int32     `json:"outcome"`
	OutcomeName     *string    `json:"outcome_name"`
	WinnerSide      *string    `json:"winner_side"`
	WinnerTeamRef   *string    `json:"winner_team_ref"`
	Scored          *bool      `json:"scored"`
	DurationSeconds *uint32    `json:"duration_seconds"`
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
}

type lobbyView struct {
	ID             string          `json:"id"`
	ExternalRef    string          `json:"external_ref"`
	State          lobby.State     `json:"state"`
	StateReason    *string         `json:"state_reason"`
	Seq            int64           `json:"seq"`
	Bot            *string         `json:"bot"`
	GCLobbyID      *string         `json:"gc_lobby_id"`
	LeagueID       *uint32         `json:"league_id"`
	LeagueNodeID   *uint32         `json:"league_node_id"`
	LeagueVerified *bool           `json:"league_verified"`
	Name           string          `json:"name"`
	Password       json.RawMessage `json:"password,omitempty"`
	GameMode       json.RawMessage `json:"game_mode"`
	Region         json.RawMessage `json:"region"`
	TVDelaySeconds *uint32         `json:"tv_delay_seconds"`
	Visibility     string          `json:"visibility"`
	AllowCheats    bool            `json:"allow_cheats"`
	Launch         string          `json:"launch"`
	ReadyCheck     bool            `json:"ready_check"`
	ShortHanded    bool            `json:"allow_short_handed"`
	KickStrangers  bool            `json:"kick_strangers"`
	Admins         []uint32        `json:"admins"`
	HostSeat       hostSeatView    `json:"host_seat"`
	Teams          struct {
		Radiant teamView `json:"radiant"`
		Dire    teamView `json:"dire"`
	} `json:"teams"`
	Strangers      []memberView      `json:"strangers"`
	PendingInvites []string          `json:"pending_invites"`
	Match          matchView         `json:"match"`
	LaunchAttempts int               `json:"launch_attempts"`
	Metadata       map[string]string `json:"metadata"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	ExpiresAt      *time.Time        `json:"expires_at"`
	EndedAt        *time.Time        `json:"ended_at"`
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

// nameJSON shows a game mode or region as given: a number stays a number.
func nameJSON(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	if _, err := strconv.ParseUint(s, 10, 32); err == nil {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return b
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

// lobbyView builds the API form of a lobby. withPassword is set only for
// the create and GET responses; no other response carries the password.
func (s *Server) lobbyView(l lobby.Lobby, withPassword bool) lobbyView {
	sp := l.Spec
	v := lobbyView{
		ID: l.ID, ExternalRef: sp.ExternalRef, State: l.State, StateReason: nonEmpty(l.StateReason), Seq: l.Seq,
		Bot: nonEmpty(l.Bot), GCLobbyID: idString(l.GCLobbyID), LeagueID: nonZero(sp.LeagueID),
		LeagueNodeID: nonZero(sp.LeagueNodeID), LeagueVerified: l.LeagueVerified, Name: sp.Name,
		GameMode: nameJSON(sp.GameMode), Region: nameJSON(sp.Region), TVDelaySeconds: nonZero(sp.TVDelaySeconds),
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
	if withPassword {
		v.Password = json.RawMessage("null")
		if sp.Password.IsSet() {
			v.Password, _ = json.Marshal(sp.Password.Reveal())
		}
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

// --- errors ---

// writeLobbyError maps lobby errors to the error envelope. Messages of
// 5xx errors are generic: the cause goes to the log.
func (s *Server) writeLobbyError(w http.ResponseWriter, r *http.Request, err error) {
	var reqErr *requestError
	var unknown *lobby.UnknownNameError
	var limited *lobby.InviteLimitedError
	msg := strings.TrimPrefix(err.Error(), "lobby: ")
	switch {
	case errors.As(err, &reqErr):
		writeError(w, reqErr.status, reqErr.code, reqErr.msg)
	case errors.As(err, &unknown):
		writeError(w, http.StatusUnprocessableEntity, "unknown_"+unknown.Field, fmt.Sprintf("%s %q is not configured (lobby.%ss) and is not a number", unknown.Field, unknown.Value, unknown.Field))
	case errors.As(err, &limited):
		if limited.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(limited.RetryAfter.Seconds()))))
		}
		writeError(w, http.StatusTooManyRequests, "invite_limited", msg)
	case errors.Is(err, lobby.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(msg, "invalid request: "))
	case errors.Is(err, lobby.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no lobby with that id")
	case errors.Is(err, lobby.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "external_ref is used by an active lobby with a different request")
	case errors.Is(err, lobby.ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid_state", strings.TrimPrefix(msg, "not allowed in the lobby's current state: "))
	case errors.Is(err, lobby.ErrNotReady):
		writeError(w, http.StatusConflict, "not_ready", strings.TrimPrefix(msg, "roster not complete or not seated: "))
	case errors.Is(err, lobby.ErrHostNotSeated):
		writeError(w, http.StatusConflict, "host_not_seated", "the host seat could not be confirmed; the bot is still on a team")
	case errors.Is(err, lobby.ErrLeagueNotHeld):
		writeError(w, http.StatusUnprocessableEntity, "league_not_held", strings.TrimPrefix(msg, "no configured bot administers the league: "))
	case errors.Is(err, lobby.ErrGCTimeout):
		s.log.Warn("lobby request timed out", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusGatewayTimeout, "gc_timeout", "the game coordinator did not answer in time")
	default:
		s.log.Error("lobby request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the lobby service cannot do that right now; try again shortly")
	}
}

// --- handlers ---

// gcContext bounds a request that waits on GC calls, so it answers before
// the server's write timeout.
func (s *Server) gcContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.opts.GCRequestTimeout)
}

// writeLobby answers with one lobby. Only the create and GET responses
// carry the password.
func (s *Server) writeLobby(w http.ResponseWriter, status int, l lobby.Lobby, withPassword bool) {
	writeJSON(w, status, s.lobbyView(l, withPassword))
}

func (s *Server) handleLobbyCreate(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := decodeBody(r, &req, false); err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	spec, err := req.spec()
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	l, created, err := s.opts.Lobbies.Create(r.Context(), spec)
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/lobbies/"+l.ID)
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	s.writeLobby(w, status, l, true)
}

func (s *Server) handleLobbyList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := lobby.Filter{ExternalRef: q.Get("external_ref"), Cursor: q.Get("cursor")}
	if raw := q.Get("state"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			st := lobby.State(strings.TrimSpace(part))
			if !st.Valid() {
				s.writeLobbyError(w, r, badRequest("state %q is not a lobby state", part))
				return
			}
			f.States = append(f.States, st)
		}
	}
	if raw := q.Get("active"); raw != "" {
		active, err := strconv.ParseBool(raw)
		if err != nil {
			s.writeLobbyError(w, r, badRequest("active must be true or false"))
			return
		}
		f.Active = active
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			s.writeLobbyError(w, r, badRequest("limit must be 1-%d", maxListLimit))
			return
		}
		f.Limit = n
	}
	ls, next, err := s.opts.Lobbies.List(r.Context(), f)
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	out := make([]lobbyView, len(ls))
	for i, l := range ls {
		out[i] = s.lobbyView(l, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"lobbies": out, "next_cursor": nonEmpty(next)})
}

func (s *Server) handleLobbyGet(w http.ResponseWriter, r *http.Request) {
	l, err := s.opts.Lobbies.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusOK, l, true)
}

func (s *Server) handleLobbyPatch(w http.ResponseWriter, r *http.Request) {
	var req patchReq
	if err := decodeBody(r, &req, false); err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	ctx, cancel := s.gcContext(r)
	defer cancel()
	l, err := s.opts.Lobbies.Update(ctx, r.PathValue("id"), req.patch())
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusOK, l, false)
}

func (s *Server) handleLobbyDelete(w http.ResponseWriter, r *http.Request) {
	l, changed, err := s.opts.Lobbies.Close(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	status := http.StatusOK
	if changed {
		status = http.StatusAccepted
	}
	s.writeLobby(w, status, l, false)
}

func (s *Server) handleLobbyInvite(w http.ResponseWriter, r *http.Request) {
	var req inviteReq
	if err := decodeBody(r, &req, false); err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	if req.Missing == (len(req.Players) > 0) {
		s.writeLobbyError(w, r, badRequest(`give either "players" or "missing": true`))
		return
	}
	ctx, cancel := s.gcContext(r)
	defer cancel()
	res, err := s.opts.Lobbies.Invite(ctx, r.PathValue("id"), lobby.InviteRequest{Players: req.Players, Missing: req.Missing})
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	type skip struct {
		AccountID uint32 `json:"account_id"`
		Reason    string `json:"reason"`
	}
	skipped := make([]skip, len(res.Skipped))
	for i, sk := range res.Skipped {
		skipped[i] = skip{sk.AccountID, sk.Reason}
	}
	writeJSON(w, http.StatusOK, map[string]any{"invited": res.Invited, "skipped": skipped})
}

func (s *Server) handleLobbyKick(w http.ResponseWriter, r *http.Request) {
	var req kickReq
	if err := decodeBody(r, &req, false); err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	if req.AccountID == 0 {
		s.writeLobbyError(w, r, badRequest("account_id is required"))
		return
	}
	if req.From == "" {
		req.From = "team"
	}
	ctx, cancel := s.gcContext(r)
	defer cancel()
	l, err := s.opts.Lobbies.Kick(ctx, r.PathValue("id"), req.AccountID, req.From)
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusAccepted, l, false)
}

func (s *Server) handleLobbyRoster(w http.ResponseWriter, r *http.Request) {
	var req rosterReq
	if err := decodeBody(r, &req, false); err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	if req.Teams == nil {
		s.writeLobbyError(w, r, badRequest("teams is required"))
		return
	}
	roster, err := req.Teams.roster()
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	ctx, cancel := s.gcContext(r)
	defer cancel()
	l, err := s.opts.Lobbies.SetRoster(ctx, r.PathValue("id"), roster)
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusOK, l, false)
}

func (s *Server) handleLobbyLaunch(w http.ResponseWriter, r *http.Request) {
	var req launchReq
	if err := decodeBody(r, &req, true); err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	l, err := s.opts.Lobbies.Launch(r.Context(), r.PathValue("id"), lobby.LaunchRequest{Force: req.Force, Trigger: lobby.TriggerAPI})
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusAccepted, l, false)
}

func (s *Server) handleLobbyFlip(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.gcContext(r)
	defer cancel()
	l, err := s.opts.Lobbies.FlipTeams(ctx, r.PathValue("id"))
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusAccepted, l, false)
}

func (s *Server) handleLobbyShuffle(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.gcContext(r)
	defer cancel()
	l, err := s.opts.Lobbies.Shuffle(ctx, r.PathValue("id"))
	if err != nil {
		s.writeLobbyError(w, r, err)
		return
	}
	s.writeLobby(w, http.StatusAccepted, l, false)
}

// noLobbies stands in when no lobby manager is wired.
type noLobbies struct{}

var errNoLobbies = fmt.Errorf("%w: no lobby manager", lobby.ErrUnavailable)

func (noLobbies) Create(context.Context, lobby.Spec) (lobby.Lobby, bool, error) {
	return lobby.Lobby{}, false, errNoLobbies
}
func (noLobbies) Get(context.Context, string) (lobby.Lobby, error) { return lobby.Lobby{}, errNoLobbies }
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
