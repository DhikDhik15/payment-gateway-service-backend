package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxStatusUpdater applies transaction status changes and merchant webhook
// outbox inserts in a single PostgreSQL transaction.
type OutboxStatusUpdater struct {
	db        *pgxpool.Pool
	publisher MerchantWebhookPublisher
}

// NewOutboxStatusUpdater constructs an atomic status+outbox helper.
func NewOutboxStatusUpdater(db *pgxpool.Pool, publisher MerchantWebhookPublisher) *OutboxStatusUpdater {
	return &OutboxStatusUpdater{db: db, publisher: publisher}
}

// UpdateStatus transitions status and enqueues the matching webhook event.
func (u *OutboxStatusUpdater) UpdateStatus(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus) error {
	return u.withTx(ctx, func(dbTx pgx.Tx) (*model.Transaction, model.TransactionStatus, error) {
		const q = `
			UPDATE transactions
			SET status = $1, updated_at = NOW()
			WHERE id = $2 AND status = $3
			RETURNING id, merchant_id, merchant_order_id, amount, currency,
			          payment_method, provider, provider_transaction_id, payment_url,
			          status, expired_at, paid_at, refunded_amount, reserved_refund_amount,
			          created_at, updated_at`
		tx, err := scanTxRow(dbTx.QueryRow(ctx, q, to, id, from))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "", repository.ErrTransactionNotFound
			}
			return nil, "", err
		}
		return tx, to, nil
	})
}

// UpdateStatusWithProvider transitions CREATED→PENDING and persists provider fields + expired_at.
func (u *OutboxStatusUpdater) UpdateStatusWithProvider(
	ctx context.Context,
	id uuid.UUID,
	from, to model.TransactionStatus,
	provider, providerTransactionID, paymentURL string,
	expiredAt *time.Time,
) error {
	return u.withTx(ctx, func(dbTx pgx.Tx) (*model.Transaction, model.TransactionStatus, error) {
		const q = `
			UPDATE transactions
			SET status = $1,
			    provider = $2,
			    provider_transaction_id = $3,
			    payment_url = $4,
			    expired_at = COALESCE($5, expired_at),
			    updated_at = NOW()
			WHERE id = $6 AND status = $7
			RETURNING id, merchant_id, merchant_order_id, amount, currency,
			          payment_method, provider, provider_transaction_id, payment_url,
			          status, expired_at, paid_at, refunded_amount, reserved_refund_amount,
			          created_at, updated_at`
		tx, err := scanTxRow(dbTx.QueryRow(ctx, q, to, provider, providerTransactionID, paymentURL, expiredAt, id, from))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "", repository.ErrTransactionNotFound
			}
			return nil, "", err
		}
		return tx, to, nil
	})
}

// CreateCreated inserts a CREATED transaction and enqueues payment.created.
func (u *OutboxStatusUpdater) CreateCreated(ctx context.Context, tx *model.Transaction) error {
	dbTx, err := u.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("outbox begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	const q = `
		INSERT INTO transactions
		    (id, merchant_id, merchant_order_id, amount, currency,
		     payment_method, provider, provider_transaction_id, payment_url,
		     status, expired_at, paid_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`
	if _, err := dbTx.Exec(ctx, q,
		tx.ID, tx.MerchantID, tx.MerchantOrderID, tx.Amount, tx.Currency,
		tx.PaymentMethod, tx.Provider, tx.ProviderTransactionID, tx.PaymentURL,
		tx.Status, tx.ExpiredAt, tx.PaidAt, tx.CreatedAt, tx.UpdatedAt,
	); err != nil {
		return fmt.Errorf("outbox create transaction: %w", err)
	}
	if u.publisher != nil {
		if err := u.publisher.EnqueueInTx(ctx, dbTx, tx, model.MerchantWebhookEventPaymentCreated); err != nil {
			return err
		}
	}
	return dbTx.Commit(ctx)
}

func (u *OutboxStatusUpdater) withTx(
	ctx context.Context,
	mutate func(dbTx pgx.Tx) (*model.Transaction, model.TransactionStatus, error),
) error {
	dbTx, err := u.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("outbox begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	tx, to, err := mutate(dbTx)
	if err != nil {
		return err
	}
	if u.publisher != nil {
		eventType, ok := model.EventTypeForStatus(to)
		if ok {
			if err := u.publisher.EnqueueInTx(ctx, dbTx, tx, eventType); err != nil {
				return err
			}
		}
	}
	return dbTx.Commit(ctx)
}

func scanTxRow(row pgx.Row) (*model.Transaction, error) {
	tx := &model.Transaction{}
	err := row.Scan(
		&tx.ID, &tx.MerchantID, &tx.MerchantOrderID, &tx.Amount, &tx.Currency,
		&tx.PaymentMethod, &tx.Provider, &tx.ProviderTransactionID, &tx.PaymentURL,
		&tx.Status, &tx.ExpiredAt, &tx.PaidAt, &tx.RefundedAmount, &tx.ReservedRefundAmount,
		&tx.CreatedAt, &tx.UpdatedAt,
	)
	return tx, err
}

// EnqueueForTransaction is a nil-safe helper for non-atomic fallback paths.
func EnqueueForTransaction(ctx context.Context, publisher MerchantWebhookPublisher, tx *model.Transaction, status model.TransactionStatus) {
	if publisher == nil || tx == nil {
		return
	}
	eventType, ok := model.EventTypeForStatus(status)
	if !ok {
		return
	}
	_ = publisher.Enqueue(ctx, tx, eventType)
}
