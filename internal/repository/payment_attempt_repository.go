package repository

import (
	"context"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PaymentAttemptRepository defines the database operations for payment attempts.
type PaymentAttemptRepository interface {
	// Create inserts a new payment_attempt row.
	Create(ctx context.Context, attempt *model.PaymentAttempt) error

	// FindByTransactionID returns all attempts for a transaction, ordered by
	// attempt_number ascending (oldest first).
	FindByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]model.PaymentAttempt, error)

	// CountByTransactionID returns how many attempts exist for a transaction.
	// Used to derive the next attempt_number without a separate SELECT.
	CountByTransactionID(ctx context.Context, transactionID uuid.UUID) (int, error)
}

// pgPaymentAttemptRepository is the PostgreSQL implementation.
type pgPaymentAttemptRepository struct {
	db *pgxpool.Pool
}

// NewPaymentAttemptRepository returns a PostgreSQL-backed PaymentAttemptRepository.
func NewPaymentAttemptRepository(db *pgxpool.Pool) PaymentAttemptRepository {
	return &pgPaymentAttemptRepository{db: db}
}

// Create inserts a payment_attempt row.
func (r *pgPaymentAttemptRepository) Create(ctx context.Context, a *model.PaymentAttempt) error {
	const q = `
		INSERT INTO payment_attempts
		    (id, transaction_id, provider, provider_transaction_id,
		     request_payload, response_payload, status, attempt_number,
		     created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`
	_, err := r.db.Exec(ctx, q,
		a.ID,
		a.TransactionID,
		a.Provider,
		a.ProviderTransactionID,
		a.RequestPayload,
		a.ResponsePayload,
		a.Status,
		a.AttemptNumber,
		a.CreatedAt,
		a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("payment attempt repository create: %w", err)
	}
	return nil
}

// FindByTransactionID returns all attempts ordered by attempt_number ASC.
func (r *pgPaymentAttemptRepository) FindByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]model.PaymentAttempt, error) {
	const q = `
		SELECT id, transaction_id, provider, provider_transaction_id,
		       request_payload, response_payload, status, attempt_number,
		       created_at, updated_at
		FROM   payment_attempts
		WHERE  transaction_id = $1
		ORDER  BY attempt_number ASC
	`
	rows, err := r.db.Query(ctx, q, transactionID)
	if err != nil {
		return nil, fmt.Errorf("payment attempt repository find by transaction id: %w", err)
	}
	defer rows.Close()

	var attempts []model.PaymentAttempt
	for rows.Next() {
		a, err := scanPaymentAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("payment attempt repository scan: %w", err)
		}
		attempts = append(attempts, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payment attempt repository rows error: %w", err)
	}
	return attempts, nil
}

// CountByTransactionID returns the number of attempts for a transaction.
func (r *pgPaymentAttemptRepository) CountByTransactionID(ctx context.Context, transactionID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM payment_attempts WHERE transaction_id = $1`
	var count int
	if err := r.db.QueryRow(ctx, q, transactionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("payment attempt repository count: %w", err)
	}
	return count, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func scanPaymentAttempt(row pgx.Row) (*model.PaymentAttempt, error) {
	a := &model.PaymentAttempt{}
	err := row.Scan(
		&a.ID,
		&a.TransactionID,
		&a.Provider,
		&a.ProviderTransactionID,
		&a.RequestPayload,
		&a.ResponsePayload,
		&a.Status,
		&a.AttemptNumber,
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return a, nil
}
