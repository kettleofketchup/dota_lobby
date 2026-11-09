package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

// retryTimeout bounds how long a retry request waits for a bot's run loop.
const retryTimeout = 5 * time.Second

// botView is one bot in /v1/bots. 64-bit IDs are strings, and
// unknown values are null rather than zero. Credentials never appear.
type botView struct {
	Name     string    `json:"name"`
	Username string    `json:"username"`
	State    bot.State `json:"state"`
	Ready    bool      `json:"ready"`
	Reason   *string   `json:"reason" nullable:"true"`
	// LastError is the last Steam EResult name, e.g. "LoggedInElsewhere".
	LastError *string `json:"last_error" nullable:"true"`
	// LastErrorMessage is the last error text, which may have no EResult.
	LastErrorMessage *string    `json:"last_error_message" nullable:"true"`
	Since            time.Time  `json:"since"`
	SteamID          *string    `json:"steam_id" nullable:"true"`
	Limited          bool       `json:"limited"`
	LobbyStale       bool       `json:"lobby_stale"`
	TokenExpiresAt   *time.Time `json:"token_expires_at" nullable:"true"`
	// RenewBlocked is why background token renewal stopped, e.g.
	// "bad_credentials"; the bot's connection is unaffected.
	RenewBlocked    *string `json:"renew_blocked" nullable:"true"`
	Attempt         int     `json:"attempt"`
	GCQueuePosition *int32  `json:"gc_queue_position" nullable:"true"`
	// ReadySince is when the bot entered gc_ready, or null.
	ReadySince *time.Time `json:"ready_since" nullable:"true"`
	// Leagues are the league IDs the bot may host, never null.
	Leagues []uint32 `json:"leagues"`
	// Lobby is the ID of the lobby the bot hosts, or null.
	Lobby *string `json:"lobby" nullable:"true"`
}

func newBotView(st bot.Status) botView {
	v := botView{
		Name:             st.Name,
		Username:         st.Username,
		State:            st.State,
		Ready:            st.Ready,
		Reason:           nonEmpty(st.Reason),
		LastError:        nonEmpty(st.LastResult),
		LastErrorMessage: nonEmpty(st.LastError),
		RenewBlocked:     nonEmpty(st.RenewBlocked),
		Since:            st.Since.UTC(),
		Limited:          st.Limited,
		LobbyStale:       st.LobbyStale,
		Attempt:          st.Attempt,
	}
	if st.SteamID != 0 {
		id := strconv.FormatUint(st.SteamID, 10)
		v.SteamID = &id
	}
	if !st.TokenExpiresAt.IsZero() {
		t := st.TokenExpiresAt.UTC()
		v.TokenExpiresAt = &t
	}
	if st.State == bot.StateGCQueued {
		q := st.QueuePosition
		v.GCQueuePosition = &q
	}
	if !st.ReadySince.IsZero() {
		t := st.ReadySince.UTC()
		v.ReadySince = &t
	}
	return v
}

