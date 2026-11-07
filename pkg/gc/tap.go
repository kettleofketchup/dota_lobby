package gc

import (
	"log/slog"
	"sync"
	"sync/atomic"

	dota2 "github.com/paralin/go-dota2"
	sprotocol "github.com/paralin/go-steam/protocol"
	"github.com/paralin/go-steam/protocol/gamecoordinator"
)

// tapJobBase keeps our hand-made GC job IDs far above the ones go-dota2's
// MakeRequest allocates (it counts up from 1), so a reply addressed to one of
// ours can never complete one of its requests, or the other way round.
const tapJobBase uint64 = 1 << 40

// noJob is the target job of a GC message that answers no request.
const noJob = uint64(^sprotocol.JobId(0))

// tap is a GCPacketHandler that sees every Dota GC packet, including ones a
// pending MakeRequest claims (go-steam hands each packet to every handler).
// It turns 7055 into PracticeLobbyResponse events and lets callers wait for
// packets by type, for the job-addressed create (D5) and the 7634 fallback
// (Q18). It runs on the Steam reader goroutine, so it never blocks.
type tap struct {
	log  *slog.Logger
	emit func(Event)
	job  atomic.Uint64

	mu      sync.Mutex
	waiters map[*tapWaiter]struct{}
}

// tapWaiter receives every packet of one message type until cancelled.
type tapWaiter struct {
	msgType uint32
	ch      chan *gamecoordinator.GCPacket
}

func newTap(log *slog.Logger, emit func(Event)) *tap {
	t := &tap{log: log, emit: emit, waiters: map[*tapWaiter]struct{}{}}
	t.job.Store(tapJobBase)
	return t
}

// nextJob returns a fresh source job ID for a hand-sent request.
func (t *tap) nextJob() uint64 { return t.job.Add(1) }

// watch returns a channel of incoming packets of msgType. The buffer is
// small and full sends are dropped: a waiter only cares about the first few.
func (t *tap) watch(msgType uint32) (<-chan *gamecoordinator.GCPacket, func()) {
	w := &tapWaiter{msgType: msgType, ch: make(chan *gamecoordinator.GCPacket, 8)}
	t.mu.Lock()
	t.waiters[w] = struct{}{}
	t.mu.Unlock()
	return w.ch, func() {
		t.mu.Lock()
		delete(t.waiters, w)
		t.mu.Unlock()
	}
}

// HandleGCPacket implements steam.GCPacketHandler.
func (t *tap) HandleGCPacket(p *gamecoordinator.GCPacket) {
	if p.AppId != dota2.AppID {
		return
	}
	switch p.MsgType {
	case msgPracticeLobbyResponse:
		// go-dota2 has no handler for 7055, so this is the only place it
		// becomes an event.
		ev, err := practiceLobbyResponse(p)
		if err != nil {
			t.log.Warn("undecodable 7055", "err", err, "target_job", uint64(p.TargetJobId))
		} else {
			t.log.Debug("gc 7055", "eresult", ev.EResult, "debug_message", ev.DebugMessage, "target_job", ev.TargetJobID)
			t.emit(ev)
		}
	case msgLeagueAdminsResponse:
		t.log.Debug("gc 7634", "target_job", uint64(p.TargetJobId), "len", len(p.Body))
	}

	t.mu.Lock()
	for w := range t.waiters {
		if w.msgType == p.MsgType {
			select {
			case w.ch <- p:
			default:
			}
		}
	}
	t.mu.Unlock()
}

// practiceLobbyResponse decodes a 7055 packet into its event.
func practiceLobbyResponse(p *gamecoordinator.GCPacket) (PracticeLobbyResponse, error) {
	eresult, debug, err := decodeGenericResult(p.Body)
	if err != nil {
		return PracticeLobbyResponse{}, err
	}
	return PracticeLobbyResponse{
		EResult:      eresult,
		DebugMessage: debug,
		TargetJobID:  uint64(p.TargetJobId),
		Raw:          append([]byte(nil), p.Body...),
	}, nil
}
