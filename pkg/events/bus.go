// Package events fans lobby events out to in-process subscribers once the
// lobby manager has stored them. The stored events stay the record:
// subscribers that fall behind lose the oldest envelopes, never the
// events themselves, and can read them back from the store.
package events

import (
	"log/slog"
	"path"
	"sync"

	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

// SubscriberBuffer is how many envelopes a subscriber may fall behind
// before the oldest are dropped.
const SubscriberBuffer = 64

// Bus is the lobby manager's Hooks: it turns each stored event into an
// envelope and hands it to every matching subscriber. It is safe for
// concurrent use.
type Bus struct {
	log *slog.Logger

	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	patterns []string
	ch       chan lobby.Envelope
}

var _ lobby.Hooks = (*Bus)(nil)

// New builds a bus. A nil logger discards.
func New(log *slog.Logger) *Bus {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Bus{log: log, subs: map[*subscriber]struct{}{}}
}

// OnEvent publishes one stored event. It never blocks.
func (b *Bus) OnEvent(l lobby.Lobby, ev lobby.Event) {
	env, err := lobby.NewEnvelope(l, ev)
	if err != nil {
		b.log.Error("cannot publish lobby event", "lobby", l.ID, "type", ev.Type, "err", err)
		return
	}
	b.Publish(env)
}

// Publish hands env to every subscriber whose patterns match its type,
// dropping that subscriber's oldest envelope when it is full.
func (b *Bus) Publish(env lobby.Envelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if !Match(s.patterns, env.Type) {
			continue
		}
		for {
			select {
			case s.ch <- env:
			default:
				select {
				case <-s.ch: // drop the oldest and try again
				default:
				}
				continue
			}
			break
		}
	}
}

// Subscribe returns envelopes whose type matches one of patterns (none
// matches every type) and a function that ends the subscription and
// closes the channel.
func (b *Bus) Subscribe(patterns []string) (<-chan lobby.Envelope, func()) {
	s := &subscriber{patterns: append([]string(nil), patterns...), ch: make(chan lobby.Envelope, SubscriberBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			close(s.ch)
			b.mu.Unlock()
		})
	}
}

// Match reports whether an event type matches one of patterns, which are
// path.Match globs such as "lobby.*" or "match.ended". No patterns match
// every type.
func Match(patterns []string, eventType string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, err := path.Match(p, eventType); err == nil && ok {
			return true
		}
	}
	return false
}
