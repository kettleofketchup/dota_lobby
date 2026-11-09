package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// LeagueSource is one source's league list for a bot.
type LeagueSource struct {
	IDs         []uint32
	RefreshedAt time.Time
	// FirstSeen is when each league in IDs first appeared in the list.
	FirstSeen map[uint32]time.Time
}

// Kinds of LeagueChange.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
)

// LeagueChange is a league appearing in or disappearing from a bot's list.
type LeagueChange struct {
	Bot      string
	LeagueID uint32
	Source   string
	// Change is ChangeAdded or ChangeRemoved.
	Change string
	At     time.Time
}

// maxChangesPerBot is how many changes are kept per bot; older ones are
// deleted when new ones are recorded.
const maxChangesPerBot = 500

// BotLeagueSet is everything stored about one bot's leagues.
type BotLeagueSet struct {
	Bot string
	// Sources is keyed by source name, e.g. "gc". A source that returned
	// an empty list has no entry; OKAt still records that it succeeded.
	Sources map[string]LeagueSource
	// OKAt is the last successful refresh; zero if none ever succeeded.
	OKAt time.Time
	// Error is the last refresh failure since that success, if any.
	Error   string
	ErrorAt time.Time
}

// Leagues stores the per-bot league lists.
type Leagues struct{ db *sql.DB }

// Leagues returns the league repository.
func (s *Store) Leagues() *Leagues { return &Leagues{db: s.db} }

// ReplaceBotLeagues replaces bot's list for source with ids, marks the
// refresh successful at at, and clears any recorded error. A league that
// stays keeps its first-seen time; one that appears or disappears is
// recorded as a change at at.
func (l *Leagues) ReplaceBotLeagues(ctx context.Context, bot, source string, ids []uint32, at time.Time) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceBotLeagues(ctx, tx, bot, source, ids, at); err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	return nil
}

