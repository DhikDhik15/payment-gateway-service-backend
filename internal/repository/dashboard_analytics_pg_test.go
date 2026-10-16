package repository_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Phase 10L: PG-backed proof of the analytics aggregates against real SQL.
//
// Requires TEST_DATABASE_URL pointing at a database migrated through 000024.
// Skips otherwise, like the other *_pg_test.go suites.

func analyticsPool(t *testing.T) *pgxpool.Pool {
	return overviewTestPool(t)
}

func analyticsProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, merchantID uuid.UUID, name string, price int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO products (id, merchant_id, name, price, currency, active)
		VALUES ($1, $2, $3, $4, 'IDR', true)`,
		id, merchantID, name, price,
	); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return id
}

var analyticsOrderSeq = 0

func analyticsOrder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, merchantID uuid.UUID, total int64) uuid.UUID {
	t.Helper()
	analyticsOrderSeq++
	id := uuid.New()
	hash := fmt.Sprintf("%064d", analyticsOrderSeq)
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (id, merchant_id, status, currency, subtotal, total_amount, idempotency_key, request_hash)
		VALUES ($1, $2, 'CREATED', 'IDR', $3, $3, $4, $5)`,
		id, merchantID, total, uuid.NewString(), hash,
	); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return id
}

func analyticsItem(t *testing.T, ctx context.Context, pool *pgxpool.Pool, merchantID, orderID uuid.UUID, productID *uuid.UUID, name string, qty, unitPrice int64, createdAt time.Time) {
	t.Helper()
	var created any
	if createdAt.IsZero() {
		created = time.Now().UTC()
	} else {
		created = createdAt
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO order_items (merchant_id, order_id, product_id, product_name, quantity, unit_price, subtotal, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		merchantID, orderID, productID, name, qty, unitPrice, qty*unitPrice, created,
	); err != nil {
		t.Fatalf("seed order item: %v", err)
	}
}

func analyticsTxn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, merchantID uuid.UUID, prefix, status string, amount int64, orderID *uuid.UUID, at time.Time, paidAt *time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO transactions
			(merchant_id, merchant_order_id, amount, currency, payment_method, status, order_id, paid_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'IDR', 'QRIS', $4, $5, $6, $7, $7)`,
		merchantID, prefix+"-"+uuid.NewString()[:6], amount, status, orderID, paidAt, at,
	); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
}

// Window under test: Oct 8–14 2026 inclusive → [2026-10-08, 2026-10-15).
func analyticsWindow() (from, to time.Time) {
	return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
}

func TestDashboardAnalyticsPG_SummaryAndProducts(t *testing.T) {
	pool := analyticsPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardAnalyticsRepository(pool)
	merchantA := overviewSeedMerchant(t, ctx, pool)
	merchantB := overviewSeedMerchant(t, ctx, pool)
	from, to := analyticsWindow()

	// Live catalog product priced 50000; snapshots stay at historical 45000.
	kopi := analyticsProduct(t, ctx, pool, merchantA, "Kopi", 50000)

	// O1: retried order — FAILED attempt + single PAID. Units counted once.
	o1 := analyticsOrder(t, ctx, pool, merchantA, 120000)
	analyticsItem(t, ctx, pool, merchantA, o1, &kopi, "Kopi", 2, 45000, time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC))
	analyticsItem(t, ctx, pool, merchantA, o1, nil, "Teh Botol", 3, 10000, time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC))
	failedAt := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantA, "a1-fail", "FAILED", 120000, &o1, failedAt, nil)
	paidO1 := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantA, "a1-paid", "PAID", 120000, &o1, paidO1, &paidO1)

	// O2: paid on the final calendar day.
	o2 := analyticsOrder(t, ctx, pool, merchantA, 45000)
	analyticsItem(t, ctx, pool, merchantA, o2, &kopi, "Kopi", 1, 45000, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	paidO2 := time.Date(2026, 10, 14, 23, 59, 59, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantA, "a2-paid", "PAID", 45000, &o2, paidO2, &paidO2)

	// O5: renamed-product snapshot groups under the live product id.
	o5 := analyticsOrder(t, ctx, pool, merchantA, 45000)
	analyticsItem(t, ctx, pool, merchantA, o5, &kopi, "Kopi Lama", 1, 45000, time.Date(2024, 5, 5, 5, 0, 0, 0, time.UTC))
	paidO5 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantA, "a5-paid", "PAID", 45000, &o5, paidO5, &paidO5)

	// O3: paid before the window — excluded everywhere.
	o3 := analyticsOrder(t, ctx, pool, merchantA, 225000)
	analyticsItem(t, ctx, pool, merchantA, o3, &kopi, "Kopi", 5, 45000, time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC))
	paidO3 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantA, "a3-paid", "PAID", 225000, &o3, paidO3, &paidO3)

	// O4: unpaid (PENDING) — never a sale.
	o4 := analyticsOrder(t, ctx, pool, merchantA, 450000)
	analyticsItem(t, ctx, pool, merchantA, o4, &kopi, "Kopi", 10, 45000, time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC))
	analyticsTxn(t, ctx, pool, merchantA, "a4-pend", "PENDING", 450000, &o4,
		time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC), nil)

	// Legacy order-less payment: gross revenue only, never units/orders.
	legacyPaid := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantA, "a-leg", "PAID", 20000, nil, legacyPaid, &legacyPaid)

	// Other merchant: must not leak into A.
	bo := analyticsOrder(t, ctx, pool, merchantB, 45000)
	analyticsItem(t, ctx, pool, merchantB, bo, nil, "Mie", 9, 5000, time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	bPaid := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	analyticsTxn(t, ctx, pool, merchantB, "b-paid", "PAID", 45000, &bo, bPaid, &bPaid)

	// — PaymentSummary: gross revenue + PAID row count.
	revenue, paid, currency, err := repo.PaymentSummary(ctx, merchantA, from, to)
	if err != nil {
		t.Fatalf("PaymentSummary: %v", err)
	}
	if revenue != 230000 || paid != 4 || currency != "IDR" {
		t.Fatalf("payment summary = (%d, %d, %s), want (230000, 4, IDR)", revenue, paid, currency)
	}

	// — OrderSummary: order-attributed value, unique orders, snapshot units.
	orderRevenue, paidOrders, units, err := repo.OrderSummary(ctx, merchantA, from, to)
	if err != nil {
		t.Fatalf("OrderSummary: %v", err)
	}
	if orderRevenue != 210000 || paidOrders != 3 || units != 7 {
		t.Fatalf("order summary = (%d, %d, %d), want (210000, 3, 7)", orderRevenue, paidOrders, units)
	}

	// — Trend: daily buckets, zero-filled, gross revenue by paid_at.
	interval, trend, err := repo.RevenueTrend(ctx, merchantA, from, to)
	if err != nil {
		t.Fatalf("RevenueTrend: %v", err)
	}
	if interval != "day" {
		t.Fatalf("interval = %q, want day", interval)
	}
	wantTrend := map[string]int64{
		"2026-10-08": 0, "2026-10-09": 0, "2026-10-10": 120000, "2026-10-11": 20000,
		"2026-10-12": 45000, "2026-10-13": 0, "2026-10-14": 45000,
	}
	if len(trend) != len(wantTrend) {
		t.Fatalf("trend has %d buckets, want %d: %+v", len(trend), len(wantTrend), trend)
	}
	for _, b := range trend {
		want, ok := wantTrend[b.Date]
		if !ok || want != b.Revenue {
			t.Fatalf("bucket %s = %d, want %d", b.Date, b.Revenue, want)
		}
	}

	// — Top products: ranked by units; snapshot prices and latest name.
	top, err := repo.TopProductsByUnits(ctx, merchantA, from, to)
	if err != nil {
		t.Fatalf("TopProductsByUnits: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("top products = %d rows, want 2: %+v", len(top), top)
	}
	if top[0].ProductName != "Kopi" || top[0].UnitsSold != 4 || top[0].Revenue != 180000 {
		t.Fatalf("top[0] wrong (want Kopi/4/180000 snapshot): %+v", top[0])
	}
	if top[0].ProductID == nil || *top[0].ProductID != kopi {
		t.Fatalf("top[0] product id wrong: %+v", top[0].ProductID)
	}
	if top[1].ProductName != "Teh Botol" || top[1].UnitsSold != 3 || top[1].Revenue != 30000 {
		t.Fatalf("top[1] wrong (want Teh Botol/3/30000): %+v", top[1])
	}
	if top[1].ProductID != nil {
		t.Fatalf("deleted-product row must have null product_id: %+v", top[1])
	}

	// — Revenue by product: same rows, revenue descending.
	byRevenue, err := repo.RevenueByProduct(ctx, merchantA, from, to)
	if err != nil {
		t.Fatalf("RevenueByProduct: %v", err)
	}
	if len(byRevenue) != 2 || byRevenue[0].Revenue < byRevenue[1].Revenue {
		t.Fatalf("revenue table not revenue-desc: %+v", byRevenue)
	}
	if byRevenue[0].ProductName != "Kopi" || byRevenue[0].Revenue != 180000 {
		t.Fatalf("revenue[0] wrong: %+v", byRevenue[0])
	}
}

func TestDashboardAnalyticsPG_EmptyPeriod(t *testing.T) {
	pool := analyticsPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardAnalyticsRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)
	from := time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2030, 5, 8, 0, 0, 0, 0, time.UTC)

	revenue, paid, currency, err := repo.PaymentSummary(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("PaymentSummary: %v", err)
	}
	if revenue != 0 || paid != 0 || currency != "IDR" {
		t.Fatalf("empty payment summary = (%d, %d, %s)", revenue, paid, currency)
	}
	orderRevenue, paidOrders, units, err := repo.OrderSummary(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("OrderSummary: %v", err)
	}
	if orderRevenue != 0 || paidOrders != 0 || units != 0 {
		t.Fatalf("empty order summary = (%d, %d, %d)", orderRevenue, paidOrders, units)
	}
	interval, trend, err := repo.RevenueTrend(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("RevenueTrend: %v", err)
	}
	if interval != "day" || len(trend) != 7 {
		t.Fatalf("empty trend = (%q, %d buckets), want (day, 7)", interval, len(trend))
	}
	for _, b := range trend {
		if b.Revenue != 0 {
			t.Fatalf("empty bucket %s nonzero: %d", b.Date, b.Revenue)
		}
	}
	top, err := repo.TopProductsByUnits(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("TopProductsByUnits: %v", err)
	}
	if len(top) != 0 {
		t.Fatalf("empty top products = %d rows", len(top))
	}
	byRevenue, err := repo.RevenueByProduct(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("RevenueByProduct: %v", err)
	}
	if len(byRevenue) != 0 {
		t.Fatalf("empty revenue table = %d rows", len(byRevenue))
	}
}

func TestDashboardAnalyticsPG_WeeklyInterval(t *testing.T) {
	pool := analyticsPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardAnalyticsRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	paidAt := time.Date(2026, 2, 11, 10, 0, 0, 0, time.UTC) // a Wednesday
	analyticsTxn(t, ctx, pool, merchantID, "wk", "PAID", 60000, nil, paidAt, &paidAt)

	interval, trend, err := repo.RevenueTrend(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("RevenueTrend: %v", err)
	}
	if interval != "week" {
		t.Fatalf("interval = %q, want week for a 90-day window", interval)
	}
	var total int64
	var foundMonday bool
	for _, b := range trend {
		total += b.Revenue
		if b.Date == "2026-02-09" && b.Revenue == 60000 {
			foundMonday = true
		}
	}
	if total != 60000 || !foundMonday {
		t.Fatalf("weekly buckets wrong (want 60000 in week of Mon 2026-02-09): %+v", trend)
	}
}
