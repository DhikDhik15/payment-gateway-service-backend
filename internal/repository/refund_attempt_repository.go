package repository

import (
	"context"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RefundAttemptRepository interface {
	Create(ctx context.Context, attempt *model.RefundAttempt) error
	CountByRefundID(ctx context.Context, refundID uuid.UUID) (int, error)
}

type pgRefundAttemptRepository struct {
	db *pgxpool.Pool
}

func NewRefundAttemptRepository(db *pgxpool.Pool) RefundAttemptRepository {
	return &pgRefundAttemptRepository{db: db}
}

func (r *pgRefundAttemptRepository) Create(ctx context.Context, a *model.RefundAttempt) error {
	const q = `
		INSERT INTO refund_attempts
		    (id, refund_id, provider, provider_refund_id, request_payload, response_payload,
		     status, attempt_number, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
	_, err := r.db.Exec(ctx, q,
		a.ID, a.RefundID, a.Provider, a.ProviderRefundID, a.RequestPayload, a.ResponsePayload,
		a.Status, a.AttemptNumber, a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("refund attempt create: %w", err)
	}
	return nil
}

func (r *pgRefundAttemptRepository) CountByRefundID(ctx context.Context, refundID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM refund_attempts WHERE refund_id = $1`
	var n int
	if err := r.db.QueryRow(ctx, q, refundID).Scan(&n); err != nil {
		return 0, fmt.Errorf("refund attempt count: %w", err)
	}
	return n, nil
}
