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
}

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
// refresh successful at at, and clears any recorded error.
func (l *Leagues) ReplaceBotLeagues(ctx context.Context, bot, source string, ids []uint32, at time.Time) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM bot_leagues WHERE bot = ? AND source = ?`, bot, source); err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	ts := formatTime(at)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO bot_leagues (bot, league_id, source, refreshed_at) VALUES (?, ?, ?, ?)`,
			bot, int64(id), source, ts); err != nil {
			return fmt.Errorf("replace leagues of %s: %w", bot, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bot_league_refresh (bot, ok_at, error, error_at) VALUES (?, ?, NULL, NULL)
		ON CONFLICT (bot) DO UPDATE SET ok_at = excluded.ok_at, error = NULL, error_at = NULL`, bot, ts); err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace leagues of %s: %w", bot, err)
	}
	return nil
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

	rows, err := l.db.QueryContext(ctx, `SELECT bot, league_id, source, refreshed_at FROM bot_leagues ORDER BY bot, source, league_id`)
	if err != nil {
		return nil, fmt.Errorf("read bot leagues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			bot, source string
			id          int64
			at          sql.NullString
		)
		if err := rows.Scan(&bot, &id, &source, &at); err != nil {
			return nil, fmt.Errorf("read bot leagues: %w", err)
		}
		t, err := parseTime(at)
		if err != nil {
			return nil, fmt.Errorf("read bot leagues: %w", err)
		}
		set := get(bot)
		src := set.Sources[source]
		src.IDs = append(src.IDs, uint32(id))
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
