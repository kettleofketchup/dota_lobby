// Package lobby runs the lobbies the API asks for. A Manager accepts a
// request as a pending lobby, leases a bot that holds its league from the
// pool, has that bot create the lobby, and then a tracker per lobby
// follows the bot's lobby snapshot until the match ends or the lobby is
// closed. Every state change is stored with the events that report it, so
// a restart re-adopts lobbies from the database.
//
// The transitions are computed by the pure derive function from full
// snapshots, so a dropped SO event is harmless: the next snapshot carries
// the same information. See docs/research/06-match-results.md and
// docs/research/11-draftforge-integration.md.
package lobby

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// State is a lobby's lifecycle state.
type State string

// Lobby states. finished, failed, closed and timed_out are terminal.
const (
	StatePending   State = "pending"
	StateCreating  State = "creating"
	StateWaiting   State = "waiting"
	StateReady     State = "ready"
	StateLaunching State = "launching"
	StateInGame    State = "in_game"
	StatePostGame  State = "post_game"
	StateFinished  State = "finished"
	StateFailed    State = "failed"
	StateClosing   State = "closing"
	StateClosed    State = "closed"
	StateTimedOut  State = "timed_out"
)

// Terminal reports whether the lobby has ended.
func (s State) Terminal() bool {
	switch s {
	case StateFinished, StateFailed, StateClosed, StateTimedOut:
		return true
	}
	return false
}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateCreating, StateWaiting, StateReady, StateLaunching, StateInGame,
		StatePostGame, StateFinished, StateFailed, StateClosing, StateClosed, StateTimedOut:
		return true
	}
	return false
}

// Side is the side a rostered player belongs on.
type Side string

// Sides.
const (
	Radiant Side = "radiant"
	Dire    Side = "dire"
)

func (s Side) team() gc.Team {
	if s == Dire {
		return gc.TeamDire
	}
	return gc.TeamRadiant
}

// Launch modes.
const (
	LaunchManual = "manual"
	LaunchAuto   = "auto"
)

// Visibilities.
const (
	VisibilityPublic   = "public"
	VisibilityFriends  = "friends"
	VisibilityUnlisted = "unlisted"
)

// Launch triggers, as reported in lobby.launching.
const (
	TriggerAPI  = "api"
	TriggerChat = "chat"
	TriggerAuto = "auto"
)

// Host seat strategies, in the order the research ranks them least to
// most reported; the configured list decides the order actually tried.
const (
	SeatCaster     = "caster"
	SeatSelfKick   = "self_kick"
	SeatPlayerPool = "player_pool"
	SeatSpectator  = "spectator"
	SeatNoTeam     = "no_team"
)

// PassKey is a lobby password. It prints, logs and marshals redacted, so
// it cannot leak through a log line, an event or a list response by
// accident; the API reveals it on purpose in single-lobby responses.
type PassKey string

const redacted = "***REDACTED***"

// Reveal returns the password.
func (p PassKey) Reveal() string { return string(p) }

// IsSet reports whether there is a password.
func (p PassKey) IsSet() bool { return p != "" }

// String implements fmt.Stringer.
func (p PassKey) String() string {
	if p == "" {
		return ""
	}
	return redacted
}

// GoString implements fmt.GoStringer, for %#v.
func (p PassKey) GoString() string { return p.String() }

// Format redacts the password for every verb, including %s, %q and %x on
// the underlying string.
func (p PassKey) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(p.String())) }

// LogValue implements slog.LogValuer.
func (p PassKey) LogValue() slog.Value { return slog.StringValue(p.String()) }

// MarshalJSON redacts.
func (p PassKey) MarshalJSON() ([]byte, error) { return []byte(`"` + p.String() + `"`), nil }

// Player is one rostered player and what has been done for them.
type Player struct {
	AccountID uint32
	Captain   bool

	// Ready is the player's own ready flag (lobby chat).
	Ready bool
	// InvitesSent counts invites, the automatic one included. It is
	// written before an invite is sent, so a restart never repeats one.
	InvitesSent  int
	LastInviteAt time.Time
	// InviteConfirmed is set once the GC confirmed an invite (4502).
	InviteConfirmed bool
}

