package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RefundOutboxHook enqueues merchant webhook deliveries inside a DB transaction.
type RefundOutboxHook func(ctx context.Context, dbTx pgx.Tx, refund *model.Refund, tx *model.Transaction, eventType model.MerchantWebhookEventType) error

// RefundRepository defines refund persistence operations.
type RefundRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*model.Refund, error)
	FindByMerchantAndID(ctx context.Context, merchantID, refundID uuid.UUID) (*model.Refund, error)
	FindByProviderRefundID(ctx context.Context, provider, providerRefundID string) (*model.Refund, error)
	ListByTransaction(ctx context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error)
	CountByTransaction(ctx context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) (int64, error)

	// Merchant-scoped list (dashboard). Optional TransactionID filter via RefundListFilter.
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error)
	CountByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (int64, error)

	// ReserveAndCreate locks the transaction, validates refundable balance, inserts PENDING refund, reserves amount.
	ReserveAndCreate(ctx context.Context, merchantID, transactionID uuid.UUID, refund *model.Refund, outbox RefundOutboxHook, createdEvent model.MerchantWebhookEventType) (*model.Transaction, error)

	// FinalizeAfterProvider updates refund + transaction counters and runs outbox hook atomically.
	FinalizeAfterProvider(
		ctx context.Context,
		refundID uuid.UUID,
		newStatus model.RefundStatus,
		providerRefundID *string,
		failureCode, failureMessage *string,
		outbox RefundOutboxHook,
		eventType model.MerchantWebhookEventType,
	) (*model.Refund, *model.Transaction, error)

	// ProcessRefundWebhookAtomically handles inbound provider refund webhooks.
	ProcessRefundWebhookAtomically(
		ctx context.Context,
		webhook *model.WebhookEvent,
		provider, providerRefundID string,
		amount int64,
		currency string,
		targetStatus model.RefundStatus,
		outbox RefundOutboxHook,
		eventType model.MerchantWebhookEventType,
	) (model.WebhookEventStatus, error)
}

type pgRefundRepository struct {
	db *pgxpool.Pool
}

func NewRefundRepository(db *pgxpool.Pool) RefundRepository {
	return &pgRefundRepository{db: db}
}

const refundSelectCols = `id, merchant_id, transaction_id, amount, currency, status, provider,
	provider_refund_id, reason, failure_code, failure_message, idempotency_key_id,
	requested_at, succeeded_at, failed_at, created_at, updated_at`

func (r *pgRefundRepository) FindByID(ctx context.Context, id uuid.UUID) (*model.Refund, error) {
	q := fmt.Sprintf(`SELECT %s FROM refunds WHERE id = $1`, refundSelectCols)
	return scanRefund(r.db.QueryRow(ctx, q, id))
}

