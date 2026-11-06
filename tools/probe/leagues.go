package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/paralin/go-dota2/protocol"
)

// cmdLogin connects with the stored token and reports how far it got (Q1).
// With --renew it also tries CM token renewal once (Q12).
func cmdLogin(ctx context.Context, c *config, rep *reporter) error {
	s := newSession(c, rep)
	var rn *renewer
	if c.renew {
		rn = newRenewer(s.sc)
	}
	defer s.close()
	if err := s.connect(ctx); err != nil {
		return err
	}
	admin, known := s.isLeagueAdmin()
	rep.emit("Q2", "game_account_client", map[string]any{"is_league_admin": admin, "cache_has_object": known})

	if rn != nil {
		token, err := loadToken(c.stateDir)
		if err != nil {
			return err
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		newToken, res, err := rn.renew(rctx, token, s.sc.SteamId().ToUint64())
		fields := map[string]any{"eresult": res.String()}
		if err != nil {
			fields["error"] = err.Error()
		} else {
			fields["issued_new_token"] = newToken != token
			if newToken != token {
				fields["new_claims"] = claimsSummary(newToken)
				if err := saveToken(c.stateDir, newToken); err != nil {
					return err
				}
			}
		}
		rep.emit("Q12", "renew_result", fields)
	}
	return nil
}

// cmdLeagues asks the GC which leagues this account administers, three ways,
// so one run answers both "does 7633 still work" (Q2) and "does 7634 echo our
// job ID, i.e. will MakeRequest on master ever complete" (Q18).
func cmdLeagues(ctx context.Context, c *config, rep *reporter) error {
	s := newSession(c, rep)
	defer s.close()
	if err := s.connect(ctx); err != nil {
		return err
	}
	admin, known := s.isLeagueAdmin()
	rep.emit("Q2", "game_account_client", map[string]any{"is_league_admin": admin, "cache_has_object": known})

	// 1. go-dota2 MakeRequest: completes only if the reply echoes the job ID.
	var leagues []*protocol.CMsgDOTALeagueInfo
	{
		rctx, cancel := context.WithTimeout(ctx, c.replyTimeout)
		resp := &protocol.CMsgDOTALeagueInfoList{}
		start := time.Now()
		err := s.d.MakeRequest(rctx,
			uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest),
			&protocol.CMsgDOTALeagueInfoListAdminsRequest{},
			uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsReponse),
			resp)
		cancel()
		fields := map[string]any{"elapsed_ms": time.Since(start).Milliseconds(), "completed": err == nil}
		if err != nil {
			fields["error"] = err.Error()
		} else {
			leagues = resp.GetInfos()
			fields["count"] = len(leagues)
		}
		rep.emit("Q18", "make_request_7633", fields)
	}

	// 2 and 3. Raw sends, with and without a source job, watching every 7634.
	for _, withJob := range []bool{true, false} {
		got, err := s.rawLeagueList(ctx, withJob)
		if err != nil {
			return err
		}
		if leagues == nil && got != nil {
			leagues = got
		}
	}

	for _, l := range leagues {
		rep.emit("Q2", "league", map[string]any{
			"league_id": l.GetLeagueId(), "name": l.GetName(), "tier": l.GetTier().String(),
			"region": l.GetRegion().String(), "status": l.GetStatus(),
			"start": l.GetStartTimestamp(), "end": l.GetEndTimestamp(),
		})
		nctx, cancel := context.WithTimeout(ctx, c.replyTimeout)
		nodes, err := s.d.RequestLeagueAvailableLobbyNodes(nctx, l.GetLeagueId())
		cancel()
		if err != nil {
			rep.emit("Q2", "league_nodes_failed", map[string]any{"league_id": l.GetLeagueId(), "error": err.Error()})
			continue
		}
		for _, n := range nodes.GetNodeInfos() {
			rep.emit("Q2", "league_node", map[string]any{
				"league_id": l.GetLeagueId(), "node_id": n.GetNodeId(), "node_name": n.GetNodeName(),
				"group": n.GetNodeGroupName(), "team_1": n.GetTeamId_1(), "team_2": n.GetTeamId_2(),
			})
		}
	}
	rep.emit("Q2", "league_summary", map[string]any{"count": len(leagues)})
	return nil
}

// rawLeagueList sends 7633 itself and waits for any 7634, reporting whether
// its target job matched.
func (s *session) rawLeagueList(ctx context.Context, withJob bool) ([]*protocol.CMsgDOTALeagueInfo, error) {
	replies, stop := s.watch(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsReponse)
	defer stop()
	job := s.send(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest, &protocol.CMsgDOTALeagueInfoListAdminsRequest{}, withJob)

	start := time.Now()
	timer := time.NewTimer(s.cfg.replyTimeout)
	defer timer.Stop()
	select {
	case p := <-replies:
		resp := &protocol.CMsgDOTALeagueInfoList{}
		if err := resp.UnmarshalVT(p.Body); err != nil {
			return nil, fmt.Errorf("decode 7634: %w", err)
		}
		s.rep.emit("Q18", "raw_7633_reply", map[string]any{
			"with_job": withJob, "source_job": job, "target_job": uint64(p.TargetJobId),
			"job_echoed": withJob && uint64(p.TargetJobId) == job,
			"elapsed_ms": time.Since(start).Milliseconds(), "count": len(resp.GetInfos()),
		})
		return resp.GetInfos(), nil
	case <-timer.C:
		s.rep.emit("Q18", "raw_7633_no_reply", map[string]any{"with_job": withJob, "timeout": s.cfg.replyTimeout.String()})
		return nil, nil
	case <-ctx.Done():
		return nil, errors.Join(errors.New("raw 7633"), ctx.Err())
	}
}
