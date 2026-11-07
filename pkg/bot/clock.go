package bot

import (
	"sync"
	"time"
)

// Clock is the time source for supervisor timers, so tests can run hours of
// backoff and cooldown without waiting.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc call.
type Timer interface {
	// Stop prevents the call if it has not started; it reports whether it
	// did.
	Stop() bool
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// AfterFunc implements Clock.
func (SystemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Gate spaces logons across every bot of a process: each Reserve gets a slot
// at least stagger after the previous one (plan §4.1 staggered start), and
// Pause holds all slots after a rate limit (plan §9). It is safe for
// concurrent use.
type Gate struct {
	stagger time.Duration

	mu     sync.Mutex
	next   time.Time
	paused time.Time
}

// NewGate returns a gate with the given spacing; 0 means no spacing.
func NewGate(stagger time.Duration) *Gate { return &Gate{stagger: stagger} }

// Reserve books the next free slot and returns how long to wait for it.
func (g *Gate) Reserve(now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	slot := now
	if g.next.After(slot) {
		slot = g.next
	}
	if g.paused.After(slot) {
		slot = g.paused
	}
	g.next = slot.Add(g.stagger)
	return slot.Sub(now)
}

// Pause holds every slot until until. An earlier pause never shortens a
// later one.
func (g *Gate) Pause(until time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.paused) {
		g.paused = until
	}
}