// botView builds one bot's view with its leagues and lobby.
func (s *Server) botView(st bot.Status, leases map[string]string) botView {
	v := newBotView(st)
	v.Leagues = s.opts.Leagues.BotLeagues(st.Name)
	if v.Leagues == nil {
		v.Leagues = []uint32{}
	}
	for name, owner := range leases {
		if strings.EqualFold(name, st.Name) {
			v.Lobby = &owner
			break
		}
	}
	return v
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type botList struct {
	Bots []botView `json:"bots" doc:"Every bot, in config order"`
}

type botsOutput struct{ Body botList }

type botNameInput struct {
	Name string `path:"name" doc:"The bot's name from the config, matched case-insensitively" example:"league_bot_1"`
}

type botOutput struct{ Body botView }

type retryAccepted struct {
	Status string `json:"status" enum:"retry_requested" doc:"Always retry_requested"`
}

type retryOutput struct{ Body retryAccepted }

func (s *Server) registerBots(r *router) {
	register(s, r, huma.Operation{
		OperationID: "list-bots", Method: http.MethodGet, Path: "/v1/bots",
		Summary: "List the bots", Description: "Every bot and its state. Credentials never appear.",
		Tags: []string{"Bots"}, Security: keyAuth, Errors: []int{http.StatusUnauthorized},
	}, s.listBots)
	register(s, r, huma.Operation{
		OperationID: "get-bot", Method: http.MethodGet, Path: "/v1/bots/{name}",
		Summary: "Get a bot", Tags: []string{"Bots"}, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, s.getBot)
	register(s, r, huma.Operation{
		OperationID: "retry-bot", Method: http.MethodPost, Path: "/v1/bots/{name}/retry",
		Summary: "Make a waiting bot try again now",
		Description: "The way out of needs_attention, e.g. after `dota_lobby auth --bot <name> --force` stored a new token " +
			"or a password was fixed; also cuts a backoff or cooldown short. A no-op for a bot that is not waiting. " +
			"503 when the bot's run loop is too backed up to take the request.",
		Tags: []string{"Bots"}, Security: keyAuth, DefaultStatus: http.StatusAccepted,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusServiceUnavailable},
	}, s.retryBot)
	register(s, r, huma.Operation{
		OperationID: "get-bot-leagues", Method: http.MethodGet, Path: "/v1/bots/{name}/leagues",
		Summary: "Get a bot's leagues",
		Description: "The leagues the bot may host, where each came from and when it first appeared, the outcome of " +
			"the bot's last league list refresh, and its recent league changes.",
		Tags: []string{"Leagues"}, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, s.getBotLeagues)
	register(s, r, huma.Operation{
		OperationID: "refresh-bot-leagues", Method: http.MethodPost, Path: "/v1/bots/{name}/leagues/refresh",
		Summary: "Refresh a bot's leagues now",
		Description: "Fetches the bot's admin league list from the game coordinator now, through its GC worker and " +
			"bounded by gc.request_timeout, and answers the updated leagues as GET /v1/bots/{name}/leagues does. " +
			"Use it after an organisation admin added the bot to a league, instead of waiting for " +
			"league.refresh_interval. 503 when the bot is not gc_ready or the request failed (the previous list " +
			"stays), 504 when the game coordinator did not answer in time, 409 when league.sources does not include gc.",
		Tags: []string{"Leagues"}, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout},
	}, s.refreshBotLeagues)
}

type botLeagueView struct {
	LeagueID    uint32     `json:"league_id"`
	Name        *string    `json:"name" nullable:"true" doc:"The league's name from the game coordinator, or null when unknown"`
	Source      string     `json:"source" enum:"gc,config" doc:"gc: the game coordinator's admin league list; config: the bot's configured leagues, unverified"`
	Verified    bool       `json:"verified" doc:"True when the game coordinator's list names the league"`
	FirstSeenAt *time.Time `json:"first_seen_at" nullable:"true" doc:"When the league first appeared in the bot's GC list; null for the config source"`
	RefreshedAt *time.Time `json:"refreshed_at" nullable:"true" doc:"When the bot's GC list was last fetched; null for the config source"`
	Stale       bool       `json:"stale" doc:"True when the GC list comes from the database and has not been fetched since the service started"`
}

type leagueRefreshStatus struct {
	OKAt    *time.Time `json:"ok_at" nullable:"true" doc:"The last successful fetch of the bot's GC league list, or null if none"`
	Error   *string    `json:"error" nullable:"true" doc:"Why the last fetch failed, or null when none failed since ok_at"`
	ErrorAt *time.Time `json:"error_at" nullable:"true" doc:"When that fetch failed"`
}

type leagueChangeView struct {
	LeagueID uint32    `json:"league_id"`
	Change   string    `json:"change" enum:"added,removed" doc:"added: the league appeared in the bot's GC list; removed: it disappeared"`
	At       time.Time `json:"at" doc:"The refresh that saw the change"`
}

type botLeagues struct {
	Bot     string              `json:"bot"`
	Leagues []botLeagueView     `json:"leagues" doc:"The leagues the bot may host, by league ID"`
	Refresh leagueRefreshStatus `json:"refresh" doc:"The outcome of the bot's last league list refresh"`
	Changes []leagueChangeView  `json:"changes" doc:"The most recent league changes, newest first"`
}

type botLeaguesOutput struct{ Body botLeagues }

func (s *Server) getBotLeagues(_ context.Context, in *botNameInput) (*botLeaguesOutput, error) {
	inv, ok := s.opts.Leagues.Inventory(in.Name)
	if !ok {
		return nil, coded(http.StatusNotFound, "not_found", "no bot named "+strconv.Quote(in.Name))
	}
	return &botLeaguesOutput{Body: newBotLeagues(inv)}, nil
}

// refreshBotLeagues fetches one bot's GC league list now, the queue wait
// included bounded by GCRequestTimeout.
func (s *Server) refreshBotLeagues(ctx context.Context, in *botNameInput) (*botLeaguesOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.GCRequestTimeout)
	defer cancel()
	q := strconv.Quote(in.Name)
	switch err := s.opts.Leagues.Refresh(ctx, in.Name); {
	case errors.Is(err, bot.ErrUnknownBot):
		return nil, coded(http.StatusNotFound, "not_found", "no bot named "+q)
	case errors.Is(err, league.ErrGCSourceDisabled):
		return nil, gcSourceDisabled()
	case errors.Is(err, bot.ErrNotReady), errors.Is(err, bot.ErrStopped):
		return nil, coded(http.StatusServiceUnavailable, "unavailable", "bot "+q+" is not gc_ready; try again once it is")
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("league list refresh timed out", "bot", in.Name, "err", err)
		return nil, coded(http.StatusGatewayTimeout, "gc_timeout", "the game coordinator did not answer in time")
	case err != nil:
		s.log.Error("league list refresh failed", "bot", in.Name, "err", err)
		return nil, coded(http.StatusServiceUnavailable, "unavailable", "the game coordinator request failed; try again shortly")
	}
	inv, ok := s.opts.Leagues.Inventory(in.Name)
	if !ok {
		return nil, coded(http.StatusNotFound, "not_found", "no bot named "+q)
	}
	s.log.Info("league list refreshed on request", "bot", inv.Bot)
	return &botLeaguesOutput{Body: newBotLeagues(inv)}, nil
}

