package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/paralin/go-dota2/cso"
	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-dota2/socache"
	"github.com/paralin/go-steam/steamid"
)

// tvDelaySeconds is the current Valve numbering of LobbyDotaTVDelay
// (GameTracking c592bf50, 2026-08-12). The vendored enum constants are stale,
// so the probe sends and reads raw wire values (plan D6).
var tvDelaySeconds = map[int32]int{0: 10, 1: 60, 2: 120, 3: 300, 4: 900}

// cmdCreate creates one lobby and records every signal the GC gives back:
// the 7055 reply, popups, and the lobby SO read-back (Q4, Q6, Q7, Q19).
func cmdCreate(ctx context.Context, c *config, rep *reporter) error {
	s := newSession(c, rep)
	defer s.close()
	if err := s.connect(ctx); err != nil {
		return err
	}
	if err := s.leaveCurrentLobby(ctx); err != nil {
		return err
	}

	details := &protocol.CMsgPracticeLobbySetDetails{
		GameName:        new(c.lobbyName),
		PassKey:         new(c.lobbyPass),
		ServerRegion:    new(uint32(c.region)),
		AllowSpectating: new(true),
		Visibility:      protocol.DOTALobbyVisibility_DOTALobbyVisibility_Unlisted.Enum(),
	}
	if c.leagueID != 0 {
		details.Leagueid = new(uint32(c.leagueID))
	}
	if c.nodeID != 0 {
		details.LeagueNodeId = new(uint32(c.nodeID))
	}
	if c.tvWire >= 0 {
		details.DotaTvDelay = protocol.LobbyDotaTVDelay(c.tvWire).Enum()
	}

	lobbyEvents, unsub, err := s.d.GetCache().SubscribeType(cso.Lobby)
	if err != nil {
		return fmt.Errorf("subscribe lobby: %w", err)
	}
	defer unsub()
	replies, stop := s.watch(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyResponse)
	defer stop()

	job := s.send(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyCreate,
		&protocol.CMsgPracticeLobbyCreate{PassKey: details.PassKey, LobbyDetails: details}, !c.noJob)
	rep.emit("Q7", "create_sent", map[string]any{
		"league_id": c.leagueID, "node_id": c.nodeID, "tv_wire": c.tvWire, "source_job": job,
	})

	// Collect until we have both the 7055 and a lobby, then a short grace
	// period for late popups, or until the window closes.
	start := time.Now()
	window := time.NewTimer(c.createWindow)
	defer window.Stop()
	var grace <-chan time.Time
	var lobby *protocol.CSODOTALobby
	gotReply := false
	for done := false; !done; {
		select {
		case p := <-replies:
			gotReply = true
			r := &protocol.CMsgGenericResult{}
			if err := r.UnmarshalVT(p.Body); err != nil {
				rep.emit("Q7", "reply_7055_undecodable", map[string]any{"error": err.Error()})
			} else {
				rep.emit("Q7", "reply_7055", map[string]any{
					"eresult": r.GetEresult(), "ok": r.GetEresult() == 1, "debug_message": r.GetDebugMessage(),
					"source_job": job, "target_job": uint64(p.TargetJobId),
					"job_echoed": job != 0 && uint64(p.TargetJobId) == job,
					"elapsed_ms": time.Since(start).Milliseconds(),
				})
			}
		case ev := <-lobbyEvents:
			if l, ok := ev.Object.(*protocol.CSODOTALobby); ok && ev.EventType != socache.EventTypeDestroy {
				if lobby == nil {
					rep.emit("Q7", "lobby_so_created", map[string]any{"elapsed_ms": time.Since(start).Milliseconds()})
				}
				lobby = l
			}
		case <-s.popups:
			// Already reported by the event loop; seeing one ends the wait sooner.
		case err := <-s.failed:
			return err
		case <-grace:
			done = true
		case <-window.C:
			done = true
		case <-ctx.Done():
			return ctx.Err()
		}
		if grace == nil && gotReply && lobby != nil {
			grace = time.After(3 * time.Second)
		}
	}
	if !gotReply {
		rep.emit("Q7", "no_7055", map[string]any{"source_job": job, "window": c.createWindow.String()})
	}

	if lobby == nil {
		rep.emit("Q4", "readback", map[string]any{"lobby": false, "requested_league": c.leagueID})
		return nil
	}
	s.reportLobby("Q4", "readback", lobby)
	verdict := "no_league_requested"
	if c.leagueID != 0 {
		verdict = "accepted"
		if lobby.GetLeagueid() != uint32(c.leagueID) {
			verdict = "rejected"
		}
	}
	tv := int32(lobby.GetDotaTvDelay())
	rep.emit("Q4", "league_verdict", map[string]any{
		"requested": c.leagueID, "read_back": lobby.GetLeagueid(), "verdict": verdict,
	})
	rep.emit("Q6", "tv_delay_readback", map[string]any{
		"sent_wire": c.tvWire, "read_wire": tv, "read_seconds": tvDelaySeconds[tv], "league": lobby.GetLeagueid() != 0,
	})

	if c.hold {
		return s.console(ctx, lobbyEvents)
	}
	return s.leaveCurrentLobby(ctx)
}

