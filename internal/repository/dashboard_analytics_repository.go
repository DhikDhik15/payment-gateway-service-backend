package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DashboardAnalyticsRepository provides merchant-scoped analytics aggregates.
// Every method filters by merchant_id from the authenticated session and the
// half-open UTC window [from, to). All aggregation happens in the database;
// no order or payment rows are loaded into memory for summation.
type DashboardAnalyticsRepository interface {
	// PaymentSummary returns gross paid revenue and the PAID transaction
	// count for paid_at ∈ [from, to). Currency follows the overview rule
	// (latest paid row, default IDR when empty).
	PaymentSummary(ctx context.Context, merchantID uuid.UUID, from, to time.Time) (revenue int64, paid int64, currency string, err error)

	// OrderSummary returns order-attributed paid value, unique paid-order
	// count, and total units sold for orders whose single PAID transaction
	// has paid_at ∈ [from, to). Multiple failed attempts never duplicate:
	// uq_transactions_order_paid allows at most one PAID row per order.
	OrderSummary(ctx context.Context, merchantID uuid.UUID, from, to time.Time) (orderRevenue, paidOrders, unitsSold int64, err error)

	// RevenueTrend returns one zero-filled bucket per day (windows up to 62
	// days) or per Monday-start week (longer windows), covering [from, to).
	RevenueTrend(ctx context.Context, merchantID uuid.UUID, from, to time.Time) (interval string, buckets []model.DashboardRevenueBucket, err error)

	// TopProductsByUnits returns at most 5 products ranked by units sold.
	// Quantities and revenue come from immutable order-item snapshots of
	// paid orders only; the displayed name is the latest snapshot.
	TopProductsByUnits(ctx context.Context, merchantID uuid.UUID, from, to time.Time) ([]model.DashboardProductSales, error)

	// RevenueByProduct returns per-product snapshot aggregates ranked by
	// revenue descending, bounded to 50 rows.
	RevenueByProduct(ctx context.Context, merchantID uuid.UUID, from, to time.Time) ([]model.DashboardProductSales, error)
}

type pgDashboardAnalyticsRepository struct {
	db *pgxpool.Pool
}

// NewDashboardAnalyticsRepository constructs a PostgreSQL-backed repository.
func NewDashboardAnalyticsRepository(db *pgxpool.Pool) DashboardAnalyticsRepository {
	return &pgDashboardAnalyticsRepository{db: db}
}