func (r *pgRefundRepository) FindByMerchantAndID(ctx context.Context, merchantID, refundID uuid.UUID) (*model.Refund, error) {
	q := fmt.Sprintf(`SELECT %s FROM refunds WHERE id = $1 AND merchant_id = $2`, refundSelectCols)
	ref, err := scanRefund(r.db.QueryRow(ctx, q, refundID, merchantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefundNotFound
	}
	return ref, err
}

func (r *pgRefundRepository) FindByProviderRefundID(ctx context.Context, provider, providerRefundID string) (*model.Refund, error) {
	q := fmt.Sprintf(`SELECT %s FROM refunds WHERE provider = $1 AND provider_refund_id = $2`, refundSelectCols)
	ref, err := scanRefund(r.db.QueryRow(ctx, q, provider, providerRefundID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefundNotFound
	}
	return ref, err
}

func (r *pgRefundRepository) ReserveAndCreate(ctx context.Context, merchantID, transactionID uuid.UUID, refund *model.Refund, outbox RefundOutboxHook, createdEvent model.MerchantWebhookEventType) (*model.Transaction, error) {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("refund repository begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	tx, err := lockTransactionForRefund(ctx, dbTx, merchantID, transactionID)
	if err != nil {
		return nil, err
	}
	if tx.Status != model.TransactionStatusPaid {
		return nil, ErrRefundNotAllowed
	}
	if refund.Currency != tx.Currency {
		return nil, ErrRefundCurrencyMismatch
	}
	available := tx.Amount - tx.RefundedAmount - tx.ReservedRefundAmount
	if refund.Amount > available {
		return nil, ErrRefundAmountExceeded
	}

	const insertRefund = `
		INSERT INTO refunds
		    (id, merchant_id, transaction_id, amount, currency, status, provider,
		     reason, idempotency_key_id, requested_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	if _, err := dbTx.Exec(ctx, insertRefund,
		refund.ID, refund.MerchantID, refund.TransactionID, refund.Amount, refund.Currency,
		refund.Status, refund.Provider, refund.Reason, refund.IdempotencyKeyID,
		refund.RequestedAt, refund.CreatedAt, refund.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("refund repository insert: %w", err)
	}

	const reserve = `
		UPDATE transactions
		SET reserved_refund_amount = reserved_refund_amount + $1,
		    updated_at = NOW()
		WHERE id = $2
		RETURNING ` + TransactionSelectCols
	updated, err := scanTransaction(dbTx.QueryRow(ctx, reserve, refund.Amount, transactionID))
	if err != nil {
		return nil, fmt.Errorf("refund repository reserve: %w", err)
	}
	if outbox != nil && createdEvent != "" {
		if err := outbox(ctx, dbTx, refund, updated, createdEvent); err != nil {
			return nil, err
		}
	}

	if err := dbTx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("refund repository commit reserve: %w", err)
	}
	return updated, nil
}

func (r *pgRefundRepository) FinalizeAfterProvider(
	ctx context.Context,
	refundID uuid.UUID,
	newStatus model.RefundStatus,
	providerRefundID *string,
	failureCode, failureMessage *string,
	outbox RefundOutboxHook,
	eventType model.MerchantWebhookEventType,
) (*model.Refund, *model.Transaction, error) {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("refund finalize begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	lockedRefund, err := scanRefund(dbTx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM refunds WHERE id = $1 FOR UPDATE`, refundSelectCols), refundID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrRefundNotFound
		}
		return nil, nil, err
	}
	if lockedRefund.Status == model.RefundStatusSucceeded ||
		lockedRefund.Status == model.RefundStatusFailed ||
		lockedRefund.Status == newStatus {
		tx, err := loadTransaction(ctx, dbTx, lockedRefund.TransactionID)
		if err != nil {
			return nil, nil, err
		}
		if err := dbTx.Commit(ctx); err != nil {
			return nil, nil, fmt.Errorf("refund finalize commit duplicate: %w", err)
		}
		return lockedRefund, tx, nil
	}

	ref, tx, err := r.applyRefundTransition(ctx, dbTx, refundID, newStatus, providerRefundID, failureCode, failureMessage)
	if err != nil {
		return nil, nil, err
	}
	if outbox != nil && eventType != "" {
		if err := outbox(ctx, dbTx, ref, tx, eventType); err != nil {
			return nil, nil, err
		}
	}
	if err := dbTx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("refund finalize commit: %w", err)
	}
	return ref, tx, nil
}

