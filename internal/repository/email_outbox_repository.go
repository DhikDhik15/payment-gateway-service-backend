package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrEmailOutboxNotFound is returned by the guarded Mark* transitions when no
// PROCESSING row matches (already terminal, or claimed by nobody).
var ErrEmailOutboxNotFound = errors.New("email outbox entry not found")

// ─── Interface ────────────────────────────────────────────────────────────────

// EmailOutboxRepository defines the database operations for the transactional
// email outbox (Phases 8C.3A + 8C.3B).
//
// EnqueueInTx (8C.3A): the caller (the invitation repository) OWNS the
// transaction — EnqueueInTx never begins one. Pattern mirrors
// MerchantWebhookPublisher.EnqueueInTx.
//
// ClaimPending / Mark* (8C.3B): worker-facing operations that mirror
// MerchantWebhookDeliveryRepository exactly — stale-PROCESSING recovery
// followed by a FOR UPDATE SKIP LOCKED claim inside one transaction, and
// status transitions guarded by `AND status = 'PROCESSING'`.
type EmailOutboxRepository interface {
	// EnqueueInTx inserts one PENDING outbox row using the caller's
	// transaction. The row commits or rolls back with that transaction —
	// never on a separate connection.
	EnqueueInTx(ctx context.Context, tx pgx.Tx, entry *model.EmailOutbox) error

	// ClaimPending first recovers stale PROCESSING rows (same policy as
	// merchant webhook deliveries: processing_at older than staleAfter
	// reverts to PENDING so a crashed worker cannot strand an email), then
	// atomically claims up to batchSize due PENDING rows:
	//
	//	PENDING → PROCESSING, processing_at = NOW(), last_attempt_at = NOW(),
	//	attempt_count = attempt_count + 1  (the claim IS the send attempt)
	//
	// using FOR UPDATE SKIP LOCKED so concurrent workers never claim the
	// same row. The returned rows carry the complete rendered email payload
	// — no second query is needed to send.
	ClaimPending(ctx context.Context, batchSize int, staleAfter time.Duration) ([]*model.EmailOutbox, error)

	// MarkSent transitions PROCESSING → SENT (sent_at = NOW(),
	// processing_at/last_error cleared). Returns ErrEmailOutboxNotFound when
	// the row is not PROCESSING.
	MarkSent(ctx context.Context, id uuid.UUID) error

	// MarkRetry transitions PROCESSING → PENDING with a future
	// next_attempt_at and a sanitized last_error (attempt_count is NOT
	// touched here — the claim already incremented it). Returns
	// ErrEmailOutboxNotFound when the row is not PROCESSING.
	MarkRetry(ctx context.Context, id uuid.UUID, nextAttemptAt time.Time, lastError string) error

	// MarkDead transitions PROCESSING → DEAD (permanent failure or max
	// attempts exhausted). Returns ErrEmailOutboxNotFound when the row is
	// not PROCESSING.
	MarkDead(ctx context.Context, id uuid.UUID, lastError string) error

	// DeleteExpiredTerminal (Phase 8C.3C) removes at most limit terminal rows
	// that have outlived retention, full-row:
	//
	//	SENT — sent_at older than sentCutoff   (strict <)
	//	DEAD — updated_at older than deadCutoff (strict <)
	//
	// PENDING and PROCESSING are NEVER eligible regardless of age — the
	// delivery worker owns those states (retry + stale recovery stay intact).
	// The single DELETE is bounded by LIMIT (never unbounded), runs as one
	// implicit transaction, returns the per-status deleted counts for run
	// logging, and only ever touches email_outbox: no invitation join, no
	// invitation mutation (merchant_user_invitations remains the sole
	// authority for invitation validity). Idempotent under concurrency — a
	// row already deleted by another instance simply matches nothing.
	DeleteExpiredTerminal(ctx context.Context, sentCutoff time.Time, deadCutoff time.Time, limit int) (sentDeleted int64, deadDeleted int64, err error)
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgEmailOutboxRepository struct {
	db *pgxpool.Pool
}

// NewEmailOutboxRepository returns the PostgreSQL-backed EmailOutboxRepository.
// EnqueueInTx runs inside the caller's transaction; ClaimPending/Mark* (Phase
// 8C.3B worker paths) use the repository-owned pool.
func NewEmailOutboxRepository(db *pgxpool.Pool) EmailOutboxRepository {
	return &pgEmailOutboxRepository{db: db}
}

const emailOutboxColumns = `id, merchant_id, reference_id, type, recipient, subject,
	text_body, html_body, status, attempt_count, next_attempt_at,
	processing_at, last_attempt_at, sent_at, last_error, created_at, updated_at`

// emailOutboxColumnsO qualifies columns for UPDATE … RETURNING with a table
// alias (same convention as deliveryColumnsD).
const emailOutboxColumnsO = `o.id, o.merchant_id, o.reference_id, o.type, o.recipient, o.subject,
	o.text_body, o.html_body, o.status, o.attempt_count, o.next_attempt_at,
	o.processing_at, o.last_attempt_at, o.sent_at, o.last_error, o.created_at, o.updated_at`

// EnqueueInTx inserts the outbox row via the caller's tx. All columns are
// written explicitly (webhook insertDelivery style); the service has already
// applied the PENDING/attempt/next_attempt defaults when it rendered the entry.
func (r *pgEmailOutboxRepository) EnqueueInTx(ctx context.Context, tx pgx.Tx, entry *model.EmailOutbox) error {
	const q = `
		INSERT INTO email_outbox
		    (id, merchant_id, reference_id, type, recipient, subject,
		     text_body, html_body, status, attempt_count, next_attempt_at,
		     processing_at, last_attempt_at, sent_at, last_error,
		     created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
	`
	_, err := tx.Exec(ctx, q,
		entry.ID,
		entry.MerchantID,
		entry.ReferenceID,
		entry.Type,
		entry.Recipient,
		entry.Subject,
		entry.TextBody,
		entry.HTMLBody,
		entry.Status,
		entry.AttemptCount,
		entry.NextAttemptAt,
		entry.ProcessingAt,
		entry.LastAttemptAt,
		entry.SentAt,
		entry.LastError,
		entry.CreatedAt,
		entry.UpdatedAt,
	)
	if err != nil {
		// Error text may only ever wrap DB causes — never message content.
		return fmt.Errorf("email outbox enqueue: %w", err)
	}
	return nil
}

// ClaimPending recovers stale PROCESSING rows, then claims due PENDING rows
// with FOR UPDATE SKIP LOCKED and marks them PROCESSING — a structural copy of
// MerchantWebhookDeliveryRepository.ClaimPending, additionally bumping
// attempt_count and last_attempt_at at claim time (the claim is the attempt).
func (r *pgEmailOutboxRepository) ClaimPending(ctx context.Context, batchSize int, staleAfter time.Duration) ([]*model.EmailOutbox, error) {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("email outbox claim begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	// Stale recovery — identical policy to merchant webhook deliveries: a
	// worker that crashed after PENDING → PROCESSING must not strand the row.
	// Reverted rows keep their (past) next_attempt_at, so they are claimable
	// by the very same claim statement below. attempt_count is untouched
	// here (it was already incremented when the crashed claim happened).
	const recoverQ = `
		UPDATE email_outbox
		SET status = 'PENDING',
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE status = 'PROCESSING'
		  AND processing_at IS NOT NULL
		  AND processing_at < NOW() - ($1 * INTERVAL '1 millisecond')`
	if _, err := dbTx.Exec(ctx, recoverQ, staleAfter.Milliseconds()); err != nil {
		return nil, fmt.Errorf("email outbox recover stale: %w", err)
	}

	const claimQ = `
		WITH cte AS (
			SELECT id
			FROM email_outbox
			WHERE status = 'PENDING'
			  AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at ASC, id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE email_outbox o
		SET status = 'PROCESSING',
		    processing_at = NOW(),
		    last_attempt_at = NOW(),
		    attempt_count = attempt_count + 1,
		    updated_at = NOW()
		FROM cte
		WHERE o.id = cte.id
		RETURNING ` + emailOutboxColumnsO

	rows, err := dbTx.Query(ctx, claimQ, batchSize)
	if err != nil {
		return nil, fmt.Errorf("email outbox claim: %w", err)
	}
	defer rows.Close()

	var claimed []*model.EmailOutbox
	for rows.Next() {
		e, err := scanEmailOutbox(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("email outbox claim commit: %w", err)
	}
	return claimed, nil
}

// MarkSent transitions the claimed row to SENT (§5). attempt_count and
// last_attempt_at were already written by the claim.
func (r *pgEmailOutboxRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE email_outbox
		SET status = 'SENT',
		    sent_at = NOW(),
		    processing_at = NULL,
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("email outbox mark sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEmailOutboxNotFound
	}
	return nil
}

// MarkRetry transitions the claimed row back to PENDING, scheduling the next
// attempt at nextAttemptAt (exponential backoff + jitter computed by the
// dispatcher using the shared webhook schedule). lastError must already be
// sanitized by the caller; it is length-bounded here via truncateErr.
func (r *pgEmailOutboxRepository) MarkRetry(ctx context.Context, id uuid.UUID, nextAttemptAt time.Time, lastError string) error {
	errMsg := truncateErr(lastError)
	const q = `
		UPDATE email_outbox
		SET status = 'PENDING',
		    next_attempt_at = $2,
		    last_error = $3,
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE id = $1 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, id, nextAttemptAt, errMsg)
	if err != nil {
		return fmt.Errorf("email outbox mark retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEmailOutboxNotFound
	}
	return nil
}

// MarkDead transitions the claimed row to DEAD — terminal, never claimed
// again (permanent failure or max attempts exhausted).
func (r *pgEmailOutboxRepository) MarkDead(ctx context.Context, id uuid.UUID, lastError string) error {
	errMsg := truncateErr(lastError)
	const q = `
		UPDATE email_outbox
		SET status = 'DEAD',
		    last_error = $2,
		    processing_at = NULL,
		    updated_at = NOW()
		WHERE id = $1 AND status = 'PROCESSING'`
	tag, err := r.db.Exec(ctx, q, id, errMsg)
	if err != nil {
		return fmt.Errorf("email outbox mark dead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEmailOutboxNotFound
	}
	return nil
}

// DeleteExpiredTerminal deletes a bounded batch of retention-eligible
// terminal rows (Phase 8C.3C). One statement ⇒ one implicit transaction;
// the LIMIT inside the id-subquery caps it at `limit` rows per call — the
// worker drains by repeating calls, never one giant DELETE.
//
// Ordering: (updated_at ASC, id ASC) is deterministic and oldest-first for
// the mixed terminal set. Eligibility does NOT depend on the order — each
// status is gated strictly by its own terminal timestamp (sent_at for SENT,
// updated_at for DEAD). created_at is deliberately not used for SENT: an
// email may retry for days before becoming SENT, and retention counts from
// actual delivery.
//
// Safety: the subquery only ever matches SENT/DEAD, so PENDING/PROCESSING
// rows are unreachable even when ancient. A SENT row with NULL sent_at
// (unreachable via MarkSent) evaluates to NULL and is skipped — never
// deleted on a missing timestamp. RETURNING status yields the exact
// per-status counts from the same atomic snapshot as the delete.
func (r *pgEmailOutboxRepository) DeleteExpiredTerminal(ctx context.Context, sentCutoff time.Time, deadCutoff time.Time, limit int) (int64, int64, error) {
	if limit <= 0 {
		return 0, 0, fmt.Errorf("email outbox delete expired terminal: invalid limit")
	}
	const q = `
		DELETE FROM email_outbox
		WHERE id IN (
			SELECT id
			FROM email_outbox
			WHERE (status = 'SENT' AND sent_at < $1)
			   OR (status = 'DEAD' AND updated_at < $2)
			ORDER BY updated_at ASC, id ASC
			LIMIT $3
		)
		RETURNING status`
	rows, err := r.db.Query(ctx, q, sentCutoff, deadCutoff, limit)
	if err != nil {
		// Parameters are timestamps + a count; error text can never carry
		// message content (recipient/subject/body are not referenced).
		return 0, 0, fmt.Errorf("email outbox delete expired terminal: %w", err)
	}
	defer rows.Close()

	var sentDeleted, deadDeleted int64
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return 0, 0, fmt.Errorf("email outbox delete expired terminal scan: %w", err)
		}
		switch status {
		case string(model.EmailOutboxStatusSent):
			sentDeleted++
		case string(model.EmailOutboxStatusDead):
			deadDeleted++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("email outbox delete expired terminal rows: %w", err)
	}
	return sentDeleted, deadDeleted, nil
}

func scanEmailOutbox(row pgx.Row) (*model.EmailOutbox, error) {
	e := &model.EmailOutbox{}
	err := row.Scan(
		&e.ID, &e.MerchantID, &e.ReferenceID, &e.Type, &e.Recipient, &e.Subject,
		&e.TextBody, &e.HTMLBody, &e.Status, &e.AttemptCount, &e.NextAttemptAt,
		&e.ProcessingAt, &e.LastAttemptAt, &e.SentAt, &e.LastError,
		&e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}
