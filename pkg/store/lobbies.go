package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotFound means no row has that key.
	ErrNotFound = errors.New("store: not found")
	// ErrDuplicateRef means a lobby that has not ended already uses the
	// external_ref.
	ErrDuplicateRef = errors.New("store: external_ref is used by an active lobby")
	// ErrVersionConflict means the lobby changed since it was read.
	ErrVersionConflict = errors.New("store: lobby was changed concurrently")
)

// TerminalStates are the lobby states that end a lobby. The partial unique
// index on external_ref in 0002_lobbies.sql lists the same set.
var TerminalStates = []string{"finished", "failed", "closed", "timed_out"}

// LobbyRow is one lobbies row. JSON columns are raw bytes; nil is NULL.
// Zero times are NULL.
type LobbyRow struct {
	ID          string
	ExternalRef string
	State       string
	StateReason string
	StateSince  time.Time
	Bot         string
	GCLobbyID   uint64
	LeagueID    uint32
	// LeagueVerified is nil until a bot is chosen.
	LeagueVerified *bool
	Spec           []byte
	PassKey        string
	Details        []byte
	HostSeat       []byte
	LaunchAttempts int
	MatchID        uint64
	// Outcome is the raw EMatchOutcome, nil until known.
	Outcome   *int32
	Match     []byte
	Seq       int64
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
	EndedAt   time.Time
}

// PlayerRow is one rostered player.
type PlayerRow struct {
	AccountID       uint32
	Side            string
	Position        int
	TeamRef         string
	Captain         bool
	Ready           bool
	InvitesSent     int
	LastInviteAt    time.Time
	InviteConfirmed bool
}

// EventRow is one lobby event. Data is a JSON object. Lobby is the JSON
// summary of the lobby when the event happened; nil for events stored
// before it was recorded.
type EventRow struct {
	ID         string
	LobbyID    string
	Seq        int64
	Type       string
	OccurredAt time.Time
	Data       []byte
	Lobby      []byte
}

// LobbyFilter selects lobbies for List. Empty fields match everything.
type LobbyFilter struct {
	States      []string
	ExternalRef string
	// Active keeps only lobbies in a non-terminal state.
	Active bool
	// Before is a cursor: only IDs that sort before it. IDs are ULIDs, so
	// this pages from newest to oldest.
	Before string
	// Limit caps the result; 0 means 50.
	Limit int
}

// Lobbies stores lobbies, their rosters and their events.
type Lobbies struct {
	db    *sql.DB
	route func(eventType string) []string
}

// Lobbies returns the lobby repository.
func (s *Store) Lobbies() *Lobbies { return &Lobbies{db: s.db} }

// WithWebhooks returns a copy of the repository that queues a webhook
// delivery for every URL route returns for an event's type, in the
// transaction that stores the event. A nil route queues nothing.
func (l *Lobbies) WithWebhooks(route func(eventType string) []string) *Lobbies {
	return &Lobbies{db: l.db, route: route}
}

const lobbyColumns = `id, external_ref, state, state_reason, state_since, bot, gc_lobby_id, league_id,
	league_verified, spec, pass_key, details, host_seat, launch_attempts, match_id, outcome, match,
	seq, version, created_at, updated_at, expires_at, ended_at`

// Insert writes a new lobby with its roster and first events in one
// transaction. It fails with ErrDuplicateRef when an active lobby already
// has the external_ref.
func (l *Lobbies) Insert(ctx context.Context, row LobbyRow, players []PlayerRow, events []EventRow) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert lobby %s: %w", row.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO lobbies (`+lobbyColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, lobbyArgs(row)...)
	if err != nil {
		if isUniqueRef(err) {
			return fmt.Errorf("insert lobby %s: %w", row.ID, ErrDuplicateRef)
		}
		return fmt.Errorf("insert lobby %s: %w", row.ID, err)
	}
	if err := writePlayers(ctx, tx, row.ID, players); err != nil {
		return fmt.Errorf("insert lobby %s: %w", row.ID, err)
	}
	if err := l.writeEvents(ctx, tx, events); err != nil {
		return fmt.Errorf("insert lobby %s: %w", row.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert lobby %s: %w", row.ID, err)
	}
	return nil
}