func (r *pgRefundRepository) ProcessRefundWebhookAtomically(
	ctx context.Context,
	webhook *model.WebhookEvent,
	provider, providerRefundID string,
	amount int64,
	currency string,
	targetStatus model.RefundStatus,
	outbox RefundOutboxHook,
	eventType model.MerchantWebhookEventType,
) (model.WebhookEventStatus, error) {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("refund webhook begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	insertedID, insertErr := insertWebhookEventTx(ctx, dbTx, webhook)
	if insertErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(insertErr, &pgErr) && pgErr.Code == "23505" {
			// The failed INSERT aborts this transaction. Roll it back via the
			// deferred cleanup, then read the already-processed event outside it.
			existing, findErr := r.findWebhookByProviderEvent(ctx, provider, webhook.EventID)
			if findErr != nil {
				return model.WebhookEventStatusIgnored, nil
			}
			return existing.Status, nil
		}
		return "", insertErr
	}

	findRefund := fmt.Sprintf(`SELECT %s FROM refunds WHERE provider = $1 AND provider_refund_id = $2 FOR UPDATE`, refundSelectCols)
	ref, err := scanRefund(dbTx.QueryRow(ctx, findRefund, provider, providerRefundID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			msg := "refund not found for provider_refund_id"
			_ = updateWebhookEventTx(ctx, dbTx, insertedID, nil, model.WebhookEventStatusFailed, &msg)
			_ = dbTx.Commit(ctx)
			return model.WebhookEventStatusFailed, ErrRefundNotFound
		}
		return "", err
	}

	if amount > 0 && amount != ref.Amount {
		msg := "refund webhook amount mismatch"
		_ = updateWebhookEventTx(ctx, dbTx, insertedID, &ref.TransactionID, model.WebhookEventStatusIgnored, &msg)
		_ = dbTx.Commit(ctx)
		return model.WebhookEventStatusIgnored, nil
	}
	if currency != "" && currency != ref.Currency {
		msg := "refund webhook currency mismatch"
		_ = updateWebhookEventTx(ctx, dbTx, insertedID, &ref.TransactionID, model.WebhookEventStatusIgnored, &msg)
		_ = dbTx.Commit(ctx)
		return model.WebhookEventStatusIgnored, nil
	}

	finalStatus := model.WebhookEventStatusProcessed
	if ref.Status == model.RefundStatusSucceeded || ref.Status == model.RefundStatusFailed {
		finalStatus = model.WebhookEventStatusIgnored
	} else if ref.Status == targetStatus {
		finalStatus = model.WebhookEventStatusIgnored
	} else {
		var tx *model.Transaction
		ref, tx, err = r.applyRefundTransition(ctx, dbTx, ref.ID, targetStatus, &providerRefundID, nil, nil)
		if err != nil {
			return "", err
		}
		if outbox != nil && eventType != "" {
			if err := outbox(ctx, dbTx, ref, tx, eventType); err != nil {
				return "", err
			}
		}
	}

	if err := updateWebhookEventTx(ctx, dbTx, insertedID, &ref.TransactionID, finalStatus, nil); err != nil {
		return "", err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return "", fmt.Errorf("refund webhook commit: %w", err)
	}
	return finalStatus, nil
}

