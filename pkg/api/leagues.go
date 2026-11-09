package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
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

type leagueView struct {
	LeagueID uint32          `json:"league_id"`
	Name     *string         `json:"name" nullable:"true"`
	Bots     []leagueBotView `json:"bots"`
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

type leagueNodesInput struct {
	ID uint32 `path:"id" minimum:"1" doc:"The league ID" example:"17929"`
}

type leagueNodes struct {
	LeagueID uint32           `json:"league_id"`
	Bot      string           `json:"bot" doc:"The bot that asked the game coordinator"`
	Nodes    []leagueNodeView `json:"nodes"`
}

type leagueNodesOutput struct{ Body leagueNodes }

func (s *Server) registerLeagues(r *router) {
	register(s, r, huma.Operation{
		OperationID: "list-leagues", Method: http.MethodGet, Path: "/v1/leagues",
		Summary: "List the leagues", Description: "Every league a bot may host, and which bots hold it.",
		Tags: []string{"Leagues"}, Security: keyAuth, Errors: []int{http.StatusUnauthorized},
	}, s.listLeagues)
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
	out := make([]leagueView, len(all))
	for i, l := range all {
		v := leagueView{LeagueID: l.LeagueID, Name: nonEmpty(l.Name), Bots: make([]leagueBotView, len(l.Bots))}
		for j, b := range l.Bots {
			bv := leagueBotView{Name: b.Name, Source: b.Source, Verified: b.Verified, Stale: b.Stale}
			if !b.RefreshedAt.IsZero() {
				t := b.RefreshedAt.UTC()
				bv.RefreshedAt = &t
			}
			v.Bots[j] = bv
		}
		out[i] = v
	}
	return &leaguesOutput{Body: leagueList{Leagues: out}}, nil
}

// listLeagueNodes asks a ready bot that holds the league for its lobby
// nodes. The whole call, time queued behind the bot's other GC work
// included, is bounded by GCRequestTimeout.
func (s *Server) listLeagueNodes(ctx context.Context, in *leagueNodesInput) (*leagueNodesOutput, error) {
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
