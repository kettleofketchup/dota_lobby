package bot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
)

func TestWorkerNotReady(t *testing.T) {
	w := NewWorker(fake.New(), func() bool { return false }, 0)
	defer w.Close()
	ran := false
	err := w.Do(t.Context(), "leagues", func(context.Context, gc.Client) error { ran = true; return nil })
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, gc.ErrNotReady) || ran {
		t.Errorf("Do = %v, ran = %t", err, ran)
	}
}

func TestWorkerFIFO(t *testing.T) {
	w := NewWorker(fake.New(), func() bool { return true }, 0)
	defer w.Close()

	// Hold the worker so the rest queue up behind it.
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = w.Do(t.Context(), "hold", func(context.Context, gc.Client) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	var (
		mu     sync.Mutex
		order  []int
		active atomic.Int32
		wg     sync.WaitGroup
	)
	for i := range 5 {
		// Queue strictly in order.
		queued := make(chan struct{})
		wg.Go(func() {
			go func() { time.Sleep(5 * time.Millisecond); close(queued) }()
			err := w.Do(t.Context(), "op", func(context.Context, gc.Client) error {
				if active.Add(1) != 1 {
					t.Error("two calls ran at once")
				}
				defer active.Add(-1)
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
		<-queued
	}
	close(release)
	wg.Wait()
	for i, v := range order {
		if v != i {
			t.Fatalf("order = %v", order)
		}
	}
}

func TestWorkerTimeouts(t *testing.T) {
	tests := []struct {
		name     string
		ctx      func(t *testing.T) (context.Context, context.CancelFunc)
		wantLeft time.Duration // remaining deadline seen by fn, roughly
	}{
		{"default timeout", func(t *testing.T) (context.Context, context.CancelFunc) {
			return context.WithCancel(t.Context())
		}, 50 * time.Millisecond},
		{"caller deadline wins", func(t *testing.T) (context.Context, context.CancelFunc) {
			return context.WithTimeout(t.Context(), time.Hour)
		}, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWorker(fake.New(), func() bool { return true }, 50*time.Millisecond)
			defer w.Close()
			ctx, cancel := tt.ctx(t)
			defer cancel()
			var left time.Duration
			err := w.Do(ctx, "op", func(ctx context.Context, _ gc.Client) error {
				dl, ok := ctx.Deadline()
				if !ok {
					return errors.New("no deadline")
				}
				left = time.Until(dl)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if left > tt.wantLeft || left < tt.wantLeft/2 {
				t.Errorf("deadline in %s, want about %s", left, tt.wantLeft)
			}
		})
	}
}

func TestWorkerRequestTimesOut(t *testing.T) {
	fc := fake.New()
	fc.BlockUntilDone("ListAdminLeagues", true)
	w := NewWorker(fc, func() bool { return true }, 20*time.Millisecond)
	defer w.Close()
	err := w.Do(t.Context(), "leagues", func(ctx context.Context, c gc.Client) error {
		_, err := c.ListAdminLeagues(ctx)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do = %v", err)
	}
}

func TestWorkerCallerCancelWhileQueued(t *testing.T) {
	w := NewWorker(fake.New(), func() bool { return true }, 0)
	defer w.Close()
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = w.Do(t.Context(), "hold", func(context.Context, gc.Client) error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	ran := atomic.Bool{}
	done := make(chan error)
	go func() {
		done <- w.Do(ctx, "late", func(context.Context, gc.Client) error { ran.Store(true); return nil })
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Do = %v", err)
	}
	close(release)
	// The cancelled job is skipped when it reaches the front.
	if err := w.Do(t.Context(), "after", func(context.Context, gc.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if ran.Load() {
		t.Error("cancelled job ran")
	}
}

func TestWorkerClose(t *testing.T) {
	w := NewWorker(fake.New(), func() bool { return true }, 0)
	release := make(chan struct{})
	started := make(chan struct{})
	held := make(chan error)
	go func() {
		held <- w.Do(t.Context(), "hold", func(context.Context, gc.Client) error { close(started); <-release; return nil })
	}()
	<-started
	queued := make(chan error)
	go func() { queued <- w.Do(t.Context(), "queued", func(context.Context, gc.Client) error { return nil }) }()
	time.Sleep(5 * time.Millisecond)

	closed := make(chan struct{})
	go func() { w.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close did not wait for the running call")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	<-closed
	if err := <-held; err != nil {
		t.Errorf("running call = %v", err)
	}
	if err := <-queued; !errors.Is(err, ErrStopped) {
		t.Errorf("queued call = %v", err)
	}
	if err := w.Do(t.Context(), "after", func(context.Context, gc.Client) error { return nil }); !errors.Is(err, ErrStopped) {
		t.Errorf("Do after Close = %v", err)
	}
	w.Close() // idempotent
}
