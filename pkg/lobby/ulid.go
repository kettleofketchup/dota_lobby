package lobby

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idGen makes ULIDs: 48 bits of milliseconds then 80 random bits, as 26
// characters that sort by creation time. Within one millisecond the random
// part counts up, so IDs from one process are strictly increasing.
type idGen struct {
	mu     sync.Mutex
	lastMS uint64
	hi     uint16 // top 16 random bits
	lo     uint64 // low 64 random bits
}

func (g *idGen) next(now time.Time) string {
	g.mu.Lock()
	ms := uint64(now.UnixMilli())
	if ms <= g.lastMS {
		ms = g.lastMS
		g.lo++
		if g.lo == 0 {
			g.hi++
		}
	} else {
		var b [10]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		g.hi, g.lo = binary.BigEndian.Uint16(b[:2]), binary.BigEndian.Uint64(b[2:])
		g.lastMS = ms
	}
	hi := ms<<16 | uint64(g.hi) // 64 bits: time, then the top random bits
	lo := g.lo
	g.mu.Unlock()

	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
