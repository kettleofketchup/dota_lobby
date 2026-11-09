package lobby

import (
	"context"
	"sync"
	"time"
)

// chatQueueSize bounds the messages waiting to be sent in one lobby; a
// full queue drops its oldest message.
const chatQueueSize = 10

// chatLine is one message for a chat channel.
type chatLine struct {
	Channel uint64
	Text    string
}

// sendQueue spaces a lobby's chat messages: the GC rate-limits chat, and a
// burst of commands must not turn into a burst of replies. It never blocks
// the caller.
type sendQueue struct {
	mu    sync.Mutex
	lines []chatLine
	kick  chan struct{}
}

func newSendQueue() *sendQueue { return &sendQueue{kick: make(chan struct{}, 1)} }

// push queues a line, dropping the oldest one when the queue is full.
func (q *sendQueue) push(l chatLine) {
	q.mu.Lock()
	if len(q.lines) >= chatQueueSize {
		q.lines = q.lines[1:]
	}
	q.lines = append(q.lines, l)
	q.mu.Unlock()
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

func (q *sendQueue) pop() (chatLine, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.lines) == 0 {
		return chatLine{}, false
	}
	l := q.lines[0]
	q.lines = q.lines[1:]
	return l, true
}

// run sends queued lines one at a time, at least interval apart, until
// ctx ends. A failed send is not retried: chat is advisory.
func (q *sendQueue) run(ctx context.Context, interval time.Duration, send func(chatLine) error, failed func(error)) {
	var last time.Time
	for {
		l, ok := q.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-q.kick:
				continue
			}
		}
		if wait := interval - time.Since(last); !last.IsZero() && wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		last = time.Now()
		if err := send(l); err != nil && failed != nil {
			failed(err)
		}
	}
}
