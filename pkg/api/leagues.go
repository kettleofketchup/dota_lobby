package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

type leagueBotView struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
	// RefreshedAt is when the GC list was fetched; null for the config
	// source.
	RefreshedAt *time.Time `json:"refreshed_at"`
	// Stale means the GC list comes from the database and has not been
	// fetched again since the service started.
	Stale bool `json:"stale"`
}

type leagueView struct {
	LeagueID uint32          `json:"league_id"`
	Name     *string         `json:"name"`
	Bots     []leagueBotView `json:"bots"`
}

type leagueNodeView struct {
	NodeID    uint32  `json:"node_id"`
	Name      *string `json:"name"`
	GroupName *string `json:"group_name"`
	TeamID1   *uint32 `json:"team_id_1"`
	TeamID2   *uint32 `json:"team_id_2"`
}

func nonZero(v uint32) *uint32 {
	if v == 0 {
		return nil
	}
	return &v
}

func (s *Server) handleLeagues(w http.ResponseWriter, _ *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{"leagues": out})
}

// handleLeagueNodes asks a ready bot that holds the league for its lobby
// nodes. The whole call, time queued behind the bot's other GC work
// included, is bounded by GCRequestTimeout.
func (s *Server) handleLeagueNodes(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	id, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "league id must be a positive 32-bit integer, got "+strconv.Quote(raw))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.GCRequestTimeout)
	defer cancel()
	name, nodes, err := s.opts.Leagues.Nodes(ctx, uint32(id))
	switch {
	case errors.Is(err, league.ErrUnknownLeague):
		writeError(w, http.StatusNotFound, "not_found", "no configured bot holds league "+raw)
		return
	case errors.Is(err, league.ErrNoReadyHolder), errors.Is(err, bot.ErrNotReady), errors.Is(err, bot.ErrStopped):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no ready bot holds league "+raw+"; try again shortly")
		return
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("league nodes request timed out", "league", id, "bot", name, "err", err)
		writeError(w, http.StatusGatewayTimeout, "gc_timeout", "the game coordinator did not answer in time")
		return
	case err != nil:
		s.log.Error("league nodes request failed", "league", id, "bot", name, "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the game coordinator request failed; try again shortly")
		return
	}
	out := make([]leagueNodeView, len(nodes))
	for i, n := range nodes {
		out[i] = leagueNodeView{
			NodeID: n.NodeID, Name: nonEmpty(n.Name), GroupName: nonEmpty(n.GroupName),
			TeamID1: nonZero(n.TeamID1), TeamID2: nonZero(n.TeamID2),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"league_id": id, "bot": name, "nodes": out})
}