func (r *pgRefundRepository) applyRefundTransition(
	ctx context.Context,
	dbTx pgx.Tx,
	refundID uuid.UUID,
	newStatus model.RefundStatus,
	providerRefundID *string,
	failureCode, failureMessage *string,
) (*model.Refund, *model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM refunds WHERE id = $1 FOR UPDATE`, refundSelectCols)
	ref, err := scanRefund(dbTx.QueryRow(ctx, q, refundID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrRefundNotFound
		}
		return nil, nil, err
	}
	if ref.Status == model.RefundStatusSucceeded {
		tx, err := loadTransaction(ctx, dbTx, ref.TransactionID)
		return ref, tx, err
	}

	tx, err := lockTransactionForRefund(ctx, dbTx, ref.MerchantID, ref.TransactionID)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()
	switch newStatus {
	case model.RefundStatusProcessing:
		if ref.Status != model.RefundStatusPending && ref.Status != model.RefundStatusProcessing {
			return ref, tx, nil
		}
		const u = `
			UPDATE refunds SET status = $1, provider_refund_id = COALESCE($2, provider_refund_id),
			    updated_at = $3 WHERE id = $4`
		if _, err := dbTx.Exec(ctx, u, model.RefundStatusProcessing, providerRefundID, now, refundID); err != nil {
			return nil, nil, err
		}
		ref.Status = model.RefundStatusProcessing
		ref.ProviderRefundID = providerRefundID

	case model.RefundStatusSucceeded:
		if ref.Status == model.RefundStatusSucceeded {
			return ref, tx, nil
		}
		const u = `
			UPDATE refunds SET status = $1, provider_refund_id = COALESCE($2, provider_refund_id),
			    succeeded_at = $3, updated_at = $3 WHERE id = $4`
		if _, err := dbTx.Exec(ctx, u, model.RefundStatusSucceeded, providerRefundID, now, refundID); err != nil {
			return nil, nil, err
		}
		const move = `
			UPDATE transactions
			SET reserved_refund_amount = reserved_refund_amount - $1,
			    refunded_amount = refunded_amount + $1,
			    updated_at = NOW()
			WHERE id = $2
			RETURNING ` + TransactionSelectCols
		tx, err = scanTransaction(dbTx.QueryRow(ctx, move, ref.Amount, ref.TransactionID))
		if err != nil {
			return nil, nil, err
		}
		ref.Status = model.RefundStatusSucceeded
		ref.SucceededAt = &now
		ref.ProviderRefundID = providerRefundID

	case model.RefundStatusFailed:
		if ref.Status == model.RefundStatusSucceeded {
			return ref, tx, nil
		}
		const u = `
			UPDATE refunds SET status = $1, failure_code = $2, failure_message = $3,
			    failed_at = $4, updated_at = $4 WHERE id = $5`
		if _, err := dbTx.Exec(ctx, u, model.RefundStatusFailed, failureCode, failureMessage, now, refundID); err != nil {
			return nil, nil, err
		}
		if ref.Status == model.RefundStatusPending || ref.Status == model.RefundStatusProcessing {
			const release = `
				UPDATE transactions
				SET reserved_refund_amount = reserved_refund_amount - $1,
				    updated_at = NOW()
				WHERE id = $2
				RETURNING ` + TransactionSelectCols
			tx, err = scanTransaction(dbTx.QueryRow(ctx, release, ref.Amount, ref.TransactionID))
			if err != nil {
				return nil, nil, err
			}
		}
		ref.Status = model.RefundStatusFailed
		ref.FailedAt = &now
	}

	ref2, err := scanRefund(dbTx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM refunds WHERE id = $1`, refundSelectCols), refundID))
	if err != nil {
		return nil, nil, err
	}
	return ref2, tx, nil
}

func lockTransactionForRefund(ctx context.Context, dbTx pgx.Tx, merchantID, transactionID uuid.UUID) (*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE id = $1 AND merchant_id = $2 FOR UPDATE`, TransactionSelectCols)
	tx, err := scanTransaction(dbTx.QueryRow(ctx, q, transactionID, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("lock transaction: %w", err)
	}
	return tx, nil
}

func loadTransaction(ctx context.Context, dbTx pgx.Tx, id uuid.UUID) (*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE id = $1`, TransactionSelectCols)
	return scanTransaction(dbTx.QueryRow(ctx, q, id))
}

func (r *pgRefundRepository) CountByTransaction(ctx context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) (int64, error) {
	q, args := buildRefundListQuery(true, merchantID, &transactionID, filter)
	var n int64
	if err := r.db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("refund count: %w", err)
	}
	return n, nil
}

