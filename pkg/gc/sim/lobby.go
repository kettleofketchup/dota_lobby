package sim

import (
	"fmt"
	"slices"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// teamSize is how many slots Radiant and Dire each have.
const teamSize = 5

// lobby is one simulated CSODOTALobby. It is guarded by the World's lock;
// what leaves it is a copy.
type lobby struct {
	w       *World
	id      uint64
	host    *Client
	created time.Time
	details gc.LobbyDetails
	state   gc.LobbyState
	matchID uint64
	outcome int32
	members []*member
	// pending are invited account IDs that have not joined.
	pending []uint32
	channel uint64
	// chatJoined is set while the host is in the lobby chat.
	chatJoined bool
	chat       []ChatLine
	// readyWaiting are auto-ready lines held until the host joins the chat.
	readyWaiting []uint32
	// launches counts launches, so a scheduled auto start can tell it is
	// stale.
	launches  uint64
	startedAt time.Time
	// actions are the auto-mode actions not run yet; running is set while
	// World.runDue runs them.
	actions []action
	running bool
}

// action is one scheduled auto-mode step of a lobby.
type action struct {
	due time.Time
	run func()
}

type member struct {
	account uint32
	team    gc.Team
	slot    uint32
	persona string
}

func (l *lobby) member(account uint32) *member {
	for _, m := range l.members {
		if m.account == account {
			return m
		}
	}
	return nil
}

func (l *lobby) remove(account uint32) {
	l.members = slices.DeleteFunc(l.members, func(m *member) bool { return m.account == account })
	l.pending = slices.DeleteFunc(l.pending, func(a uint32) bool { return a == account })
}

// seat moves m to team. A Radiant or Dire slot must be free; nil takes the
// first free one. Other teams have no slot limit.
func (l *lobby) seat(m *member, team gc.Team, slot *uint32) error {
	if team != gc.TeamRadiant && team != gc.TeamDire {
		m.team, m.slot = team, 0
		return nil
	}
	taken := map[uint32]bool{}
	for _, o := range l.members {
		if o != m && o.team == team {
			taken[o.slot] = true
		}
	}
	if slot != nil {
		if *slot >= teamSize {
			return fmt.Errorf("%w: slot %d is not 0-%d", ErrSeatTaken, *slot, teamSize-1)
		}
		if taken[*slot] {
			return fmt.Errorf("%w: %s slot %d", ErrSeatTaken, team, *slot)
		}
		m.team, m.slot = team, *slot
		return nil
	}
	for s := range uint32(teamSize) {
		if !taken[s] {
			m.team, m.slot = team, s
			return nil
		}
	}
	return fmt.Errorf("%w: %s is full", ErrSeatTaken, team)
}

// say appends a member's chat line and delivers it to the host when the
// host is in the chat. The host never gets its own lines back.
func (l *lobby) say(m *member, text string) bool {
	delivered := l.chatJoined && m.account != l.host.bot.AccountID
	l.chat = append(l.chat, ChatLine{AccountID: m.account, PersonaName: m.persona, Text: text, At: l.w.opts.Now(), Delivered: delivered})
	if over := len(l.chat) - chatHistory; over > 0 {
		l.chat = slices.Delete(l.chat, 0, over)
	}
	if delivered {
		l.host.chats.publish(gc.ChatMessage{ChannelID: l.channel, AccountID: m.account, PersonaName: m.persona, Text: text})
	}
	return delivered
}

// publish makes a snapshot the host's current lobby and streams it.
func (l *lobby) publish(kind gc.LobbyEventKind) {
	v := l.view()
	c := l.host
	if kind == gc.LobbyDestroyed {
		if c.lobby == l {
			c.lobby, c.current = nil, nil
		}
	} else {
		c.lobby, c.current = l, v
	}
	c.lobbies.publish(gc.LobbyEvent{Kind: kind, Lobby: v})
}

// view is a snapshot as the adapter decodes one from the SO cache.
func (l *lobby) view() *gc.LobbyView {
	d := l.details
	wire, _ := gc.TVDelayToWire(d.TVDelaySeconds)
	v := &gc.LobbyView{
		LobbyID:        l.id,
		LeaderID:       gc.SteamID64(l.host.bot.AccountID),
		MatchID:        l.matchID,
		State:          l.state,
		Outcome:        l.outcome,
		LeagueID:       d.LeagueID,
		LeagueNodeID:   d.LeagueNodeID,
		ServerRegion:   d.ServerRegion,
		GameMode:       d.GameMode,
		GameName:       d.GameName,
		Visibility:     d.Visibility,
		TVDelaySeconds: d.TVDelaySeconds,
		TVDelayWire:    wire,
	}
	if l.state == gc.LobbyRun {
		v.GameState = 5 // DOTA_GAMERULES_STATE_GAME_IN_PROGRESS
	}
	for _, m := range l.members {
		v.Members = append(v.Members, gc.Member{
			SteamID: gc.SteamID64(m.account), Team: m.team, TeamKnown: true, Slot: m.slot,
		})
	}
	for _, a := range l.pending {
		v.Pending = append(v.Pending, gc.SteamID64(a))
	}
	return v
}

func (l *lobby) info() LobbyInfo {
	out := LobbyInfo{
		LobbyID:       l.id,
		Bot:           l.host.bot.Name,
		HostAccountID: l.host.bot.AccountID,
		State:         l.state,
		Details:       l.details,
		MatchID:       l.matchID,
		Outcome:       l.outcome,
		Members:       make([]MemberInfo, 0, len(l.members)),
		Pending:       slices.Clone(l.pending),
		ChatJoined:    l.chatJoined,
		Chat:          slices.Clone(l.chat),
		CreatedAt:     l.created,
	}
	if out.Pending == nil {
		out.Pending = []uint32{}
	}
	if out.Chat == nil {
		out.Chat = []ChatLine{}
	}
	out.Details.LobbyID = l.id
	for _, m := range l.members {
		out.Members = append(out.Members, MemberInfo{
			AccountID: m.account, Team: m.team, Slot: m.slot, PersonaName: m.persona,
			Host: m.account == l.host.bot.AccountID,
		})
	}
	return out
}

// result is the match as MatchDetails reports it once it has ended.
func (l *lobby) result(now time.Time) *gc.MatchResult {
	r := &gc.MatchResult{
		GCResult:  1,
		MatchID:   l.matchID,
		Outcome:   l.outcome,
		StartTime: uint32(l.startedAt.Unix()),
		LeagueID:  l.details.LeagueID,
		GameMode:  int32(l.details.GameMode),
	}
	if d := now.Sub(l.startedAt); d > 0 {
		r.DurationSec = uint32(d / time.Second)
	}
	for _, m := range l.members {
		if m.team != gc.TeamRadiant && m.team != gc.TeamDire {
			continue
		}
		slot := m.slot
		if m.team == gc.TeamDire {
			slot += 128
		}
		r.Players = append(r.Players, gc.MatchPlayer{AccountID: m.account, PlayerSlot: slot})
	}
	return r
}

// flip swaps Radiant and Dire.
func (l *lobby) flip() {
	for _, m := range l.members {
		switch m.team {
		case gc.TeamRadiant:
			m.team = gc.TeamDire
		case gc.TeamDire:
			m.team = gc.TeamRadiant
		}
	}
}

// shuffle deals the seated players to Radiant and Dire in turn, by
// account ID: deterministic, unlike the GC's skill-based shuffle.
func (l *lobby) shuffle() {
	var seated []*member
	for _, m := range l.members {
		if m.team == gc.TeamRadiant || m.team == gc.TeamDire {
			seated = append(seated, m)
		}
	}
	slices.SortFunc(seated, func(a, b *member) int {
		switch {
		case a.account < b.account:
			return -1
		case a.account > b.account:
			return 1
		}
		return 0
	})
	for i, m := range seated {
		m.team, m.slot = gc.TeamRadiant, uint32(i/2)
		if i%2 == 1 {
			m.team = gc.TeamDire
		}
	}
}
