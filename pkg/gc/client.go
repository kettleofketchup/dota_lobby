// Package gc is the seam between the service and Steam/Dota 2. It is the only
// package allowed to import go-steam and go-dota2: everything else
// works with the domain types defined here, so an upstream API break stays
// inside this package.
package gc

import (
	"context"
	"errors"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// Client is one Steam account with its Dota 2 GC session. It is safe for one
// event consumer plus one worker goroutine. Blocking methods take a ctx and
// must never be called from the goroutine that drains Events(), because the
// GC reader blocks until its events are consumed (gap-01 §3).
type Client interface {
	// Connect dials Steam and logs on with creds once the CM handshake is
	// done. It returns at once; the outcome arrives on Events(). ctx bounds
	// the whole connection, not just the dial: cancelling it disconnects.
	// It fails with ErrAlreadyConnected until the previous connection's
	// Disconnected event has been delivered. Successful calls number the
	// connections 1, 2, 3, ...; see Disconnected.Conn.
	Connect(ctx context.Context, creds Credentials) error
	// Authenticate exchanges a password (and Steam Guard code) for a refresh
	// token over the Steam Web API. It opens no CM connection, so it can run
	// while disconnected. Failures from the auth session are *AuthError.
	Authenticate(ctx context.Context, creds Credentials) (string, error)
	// Disconnect ends the current connection. It does not block; a
	// Disconnected event follows. It never logs off.
	Disconnect()
	// Events is the normalized event stream. It is shared by every
	// connection this client makes and is never closed.
	Events() <-chan Event
	// SetPlaying announces (or stops announcing) that Dota 2 is running.
	SetPlaying(playing bool)
	// SayHello sends one GC hello. Repeating it until welcomed is the
	// caller's job.
	SayHello()
	// SteamID is the SteamID64 of the logged-on account, or 0 before the
	// first logon.
	SteamID() uint64
	// AccountFlags reports the EAccountFlags bits from the last logon.
	AccountFlags() (flags uint32, known bool)

	// CurrentLobby returns the lobby in the SO cache, if any. After a GC
	// session loss the cache may be stale until the next welcome.
	CurrentLobby() (*LobbyView, bool)
	// SubscribeLobby streams lobby SO changes across reconnects. Slow
	// readers lose the oldest events; the channel is never closed, so call
	// the returned func to stop.
	SubscribeLobby() (<-chan LobbyEvent, func())
	// CreateLobby leaves any stale lobby, sends a job-addressed create and
	// waits for the lobby to appear in the SO cache. A failed 7055
	// or a NOT_LEAGUE_ADMIN popup returns *CreateError. A lobby may still
	// exist after an error, so callers check CurrentLobby before reuse.
	CreateLobby(ctx context.Context, d LobbyDetails) (*LobbyView, error)
	// SetLobbyDetails sends the full detail set. LobbyID 0 means the current
	// lobby.
	SetLobbyDetails(d LobbyDetails)
	Invite(steamID64 uint64)
	KickFromTeam(accountID uint32)
	Kick(accountID uint32)
	JoinTeam(team Team, slot uint32)
	FlipTeams()
	BalancedShuffle()
	// Launch sends an empty CMsgPracticeLobbyLaunch (no nonce, gap-02).
	Launch()
	Leave()
	// Abandon is only for the experimental in-game delete path (Q14).
	Abandon()
	Destroy(ctx context.Context) error

	// ListAdminLeagues asks the GC which leagues this account administers
	// (7633 -> 7634). If the reply does not echo our job ID, a 7634 seen on
	// the tap in the same window is used instead (Q18).
	ListAdminLeagues(ctx context.Context) ([]LeagueInfo, error)
	// IsLeagueAdmin reads CSODOTAGameAccountClient.is_league_admin. known is
	// false until the GC has sent that object.
	IsLeagueAdmin() (admin, known bool)
	LeagueNodes(ctx context.Context, leagueID uint32) ([]LeagueNode, error)
	MatchDetails(ctx context.Context, matchID uint64) (*MatchResult, error)
}

// Errors returned by Client implementations.
var (
	// ErrNotConnected means there is no Steam connection to act on.
	ErrNotConnected = errors.New("gc: not connected to Steam")
	// ErrAlreadyConnected means Connect was called before the previous
	// connection delivered its Disconnected event.
	ErrAlreadyConnected = errors.New("gc: already connected")
	// ErrNotReady means the GC session is not up (no welcome yet, or lost).
	ErrNotReady = errors.New("gc: game coordinator session not ready")
	// ErrNoCredentials means Credentials lack what the call needs.
	ErrNoCredentials = errors.New("gc: missing credentials")
	// ErrSteamGuardRequired means Steam asked for a guard code that no
	// TwoFactorCode or SteamGuard callback could supply.
	ErrSteamGuardRequired = errors.New("gc: steam guard confirmation required")
)

// Credentials is what Connect and Authenticate need for one account. Secrets
// stay config.Secret so they redact in logs.
type Credentials struct {
	Username string
	// AccessToken is the refresh token from Authenticate. Connect prefers it
	// over Password.
	AccessToken config.Secret
	Password    config.Secret
	// TwoFactorCode is the current TOTP code, computed by the caller just
	// before the call (pkg/auth owns the shared_secret).
	TwoFactorCode string
	// AuthCode is an emailed Steam Guard code, for accounts without TOTP.
	AuthCode string
	// SteamGuard is asked for a code inside the same auth session when Steam
	// wants one that TwoFactorCode/AuthCode did not cover (an operator
	// prompt). Nil fails fast with ErrSteamGuardRequired.
	SteamGuard func(ctx context.Context, kinds []GuardKind) (string, error)
	// DeviceName labels the session in the account's authorized devices.
	DeviceName string
}

// GuardKind names a Steam Guard confirmation type Steam will accept.
type GuardKind string

// Guard kinds, from EAuthSessionGuardType.
const (
	GuardNone               GuardKind = "none"
	GuardEmailCode          GuardKind = "email_code"
	GuardDeviceCode         GuardKind = "device_code"
	GuardDeviceConfirmation GuardKind = "device_confirmation"
	GuardEmailConfirmation  GuardKind = "email_confirmation"
	GuardMachineToken       GuardKind = "machine_token"
	GuardUnknown            GuardKind = "unknown"
)

// AuthError is a failed Steam auth session (Authenticate, or a password
// logon). State is go-steam's AuthSessionState, e.g. "device_code" or
// "denied"; Guards lists what Steam would have accepted. Result is the
// x-eresult Steam's Web API sent with a denial, 0 when there was none.
type AuthError struct {
	State  string
	Guards []GuardKind
	Result EResult
	Err    error
}

func (e *AuthError) Error() string {
	if e.Err != nil {
		return "steam auth: " + e.Err.Error()
	}
	return "steam auth: " + e.State
}

func (e *AuthError) Unwrap() error { return e.Err }

// NeedsGuard reports whether the session failed because Steam wanted a
// Steam Guard code or confirmation it did not get.
func (e *AuthError) NeedsGuard() bool {
	switch e.State {
	case "email_code", "device_code", "manual_confirmation", "unsupported_guard":
		return true
	}
	return errors.Is(e.Err, ErrSteamGuardRequired)
}

// CreateFailure classifies a CreateLobby error.
type CreateFailure string

const (
	// CreateFailed is a job-addressed 7055 with eresult != 1. On its own it
	// is not a league rejection: it is also sent for a stale lobby (#506).
	CreateFailed CreateFailure = "create_failed"
	// CreateLeagueRejected is a NOT_LEAGUE_ADMIN popup inside the create
	// window. The authoritative check is still the leagueid read-back.
	CreateLeagueRejected CreateFailure = "league_rejected"
	// CreateStaleLobby means the previous lobby could not be left.
	CreateStaleLobby CreateFailure = "stale_lobby"
)

// CreateError is returned by CreateLobby when the GC said no.
type CreateError struct {
	Reason       CreateFailure
	EResult      uint32 // 7055 CMsgGenericResult.eresult, 0 if none
	DebugMessage string // 7055 debug_message
	PopupID      int32  // popup id for CreateLeagueRejected
}

func (e *CreateError) Error() string {
	switch {
	case e.EResult != 0 && e.DebugMessage != "":
		return "create lobby: " + string(e.Reason) + " (eresult " + EResult(int32(e.EResult)).String() + ": " + e.DebugMessage + ")"
	case e.EResult != 0:
		return "create lobby: " + string(e.Reason) + " (eresult " + EResult(int32(e.EResult)).String() + ")"
	default:
		return "create lobby: " + string(e.Reason)
	}
}

// Event is one normalized Steam or GC event. The concrete types below are
// the whole set; switch on them.
type Event interface{ isEvent() }

// Connected means the CM handshake finished; logon has been sent.
type Connected struct{}

// LoggedOn means Steam accepted the logon.
type LoggedOn struct {
	SteamID        uint64
	AccountFlags   uint32
	ExtendedResult EResult
}

// Limited reports a limited (no purchase) account, which cannot do some
// things a full account can (invites to non-friends, chat).
func (e LoggedOn) Limited() bool { return e.AccountFlags&AccountFlagLimitedUser != 0 }

// LogOnFailed is a refused logon. For a CM refusal Result is set and a
// Disconnected follows. For a password logon whose auth session failed,
// Result is 0 and Err is an *AuthError.
type LogOnFailed struct {
	Result EResult
	Err    error
}

// LoggedOff means Steam ended the session, e.g. LoggedInElsewhere (6) or
// LogonSessionReplaced (34). go-steam does not treat these specially.
type LoggedOff struct{ Result EResult }

// Disconnected is the last event of every connection, including a failed
// dial. It is the one event that decides reconnects. Conn is
// the number of the connection it ends (the nth successful Connect), so a
// late Disconnected from an abandoned connection can be told from the
// current one's; 0 means unknown.
type Disconnected struct{ Conn uint64 }

// Fatal carries an error go-steam reported. go-steam's event channel cannot
// tell fatal errors from recoverable ones, so this may be either; a fatal one
// is always followed by Disconnected.
type Fatal struct{ Err error }

// SteamFailure is Fail/ServiceUnavailable/TryAnotherCM on logon; a
// disconnect usually follows.
type SteamFailure struct{ Result EResult }

// AccountInfo is the account profile Steam sends after logon.
type AccountInfo struct {
	PersonaName  string
	AccountFlags uint32
}

// Limited reports a limited (no purchase) account.
func (e AccountInfo) Limited() bool { return e.AccountFlags&AccountFlagLimitedUser != 0 }

// GCStatus is a GC connection status change. QueuePosition/QueueSize are set
// while in the logon queue.
type GCStatus struct {
	Old, New      GCConnStatus
	QueuePosition int32
	QueueSize     int32
}

// GCWelcomed means the GC accepted our hello; requests can be sent.
type GCWelcomed struct{}

// PlayingSession says whether another session holds the account's playing
// slot. While Blocked, do not SetPlaying(true) or SayHello: it would kick
// the other session.
type PlayingSession struct {
	Blocked bool
	AppID   uint32
}

// PracticeLobbyResponse is a 7055, decoded as CMsgGenericResult (gap-05).
// EResult 1 is OK. TargetJobID tells which request it answers, if any.
type PracticeLobbyResponse struct {
	EResult      uint32
	DebugMessage string
	TargetJobID  uint64
	Raw          []byte
}

// Popup is a CMsgDOTAPopup. ID 27 is NOT_LEAGUE_ADMIN.
type Popup struct {
	ID         int32
	Name       string
	CustomText string
	IntData    int32
}

// InvitationCreated confirms a lobby invite was sent.
type InvitationCreated struct {
	SteamID uint64
	Offline bool
}

// MatchSignedOut means the GC finished recording a match.
type MatchSignedOut struct{ MatchID uint64 }

func (Connected) isEvent()             {}
func (LoggedOn) isEvent()              {}
func (LogOnFailed) isEvent()           {}
func (LoggedOff) isEvent()             {}
func (Disconnected) isEvent()          {}
func (Fatal) isEvent()                 {}
func (SteamFailure) isEvent()          {}
func (AccountInfo) isEvent()           {}
func (GCStatus) isEvent()              {}
func (GCWelcomed) isEvent()            {}
func (PlayingSession) isEvent()        {}
func (PracticeLobbyResponse) isEvent() {}
func (Popup) isEvent()                 {}
func (InvitationCreated) isEvent()     {}
func (MatchSignedOut) isEvent()        {}

// AccountFlagLimitedUser is EAccountFlags_LimitedUser.
const AccountFlagLimitedUser uint32 = 4096

// PopupNotLeagueAdmin is CMsgDOTAPopup.NOT_LEAGUE_ADMIN.
const PopupNotLeagueAdmin int32 = 27

// LobbyState is CSODOTALobby.State. The values are the wire values.
type LobbyState int32

const (
	LobbyUI           LobbyState = 0
	LobbyServerSetup  LobbyState = 1
	LobbyRun          LobbyState = 2
	LobbyPostGame     LobbyState = 3
	LobbyReadyUp      LobbyState = 4
	LobbyNotReady     LobbyState = 5
	LobbyServerAssign LobbyState = 6
)

func (s LobbyState) String() string {
	switch s {
	case LobbyUI:
		return "ui"
	case LobbyServerSetup:
		return "server_setup"
	case LobbyRun:
		return "run"
	case LobbyPostGame:
		return "postgame"
	case LobbyReadyUp:
		return "ready_up"
	case LobbyNotReady:
		return "not_ready"
	case LobbyServerAssign:
		return "server_assign"
	}
	return "unknown"
}

// Team is DOTA_GC_TEAM. The values are the wire values.
type Team int32

const (
	TeamRadiant     Team = 0
	TeamDire        Team = 1
	TeamBroadcaster Team = 2
	TeamSpectator   Team = 3
	TeamPlayerPool  Team = 4
	TeamNoTeam      Team = 5
)

func (t Team) String() string {
	switch t {
	case TeamRadiant:
		return "radiant"
	case TeamDire:
		return "dire"
	case TeamBroadcaster:
		return "broadcaster"
	case TeamSpectator:
		return "spectator"
	case TeamPlayerPool:
		return "player_pool"
	case TeamNoTeam:
		return "no_team"
	}
	return "unknown"
}

// Visibility is DOTALobbyVisibility.
type Visibility int32

const (
	VisibilityPublic   Visibility = 0
	VisibilityFriends  Visibility = 1
	VisibilityUnlisted Visibility = 2
)

// LobbyDetails is what the service sets on a lobby. Zero values are sent as
// zero, except where noted, because SetLobbyDetails is always sent with the
// full merged set.
type LobbyDetails struct {
	// LobbyID is required by the GC for SetLobbyDetails; 0 means the
	// current lobby. Ignored by CreateLobby.
	LobbyID         uint64
	GameName        string
	PassKey         string
	ServerRegion    uint32
	GameMode        uint32
	LeagueID        uint32
	LeagueNodeID    uint32
	SeriesType      uint32
	RadiantWins     uint32
	DireWins        uint32
	Visibility      Visibility
	AllowSpectating bool
	AllowCheats     bool
	FillWithBots    bool
	AllChat         bool
	// TVDelaySeconds is 10, 60, 120, 300 or 900; 0 leaves it unset (the GC
	// default). It is mapped through the D6 table in wire.go.
	TVDelaySeconds uint32
}

// LobbyView is a snapshot of the CSODOTALobby SO.
type LobbyView struct {
	LobbyID, LeaderID, MatchID, ServerID uint64
	State                                LobbyState
	GameState                            int32 // advisory only (06 §3.3)
	Outcome                              int32 // EMatchOutcome raw
	LeagueID, LeagueNodeID               uint32
	ServerRegion, GameMode               uint32
	GameName                             string
	Visibility                           Visibility
	// TVDelaySeconds is field 53 decoded through the D6 table; 0 when the
	// wire value is not in the table (TVDelayWire keeps it).
	TVDelaySeconds uint32
	TVDelayWire    int32
	// Members is all_members filtered by member_indices when the GC sends
	// them (Q15 caveat: inferred); otherwise all_members.
	Members []Member
	Pending []uint64 // pending_invites, SteamID64
	// Raw is the *protocol.CSODOTALobby, for debug logging only.
	Raw any
}

// Member is one lobby member.
type Member struct {
	SteamID uint64
	Team    Team
	// TeamKnown is false when the wire omitted team; an unset team decodes
	// as Radiant and must not be acted on (05 §3.3).
	TeamKnown bool
	Slot      uint32
	Leaver    int32
	HeroID    int32
}

// LobbyEventKind is the SO cache change type.
type LobbyEventKind int

const (
	LobbyCreated LobbyEventKind = iota
	LobbyUpdated
	LobbyDestroyed
)

func (k LobbyEventKind) String() string {
	switch k {
	case LobbyCreated:
		return "created"
	case LobbyUpdated:
		return "updated"
	case LobbyDestroyed:
		return "destroyed"
	}
	return "unknown"
}

// LobbyEvent is one lobby SO change. For LobbyDestroyed, Lobby is the last
// snapshot.
type LobbyEvent struct {
	Kind  LobbyEventKind
	Lobby *LobbyView
}

// LeagueInfo is one entry of CMsgDOTALeagueInfoList.
type LeagueInfo struct {
	LeagueID             uint32
	Name                 string
	Tier, Region, Status int32
	StartTS, EndTS       uint32
}

// LeagueNode is one entry of CMsgDOTALeagueAvailableLobbyNodes.
type LeagueNode struct {
	NodeID    uint32
	Name      string
	GroupName string
	TeamID1   uint32
	TeamID2   uint32
}

// MatchResult is the part of CMsgGCMatchDetailsResponse the resolver uses.
type MatchResult struct {
	// GCResult is the response's own result field (1 = OK).
	GCResult     uint32
	MatchID      uint64
	Outcome      int32 // EMatchOutcome raw: 2 radiant win, 3 dire win
	DurationSec  uint32
	StartTime    uint32
	LeagueID     uint32
	LobbyType    uint32
	GameMode     int32
	RadiantScore uint32
	DireScore    uint32
	Players      []MatchPlayer
}

// MatchPlayer is one player of a MatchResult.
type MatchPlayer struct {
	AccountID  uint32
	PlayerSlot uint32
	HeroID     int32
	Kills      uint32
	Deaths     uint32
	Assists    uint32
	Leaver     uint32
}