// Team is one side's roster.
type Team struct {
	TeamRef string
	Name    string
	Players []Player
}

// Roster is both sides. An empty roster makes an open lobby: anyone may
// sit anywhere.
type Roster struct {
	Radiant Team
	Dire    Team
}

// Empty reports whether no player is rostered.
func (r Roster) Empty() bool { return len(r.Radiant.Players) == 0 && len(r.Dire.Players) == 0 }

// Side returns the side of a rostered account.
func (r Roster) Side(account uint32) (Side, bool) {
	for _, p := range r.Radiant.Players {
		if p.AccountID == account {
			return Radiant, true
		}
	}
	for _, p := range r.Dire.Players {
		if p.AccountID == account {
			return Dire, true
		}
	}
	return "", false
}

// Team returns one side's roster.
func (r *Roster) Team(s Side) *Team {
	if s == Dire {
		return &r.Dire
	}
	return &r.Radiant
}

// Player returns a pointer to a rostered player, or nil.
func (r *Roster) Player(account uint32) *Player {
	for _, t := range []*Team{&r.Radiant, &r.Dire} {
		for i := range t.Players {
			if t.Players[i].AccountID == account {
				return &t.Players[i]
			}
		}
	}
	return nil
}

// All returns every rostered player with their side, Radiant first.
func (r Roster) All() []RosterEntry {
	out := make([]RosterEntry, 0, len(r.Radiant.Players)+len(r.Dire.Players))
	for _, p := range r.Radiant.Players {
		out = append(out, RosterEntry{Side: Radiant, Player: p})
	}
	for _, p := range r.Dire.Players {
		out = append(out, RosterEntry{Side: Dire, Player: p})
	}
	return out
}

// RosterEntry is a player with their side.
type RosterEntry struct {
	Side Side
	Player
}

func (r Roster) clone() Roster {
	r.Radiant.Players = slices.Clone(r.Radiant.Players)
	r.Dire.Players = slices.Clone(r.Dire.Players)
	return r
}

// Spec is what a lobby was asked to be: the create request, as PATCH has
// changed it since. GameMode and Region are names from lobby.game_modes
// and lobby.regions, or decimal wire numbers.
type Spec struct {
	ExternalRef      string
	Name             string
	Password         PassKey
	LeagueID         uint32
	LeagueNodeID     uint32
	GameMode         string
	Region           string
	TVDelaySeconds   uint32 // 0 = the GC default
	AllowCheats      bool
	Visibility       string
	Launch           string
	ReadyCheck       bool
	AllowShortHanded bool
	KickStrangers    bool
	Admins           []uint32
	Metadata         map[string]string
	Teams            Roster
}

func (s Spec) clone() Spec {
	s.Admins = slices.Clone(s.Admins)
	if s.Metadata != nil {
		m := make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			m[k] = v
		}
		s.Metadata = m
	}
	s.Teams = s.Teams.clone()
	return s
}

// HostSeat records how the host got out of a player slot.
type HostSeat struct {
	// Strategy is the strategy that took effect, or the last one tried.
	Strategy string `json:"strategy,omitempty"`
	// Confirmed means the lobby snapshot showed the host where Strategy
	// puts it.
	Confirmed bool `json:"confirmed"`
	// Team is the host's team as last seen ("player_pool", ...), empty
	// when unknown.
	Team string `json:"team,omitempty"`
	// Next is the index in lobby.host_seat_strategy the next attempt
	// starts from; a failed launch moves it on.
	Next int `json:"next"`
}

