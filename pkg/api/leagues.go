package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
	"github.com/kettleofketchup/dota_lobby/pkg/pool"
)

type leagueBotView struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
	// RefreshedAt is when the GC list was fetched; null for the config
	// source.
	RefreshedAt *time.Time `json:"refreshed_at" nullable:"true"`
	// Stale means the GC list comes from the database and has not been
	// fetched again since the service started.
	Stale bool `json:"stale"`
}

// leagueHolderView is one bot that holds a league, with what decides
// whether it can take a lobby for it now.
type leagueHolderView struct {
	Bot      string `json:"bot" doc:"The bot's name from the config"`
	Source   string `json:"source" enum:"gc,config" doc:"gc: the game coordinator's admin league list; config: the bot's configured leagues, unverified"`
	Verified bool   `json:"verified" doc:"True when the game coordinator's list names the league"`
	// FirstSeenAt and RefreshedAt are null for the config source.
	FirstSeenAt *time.Time `json:"first_seen_at" nullable:"true" doc:"When the league first appeared in the bot's GC list; null for the config source"`
	RefreshedAt *time.Time `json:"refreshed_at" nullable:"true" doc:"When the bot's GC list was last fetched; null for the config source"`
	BotState    bot.State  `json:"bot_state" doc:"The bot's connection state, as in GET /v1/bots"`
	Ready       bool       `json:"ready" doc:"True when the bot is gc_ready"`
	LobbyID     *string    `json:"lobby_id" nullable:"true" doc:"The lobby the bot is hosting, or null when it is not leased"`
}

type leagueView struct {
	LeagueID uint32  `json:"league_id"`
	Name     *string `json:"name" nullable:"true"`
	// Bots is the older, shorter form of Holders, kept for existing
	// clients.
	Bots         []leagueBotView    `json:"bots" doc:"The holders in the original shape; prefer holders"`
	Holders      []leagueHolderView `json:"holders" doc:"Every enabled bot that may host the league, in config order"`
	HoldersTotal int                `json:"holders_total" doc:"How many bots hold the league"`
	HoldersFree  int                `json:"holders_free" doc:"How many holders could take a new lobby now: gc_ready, lobby cache current and not hosting a lobby"`
}

type leagueNodeView struct {
	NodeID    uint32  `json:"node_id"`
	Name      *string `json:"name" nullable:"true"`
	GroupName *string `json:"group_name" nullable:"true"`
	TeamID1   *uint32 `json:"team_id_1" nullable:"true"`
	TeamID2   *uint32 `json:"team_id_2" nullable:"true"`
}

func nonZero(v uint32) *uint32 {
	if v == 0 {
		return nil
	}
	return &v
}

type leagueList struct {
	Leagues []leagueView `json:"leagues" doc:"Every league a bot may host"`
}

type leaguesOutput struct{ Body leagueList }

type leagueIDInput struct {
	ID uint32 `path:"id" minimum:"1" doc:"The league ID" example:"17929"`
}

type leagueOutput struct{ Body leagueView }

type leagueNodes struct {
	LeagueID uint32           `json:"league_id"`
	Bot      string           `json:"bot" doc:"The bot that asked the game coordinator"`
	Nodes    []leagueNodeView `json:"nodes"`
}

type leagueNodesOutput struct{ Body leagueNodes }