// reportLobby logs the fields the plan relies on and captures the raw SO.
func (s *session) reportLobby(q, event string, l *protocol.CSODOTALobby) {
	raw, _ := l.MarshalVT()
	members := make([]map[string]any, 0, len(l.GetAllMembers()))
	for _, m := range l.GetAllMembers() {
		members = append(members, map[string]any{
			"steam_id": m.GetId(), "team": m.GetTeam().String(), "slot": m.GetSlot(), "self": m.GetId() == s.sc.SteamId().ToUint64(),
		})
	}
	s.rep.emit(q, event, map[string]any{
		"lobby":          true,
		"lobby_id":       l.GetLobbyId(),
		"state":          l.GetState().String(),
		"game_state":     l.GetGameState().String(),
		"lobby_type":     l.GetLobbyType().String(),
		"leader_id":      l.GetLeaderId(),
		"leagueid":       l.GetLeagueid(),
		"league_node_id": l.GetLeagueNodeId(),
		"dota_tv_delay":  int32(l.GetDotaTvDelay()),
		"match_id":       l.GetMatchId(),
		"match_outcome":  l.GetMatchOutcome().String(),
		"server_id":      l.GetServerId(),
		"members":        members,
		"capture":        s.capture("so", 2004, raw),
	})
}

// leaveCurrentLobby destroys (if leader) and leaves any lobby the bot is in,
// then waits for the lobby SO to go away.
func (s *session) leaveCurrentLobby(ctx context.Context) error {
	l := s.currentLobby()
	if l == nil {
		return nil
	}
	s.rep.emit("", "leaving_old_lobby", map[string]any{"lobby_id": l.GetLobbyId(), "state": l.GetState().String()})
	events, unsub, err := s.d.GetCache().SubscribeType(cso.Lobby)
	if err != nil {
		return fmt.Errorf("subscribe lobby: %w", err)
	}
	defer unsub()

	if l.GetLeaderId() == s.sc.SteamId().ToUint64() {
		dctx, cancel := context.WithTimeout(ctx, s.cfg.replyTimeout)
		resp, err := s.d.DestroyLobby(dctx)
		cancel()
		fields := map[string]any{}
		if err != nil {
			fields["error"] = err.Error()
		} else {
			fields["result"] = resp.GetResult().String()
		}
		s.rep.emit("Q18", "destroy_lobby", fields)
	}
	if l.GetState() != protocol.CSODOTALobby_UI {
		s.d.AbandonLobby()
	}
	s.d.LeaveLobby()

	deadline := time.After(15 * time.Second)
	for s.currentLobby() != nil {
		select {
		case <-events:
		case <-deadline:
			return fmt.Errorf("lobby %d still present after leave", l.GetLobbyId())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.rep.emit("", "left_lobby", map[string]any{"lobby_id": l.GetLobbyId()})
	return nil
}

const consoleHelp = `commands:
  dump                      print the lobby
  invite <steamid64>        invite a player
  team <radiant|dire> <n>   move the bot into a player slot
  seat <strategy>           self_kick | player_pool | spectator | caster | unassigned (Q5)
  tv <wire>                 set dota_tv_delay raw wire value (Q6)
  launch                    launch with an empty body (nonce check)
  leave | destroy           leave the lobby / destroy and leave
  quit                      leave and exit`

// console keeps the lobby open for manual experiments (Q5, Q8) and logs
// every lobby state change while it runs.
func (s *session) console(ctx context.Context, lobbyEvents <-chan *socache.CacheEvent) error {
	go s.watchLobby(ctx, lobbyEvents)
	fmt.Fprintln(os.Stderr, consoleHelp)
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		fmt.Fprint(os.Stderr, "probe> ")
		var line string
		var ok bool
		select {
		case line, ok = <-lines:
			if !ok {
				return s.leaveCurrentLobby(ctx)
			}
		case err := <-s.failed:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if err := s.consoleCmd(ctx, f); err != nil {
			if err == errQuit {
				return s.leaveCurrentLobby(ctx)
			}
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

var errQuit = fmt.Errorf("quit")

func (s *session) consoleCmd(ctx context.Context, f []string) error {
	lobby := s.currentLobby()
	switch f[0] {
	case "help":
		fmt.Fprintln(os.Stderr, consoleHelp)
	case "dump":
		if lobby == nil {
			return fmt.Errorf("not in a lobby")
		}
		s.reportLobby("", "lobby_dump", lobby)
	case "invite":
		if len(f) != 2 {
			return fmt.Errorf("usage: invite <steamid64>")
		}
		id, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return fmt.Errorf("bad steam id: %w", err)
		}
		s.d.InviteLobbyMember(steamid.SteamId(id))
		s.rep.emit("", "invite_sent", map[string]any{"steam_id": id})
	case "team":
		if len(f) != 3 {
			return fmt.Errorf("usage: team <radiant|dire> <slot>")
		}
		team := protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_GOOD_GUYS
		if f[1] == "dire" {
			team = protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_BAD_GUYS
		}
		slot, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			return fmt.Errorf("bad slot: %w", err)
		}
		s.d.JoinLobbyTeam(team, uint32(slot))
	case "seat":
		if len(f) != 2 {
			return fmt.Errorf("usage: seat <strategy>")
		}
		switch f[1] {
		case "self_kick":
			s.d.KickLobbyMemberFromTeam(s.sc.SteamId().GetAccountId())
		case "player_pool":
			s.d.JoinLobbyTeam(protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_PLAYER_POOL, 0)
		case "spectator":
			s.d.JoinLobbyTeam(protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_SPECTATOR, 0)
		case "unassigned":
			s.d.JoinLobbyTeam(protocol.DOTA_GC_TEAM_DOTA_GC_TEAM_NOTEAM, 0)
		case "caster":
			s.d.JoinLobbyBroadcastChannel(0, "", "", "")
		default:
			return fmt.Errorf("unknown strategy %q", f[1])
		}
		s.rep.emit("Q5", "seat_requested", map[string]any{"strategy": f[1]})
		time.Sleep(2 * time.Second)
		if l := s.currentLobby(); l != nil {
			s.reportLobby("Q5", "seat_result", l)
		}
	case "tv":
		if len(f) != 2 || lobby == nil {
			return fmt.Errorf("usage: tv <wire> (in a lobby)")
		}
		w, err := strconv.ParseInt(f[1], 10, 32)
		if err != nil {
			return fmt.Errorf("bad wire value: %w", err)
		}
		s.d.SetLobbyDetails(&protocol.CMsgPracticeLobbySetDetails{
			LobbyId:     new(lobby.GetLobbyId()),
			DotaTvDelay: protocol.LobbyDotaTVDelay(int32(w)).Enum(),
		})
		s.rep.emit("Q6", "tv_set_sent", map[string]any{"wire": w})
	case "launch":
		s.d.LaunchLobby()
		s.rep.emit("Q5", "launch_sent", map[string]any{"empty_body": true})
	case "leave":
		s.d.LeaveLobby()
	case "destroy":
		dctx, cancel := context.WithTimeout(ctx, s.cfg.replyTimeout)
		defer cancel()
		resp, err := s.d.DestroyLobby(dctx)
		if err != nil {
			return fmt.Errorf("destroy: %w", err)
		}
		s.rep.emit("", "destroy_lobby", map[string]any{"result": resp.GetResult().String()})
	case "quit", "exit":
		return errQuit
	default:
		return fmt.Errorf("unknown command %q (try help)", f[0])
	}
	return nil
}

// watchLobby reports each change to the fields that drive the lifecycle
// state machine, plus the final snapshot when the lobby goes away (Q8).
func (s *session) watchLobby(ctx context.Context, events <-chan *socache.CacheEvent) {
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			l, isLobby := ev.Object.(*protocol.CSODOTALobby)
			if !isLobby {
				continue
			}
			if ev.EventType == socache.EventTypeDestroy {
				s.reportLobby("Q8", "lobby_destroyed", l)
				continue
			}
			key := fmt.Sprintf("%s|%s|%d|%s|%d|%d", l.GetState(), l.GetGameState(), l.GetMatchId(),
				l.GetMatchOutcome(), l.GetLeagueid(), len(l.GetAllMembers()))
			if key != last {
				last = key
				s.reportLobby("Q8", "lobby_changed", l)
			}
		}
	}
}