func replaceBotLeagues(ctx context.Context, tx *sql.Tx, bot, source string, ids []uint32, at time.Time) error {
	old, err := storedIDs(ctx, tx, bot, source)
	if err != nil {
		return err
	}
	ts := formatTime(at)
	want := map[uint32]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var changes []LeagueChange
	for _, id := range old {
		if want[id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bot_leagues WHERE bot = ? AND source = ? AND league_id = ?`,
			bot, source, int64(id)); err != nil {
			return err
		}
		changes = append(changes, LeagueChange{Bot: bot, LeagueID: id, Source: source, Change: ChangeRemoved, At: at})
	}
	had := map[uint32]bool{}
	for _, id := range old {
		had[id] = true
	}
	for _, id := range sortedKeys(want) {
		if had[id] {
			if _, err := tx.ExecContext(ctx, `UPDATE bot_leagues SET refreshed_at = ? WHERE bot = ? AND source = ? AND league_id = ?`,
				ts, bot, source, int64(id)); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bot_leagues (bot, league_id, source, refreshed_at, first_seen_at) VALUES (?, ?, ?, ?, ?)`,
			bot, int64(id), source, ts, ts); err != nil {
			return err
		}
		changes = append(changes, LeagueChange{Bot: bot, LeagueID: id, Source: source, Change: ChangeAdded, At: at})
	}
	if err := recordChanges(ctx, tx, bot, changes); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bot_league_refresh (bot, ok_at, error, error_at) VALUES (?, ?, NULL, NULL)
		ON CONFLICT (bot) DO UPDATE SET ok_at = excluded.ok_at, error = NULL, error_at = NULL`, bot, ts)
	return err
}

func storedIDs(ctx context.Context, tx *sql.Tx, bot, source string) ([]uint32, error) {
	rows, err := tx.QueryContext(ctx, `SELECT league_id FROM bot_leagues WHERE bot = ? AND source = ? ORDER BY league_id`, bot, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []uint32
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, uint32(id))
	}
	return out, rows.Err()
}

func recordChanges(ctx context.Context, tx *sql.Tx, bot string, changes []LeagueChange) error {
	if len(changes) == 0 {
		return nil
	}
	for _, c := range changes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bot_league_changes (bot, league_id, source, change, at) VALUES (?, ?, ?, ?, ?)`,
			c.Bot, int64(c.LeagueID), c.Source, c.Change, formatTime(c.At)); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM bot_league_changes WHERE bot = ? AND id NOT IN
		(SELECT id FROM bot_league_changes WHERE bot = ? ORDER BY id DESC LIMIT ?)`, bot, bot, maxChangesPerBot)
	return err
}

func sortedKeys(m map[uint32]bool) []uint32 {
	out := make([]uint32, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// LeagueChanges returns up to perBot of each bot's most recent changes,
// newest first, keyed by bot name.
func (l *Leagues) LeagueChanges(ctx context.Context, perBot int) (map[string][]LeagueChange, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT bot, league_id, source, change, at FROM (
		SELECT bot, league_id, source, change, at, id,
		       ROW_NUMBER() OVER (PARTITION BY bot ORDER BY id DESC) AS n
		FROM bot_league_changes) WHERE n <= ? ORDER BY bot, id DESC`, perBot)
	if err != nil {
		return nil, fmt.Errorf("read league changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]LeagueChange{}
	for rows.Next() {
		var (
			c  LeagueChange
			id int64
			at sql.NullString
		)
		if err := rows.Scan(&c.Bot, &id, &c.Source, &c.Change, &at); err != nil {
			return nil, fmt.Errorf("read league changes: %w", err)
		}
		c.LeagueID = uint32(id)
		if c.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("read league changes: %w", err)
		}
		out[c.Bot] = append(out[c.Bot], c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read league changes: %w", err)
	}
	return out, nil
}

// RecordRefreshError records a failed refresh for bot. The stored lists
// and the last success are kept.
func (l *Leagues) RecordRefreshError(ctx context.Context, bot string, refreshErr error, at time.Time) error {
	msg := "unknown error"
	if refreshErr != nil {
		msg = refreshErr.Error()
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO bot_league_refresh (bot, ok_at, error, error_at) VALUES (?, NULL, ?, ?)
		ON CONFLICT (bot) DO UPDATE SET error = excluded.error, error_at = excluded.error_at`,
		bot, msg, formatTime(at)); err != nil {
		return fmt.Errorf("record league refresh error for %s: %w", bot, err)
	}
	return nil
}

// BotLeagues returns every bot with stored leagues or a refresh record,
// keyed by bot name. League IDs are sorted.
func (l *Leagues) BotLeagues(ctx context.Context) (map[string]BotLeagueSet, error) {
	out := map[string]BotLeagueSet{}
	get := func(bot string) BotLeagueSet {
		set, ok := out[bot]
		if !ok {
			set = BotLeagueSet{Bot: bot, Sources: map[string]LeagueSource{}}
		}
		return set
	}

	rows, err := l.db.QueryContext(ctx, `SELECT bot, league_id, source, refreshed_at, first_seen_at FROM bot_leagues ORDER BY bot, source, league_id`)
	if err != nil {
		return nil, fmt.Errorf("read bot leagues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			bot, source string
			id          int64
			at, first   sql.NullString
		)
		if err := rows.Scan(&bot, &id, &source, &at, &first); err != nil {
			return nil, fmt.Errorf("read bot leagues: %w", err)
		}
		t, err := parseTime(at)
		if err != nil {
			return nil, fmt.Errorf("read bot leagues: %w", err)
		}
		ft, err := parseTime(first)
		if err != nil {
			return nil, fmt.Errorf("read bot leagues: %w", err)
		}
		if ft.IsZero() {
			ft = t
		}
		set := get(bot)
		src := set.Sources[source]
		src.IDs = append(src.IDs, uint32(id))
		if src.FirstSeen == nil {
			src.FirstSeen = map[uint32]time.Time{}
		}
		src.FirstSeen[uint32(id)] = ft
		if t.After(src.RefreshedAt) {
			src.RefreshedAt = t
		}
		set.Sources[source] = src
		out[bot] = set
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read bot leagues: %w", err)
	}

	refresh, err := l.db.QueryContext(ctx, `SELECT bot, ok_at, error, error_at FROM bot_league_refresh`)
	if err != nil {
		return nil, fmt.Errorf("read league refreshes: %w", err)
	}
	defer func() { _ = refresh.Close() }()
	for refresh.Next() {
		var (
			bot             string
			okAt, errAt, em sql.NullString
		)
		if err := refresh.Scan(&bot, &okAt, &em, &errAt); err != nil {
			return nil, fmt.Errorf("read league refreshes: %w", err)
		}
		set := get(bot)
		if set.OKAt, err = parseTime(okAt); err != nil {
			return nil, fmt.Errorf("read league refreshes: %w", err)
		}
		if set.ErrorAt, err = parseTime(errAt); err != nil {
			return nil, fmt.Errorf("read league refreshes: %w", err)
		}
		set.Error = em.String
		out[bot] = set
	}
	if err := refresh.Err(); err != nil {
		return nil, fmt.Errorf("read league refreshes: %w", err)
	}
	for bot, set := range out {
		for name, src := range set.Sources {
			slices.Sort(src.IDs)
			set.Sources[name] = src
		}
		out[bot] = set
	}
	return out, nil
}