// Match is what the lobby reported about its match.
type Match struct {
	MatchID uint64 `json:"match_id,omitempty"`
	// Outcome is the raw EMatchOutcome, nil until the lobby reports one.
	Outcome   *int32    `json:"outcome,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// ServerAt is when the lobby first left UI for a server state after
	// the current launch; RunAt is when it first reached RUN.
	ServerAt time.Time `json:"server_at,omitzero"`
	RunAt    time.Time `json:"run_at,omitzero"`
	// RadiantTeamRef and DireTeamRef are the side mapping at launch.
	RadiantTeamRef string `json:"radiant_team_ref,omitempty"`
	DireTeamRef    string `json:"dire_team_ref,omitempty"`
	// HostSeatStrategy is the host seat strategy of the launch that
	// started the match.
	HostSeatStrategy string `json:"host_seat_strategy,omitempty"`
	// StaleMatchID is the match_id the lobby still showed when a launch
	// failed. Whether the GC clears it is not known, so a later launch does
	// not take it for a new match.
	StaleMatchID uint64 `json:"stale_match_id,omitempty"`
}

// DurationSeconds is the time from RUN to the outcome, as the lobby's state
// changes show it; nil when either is unknown. It is an approximation:
// the match's own duration comes from match details.
func (m Match) DurationSeconds() *uint32 {
	if m.RunAt.IsZero() || m.EndedAt.IsZero() || m.EndedAt.Before(m.RunAt) {
		return nil
	}
	d := uint32(m.EndedAt.Sub(m.RunAt) / time.Second)
	return &d
}

// WinnerSide is "radiant" or "dire" for outcomes 2 and 3, else "".
func (m Match) WinnerSide() Side {
	if m.Outcome == nil {
		return ""
	}
	switch *m.Outcome {
	case 2:
		return Radiant
	case 3:
		return Dire
	}
	return ""
}

// WinnerTeamRef maps the winning side through the side mapping at launch.
func (m Match) WinnerTeamRef() string {
	switch m.WinnerSide() {
	case Radiant:
		return m.RadiantTeamRef
	case Dire:
		return m.DireTeamRef
	}
	return ""
}

// Scored reports whether the outcome counts (2..13); nil when no outcome
// is known or it is not one the research classifies.
func (m Match) Scored() *bool {
	if m.Outcome == nil || *m.Outcome == 0 {
		return nil
	}
	o := *m.Outcome
	var v bool
	switch {
	case o >= 2 && o <= 13:
		v = true
	case o >= 64 && o <= 69:
		v = false
	default:
		return nil
	}
	return &v
}

// outcomeNames are the EMatchOutcome names (research 06 §1.4).
var outcomeNames = map[int32]string{
	0:  "unknown",
	2:  "radiant_victory",
	3:  "dire_victory",
	4:  "neutral_victory",
	5:  "no_team_wins",
	6:  "custom1_victory",
	7:  "custom2_victory",
	8:  "custom3_victory",
	9:  "custom4_victory",
	10: "custom5_victory",
	11: "custom6_victory",
	12: "custom7_victory",
	13: "custom8_victory",
	64: "not_scored_poor_network",
	65: "not_scored_leaver",
	66: "not_scored_server_crash",
	67: "not_scored_never_started",
	68: "not_scored_canceled",
	69: "not_scored_suspicious",
}

// OutcomeName names a raw EMatchOutcome, "outcome_<n>" when unknown.
func OutcomeName(o int32) string {
	if n, ok := outcomeNames[o]; ok {
		return n
	}
	return fmt.Sprintf("outcome_%d", o)
}

// ScoredOutcome reports whether o ends a match with a result: 2..13.
func ScoredOutcome(o int32) bool { return o >= 2 && o <= 13 }

// Member is one lobby member as the API shows it.
type Member struct {
	AccountID uint32
	// Seat is the member's team ("radiant", "player_pool", ...) or
	// "unknown" when the snapshot omitted it.
	Seat  string
	Coach bool
}

// Live is what the bot's lobby snapshot shows now. It is not stored.
type Live struct {
	Members []Member
	// Pending is pending_invites, SteamID64s.
	Pending []uint64
	// Seen lists everyone who was a member while this process tracked the
	// lobby, so a player who left can be told from one never seen.
	Seen []uint32
}

// Lobby is one lobby with everything the service knows about it.
type Lobby struct {
	ID          string
	State       State
	StateReason string
	StateSince  time.Time
	Seq         int64
	Version     int64
	Bot         string
	GCLobbyID   uint64
	// LeagueVerified is whether the GC confirmed the bot holds the league,
	// nil until a bot is chosen.
	LeagueVerified *bool
	Spec           Spec
	// Details is the full set last sent to the GC, password included.
	Details        gc.LobbyDetails
	HostSeat       HostSeat
	LaunchAttempts int
	Match          Match
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
	EndedAt        time.Time
	// Live is nil when there is no snapshot (pending, ended, bot away).
	Live *Live
}

func (l Lobby) clone() Lobby {
	l.Spec = l.Spec.clone()
	if l.LeagueVerified != nil {
		v := *l.LeagueVerified
		l.LeagueVerified = &v
	}
	if l.Match.Outcome != nil {
		o := *l.Match.Outcome
		l.Match.Outcome = &o
	}
	if l.Live != nil {
		live := *l.Live
		live.Members = slices.Clone(live.Members)
		live.Pending = slices.Clone(live.Pending)
		live.Seen = slices.Clone(live.Seen)
		l.Live = &live
	}
	return l
}

// Member returns the live member with an account ID.
func (l Lobby) Member(account uint32) (Member, bool) {
	if l.Live == nil {
		return Member{}, false
	}
	for _, m := range l.Live.Members {
		if m.AccountID == account {
			return m, true
		}
	}
	return Member{}, false
}

// Player statuses, as the API shows them.
const (
	StatusPending = "pending"
	StatusInvited = "invited"
	StatusJoined  = "joined"
	StatusSeated  = "seated"
	StatusLeft    = "left"
)

// PlayerStatus is a rostered player's status and seat ("" for none).
func (l Lobby) PlayerStatus(e RosterEntry) (status, seat string) {
	if m, ok := l.Member(e.AccountID); ok {
		if m.Seat == string(e.Side) {
			return StatusSeated, m.Seat
		}
		return StatusJoined, m.Seat
	}
	if l.Live != nil && slices.Contains(l.Live.Seen, e.AccountID) {
		return StatusLeft, ""
	}
	if e.InvitesSent > 0 || (l.Live != nil && slices.Contains(l.Live.Pending, gc.SteamID64(e.AccountID))) {
		return StatusInvited, ""
	}
	return StatusPending, ""
}

// Strangers are members who are neither rostered nor the host.
func (l Lobby) Strangers(host uint32) []Member {
	if l.Live == nil {
		return nil
	}
	var out []Member
	for _, m := range l.Live.Members {
		if m.AccountID == host {
			continue
		}
		if _, ok := l.Spec.Teams.Side(m.AccountID); !ok {
			out = append(out, m)
		}
	}
	return out
}

// Event is one stored lobby event.
type Event struct {
	ID         string
	LobbyID    string
	Seq        int64
	Type       string
	OccurredAt time.Time
	// Data never holds the password.
	Data map[string]any
}

// Hooks hears about every event after it is stored. OnEvent must not
// block; it runs on the goroutine that made the change.
type Hooks interface {
	OnEvent(l Lobby, ev Event)
}

// Errors the API maps to status codes.
var (
	ErrNotFound      = errors.New("lobby: not found")
	ErrConflict      = errors.New("lobby: external_ref is used by an active lobby with a different request")
	ErrInvalidState  = errors.New("lobby: not allowed in the lobby's current state")
	ErrNotReady      = errors.New("lobby: roster not complete or not seated")
	ErrHostNotSeated = errors.New("lobby: the host seat could not be confirmed")
	ErrLeagueNotHeld = errors.New("lobby: no configured bot administers the league")
	ErrUnknownName   = errors.New("lobby: unknown name")
	ErrInvalid       = errors.New("lobby: invalid request")
	ErrUnavailable   = errors.New("lobby: unavailable")
	ErrGCTimeout     = errors.New("lobby: game coordinator request timed out")
)

// InviteLimitedError means every requested invite was rate-limited.
type InviteLimitedError struct {
	// RetryAfter is when the soonest one may be sent; 0 when every player
	// reached lobby.invite.max_per_player.
	RetryAfter time.Duration
}

func (e *InviteLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("lobby: invites limited; retry after %s", e.RetryAfter.Round(time.Second))
	}
	return "lobby: every player reached the invite limit"
}

// LogValue keeps slog from reflecting into the lobby, whose Details carry
// the password.
func (l Lobby) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", l.ID),
		slog.String("external_ref", l.Spec.ExternalRef),
		slog.String("state", string(l.State)),
		slog.String("bot", l.Bot),
		slog.Uint64("gc_lobby_id", l.GCLobbyID),
	)
}
