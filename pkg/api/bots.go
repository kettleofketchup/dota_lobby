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