func (s *Server) registerLeagues(r *router) {
	register(s, r, huma.Operation{
		OperationID: "list-leagues", Method: http.MethodGet, Path: "/v1/leagues",
		Summary: "List the leagues",
		Description: "Every league a bot may host, which bots hold it, and how many of them could take a lobby now. " +
			"A bot holds a league when the game coordinator's admin league list names it (an organisation admin " +
			"added the bot's account to the league), or, until that list is first fetched or with " +
			"league.allow_unverified, when the bot's configured leagues name it.",
		Tags: []string{"Leagues"}, Security: keyAuth, Errors: []int{http.StatusUnauthorized},
	}, s.listLeagues)
	register(s, r, huma.Operation{
		OperationID: "get-league", Method: http.MethodGet, Path: "/v1/leagues/{id}",
		Summary: "Get a league", Description: "One league in the shape GET /v1/leagues lists. 404 when no enabled bot holds it.",
		Tags: []string{"Leagues"}, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, s.getLeague)
	register(s, r, huma.Operation{
		OperationID: "refresh-leagues", Method: http.MethodPost, Path: "/v1/leagues/refresh",
		Summary: "Refresh every ready bot's leagues",
		Description: "Fetches the game coordinator's admin league list of every gc_ready bot now, a few bots at a time, " +
			"bounded as a whole by gc.request_timeout, and reports each enabled bot's outcome. " +
			"A failed bot keeps its previous list. 409 invalid_state when league.sources does not include gc.",
		Tags: []string{"Leagues"}, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized, http.StatusConflict},
	}, s.refreshLeagues)
	register(s, r, huma.Operation{
		OperationID: "list-league-nodes", Method: http.MethodGet, Path: "/v1/leagues/{id}/nodes",
		Summary:     "List a league's lobby nodes",
		Description: "Asks a ready bot that holds the league for its nodes, bounded by gc.request_timeout.",
		Tags:        []string{"Leagues"}, Security: keyAuth,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
	}, s.listLeagueNodes)
}

func (s *Server) listLeagues(context.Context, *struct{}) (*leaguesOutput, error) {
	all := s.opts.Leagues.All()
	h := s.holderState()
	out := make([]leagueView, len(all))
	for i, l := range all {
		out[i] = h.view(l)
	}
	return &leaguesOutput{Body: leagueList{Leagues: out}}, nil
}

func (s *Server) getLeague(_ context.Context, in *leagueIDInput) (*leagueOutput, error) {
	l, ok := s.opts.Leagues.League(in.ID)
	if !ok {
		return nil, coded(http.StatusNotFound, "not_found", "no configured bot holds league "+strconv.FormatUint(uint64(in.ID), 10))
	}
	return &leagueOutput{Body: s.holderState().view(l)}, nil
}

// holderState is the bots' states and leases, read once per request.
type holderState struct {
	statuses map[string]bot.Status // by lowercased name
	leases   map[string]string     // lowercased bot name -> lobby ID
}

func (s *Server) holderState() holderState {
	h := holderState{statuses: map[string]bot.Status{}, leases: map[string]string{}}
	for _, st := range s.opts.Bots.Statuses() {
		h.statuses[strings.ToLower(st.Name)] = st
	}
	for name, owner := range s.opts.Leases.Leases() {
		h.leases[strings.ToLower(name)] = owner
	}
	return h
}

func (h holderState) view(l league.League) leagueView {
	v := leagueView{
		LeagueID: l.LeagueID, Name: nonEmpty(l.Name),
		Bots:    make([]leagueBotView, len(l.Bots)),
		Holders: make([]leagueHolderView, len(l.Bots)),
	}
	for i, b := range l.Bots {
		v.Bots[i] = leagueBotView{Name: b.Name, Source: b.Source, Verified: b.Verified, Stale: b.Stale, RefreshedAt: utcOrNil(b.RefreshedAt)}
		k := strings.ToLower(b.Name)
		st := h.statuses[k]
		hv := leagueHolderView{
			Bot: b.Name, Source: b.Source, Verified: b.Verified,
			FirstSeenAt: utcOrNil(b.FirstSeenAt), RefreshedAt: utcOrNil(b.RefreshedAt),
			BotState: st.State, Ready: st.Ready,
		}
		if owner, leased := h.leases[k]; leased {
			hv.LobbyID = &owner
		} else if pool.CanHost(st) {
			v.HoldersFree++
		}
		v.Holders[i] = hv
	}
	v.HoldersTotal = len(l.Bots)
	return v
}

func utcOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

