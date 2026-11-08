package bot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// ErrNotReady means the bot is not in gc_ready. It wraps gc.ErrNotReady, so
// errors.Is works with either.
var ErrNotReady = fmt.Errorf("bot: not ready: %w", gc.ErrNotReady)

// ErrStopped means the bot's worker has shut down.
var ErrStopped = errors.New("bot: stopped")

// DefaultRequestTimeout bounds a Do call whose ctx has no deadline
// (gc.request_timeout).
const DefaultRequestTimeout = 10 * time.Second

// workerQueue is how many calls may wait before Do blocks on its ctx.
const workerQueue = 64

// Worker runs GC requests for one bot on a single goroutine, in FIFO order,
// so two requests of the same type never overlap. The event pump
// and supervisor never block on it.
type Worker struct {
	client  gc.Client
	ready   func() bool
	timeout time.Duration

	jobs chan *job
	stop chan struct{}
	done chan struct{}
	once sync.Once

	// abortCtx ends the running call's ctx when Shutdown gives up waiting.
	abortCtx context.Context
	abort    context.CancelFunc
}

type job struct {
	ctx context.Context
	op  string
	fn  func(ctx context.Context, c gc.Client) error
	res chan error
}

// NewWorker starts a worker. ready is checked when a call is queued and
// again when it runs; timeout <= 0 means DefaultRequestTimeout.
func NewWorker(client gc.Client, ready func() bool, timeout time.Duration) *Worker {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	w := &Worker{
		client:  client,
		ready:   ready,
		timeout: timeout,
		jobs:    make(chan *job, workerQueue),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	w.abortCtx, w.abort = context.WithCancel(context.Background())
	go w.run()
	return w
}

// Do runs fn on the bot's GC goroutine and returns its error. It fails with
// ErrNotReady unless the bot is gc_ready. fn gets ctx bounded by the
// worker's timeout when ctx has no deadline of its own; pass a ctx with a
// longer deadline for slow requests such as a lobby create (2 min).
func (w *Worker) Do(ctx context.Context, op string, fn func(ctx context.Context, c gc.Client) error) error {
	if !w.ready() {
		return fmt.Errorf("%s: %w", op, ErrNotReady)
	}
	j := &job{ctx: ctx, op: op, fn: fn, res: make(chan error, 1)}
	select {
	case w.jobs <- j:
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", op, ctx.Err())
	case <-w.stop:
		return fmt.Errorf("%s: %w", op, ErrStopped)
	}
	select {
	case err := <-j.res:
		return err
	case <-w.done:
		// Queued after the worker drained its queue for the last time.
		select {
		case err := <-j.res:
			return err
		default:
			return fmt.Errorf("%s: %w", op, ErrStopped)
		}
	case <-ctx.Done():
		// The job sees the same ctx, so it is skipped or cut short.
		return fmt.Errorf("%s: %w", op, ctx.Err())
	}
}

// Close stops the worker. Queued calls fail with ErrStopped; a running call
// is waited for, however long it takes. It is safe to call more than once.
func (w *Worker) Close() { _ = w.Shutdown(context.Background()) }

// Shutdown stops the worker like Close, but waits for the running call only
// until ctx ends. Then it cancels that call's ctx and returns ctx's error
// without waiting further, so a request stuck on a dead connection cannot
// hold up a bounded shutdown. Queued calls fail with ErrStopped.
func (w *Worker) Shutdown(ctx context.Context) error {
	w.once.Do(func() { close(w.stop) })
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		w.abort()
		return fmt.Errorf("stop worker: %w", ctx.Err())
	}
}

func (w *Worker) run() {
	defer close(w.done)
	for {
		// Stop wins over queued work even when both are ready.
		select {
		case <-w.stop:
			w.failQueued()
			return
		default:
		}
		select {
		case <-w.stop:
			w.failQueued()
			return
		case j := <-w.jobs:
			j.res <- w.exec(j)
		}
	}
}

func (w *Worker) exec(j *job) error {
	if err := j.ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", j.op, err)
	}
	if !w.ready() {
		return fmt.Errorf("%s: %w", j.op, ErrNotReady)
	}
	ctx, cancelAbort := context.WithCancel(j.ctx)
	defer cancelAbort()
	// Shutdown cancels the call when it gives up waiting.
	defer context.AfterFunc(w.abortCtx, cancelAbort)()
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.timeout)
		defer cancel()
	}
	return j.fn(ctx, w.client)
}

func (w *Worker) failQueued() {
	for {
		select {
		case j := <-w.jobs:
			j.res <- fmt.Errorf("%s: %w", j.op, ErrStopped)
		default:
			return
		}
	}
}
