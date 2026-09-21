package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DashboardOverviewRepository provides merchant-scoped aggregates for the dashboard.
type DashboardOverviewRepository interface {
	// CountPaymentsByStatus groups transactions by status for merchantID where
	// created_at ∈ [from, to).
	CountPaymentsByStatus(ctx context.Context, merchantID uuid.UUID, from, to time.Time) (model.DashboardPaymentStats, error)

	// SumPaidRevenue returns SUM(amount) for PAID transactions with
	// paid_at ∈ [from, to). Currency is taken from the dominant currency among
	// those rows (MVP: first non-null currency, default IDR when empty).
	SumPaidRevenue(ctx context.Context, merchantID uuid.UUID, from, to time.Time) (amount int64, currency string, err error)

	// SumSucceededRefunds returns count and SUM(amount) for SUCCEEDED refunds
	// where COALESCE(succeeded_at, created_at) ∈ [from, to).
	SumSucceededRefunds(ctx context.Context, merchantID uuid.UUID, from, to time.Time) (count, amount int64, currency string, err error)
}

type pgDashboardOverviewRepository struct {
	db *pgxpool.Pool
}

// NewDashboardOverviewRepository constructs a PostgreSQL-backed overview repository.
func NewDashboardOverviewRepository(db *pgxpool.Pool) DashboardOverviewRepository {
	return &pgDashboardOverviewRepository{db: db}
}

func (r *pgDashboardOverviewRepository) CountPaymentsByStatus(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) (model.DashboardPaymentStats, error) {
	const q = `
		SELECT status, COUNT(*)::bigint
		FROM transactions
		WHERE merchant_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		GROUP BY status
	`
	rows, err := r.db.Query(ctx, q, merchantID, from, to)
	if err != nil {
		return model.DashboardPaymentStats{}, fmt.Errorf("overview count payments: %w", err)
	}
	defer rows.Close()

	var stats model.DashboardPaymentStats
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return model.DashboardPaymentStats{}, fmt.Errorf("overview count payments scan: %w", err)
		}
		stats.Total += n
		switch model.TransactionStatus(status) {
		case model.TransactionStatusCreated:
			stats.Created = n
		case model.TransactionStatusPending:
			stats.Pending = n
		case model.TransactionStatusPaid:
			stats.Paid = n
		case model.TransactionStatusFailed:
			stats.Failed = n
		case model.TransactionStatusExpired:
			stats.Expired = n
		case model.TransactionStatusCancelled:
			stats.Cancelled = n
		}
	}
	if err := rows.Err(); err != nil {
		return model.DashboardPaymentStats{}, fmt.Errorf("overview count payments rows: %w", err)
	}
	return stats, nil
}

func (r *pgDashboardOverviewRepository) SumPaidRevenue(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) (int64, string, error) {
	const q = `
		SELECT
			COALESCE(SUM(amount), 0)::bigint,
			COALESCE(
				(SELECT currency FROM transactions
				 WHERE merchant_id = $1 AND status = 'PAID'
				   AND paid_at IS NOT NULL
				   AND paid_at >= $2 AND paid_at < $3
				 ORDER BY paid_at DESC
				 LIMIT 1),
				'IDR'
			)
		FROM transactions
		WHERE merchant_id = $1
		  AND status = 'PAID'
		  AND paid_at IS NOT NULL
		  AND paid_at >= $2
		  AND paid_at < $3
	`
	var amount int64
	var currency string
	if err := r.db.QueryRow(ctx, q, merchantID, from, to).Scan(&amount, &currency); err != nil {
		return 0, "", fmt.Errorf("overview sum revenue: %w", err)
	}
	return amount, currency, nil
}

func (r *pgDashboardOverviewRepository) SumSucceededRefunds(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) (int64, int64, string, error) {
	const q = `
		SELECT
			COUNT(*)::bigint,
			COALESCE(SUM(amount), 0)::bigint,
			COALESCE(
				(SELECT currency FROM refunds
				 WHERE merchant_id = $1 AND status = 'SUCCEEDED'
				   AND COALESCE(succeeded_at, created_at) >= $2
				   AND COALESCE(succeeded_at, created_at) < $3
				 ORDER BY COALESCE(succeeded_at, created_at) DESC
				 LIMIT 1),
				'IDR'
			)
		FROM refunds
		WHERE merchant_id = $1
		  AND status = 'SUCCEEDED'
		  AND COALESCE(succeeded_at, created_at) >= $2
		  AND COALESCE(succeeded_at, created_at) < $3
	`
	var count, amount int64
	var currency string
	if err := r.db.QueryRow(ctx, q, merchantID, from, to).Scan(&count, &amount, &currency); err != nil {
		return 0, 0, "", fmt.Errorf("overview sum refunds: %w", err)
	}
	return count, amount, currency, nil
}