type refreshResultView struct {
	Bot     string   `json:"bot"`
	Status  string   `json:"status" enum:"ok,failed,not_ready" doc:"ok: the list was fetched; failed: the request failed and the previous list stays; not_ready: the bot was not gc_ready and was not asked"`
	Error   *string  `json:"error" nullable:"true" doc:"Why the refresh failed; null unless status is failed"`
	Leagues []uint32 `json:"leagues" doc:"The leagues the bot holds after the refresh"`
}

type refreshResults struct {
	Results []refreshResultView `json:"results" doc:"Every enabled bot, in config order"`
}

type refreshResultsOutput struct{ Body refreshResults }

// refreshLeagues refreshes every ready bot's GC league list, the whole
// call bounded by GCRequestTimeout.
func (s *Server) refreshLeagues(ctx context.Context, _ *struct{}) (*refreshResultsOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.GCRequestTimeout)
	defer cancel()
	results, err := s.opts.Leagues.RefreshAll(ctx)
	if errors.Is(err, league.ErrGCSourceDisabled) {
		return nil, gcSourceDisabled()
	}
	if err != nil {
		return nil, fmt.Errorf("refresh leagues: %w", err)
	}
	out := make([]refreshResultView, len(results))
	for i, res := range results {
		v := refreshResultView{Bot: res.Bot, Status: "ok", Leagues: s.opts.Leagues.BotLeagues(res.Bot)}
		switch {
		case res.NotReady:
			v.Status = "not_ready"
		case res.Err != nil:
			v.Status = "failed"
			msg := res.Err.Error()
			if errors.Is(res.Err, context.DeadlineExceeded) {
				msg = "the game coordinator did not answer in time"
			}
			v.Error = &msg
		}
		if v.Leagues == nil {
			v.Leagues = []uint32{}
		}
		out[i] = v
	}
	s.log.Info("league lists refreshed on request", "bots", len(out))
	return &refreshResultsOutput{Body: refreshResults{Results: out}}, nil
}

func gcSourceDisabled() huma.StatusError {
	return coded(http.StatusConflict, "invalid_state", `league.sources does not include "gc"; there is no league list to refresh`)
}

// listLeagueNodes asks a ready bot that holds the league for its lobby
// nodes. The whole call, time queued behind the bot's other GC work
// included, is bounded by GCRequestTimeout.
func (s *Server) listLeagueNodes(ctx context.Context, in *leagueIDInput) (*leagueNodesOutput, error) {
	id := in.ID
	raw := strconv.FormatUint(uint64(id), 10)
	ctx, cancel := context.WithTimeout(ctx, s.opts.GCRequestTimeout)
	defer cancel()
	name, nodes, err := s.opts.Leagues.Nodes(ctx, id)
	switch {
	case errors.Is(err, league.ErrUnknownLeague):
		return nil, coded(http.StatusNotFound, "not_found", "no configured bot holds league "+raw)
	case errors.Is(err, league.ErrNoReadyHolder), errors.Is(err, bot.ErrNotReady), errors.Is(err, bot.ErrStopped):
		return nil, coded(http.StatusServiceUnavailable, "unavailable", "no ready bot holds league "+raw+"; try again shortly")
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("league nodes request timed out", "league", id, "bot", name, "err", err)
		return nil, coded(http.StatusGatewayTimeout, "gc_timeout", "the game coordinator did not answer in time")
	case err != nil:
		s.log.Error("league nodes request failed", "league", id, "bot", name, "err", err)
		return nil, coded(http.StatusServiceUnavailable, "unavailable", "the game coordinator request failed; try again shortly")
	}
	out := make([]leagueNodeView, len(nodes))
	for i, n := range nodes {
		out[i] = leagueNodeView{
			NodeID: n.NodeID, Name: nonEmpty(n.Name), GroupName: nonEmpty(n.GroupName),
			TeamID1: nonZero(n.TeamID1), TeamID2: nonZero(n.TeamID2),
		}
	}
	return &leagueNodesOutput{Body: leagueNodes{LeagueID: id, Bot: name, Nodes: out}}, nil
}
