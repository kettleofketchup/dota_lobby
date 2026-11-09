package gc

import "sync"

// fanout copies values to every subscriber without ever blocking the
// sender: a full subscriber loses its oldest value, the same policy as
// go-dota2's SO cache. Subscriptions outlive connections.
type fanout[T any] struct {
	mu   sync.Mutex
	subs map[chan T]struct{}
	size int
}

func newFanout[T any](size int) *fanout[T] {
	return &fanout[T]{subs: map[chan T]struct{}{}, size: size}
}

// subscribe returns a channel that is never closed and the func that stops
// it.
func (f *fanout[T]) subscribe() (<-chan T, func()) {
	ch := make(chan T, f.size)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}
}

func (f *fanout[T]) publish(v T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		for range 2 {
			select {
			case ch <- v:
			default:
				select {
				case <-ch:
				default:
				}
				continue
			}
			break
		}
	}
}
