// Package pool leases bots to lobbies. A bot hosts at most one lobby at a
// time (one account sits in at most one lobby), so each lobby reserves a
// free, ready bot that holds its league and releases it when the lobby
// ends. Selection prefers bots whose league the GC verified, then the bot
// released longest ago, then the name. See docs/research/05-prior-art.md.
package pool

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

var (
	// ErrNoHolder means no configured bot holds the league, ready or not.
	ErrNoHolder = errors.New("pool: no configured bot holds this league")
	// ErrNoneFree means bots hold the league but none is ready and free
	// right now.
	ErrNoneFree = errors.New("pool: no free bot holds this league")
	// ErrLeased means the bot is already leased to another owner.
	ErrLeased = errors.New("pool: bot is leased to another owner")
)

// Holders lists the bots that hold a league; *league.Registry implements
// it.
type Holders interface {
	Holders(league uint32) []league.Holding
}

// Lease is one bot reserved for one owner (a lobby ID).
type Lease struct {
	Bot      string
	Owner    string
	LeagueID uint32
	// Verified is whether the GC confirmed the bot holds LeagueID. A
	// restored lease is unverified.
	Verified bool
	Since    time.Time
}

// Options configures a Pool.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
}

// Pool hands out leases. Its methods are safe for concurrent use.
type Pool struct {
	bots    bot.Source
	holders Holders
	now     func() time.Time

	mu       sync.Mutex
	leases   map[string]*Lease    // by lowercased bot name
	released map[string]time.Time // last release, by lowercased bot name
}

// New builds an empty pool.
func New(bots bot.Source, holders Holders, opts Options) *Pool {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Pool{
		bots:     bots,
		holders:  holders,
		now:      opts.Now,
		leases:   map[string]*Lease{},
		released: map[string]time.Time{},
	}
}

func key(name string) string { return strings.ToLower(name) }

type candidate struct {
	holding  league.Holding
	released time.Time
}

// Reserve leases a ready, free bot that holds league l (0 = no league) to
// owner. An owner holds at most one lease: reserving again returns the
// lease it already has. It fails with ErrNoHolder when no configured bot
// holds l and with ErrNoneFree when none of the holders can take it now.
func (p *Pool) Reserve(ctx context.Context, l uint32, owner string) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reserve bot: %w", err)
	}
	holders := p.holders.Holders(l)
	if len(holders) == 0 {
		return nil, fmt.Errorf("reserve bot for league %d: %w", l, ErrNoHolder)
	}
	// Statuses are read before taking the lock; a bot that drops in
	// between is the lobby layer's problem either way.
	ready := make(map[string]bool, len(holders))
	for _, h := range holders {
		if b, ok := p.bots.Bot(h.Bot); ok {
			st := b.Status()
			ready[key(h.Bot)] = st.Ready && !st.LobbyStale
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ls := range p.leases {
		if ls.Owner == owner {
			return ls, nil
		}
	}
	var cands []candidate
	for _, h := range holders {
		k := key(h.Bot)
		if _, leased := p.leases[k]; leased || !ready[k] {
			continue
		}
		cands = append(cands, candidate{holding: h, released: p.released[k]})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("reserve bot for league %d: %w", l, ErrNoneFree)
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		if a.holding.Verified != b.holding.Verified {
			if a.holding.Verified {
				return -1
			}
			return 1
		}
		if c := a.released.Compare(b.released); c != 0 {
			return c
		}
		return cmp.Compare(key(a.holding.Bot), key(b.holding.Bot))
	})
	best := cands[0].holding
	ls := &Lease{Bot: best.Bot, Owner: owner, LeagueID: l, Verified: best.Verified, Since: p.now()}
	p.leases[key(best.Bot)] = ls
	return ls, nil
}

// Release ends a lease. Releasing a lease that was already released, or
// replaced by a later one, does nothing.
func (p *Pool) Release(ls *Lease) {
	if ls == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := key(ls.Bot)
	if cur, ok := p.leases[k]; ok && cur == ls {
		delete(p.leases, k)
		p.released[k] = p.now()
	}
}

// Restore leases a specific bot to owner again, for a lobby re-adopted
// after a restart. The bot need not be ready. Restoring the same owner
// twice returns its lease.
func (p *Pool) Restore(name, owner string) (*Lease, error) {
	b, ok := p.bots.Bot(name)
	if !ok {
		return nil, fmt.Errorf("restore lease: %w: %q", bot.ErrUnknownBot, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := key(name)
	if cur, ok := p.leases[k]; ok {
		if cur.Owner == owner {
			return cur, nil
		}
		return nil, fmt.Errorf("restore lease on %s for %s: %w (held by %s)", name, owner, ErrLeased, cur.Owner)
	}
	ls := &Lease{Bot: b.Name(), Owner: owner, Since: p.now()}
	p.leases[k] = ls
	return ls, nil
}

// Leases maps each leased bot's name to its owner.
func (p *Pool) Leases() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.leases))
	for _, ls := range p.leases {
		out[ls.Bot] = ls.Owner
	}
	return out
}