// Transition saves row if the stored version is still expected, writing
// row.Version as the new version. players replaces the roster when it is
// not nil. events are inserted in the same transaction, so a state change
// and the events reporting it are stored together or not at all. It fails
// with ErrVersionConflict when the stored version differs and ErrNotFound
// when there is no such lobby.
func (l *Lobbies) Transition(ctx context.Context, expected int64, row LobbyRow, players []PlayerRow, events []EventRow) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update lobby %s: %w", row.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	args := lobbyArgs(row)[1:] // every column but id
	args = append(args, row.ID, expected)
	res, err := tx.ExecContext(ctx, `UPDATE lobbies SET external_ref = ?, state = ?, state_reason = ?,
		state_since = ?, bot = ?, gc_lobby_id = ?, league_id = ?, league_verified = ?, spec = ?, pass_key = ?,
		details = ?, host_seat = ?, launch_attempts = ?, match_id = ?, outcome = ?, match = ?, seq = ?,
		version = ?, created_at = ?, updated_at = ?, expires_at = ?, ended_at = ?
		WHERE id = ? AND version = ?`, args...)
	if err != nil {
		return fmt.Errorf("update lobby %s: %w", row.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update lobby %s: %w", row.ID, err)
	}
	if n == 0 {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM lobbies WHERE id = ?`, row.ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("update lobby %s: %w", row.ID, ErrNotFound)
		}
		return fmt.Errorf("update lobby %s at version %d: %w", row.ID, expected, ErrVersionConflict)
	}
	if players != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM lobby_players WHERE lobby_id = ?`, row.ID); err != nil {
			return fmt.Errorf("update lobby %s: %w", row.ID, err)
		}
		if err := writePlayers(ctx, tx, row.ID, players); err != nil {
			return fmt.Errorf("update lobby %s: %w", row.ID, err)
		}
	}
	if err := l.writeEvents(ctx, tx, events); err != nil {
		return fmt.Errorf("update lobby %s: %w", row.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update lobby %s: %w", row.ID, err)
	}
	return nil
}

// Get returns one lobby, or ErrNotFound.
func (l *Lobbies) Get(ctx context.Context, id string) (LobbyRow, error) {
	rows, err := l.query(ctx, `WHERE id = ?`, id)
	if err != nil {
		return LobbyRow{}, fmt.Errorf("get lobby %s: %w", id, err)
	}
	if len(rows) == 0 {
		return LobbyRow{}, fmt.Errorf("get lobby %s: %w", id, ErrNotFound)
	}
	return rows[0], nil
}

// GetActiveByRef returns the active lobby with external_ref, or
// ErrNotFound.
func (l *Lobbies) GetActiveByRef(ctx context.Context, ref string) (LobbyRow, error) {
	rows, err := l.query(ctx, `WHERE external_ref = ? AND state NOT IN `+terminalList(), ref)
	if err != nil {
		return LobbyRow{}, fmt.Errorf("get lobby by ref: %w", err)
	}
	if len(rows) == 0 {
		return LobbyRow{}, fmt.Errorf("get lobby by ref: %w", ErrNotFound)
	}
	return rows[0], nil
}

// ActiveLobbies returns every lobby in a non-terminal state, oldest first.
func (l *Lobbies) ActiveLobbies(ctx context.Context) ([]LobbyRow, error) {
	rows, err := l.query(ctx, `WHERE state NOT IN `+terminalList()+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("active lobbies: %w", err)
	}
	return rows, nil
}

// List returns lobbies matching f, newest first.
func (l *Lobbies) List(ctx context.Context, f LobbyFilter) ([]LobbyRow, error) {
	var (
		where []string
		args  []any
	)
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		for _, s := range f.States {
			args = append(args, s)
		}
	}
	if f.ExternalRef != "" {
		where = append(where, "external_ref = ?")
		args = append(args, f.ExternalRef)
	}
	if f.Active {
		where = append(where, "state NOT IN "+terminalList())
	}
	if f.Before != "" {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	rows, err := l.query(ctx, clause+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list lobbies: %w", err)
	}
	return rows, nil
}

