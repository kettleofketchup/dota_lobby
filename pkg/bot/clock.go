package bot

import (
	"slices"
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

// Gate spaces logons across every bot of a process: each bot's slot is at
// least stagger away from every other bot's, and Pause holds all slots
// after a rate limit. A bot holds at most one booking: booking again
// replaces the previous one, so retry and backoff loops do not push other
// bots' first logons later. It is safe for concurrent use.
type Gate struct {
	stagger time.Duration

	mu     sync.Mutex
	slots  map[string]time.Time // booked slot per owner
	paused time.Time
}

// NewGate returns a gate with the given spacing; 0 means no spacing.
func NewGate(stagger time.Duration) *Gate {
	return &Gate{stagger: stagger, slots: map[string]time.Time{}}
}

// Reserve books owner's next free slot, dropping its previous booking, and
// returns how long to wait for it.
func (g *Gate) Reserve(owner string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reserve(owner, now)
}

func (g *Gate) reserve(owner string, now time.Time) time.Duration {
	delete(g.slots, owner)
	var booked []time.Time
	for o, at := range g.slots {
		if !at.Add(g.stagger).After(now) {
			delete(g.slots, o) // spaced out long ago
			continue
		}
		booked = append(booked, at)
	}
	slices.SortFunc(booked, time.Time.Compare)
	slot := now
	if g.paused.After(slot) {
		slot = g.paused
	}
	// Walk the other bookings in order, moving past any within stagger.
	for _, at := range booked {
		if slot.After(at.Add(-g.stagger)) && slot.Before(at.Add(g.stagger)) {
			slot = at.Add(g.stagger)
		}
	}
	g.slots[owner] = slot
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

// Recheck is called when owner's reserved slot comes due. If a Pause began
// after the slot was booked, it books a fresh slot after the pause (keeping
// the stagger between the waiting bots) and returns the wait; otherwise 0.
func (g *Gate) Recheck(owner string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused.After(now) {
		return 0
	}
	return g.reserve(owner, now)
}
