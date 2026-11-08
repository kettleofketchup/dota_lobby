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
// at least stagger after the previous one, and
// Pause holds all slots after a rate limit. It is safe for
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

// Recheck is called when a reserved slot comes due. If a Pause began after
// the slot was booked, it books a fresh slot after the pause (keeping the
// stagger between the waiting bots) and returns the wait; otherwise 0.
func (g *Gate) Recheck(now time.Time) time.Duration {
	g.mu.Lock()
	paused := g.paused.After(now)
	g.mu.Unlock()
	if !paused {
		return 0
	}
	return g.Reserve(now)
}
