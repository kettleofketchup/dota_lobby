package lobby

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// Summary is the lobby part of an event envelope: enough for a receiver to
// find its own record. It never holds the password.
type Summary struct {
	ID          string            `json:"id"`
	ExternalRef string            `json:"external_ref"`
	State       State             `json:"state"`
	Bot         *string           `json:"bot"`
	GCLobbyID   *string           `json:"gc_lobby_id"`
	LeagueID    *uint32           `json:"league_id"`
	Metadata    map[string]string `json:"metadata"`
}

// Summary returns the lobby's envelope summary.
func (l Lobby) Summary() Summary {
	s := Summary{ID: l.ID, ExternalRef: l.Spec.ExternalRef, State: l.State, Metadata: map[string]string{}}
	if l.Bot != "" {
		b := l.Bot
		s.Bot = &b
	}
	if l.GCLobbyID != 0 {
		id := strconv.FormatUint(l.GCLobbyID, 10)
		s.GCLobbyID = &id
	}
	if l.Spec.LeagueID != 0 {
		id := l.Spec.LeagueID
		s.LeagueID = &id
	}
	for k, v := range l.Spec.Metadata {
		s.Metadata[k] = v
	}
	return s
}

// Envelope is an event as webhooks deliver it and the events endpoint
// lists it.
type Envelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Seq        int64           `json:"seq"`
	OccurredAt time.Time       `json:"occurred_at"`
	Lobby      Summary         `json:"lobby"`
	Data       json.RawMessage `json:"data"`
}

// NewEnvelope builds the envelope of an event of l, as l was right after
// the change the event reports.
func NewEnvelope(l Lobby, ev Event) (Envelope, error) {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode event %s: %w", ev.Type, err)
	}
	if ev.Data == nil {
		data = json.RawMessage("{}")
	}
	return Envelope{ID: ev.ID, Type: ev.Type, Seq: ev.Seq, OccurredAt: ev.OccurredAt.UTC(), Lobby: l.Summary(), Data: data}, nil
}

// EnvelopeFromRow builds an envelope from a stored event. fallback is used
// for events stored without a lobby summary; nil makes that an error.
func EnvelopeFromRow(row store.EventRow, fallback *Summary) (Envelope, error) {
	env := Envelope{ID: row.ID, Type: row.Type, Seq: row.Seq, OccurredAt: row.OccurredAt.UTC(), Data: json.RawMessage(row.Data)}
	if len(env.Data) == 0 {
		env.Data = json.RawMessage("{}")
	}
	switch {
	case row.Lobby != nil:
		if err := json.Unmarshal(row.Lobby, &env.Lobby); err != nil {
			return Envelope{}, fmt.Errorf("decode lobby of event %s: %w", row.ID, err)
		}
		if env.Lobby.Metadata == nil {
			env.Lobby.Metadata = map[string]string{}
		}
	case fallback != nil:
		env.Lobby = *fallback
	default:
		return Envelope{}, fmt.Errorf("event %s has no lobby summary", row.ID)
	}
	return env, nil
}

// MaxEventsPage caps Events' limit.
const MaxEventsPage = 500

// Events returns a lobby's events with seq > after, oldest first, at most
// limit (1-MaxEventsPage; 0 means 100). Events stored before lobby
// summaries were recorded carry the lobby as it is now.
func (m *Manager) Events(ctx context.Context, id string, after int64, limit int) ([]Envelope, error) {
	if after < 0 || limit < 0 || limit > MaxEventsPage {
		return nil, fmt.Errorf("%w: after must not be negative and limit must be 1-%d", ErrInvalid, MaxEventsPage)
	}
	l, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := m.opts.Store.Events(ctx, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	now := l.Summary()
	out := make([]Envelope, 0, len(rows))
	for _, row := range rows {
		env, err := EnvelopeFromRow(row, &now)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		out = append(out, env)
	}
	return out, nil
}
