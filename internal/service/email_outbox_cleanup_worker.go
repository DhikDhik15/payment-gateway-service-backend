package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/repository"
)

// cleanupMaxBatchesPerRun bounds one cleanup run: the drain loop repeats
// DeleteExpiredTerminal until a batch deletes nothing, but stops here even
// if eligible rows remain — the rest waits for the next tick. The project
// has no other per-tick loop convention (ExpiryWorker and the delivery
// workers each process exactly one bounded batch per tick), so this constant
// is the bounded drain strategy: at the default batch size of 100 a single
// run deletes at most 10,000 rows, guaranteeing a pathological backlog can
// never monopolize the worker or produce one giant DELETE.
const cleanupMaxBatchesPerRun = 100

// EmailOutboxCleanupWorker (Phase 8C.3C) enforces terminal-row retention for
// email_outbox. Structural mirror of ExpiryWorker (immediate first run →
// ticker → context cancellation → Wait), deliberately separate from
// EmailOutboxWorker:
//
//	EmailOutboxWorker        PENDING / PROCESSING  → delivery, retry, stale recovery
//	EmailOutboxCleanupWorker SENT / DEAD           → age-based full-row DELETE
//
// The state machines are disjoint BY CONSTRUCTION: the repository query only
// ever matches SENT and PROCESSING/PENDING rows are unreachable from cleanup,
// so at-least-once delivery is never disturbed — regardless of how old a
// PENDING or stuck PROCESSING row is (stale recovery stays exclusively with
// the delivery worker). Cleanup never reads or writes merchant_user_invitations:
// deleting an outbox row removes delivery history only, never invitation
// validity, token_hash, or token state.
//
// Shutdown: the worker stops on workerCtx cancellation before srv.Shutdown()
// (same lifecycle as ExpiryWorker / MerchantWebhook / EmailOutbox workers).
type EmailOutboxCleanupWorker struct {
	repo          repository.EmailOutboxRepository
	interval      time.Duration
	batchSize     int
	sentRetention time.Duration
	deadRetention time.Duration
	done          chan struct{}
}

// NewEmailOutboxCleanupWorker constructs the retention worker. Config
// validation already rejects non-positive values; the fallbacks below mirror
// EmailOutboxDispatcher's defensive-constructor convention and are chosen to
// be the APPROVED policy (7d / 30d) — a zero retention can therefore never
// degrade into "delete everything".
func NewEmailOutboxCleanupWorker(
	repo repository.EmailOutboxRepository,
	interval time.Duration,
	batchSize int,
	sentRetention time.Duration,
	deadRetention time.Duration,
) *EmailOutboxCleanupWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	if sentRetention <= 0 {
		sentRetention = 168 * time.Hour // approved policy: SENT 7 days
	}
	if deadRetention <= 0 {
		deadRetention = 720 * time.Hour // approved policy: DEAD 30 days
	}
	return &EmailOutboxCleanupWorker{
		repo:          repo,
		interval:      interval,
		batchSize:     batchSize,
		sentRetention: sentRetention,
		deadRetention: deadRetention,
		done:          make(chan struct{}),
	}
}

// Start launches the worker in a new goroutine. It runs until ctx is
// cancelled; call Wait() after cancellation to block until it exits.
func (w *EmailOutboxCleanupWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

// Wait blocks until the background goroutine has fully stopped. Must be
// called after the context passed to Start is cancelled.
func (w *EmailOutboxCleanupWorker) Wait() {
	<-w.done
}

// run is the main loop: one cleanup pass immediately on startup, then one
// per tick, exiting on context cancellation.
func (w *EmailOutboxCleanupWorker) run(ctx context.Context) {
	defer close(w.done)

	slog.Info("email outbox cleanup worker started",
		slog.Duration("interval", w.interval),
		slog.Int("batch_size", w.batchSize),
		slog.Duration("sent_retention", w.sentRetention),
		slog.Duration("dead_retention", w.deadRetention),
	)

	// Run once immediately on startup.
	w.tick(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("email outbox cleanup worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick executes one cleanup run: compute the cutoffs from the approved
// retention windows, then drain eligible terminal rows in bounded batches
// until a batch deletes nothing (or cleanupMaxBatchesPerRun is reached).
//
// Logging carries operational metadata ONLY — counts, batch count, duration,
// errors. Never recipient, subject, body, token, token_hash, or deleted rows.
// A repository failure is logged and the run ends; the worker survives and
// retries on the next tick — one bad run must never terminate the process.
func (w *EmailOutboxCleanupWorker) tick(ctx context.Context) {
	now := time.Now().UTC()
	sentCutoff := now.Add(-w.sentRetention)
	deadCutoff := now.Add(-w.deadRetention)
	started := time.Now()

	var sentDeleted, deadDeleted int64
	batches := 0
	for batches < cleanupMaxBatchesPerRun {
		sent, dead, err := w.repo.DeleteExpiredTerminal(ctx, sentCutoff, deadCutoff, w.batchSize)
		if err != nil {
			slog.Error("email outbox cleanup: delete batch failed",
				slog.String("error", err.Error()),
				slog.Int("batches", batches),
				slog.Int64("sent_deleted", sentDeleted),
				slog.Int64("dead_deleted", deadDeleted),
			)
			return // continue on the next tick
		}
		batches++
		sentDeleted += sent
		deadDeleted += dead
		if sent+dead == 0 {
			break // drain complete — nothing eligible remains
		}
	}

	// Single run-level record (also the run counter when nothing matched).
	slog.Info("email outbox cleanup completed",
		slog.Int64("sent_deleted", sentDeleted),
		slog.Int64("dead_deleted", deadDeleted),
		slog.Int64("total_deleted", sentDeleted+deadDeleted),
		slog.Int("batches", batches),
		slog.Duration("duration", time.Since(started)),
	)
}
