package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
)

// ─── Phase 8C.3B — asynchronous email outbox delivery ─────────────────────────
//
// Structural mirror of MerchantWebhookDispatcher + MerchantWebhookWorker
// (merchant_webhook_dispatcher.go): one dispatcher owns claim → send →
// state-transition, one worker polls it on an interval. The retry schedule,
// jitter, and stale-PROCESSING policy are literally the shared webhook ones
// (defaultWebhookBackoff / backoffForAttempt / applyJitter) — this file
// intentionally does NOT define a second algorithm.
//
// Delivery semantics: AT-LEAST-ONCE, never exactly-once. If the process
// crashes after EmailSender.Send() returns but before the SENT/DEAD UPDATE
// commits, stale-PROCESSING recovery re-claims the row and the SMTP
// submission is repeated — the recipient may receive a duplicate. That
// duplicate window is accepted by design (same as webhook delivery); do not
// "fix" it with delivery-side dedup in this phase.
//
// Responsibilities split (§14): EmailSender owns SMTP connection/TLS/auth/
// MIME/timeout/transport errors; this file owns queue state, claiming,
// retries, scheduling, and transitions. The worker never sees invitation
// data, never regenerates a token, and never rebuilds a message — the outbox
// row IS the authoritative rendered payload.

// ─── Error classification ─────────────────────────────────────────────────────

// isPermanentEmailError classifies an EmailSender failure for the outbox
// state machine. Structured checks only — never error-string matching:
//
//   - ErrEmailMessageInvalid: the message failed validation BEFORE any I/O
//     (missing/malformed fields, header injection). A retry can never
//     succeed → permanent.
//   - *textproto.Error: the SMTP server's own reply, unwrapped from the
//     sender's error chain (the sender wraps it with double %w).
//     5xx → permanent recipient/server rejection; 4xx → temporary → retry.
//   - Everything else (dial, TLS, transport auth, context deadline,
//     anything unknown): retryable — bounded by max attempts, so a truly
//     permanent unknown still terminates at DEAD.
func isPermanentEmailError(err error) bool {
	if errors.Is(err, ErrEmailMessageInvalid) {
		return true
	}
	var smtpErr *textproto.Error
	if errors.As(err, &smtpErr) {
		return smtpErr.Code >= 500
	}
	return false
}

// maxEmailErrorLen bounds persisted/logged error summaries (same 500-char
// policy as the repository's truncateErr — applied here too so the worker's
// own logs are bounded as well).
const maxEmailErrorLen = 500

// sanitizeEmailOutboxError reduces a Send failure to a safe operational
// summary for last_error and logs (§7/§16): bounded length, and the full
// recipient address redacted to a domain marker. The sender never embeds
// bodies, subject, token, or credentials in its errors; the recipient
// redaction additionally covers SMTP server replies that echo the address
// (e.g. "550 <user@x> unknown").
func sanitizeEmailOutboxError(err error, recipient string) string {
	msg := err.Error()
	if recipient != "" {
		dom := emailDomain(normalizeEmailAddr(recipient))
		if dom != "" {
			// emailDomain already returns the domain with its "@" prefix.
			marker := "[recipient" + dom + "]"
			msg = strings.ReplaceAll(msg, recipient, marker)
			if norm := normalizeEmailAddr(recipient); norm != recipient {
				msg = strings.ReplaceAll(msg, norm, marker)
			}
		}
	}
	if len(msg) > maxEmailErrorLen {
		msg = msg[:maxEmailErrorLen]
	}
	return msg
}

// ─── Dispatcher ───────────────────────────────────────────────────────────────

// EmailOutboxDispatcher claims and delivers queued emails.
type EmailOutboxDispatcher interface {
	ProcessBatch(ctx context.Context) (int, error)
}

type emailOutboxDispatcher struct {
	outboxRepo  repository.EmailOutboxRepository
	emailSender EmailSender
	maxAttempts int
	staleAfter  time.Duration
	batchSize   int
}

// NewEmailOutboxDispatcher constructs the outbox dispatcher. Defaults mirror
// NewMerchantWebhookDispatcher (8 attempts, 20 per batch, stale window 2m).
func NewEmailOutboxDispatcher(
	outboxRepo repository.EmailOutboxRepository,
	emailSender EmailSender,
	maxAttempts int,
	staleAfter time.Duration,
	batchSize int,
) EmailOutboxDispatcher {
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	if batchSize <= 0 {
		batchSize = 20
	}
	return &emailOutboxDispatcher{
		outboxRepo:  outboxRepo,
		emailSender: emailSender,
		maxAttempts: maxAttempts,
		staleAfter:  staleAfter,
		batchSize:   batchSize,
	}
}

// ProcessBatch claims one batch (stale recovery included) and delivers each
// claimed row. Per-row failures are logged and skipped — the row stays
// PROCESSING and stale recovery re-claims it later — so one bad row never
// blocks the batch (same policy as the webhook dispatcher).
func (d *emailOutboxDispatcher) ProcessBatch(ctx context.Context) (int, error) {
	claimed, err := d.outboxRepo.ClaimPending(ctx, d.batchSize, d.staleAfter)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, entry := range claimed {
		if err := d.deliverOne(ctx, entry); err != nil {
			// Safe log: IDs + attempt + wrapped cause only (the repository
			// wraps DB causes; message content is never part of these errors).
			slog.Error("email outbox delivery error",
				slog.String("outbox_id", entry.ID.String()),
				slog.String("merchant_id", entry.MerchantID.String()),
				slog.Int("attempt", entry.AttemptCount),
				slog.String("error", err.Error()),
			)
			continue
		}
		processed++
	}
	return processed, nil
}

