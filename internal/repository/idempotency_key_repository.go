package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrIdempotencyNotFound = errors.New("idempotency key not found")
	ErrIdempotencyConflict = errors.New("idempotency key already exists")
)

type IdempotencyKeyRepository interface {
	GetByMerchantAndKey(ctx context.Context, merchantID uuid.UUID, key string) (*model.IdempotencyKey, error)
	Create(ctx context.Context, key *model.IdempotencyKey) error
	Reserve(ctx context.Context, key *model.IdempotencyKey) (bool, error)
	UpdateProcessing(ctx context.Context, id uuid.UUID) error
	AttachRefund(ctx context.Context, id, refundID uuid.UUID) error
	Complete(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, transactionID *uuid.UUID, refundID *uuid.UUID) error
	Fail(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, transactionID *uuid.UUID, refundID *uuid.UUID) error
}

type pgIdempotencyKeyRepository struct {
	db *pgxpool.Pool
}

func NewIdempotencyKeyRepository(db *pgxpool.Pool) IdempotencyKeyRepository {
	return &pgIdempotencyKeyRepository{db: db}
}

// Reserve atomically claims a new key, or replaces an expired row. An active
// conflict returns false so the caller can inspect the existing state.
func (r *pgIdempotencyKeyRepository) Reserve(ctx context.Context, key *model.IdempotencyKey) (bool, error) {
	const q = `
		INSERT INTO idempotency_keys
			(id, merchant_id, key, request_hash, status, created_at, updated_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (merchant_id, key) DO UPDATE
		SET request_hash = EXCLUDED.request_hash,
			status = EXCLUDED.status,
			response_status = NULL,
			response_body = NULL,
			transaction_id = NULL,
			refund_id = NULL,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			expires_at = EXCLUDED.expires_at
		WHERE idempotency_keys.expires_at <= NOW()
		RETURNING id
	`
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, q, key.ID, key.MerchantID, key.Key, key.RequestHash, key.Status, key.CreatedAt, key.UpdatedAt, key.ExpiresAt).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("idempotency repository reserve: %w", err)
	}
	key.ID = id
	return true, nil
}

func (r *pgIdempotencyKeyRepository) Create(ctx context.Context, key *model.IdempotencyKey) error {
	const q = `
		INSERT INTO idempotency_keys
			(id, merchant_id, key, request_hash, status, response_status, response_body, transaction_id, created_at, updated_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`
	_, err := r.db.Exec(ctx, q, key.ID, key.MerchantID, key.Key, key.RequestHash, key.Status, key.ResponseStatus, key.ResponseBody, key.TransactionID, key.CreatedAt, key.UpdatedAt, key.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrIdempotencyConflict
		}
		return fmt.Errorf("idempotency repository create: %w", err)
	}
	return nil
}

func (r *pgIdempotencyKeyRepository) GetByMerchantAndKey(ctx context.Context, merchantID uuid.UUID, key string) (*model.IdempotencyKey, error) {
	const q = `
		SELECT id, merchant_id, key, request_hash, status, response_status, response_body, transaction_id, refund_id, created_at, updated_at, expires_at
		FROM idempotency_keys
		WHERE merchant_id = $1 AND key = $2
	`
	row := &model.IdempotencyKey{}
	err := r.db.QueryRow(ctx, q, merchantID, key).Scan(&row.ID, &row.MerchantID, &row.Key, &row.RequestHash, &row.Status, &row.ResponseStatus, &row.ResponseBody, &row.TransactionID, &row.RefundID, &row.CreatedAt, &row.UpdatedAt, &row.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrIdempotencyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("idempotency repository get: %w", err)
	}
	return row, nil
}

func (r *pgIdempotencyKeyRepository) UpdateProcessing(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE idempotency_keys SET status = 'PROCESSING', updated_at = NOW() WHERE id = $1`
	if _, err := r.db.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("idempotency repository update processing: %w", err)
	}
	return nil
}

func (r *pgIdempotencyKeyRepository) AttachRefund(ctx context.Context, id, refundID uuid.UUID) error {
	const q = `UPDATE idempotency_keys SET refund_id = $2, updated_at = NOW() WHERE id = $1 AND status = 'PROCESSING'`
	if _, err := r.db.Exec(ctx, q, id, refundID); err != nil {
		return fmt.Errorf("idempotency repository attach refund: %w", err)
	}
	return nil
}

func (r *pgIdempotencyKeyRepository) Complete(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, transactionID *uuid.UUID, refundID *uuid.UUID) error {
	const q = `UPDATE idempotency_keys SET status = 'COMPLETED', response_status = $2, response_body = $3, transaction_id = $4, refund_id = $5, updated_at = NOW() WHERE id = $1`
	if _, err := r.db.Exec(ctx, q, id, responseStatus, responseBody, transactionID, refundID); err != nil {
		return fmt.Errorf("idempotency repository complete: %w", err)
	}
	return nil
}

func (r *pgIdempotencyKeyRepository) Fail(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, transactionID *uuid.UUID, refundID *uuid.UUID) error {
	const q = `UPDATE idempotency_keys SET status = 'FAILED', response_status = $2, response_body = $3, transaction_id = $4, refund_id = $5, updated_at = NOW() WHERE id = $1`
	if _, err := r.db.Exec(ctx, q, id, responseStatus, responseBody, transactionID, refundID); err != nil {
		return fmt.Errorf("idempotency repository fail: %w", err)
	}
	return nil
}