func (r *pgDashboardAnalyticsRepository) PaymentSummary(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) (int64, int64, string, error) {
	const q = `
		SELECT
			COALESCE(SUM(amount), 0)::bigint,
			COUNT(*)::bigint,
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
	var revenue, paid int64
	var currency string
	if err := r.db.QueryRow(ctx, q, merchantID, from, to).Scan(&revenue, &paid, &currency); err != nil {
		return 0, 0, "", fmt.Errorf("analytics payment summary: %w", err)
	}
	return revenue, paid, currency, nil
}

func (r *pgDashboardAnalyticsRepository) OrderSummary(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) (int64, int64, int64, error) {
	const q = `
		WITH paid_orders AS (
			SELECT DISTINCT t.order_id AS order_id
			FROM transactions t
			WHERE t.merchant_id = $1
			  AND t.status = 'PAID'
			  AND t.order_id IS NOT NULL
			  AND t.paid_at IS NOT NULL
			  AND t.paid_at >= $2 AND t.paid_at < $3
		)
		SELECT
			COALESCE((
				SELECT SUM(t.amount)::bigint
				FROM transactions t
				JOIN paid_orders o ON o.order_id = t.order_id AND t.merchant_id = $1
				WHERE t.status = 'PAID'
			), 0),
			(SELECT COUNT(*)::bigint FROM paid_orders),
			COALESCE((
				SELECT SUM(oi.quantity)::bigint
				FROM order_items oi
				JOIN paid_orders o ON o.order_id = oi.order_id AND oi.merchant_id = $1
			), 0)
	`
	var orderRevenue, paidOrders, unitsSold int64
	if err := r.db.QueryRow(ctx, q, merchantID, from, to).Scan(&orderRevenue, &paidOrders, &unitsSold); err != nil {
		return 0, 0, 0, fmt.Errorf("analytics order summary: %w", err)
	}
	return orderRevenue, paidOrders, unitsSold, nil
}

func (r *pgDashboardAnalyticsRepository) RevenueTrend(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) (string, []model.DashboardRevenueBucket, error) {
	interval := "day"
	step := "1 day"
	seriesStart := "($2::timestamptz)"
	bucketExpr := "d::date"
	joinFrom := "d"
	joinTo := "d + interval '1 day'"
	if to.Sub(from) > 62*24*time.Hour {
		interval = "week"
		step = "1 week"
		seriesStart = "(date_trunc('week', $2::timestamptz))"
		bucketExpr = "(d::date)"
		joinFrom = "d"
		joinTo = "d + interval '1 week'"
	}
	q := fmt.Sprintf(`
		SELECT %s AS bucket,
			COALESCE(SUM(t.amount), 0)::bigint AS revenue
		FROM generate_series(%s, $3::timestamptz - interval '1 millisecond', '%s') d
		LEFT JOIN transactions t
		  ON t.merchant_id = $1
		 AND t.status = 'PAID'
		 AND t.paid_at IS NOT NULL
		 AND t.paid_at >= %s AND t.paid_at < %s
		GROUP BY d
		ORDER BY d
	`, bucketExpr, seriesStart, step, joinFrom, joinTo)
	rows, err := r.db.Query(ctx, q, merchantID, from, to)
	if err != nil {
		return "", nil, fmt.Errorf("analytics revenue trend: %w", err)
	}
	defer rows.Close()

	buckets := []model.DashboardRevenueBucket{}
	for rows.Next() {
		var day time.Time
		var revenue int64
		if err := rows.Scan(&day, &revenue); err != nil {
			return "", nil, fmt.Errorf("analytics revenue trend scan: %w", err)
		}
		buckets = append(buckets, model.DashboardRevenueBucket{
			Date:    day.UTC().Format("2006-01-02"),
			Revenue: revenue,
		})
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("analytics revenue trend rows: %w", err)
	}
	return interval, buckets, nil
}

// productSalesQuery aggregates order-item snapshots of paid orders in the
// window. Identity is the catalog product_id when present, else the snapshot
// name (covers deleted products whose product_id was SET NULL). Display name
// is the latest snapshot so renames stay recognizable; quantities and revenue
// always come from the immutable snapshots, never live catalog prices.
const productSalesQuery = `
	SELECT
		(array_agg(oi.product_id ORDER BY oi.created_at DESC))[1] AS product_id,
		(array_agg(oi.product_name ORDER BY oi.created_at DESC))[1] AS product_name,
		SUM(oi.quantity)::bigint AS units_sold,
		SUM(oi.subtotal)::bigint AS revenue
	FROM order_items oi
	WHERE oi.merchant_id = $1
	  AND EXISTS (
		SELECT 1 FROM transactions t
		WHERE t.merchant_id = oi.merchant_id
		  AND t.order_id = oi.order_id
		  AND t.status = 'PAID'
		  AND t.paid_at IS NOT NULL
		  AND t.paid_at >= $2 AND t.paid_at < $3
	  )
	GROUP BY COALESCE(oi.product_id::text, 'name:' || oi.product_name)
`

func (r *pgDashboardAnalyticsRepository) scanProductSales(rows pgx.Rows) ([]model.DashboardProductSales, error) {
	defer rows.Close()
	out := []model.DashboardProductSales{}
	for rows.Next() {
		var row model.DashboardProductSales
		if err := rows.Scan(&row.ProductID, &row.ProductName, &row.UnitsSold, &row.Revenue); err != nil {
			return nil, fmt.Errorf("analytics product sales scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics product sales rows: %w", err)
	}
	return out, nil
}

func (r *pgDashboardAnalyticsRepository) TopProductsByUnits(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) ([]model.DashboardProductSales, error) {
	rows, err := r.db.Query(ctx, productSalesQuery+`
		ORDER BY units_sold DESC, revenue DESC
		LIMIT 5
	`, merchantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics top products: %w", err)
	}
	return r.scanProductSales(rows)
}

func (r *pgDashboardAnalyticsRepository) RevenueByProduct(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to time.Time,
) ([]model.DashboardProductSales, error) {
	rows, err := r.db.Query(ctx, productSalesQuery+`
		ORDER BY revenue DESC, units_sold DESC
		LIMIT 50
	`, merchantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics revenue by product: %w", err)
	}
	return r.scanProductSales(rows)
}