// Players returns a lobby's roster ordered by side and position.
func (l *Lobbies) Players(ctx context.Context, lobbyID string) ([]PlayerRow, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT account_id, side, position, team_ref, captain, ready,
		invites_sent, last_invite_at, invite_confirmed FROM lobby_players WHERE lobby_id = ?
		ORDER BY side DESC, position, account_id`, lobbyID)
	if err != nil {
		return nil, fmt.Errorf("players of %s: %w", lobbyID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []PlayerRow
	for rows.Next() {
		var (
			p               PlayerRow
			id              int64
			ref, invitedAt  sql.NullString
			captain, ready  int
			inviteConfirmed int
		)
		if err := rows.Scan(&id, &p.Side, &p.Position, &ref, &captain, &ready, &p.InvitesSent, &invitedAt, &inviteConfirmed); err != nil {
			return nil, fmt.Errorf("players of %s: %w", lobbyID, err)
		}
		p.AccountID, p.TeamRef = uint32(id), ref.String
		p.Captain, p.Ready, p.InviteConfirmed = captain != 0, ready != 0, inviteConfirmed != 0
		if p.LastInviteAt, err = parseTime(invitedAt); err != nil {
			return nil, fmt.Errorf("players of %s: %w", lobbyID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("players of %s: %w", lobbyID, err)
	}
	return out, nil
}

// Events returns a lobby's events with seq > after, in order, at most
// limit (0 means 100).
func (l *Lobbies) Events(ctx context.Context, lobbyID string, after int64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := l.db.QueryContext(ctx, `SELECT id, lobby_id, seq, type, occurred_at, data, lobby FROM lobby_events
		WHERE lobby_id = ? AND seq > ? ORDER BY seq LIMIT ?`, lobbyID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("events of %s: %w", lobbyID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := scanEvent(rows, &e); err != nil {
			return nil, fmt.Errorf("events of %s: %w", lobbyID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events of %s: %w", lobbyID, err)
	}
	return out, nil
}

// Sweep deletes lobbies that ended before cutoff, with their rosters and
// events, and returns how many lobbies it deleted.
func (l *Lobbies) Sweep(ctx context.Context, cutoff time.Time) (int, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sweep lobbies: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	sel := `SELECT id FROM lobbies WHERE state IN ` + terminalList() + ` AND ended_at IS NOT NULL AND ended_at < ?`
	ts := formatTime(cutoff)
	for _, table := range []string{"webhook_deliveries", "lobby_events", "lobby_players"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE lobby_id IN (`+sel+`)`, ts); err != nil {
			return 0, fmt.Errorf("sweep lobbies: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM lobbies WHERE id IN (`+sel+`)`, ts)
	if err != nil {
		return 0, fmt.Errorf("sweep lobbies: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep lobbies: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sweep lobbies: %w", err)
	}
	return int(n), nil
}

func (l *Lobbies) query(ctx context.Context, clause string, args ...any) ([]LobbyRow, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+lobbyColumns+` FROM lobbies `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LobbyRow
	for rows.Next() {
		r, err := scanLobby(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanLobby(rows *sql.Rows) (LobbyRow, error) {
	var (
		r                                        LobbyRow
		reason, since, bot, pass                 sql.NullString
		spec, details, hostSeat, match           sql.NullString
		created, updated, expires, ended         sql.NullString
		gcID, verified, matchID, outcome, league sql.NullInt64
	)
	if err := rows.Scan(&r.ID, &r.ExternalRef, &r.State, &reason, &since, &bot, &gcID, &league,
		&verified, &spec, &pass, &details, &hostSeat, &r.LaunchAttempts, &matchID, &outcome, &match,
		&r.Seq, &r.Version, &created, &updated, &expires, &ended); err != nil {
		return LobbyRow{}, err
	}
	r.StateReason, r.Bot, r.PassKey = reason.String, bot.String, pass.String
	r.GCLobbyID, r.LeagueID, r.MatchID = uint64(gcID.Int64), uint32(league.Int64), uint64(matchID.Int64)
	if verified.Valid {
		v := verified.Int64 != 0
		r.LeagueVerified = &v
	}
	if outcome.Valid {
		o := int32(outcome.Int64)
		r.Outcome = &o
	}
	r.Spec, r.Details, r.HostSeat, r.Match = rawJSON(spec), rawJSON(details), rawJSON(hostSeat), rawJSON(match)
	var err error
	for _, t := range []struct {
		dst *time.Time
		src sql.NullString
	}{{&r.StateSince, since}, {&r.CreatedAt, created}, {&r.UpdatedAt, updated}, {&r.ExpiresAt, expires}, {&r.EndedAt, ended}} {
		if *t.dst, err = parseTime(t.src); err != nil {
			return LobbyRow{}, err
		}
	}
	return r, nil
}

func lobbyArgs(r LobbyRow) []any {
	var verified any
	if r.LeagueVerified != nil {
		verified = boolInt(*r.LeagueVerified)
	}
	var outcome any
	if r.Outcome != nil {
		outcome = int64(*r.Outcome)
	}
	return []any{
		r.ID, r.ExternalRef, r.State, nullString(r.StateReason), formatTime(r.StateSince), nullString(r.Bot),
		nullUint(r.GCLobbyID), int64(r.LeagueID), verified, string(r.Spec), nullString(r.PassKey),
		nullBytes(r.Details), nullBytes(r.HostSeat), r.LaunchAttempts, nullUint(r.MatchID), outcome,
		nullBytes(r.Match), r.Seq, r.Version, formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
		nullTime(r.ExpiresAt), nullTime(r.EndedAt),
	}
}

func writePlayers(ctx context.Context, tx *sql.Tx, lobbyID string, players []PlayerRow) error {
	for _, p := range players {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lobby_players (lobby_id, account_id, side, position,
			team_ref, captain, ready, invites_sent, last_invite_at, invite_confirmed)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			lobbyID, int64(p.AccountID), p.Side, p.Position, nullString(p.TeamRef), boolInt(p.Captain),
			boolInt(p.Ready), p.InvitesSent, nullTime(p.LastInviteAt), boolInt(p.InviteConfirmed)); err != nil {
			return fmt.Errorf("write player %d: %w", p.AccountID, err)
		}
	}
	return nil
}

func (l *Lobbies) writeEvents(ctx context.Context, tx *sql.Tx, events []EventRow) error {
	for _, e := range events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lobby_events (id, lobby_id, seq, type, occurred_at, data, lobby)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, e.ID, e.LobbyID, e.Seq, e.Type, formatTime(e.OccurredAt), string(e.Data),
			nullBytes(e.Lobby)); err != nil {
			return fmt.Errorf("write event %s #%d: %w", e.Type, e.Seq, err)
		}
		if l.route == nil {
			continue
		}
		for _, url := range l.route(e.Type) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO webhook_deliveries (event_id, url, lobby_id, seq, status, next_at)
				VALUES (?, ?, ?, ?, ?, ?)`, e.ID, url, e.LobbyID, e.Seq, DeliveryPending, formatTime(e.OccurredAt)); err != nil {
				return fmt.Errorf("queue event %s #%d for a webhook: %w", e.Type, e.Seq, err)
			}
		}
	}
	return nil
}

// scanEvent reads the columns id, lobby_id, seq, type, occurred_at, data,
// lobby, in that order, into e, and any further columns into extra.
func scanEvent(row interface{ Scan(...any) error }, e *EventRow, extra ...any) error {
	var (
		at, lobby sql.NullString
		d         string
	)
	dest := append([]any{&e.ID, &e.LobbyID, &e.Seq, &e.Type, &at, &d, &lobby}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	var err error
	if e.OccurredAt, err = parseTime(at); err != nil {
		return err
	}
	e.Data, e.Lobby = []byte(d), rawJSON(lobby)
	return nil
}

func isUniqueRef(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed: lobbies.external_ref")
}

func terminalList() string {
	return "('" + strings.Join(TerminalStates, "', '") + "')"
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

func nullUint(v uint64) any {
	if v == 0 {
		return nil
	}
	return int64(v)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func rawJSON(s sql.NullString) []byte {
	if !s.Valid {
		return nil
	}
	return []byte(s.String)
}