// deliverOne sends exactly the persisted payload and applies the state
// transition for the outcome.
func (d *emailOutboxDispatcher) deliverOne(ctx context.Context, entry *model.EmailOutbox) error {
	// §4: the outbox row is the authoritative rendered email. No invitation
	// lookup, no token regeneration, no re-rendering. From is intentionally
	// empty — EmailSender resolves the configured SMTP_FROM at send time.
	msg := EmailMessage{
		To:       []string{entry.Recipient},
		Subject:  entry.Subject,
		TextBody: entry.TextBody,
		HTMLBody: entry.HTMLBody,
	}

	sendErr := d.emailSender.Send(ctx, msg)
	if sendErr == nil {
		// §5: PROCESSING → SENT (terminal; never re-claimed).
		if err := d.outboxRepo.MarkSent(ctx, entry.ID); err != nil {
			return err
		}
		slog.Info("email outbox marked sent",
			slog.String("outbox_id", entry.ID.String()),
			slog.String("merchant_id", entry.MerchantID.String()),
			slog.Int("attempt", entry.AttemptCount),
			slog.String("recipient_domain", emailDomain(normalizeEmailAddr(entry.Recipient))),
		)
		return nil
	}

	safeErr := sanitizeEmailOutboxError(sendErr, entry.Recipient)

	// §9: permanent failure → DEAD immediately, no retry.
	if isPermanentEmailError(sendErr) {
		if err := d.outboxRepo.MarkDead(ctx, entry.ID, "permanent failure: "+safeErr); err != nil {
			return err
		}
		slog.Warn("email outbox marked dead (permanent failure)",
			slog.String("outbox_id", entry.ID.String()),
			slog.String("merchant_id", entry.MerchantID.String()),
			slog.Int("attempt", entry.AttemptCount),
			slog.String("error", safeErr),
		)
		return nil
	}

	// §8: attempts exhausted → DEAD (attempt_count was incremented by the
	// claim, so entry.AttemptCount is the attempt that just failed).
	if entry.AttemptCount >= d.maxAttempts {
		finalErr := fmt.Sprintf("max attempts (%d) exhausted: %s", d.maxAttempts, safeErr)
		if err := d.outboxRepo.MarkDead(ctx, entry.ID, finalErr); err != nil {
			return err
		}
		slog.Warn("email outbox marked dead (max attempts)",
			slog.String("outbox_id", entry.ID.String()),
			slog.String("merchant_id", entry.MerchantID.String()),
			slog.Int("attempt", entry.AttemptCount),
			slog.Int("max_attempts", d.maxAttempts),
		)
		return nil
	}

	// §7: retryable → PENDING with the shared webhook backoff + jitter.
	delay := backoffForAttempt(entry.AttemptCount)
	next := time.Now().UTC().Add(delay)
	if err := d.outboxRepo.MarkRetry(ctx, entry.ID, next, safeErr); err != nil {
		return err
	}
	slog.Info("email outbox retry scheduled",
		slog.String("outbox_id", entry.ID.String()),
		slog.String("merchant_id", entry.MerchantID.String()),
		slog.Int("attempt", entry.AttemptCount),
		slog.Duration("delay", delay),
		slog.String("error", safeErr),
	)
	return nil
}

// ─── Worker ───────────────────────────────────────────────────────────────────

// EmailOutboxWorker runs the dispatcher on an interval. Start/Wait lifecycle
// is identical to MerchantWebhookWorker.
type EmailOutboxWorker struct {
	dispatcher EmailOutboxDispatcher
	interval   time.Duration
	done       chan struct{}
}

// NewEmailOutboxWorker constructs the background outbox delivery worker.
func NewEmailOutboxWorker(dispatcher EmailOutboxDispatcher, interval time.Duration) *EmailOutboxWorker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &EmailOutboxWorker{
		dispatcher: dispatcher,
		interval:   interval,
		done:       make(chan struct{}),
	}
}

// Start launches the worker goroutine.
func (w *EmailOutboxWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

// Wait blocks until the worker has stopped.
func (w *EmailOutboxWorker) Wait() {
	<-w.done
}

func (w *EmailOutboxWorker) run(ctx context.Context) {
	defer close(w.done)
	slog.Info("email outbox worker started", slog.Duration("interval", w.interval))
	w.tick(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("email outbox worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *EmailOutboxWorker) tick(ctx context.Context) {
	n, err := w.dispatcher.ProcessBatch(ctx)
	if err != nil {
		// Recoverable: DB hiccups are logged and retried on the next tick
		// (claim errors abort the whole batch — webhook parity).
		slog.Error("email outbox worker: batch error", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		slog.Info("email outbox worker: emails processed", slog.Int("count", n))
	}
}
