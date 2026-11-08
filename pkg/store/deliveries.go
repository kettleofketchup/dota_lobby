package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Delivery statuses.
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryDead      = "dead"
)

// Delivery is one event queued for one webhook URL, with the event.
type Delivery struct {
	URL      string
	Attempts int
	NextAt   time.Time
	Event    EventRow
}

// DeliveryCounts are the number of deliveries per status.
type DeliveryCounts struct {
	Pending, Delivered, Dead int
}

// Deliveries is the webhook delivery queue. Rows are created by Lobbies
// (see WithWebhooks) in the transaction that stores their event.
type Deliveries struct{ db *sql.DB }

// Deliveries returns the webhook delivery repository.
func (s *Store) Deliveries() *Deliveries { return &Deliveries{db: s.db} }

// Due returns pending deliveries whose next attempt is at or before now,
// earliest first, at most limit (0 means 100). Only the lowest pending seq
// of each (URL, lobby) is ever returned, so a lobby's events reach a
// receiver in order; a delivered or dead row no longer holds the next one
// back.
func (d *Deliveries) Due(ctx context.Context, now time.Time, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.db.QueryContext(ctx, `SELECT e.id, e.lobby_id, e.seq, e.type, e.occurred_at, e.data, e.lobby,
		w.url, w.attempts, w.next_at
		FROM webhook_deliveries w JOIN lobby_events e ON e.id = w.event_id
		WHERE w.status = ? AND w.next_at <= ?
		AND NOT EXISTS (SELECT 1 FROM webhook_deliveries p WHERE p.url = w.url AND p.lobby_id = w.lobby_id
			AND p.status = ? AND p.seq < w.seq)
		ORDER BY w.next_at, w.lobby_id, w.seq LIMIT ?`,
		DeliveryPending, formatTime(now), DeliveryPending, limit)
	if err != nil {
		return nil, fmt.Errorf("due deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Delivery
	for rows.Next() {
		var (
			dl   Delivery
			next sql.NullString
		)
		if err := scanEvent(rows, &dl.Event, &dl.URL, &dl.Attempts, &next); err != nil {
			return nil, fmt.Errorf("due deliveries: %w", err)
		}
		if dl.NextAt, err = parseTime(next); err != nil {
			return nil, fmt.Errorf("due deliveries: %w", err)
		}
		out = append(out, dl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("due deliveries: %w", err)
	}
	return out, nil
}

// MarkDelivered records a successful attempt. status is the HTTP status
// the receiver answered.
func (d *Deliveries) MarkDelivered(ctx context.Context, eventID, url string, attempts, status int, at time.Time) error {
	return d.mark(ctx, eventID, url, `status = ?, attempts = ?, last_status = ?, last_error = NULL, delivered_at = ?`,
		DeliveryDelivered, attempts, status, formatTime(at))
}

// MarkRetry records a failed attempt and when to try again. status is 0
// when no response arrived.
func (d *Deliveries) MarkRetry(ctx context.Context, eventID, url string, attempts, status int, errMsg string, next time.Time) error {
	return d.mark(ctx, eventID, url, `attempts = ?, last_status = ?, last_error = ?, next_at = ?`,
		attempts, nullInt(status), nullString(errMsg), formatTime(next))
}

// MarkDead gives up on a delivery; the lobby's next event may then go.
func (d *Deliveries) MarkDead(ctx context.Context, eventID, url string, attempts, status int, errMsg string) error {
	return d.mark(ctx, eventID, url, `status = ?, attempts = ?, last_status = ?, last_error = ?`,
		DeliveryDead, attempts, nullInt(status), nullString(errMsg))
}

func (d *Deliveries) mark(ctx context.Context, eventID, url, set string, args ...any) error {
	args = append(args, eventID, url)
	res, err := d.db.ExecContext(ctx, `UPDATE webhook_deliveries SET `+set+` WHERE event_id = ? AND url = ?`, args...)
	if err != nil {
		return fmt.Errorf("update delivery %s: %w", eventID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update delivery %s: %w", eventID, err)
	}
	if n == 0 {
		return fmt.Errorf("update delivery %s: %w", eventID, ErrNotFound)
	}
	return nil
}

// Counts returns how many deliveries are in each status.
func (d *Deliveries) Counts(ctx context.Context) (DeliveryCounts, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM webhook_deliveries GROUP BY status`)
	if err != nil {
		return DeliveryCounts{}, fmt.Errorf("count deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var c DeliveryCounts
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return DeliveryCounts{}, fmt.Errorf("count deliveries: %w", err)
		}
		switch status {
		case DeliveryPending:
			c.Pending = n
		case DeliveryDelivered:
			c.Delivered = n
		case DeliveryDead:
			c.Dead = n
		}
	}
	if err := rows.Err(); err != nil {
		return DeliveryCounts{}, fmt.Errorf("count deliveries: %w", err)
	}
	return c, nil
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