func (r *pgRefundRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error) {
	var txID *uuid.UUID
	if filter.TransactionID != nil {
		txID = filter.TransactionID
	}
	q, args := buildRefundListQuery(false, merchantID, txID, filter)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("refund list by merchant: %w", err)
	}
	defer rows.Close()
	var out []*model.Refund
	for rows.Next() {
		ref, err := scanRefund(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (r *pgRefundRepository) CountByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (int64, error) {
	var txID *uuid.UUID
	if filter.TransactionID != nil {
		txID = filter.TransactionID
	}
	q, args := buildRefundListQuery(true, merchantID, txID, filter)
	var n int64
	if err := r.db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("refund count by merchant: %w", err)
	}
	return n, nil
}

func (r *pgRefundRepository) ListByTransaction(ctx context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error) {
	q, args := buildRefundListQuery(false, merchantID, &transactionID, filter)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("refund list: %w", err)
	}
	defer rows.Close()
	var out []*model.Refund
	for rows.Next() {
		ref, err := scanRefund(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func buildRefundListQuery(countOnly bool, merchantID uuid.UUID, transactionID *uuid.UUID, filter model.RefundListFilter) (string, []any) {
	args := []any{merchantID}
	where := "WHERE merchant_id = $1"
	pos := 1
	if transactionID != nil {
		pos++
		where += fmt.Sprintf(" AND transaction_id = $%d", pos)
		args = append(args, *transactionID)
	}
	if filter.Status != nil {
		pos++
		where += fmt.Sprintf(" AND status = $%d", pos)
		args = append(args, string(*filter.Status))
	}
	if filter.CreatedFrom != nil {
		pos++
		where += fmt.Sprintf(" AND created_at >= $%d", pos)
		args = append(args, *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		pos++
		where += fmt.Sprintf(" AND created_at < $%d", pos)
		args = append(args, *filter.CreatedTo)
	}
	if countOnly {
		return fmt.Sprintf("SELECT COUNT(*) FROM refunds %s", where), args
	}
	offset := (filter.Page - 1) * filter.Limit
	pos++
	limitPos := pos
	pos++
	offsetPos := pos
	args = append(args, filter.Limit, offset)
	return fmt.Sprintf(
		`SELECT %s FROM refunds %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		refundSelectCols, where, limitPos, offsetPos,
	), args
}

func scanRefund(row pgx.Row) (*model.Refund, error) {
	ref := &model.Refund{}
	err := row.Scan(
		&ref.ID, &ref.MerchantID, &ref.TransactionID, &ref.Amount, &ref.Currency, &ref.Status,
		&ref.Provider, &ref.ProviderRefundID, &ref.Reason, &ref.FailureCode, &ref.FailureMessage,
		&ref.IdempotencyKeyID, &ref.RequestedAt, &ref.SucceededAt, &ref.FailedAt,
		&ref.CreatedAt, &ref.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return ref, nil
}

func (r *pgRefundRepository) findWebhookByProviderEvent(ctx context.Context, provider, eventID string) (*model.WebhookEvent, error) {
	const q = `SELECT id, provider, event_id, event_type, transaction_id, provider_transaction_id,
		payload, signature, status, error_message, processed_at, created_at, updated_at
		FROM webhook_events WHERE provider = $1 AND event_id = $2`
	ev := &model.WebhookEvent{}
	err := r.db.QueryRow(ctx, q, provider, eventID).Scan(
		&ev.ID, &ev.Provider, &ev.EventID, &ev.EventType, &ev.TransactionID, &ev.ProviderTransactionID,
		&ev.Payload, &ev.Signature, &ev.Status, &ev.ErrorMessage, &ev.ProcessedAt, &ev.CreatedAt, &ev.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return ev, nil
}

// Shared helpers for webhook insert (reuse webhook_event_repository logic).
func insertWebhookEventTx(ctx context.Context, dbTx pgx.Tx, event *model.WebhookEvent) (uuid.UUID, error) {
	const q = `
		INSERT INTO webhook_events
		    (id, provider, event_id, event_type, transaction_id, provider_transaction_id,
		     payload, signature, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id`
	var id uuid.UUID
	err := dbTx.QueryRow(ctx, q,
		event.ID, event.Provider, event.EventID, event.EventType, event.TransactionID,
		event.ProviderTransactionID, event.Payload, event.Signature, event.Status,
		event.CreatedAt, event.UpdatedAt,
	).Scan(&id)
	return id, err
}
