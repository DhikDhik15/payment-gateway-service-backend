package service

import (
	"context"
	"log/slog"
	"time"
)

// ExpiryWorker runs ExpiryService on a configurable interval in the background.
// It integrates with the application's context-based graceful shutdown.
//
//	worker := NewExpiryWorker(expirySvc, 30*time.Second, 100)
//	ctx, cancel := context.WithCancel(context.Background())
//	worker.Start(ctx)
//	// ... application runs ...
//	cancel() // triggers graceful stop
//	worker.Wait()
type ExpiryWorker struct {
	svc       ExpiryService
	interval  time.Duration
	batchSize int
	done      chan struct{}
}

// NewExpiryWorker constructs an ExpiryWorker.
//
//   - svc       — the ExpiryService to call on each tick
//   - interval  — how often to run (e.g. 30 * time.Second)
//   - batchSize — maximum transactions to process per run
func NewExpiryWorker(svc ExpiryService, interval time.Duration, batchSize int) *ExpiryWorker {
	return &ExpiryWorker{
		svc:       svc,
		interval:  interval,
		batchSize: batchSize,
		done:      make(chan struct{}),
	}
}

// Start launches the worker in a new goroutine. The worker runs until ctx is
// cancelled. Call Wait() after cancellation to block until the goroutine exits.
func (w *ExpiryWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

// Wait blocks until the background goroutine has fully stopped.
// Must be called after the context passed to Start is cancelled.
func (w *ExpiryWorker) Wait() {
	<-w.done
}

// run is the main loop. It ticks at w.interval and calls ProcessExpired on
// each tick. It also runs once immediately so that the first expiry check
// does not wait a full interval after startup.
func (w *ExpiryWorker) run(ctx context.Context) {
	defer close(w.done)

	slog.Info("expiry worker started",
		slog.Duration("interval", w.interval),
		slog.Int("batch_size", w.batchSize),
	)

	// Run once immediately on startup.
	w.tick(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("expiry worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick executes a single expiry pass. DB or service errors are logged but
// never propagate out — the worker must continue running on subsequent ticks.
func (w *ExpiryWorker) tick(ctx context.Context) {
	n, err := w.svc.ProcessExpired(ctx, w.batchSize)
	if err != nil {
		slog.Error("expiry worker: error processing expired transactions",
			slog.String("error", err.Error()),
		)
		return
	}
	if n > 0 {
		slog.Info("expiry worker: expired transactions processed",
			slog.Int("count", n),
		)
	}
}