func newBotLeagues(inv league.Inventory) botLeagues {
	out := botLeagues{
		Bot:     inv.Bot,
		Leagues: make([]botLeagueView, len(inv.Leagues)),
		Refresh: leagueRefreshStatus{OKAt: utcOrNil(inv.OKAt), Error: nonEmpty(inv.Error), ErrorAt: utcOrNil(inv.ErrorAt)},
		Changes: make([]leagueChangeView, len(inv.Changes)),
	}
	for i, l := range inv.Leagues {
		out.Leagues[i] = botLeagueView{
			LeagueID: l.LeagueID, Name: nonEmpty(l.Name), Source: l.Source, Verified: l.Verified,
			FirstSeenAt: utcOrNil(l.FirstSeenAt), RefreshedAt: utcOrNil(l.RefreshedAt), Stale: l.Stale,
		}
	}
	for i, c := range inv.Changes {
		out.Changes[i] = leagueChangeView{LeagueID: c.LeagueID, Change: c.Kind, At: c.At.UTC()}
	}
	return out
}

func (s *Server) listBots(context.Context, *struct{}) (*botsOutput, error) {
	sts := s.opts.Bots.Statuses()
	leases := s.opts.Leases.Leases()
	out := make([]botView, len(sts))
	for i, st := range sts {
		out[i] = s.botView(st, leases)
	}
	return &botsOutput{Body: botList{Bots: out}}, nil
}

func (s *Server) getBot(_ context.Context, in *botNameInput) (*botOutput, error) {
	for _, st := range s.opts.Bots.Statuses() {
		if strings.EqualFold(st.Name, in.Name) {
			return &botOutput{Body: s.botView(st, s.opts.Leases.Leases())}, nil
		}
	}
	return nil, coded(http.StatusNotFound, "not_found", "no bot named "+strconv.Quote(in.Name))
}

func (s *Server) retryBot(ctx context.Context, in *botNameInput) (*retryOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()
	switch err := s.opts.Bots.Retry(ctx, in.Name); {
	case errors.Is(err, bot.ErrUnknownBot):
		return nil, coded(http.StatusNotFound, "not_found", "no bot named "+strconv.Quote(in.Name))
	case err != nil:
		s.log.Error("bot retry not accepted", "bot", in.Name, "err", err)
		return nil, coded(http.StatusServiceUnavailable, "unavailable", "bot "+strconv.Quote(in.Name)+" is not accepting requests; try again shortly")
	}
	s.log.Info("bot retry requested", "bot", in.Name)
	return &retryOutput{Body: retryAccepted{Status: "retry_requested"}}, nil
}
