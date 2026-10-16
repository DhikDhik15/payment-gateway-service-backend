package repository_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Phase 10J: PG-backed proof of the overview half-open window [from, to) in UTC.
//
// Requires TEST_DATABASE_URL pointing at a database migrated through 000024.
// Skips otherwise, like the other *_pg_test.go suites.

func overviewTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func overviewSeedMerchant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	code := "ov-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, code, api_key, api_secret, status)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE')`,
		id, "Overview Merchant", code, uuid.NewString(), uuid.NewString(),
	); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	return id
}

type overviewTxSeed struct {
	status    string
	createdAt time.Time
	paidAt    *time.Time
}

func overviewSeedTransactions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, merchantID uuid.UUID, prefix string, seeds []overviewTxSeed) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, len(seeds))
	for i, s := range seeds {
		id := uuid.New()
		orderID := prefix + "-" + string(rune('A'+i)) + "-" + uuid.NewString()[:6]
		if _, err := pool.Exec(ctx, `
			INSERT INTO transactions
				(id, merchant_id, merchant_order_id, amount, currency, payment_method, status, paid_at, created_at, updated_at)
			VALUES ($1, $2, $3, 10000, 'IDR', 'QRIS', $4, $5, $6, $6)`,
			id, merchantID, orderID, s.status, s.paidAt, s.createdAt,
		); err != nil {
			t.Fatalf("seed transaction: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestDashboardOverviewPG_HalfOpenBoundaries(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardOverviewRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	justBeforeEnd := to.Add(-time.Millisecond)
	justBeforeStart := from.Add(-time.Millisecond)
	mid := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)

	overviewSeedTransactions(t, ctx, pool, merchantID, "bnd", []overviewTxSeed{
		{status: "PAID", createdAt: from},               // exactly at start → included
		{status: "FAILED", createdAt: mid},              // inside → included
		{status: "EXPIRED", createdAt: justBeforeEnd},   // just before exclusive end → included
		{status: "CANCELLED", createdAt: to},            // exactly at exclusive end → excluded
		{status: "PENDING", createdAt: justBeforeStart}, // before start → excluded
	})

	stats, err := repo.CountPaymentsByStatus(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Total != 3 {
		t.Fatalf("total = %d, want 3 (start + inside + just-before-end)", stats.Total)
	}
	if stats.Paid != 1 || stats.Failed != 1 || stats.Expired != 1 {
		t.Fatalf("per-status mismatch: %+v", stats)
	}
	if stats.Cancelled != 0 || stats.Pending != 0 {
		t.Fatalf("boundary rows leaked in: %+v", stats)
	}
	if stats.UnsuccessfulTransactions != 2 {
		t.Fatalf("unsuccessful = %d, want failed(1)+expired(1)", stats.UnsuccessfulTransactions)
	}
}

func TestDashboardOverviewPG_MonthYearAndLeapBoundaries(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardOverviewRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	// Month boundary: [2026-01-01, 2026-02-01).
	janFrom := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	febFrom := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	overviewSeedTransactions(t, ctx, pool, merchantID, "mon", []overviewTxSeed{
		{status: "PAID", createdAt: time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)},
		{status: "PAID", createdAt: febFrom},
	})
	stats, err := repo.CountPaymentsByStatus(ctx, merchantID, janFrom, febFrom)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Total != 1 {
		t.Fatalf("month boundary total = %d, want 1", stats.Total)
	}

	// Year boundary: [2025-12-01, 2026-01-01).
	decFrom := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	overviewSeedTransactions(t, ctx, pool, merchantID, "yr", []overviewTxSeed{
		{status: "FAILED", createdAt: time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)},
		{status: "FAILED", createdAt: janFrom},
	})
	stats, err = repo.CountPaymentsByStatus(ctx, merchantID, decFrom, janFrom)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("year boundary failed = %d, want 1", stats.Failed)
	}

	// Leap day single-day range: [2024-02-29, 2024-03-01).
	leapFrom := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	leapTo := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	overviewSeedTransactions(t, ctx, pool, merchantID, "leap", []overviewTxSeed{
		{status: "EXPIRED", createdAt: time.Date(2024, 2, 29, 15, 0, 0, 0, time.UTC)},
		{status: "EXPIRED", createdAt: leapTo},
	})
	stats, err = repo.CountPaymentsByStatus(ctx, merchantID, leapFrom, leapTo)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Expired != 1 || stats.UnsuccessfulTransactions != 1 {
		t.Fatalf("leap-day mismatch: %+v", stats)
	}
}

func TestDashboardOverviewPG_MerchantIsolation(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardOverviewRepository(pool)
	merchantA := overviewSeedMerchant(t, ctx, pool)
	merchantB := overviewSeedMerchant(t, ctx, pool)

	mid := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	overviewSeedTransactions(t, ctx, pool, merchantA, "iso-a", []overviewTxSeed{
		{status: "PAID", createdAt: mid},
	})
	overviewSeedTransactions(t, ctx, pool, merchantB, "iso-b", []overviewTxSeed{
		{status: "PAID", createdAt: mid},
		{status: "FAILED", createdAt: mid},
	})

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	stats, err := repo.CountPaymentsByStatus(ctx, merchantA, from, to)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Total != 1 || stats.Paid != 1 || stats.UnsuccessfulTransactions != 0 {
		t.Fatalf("merchant A sees foreign rows: %+v", stats)
	}
}

func TestDashboardOverviewPG_EmptyPeriodReturnsZeros(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardOverviewRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	from := time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2030, 5, 8, 0, 0, 0, 0, time.UTC)
	stats, err := repo.CountPaymentsByStatus(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Total != 0 || stats.Created != 0 || stats.Pending != 0 || stats.Paid != 0 ||
		stats.Failed != 0 || stats.Expired != 0 || stats.Cancelled != 0 || stats.UnsuccessfulTransactions != 0 {
		t.Fatalf("empty period must be all zeros: %+v", stats)
	}

	amount, currency, err := repo.SumPaidRevenue(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("SumPaidRevenue: %v", err)
	}
	if amount != 0 || currency != "IDR" {
		t.Fatalf("empty revenue = (%d, %s), want (0, IDR)", amount, currency)
	}

	count, ramount, rcurr, err := repo.SumSucceededRefunds(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("SumSucceededRefunds: %v", err)
	}
	if count != 0 || ramount != 0 || rcurr != "IDR" {
		t.Fatalf("empty refunds = (%d, %d, %s), want zeros + IDR", count, ramount, rcurr)
	}
}

func TestDashboardOverviewPG_RevenueUsesPaidAtNotCreatedAt(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardOverviewRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	paidInPeriod := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	paidLater := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	createdEarlier := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

	overviewSeedTransactions(t, ctx, pool, merchantID, "rev", []overviewTxSeed{
		// Created in period, paid later: counts in payments, not in revenue.
		{status: "PAID", createdAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), paidAt: &paidLater},
		// Created before period, paid inside: revenue only.
		{status: "PAID", createdAt: createdEarlier, paidAt: &paidInPeriod},
	})

	stats, err := repo.CountPaymentsByStatus(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Paid != 1 {
		t.Fatalf("payments.paid = %d, want 1 (created in period)", stats.Paid)
	}

	amount, _, err := repo.SumPaidRevenue(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("SumPaidRevenue: %v", err)
	}
	if amount != 10000 {
		t.Fatalf("revenue = %d, want 10000 (paid in period only)", amount)
	}
}

func TestDashboardOverviewPG_SucceededRefundsUseSucceededAt(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	repo := repository.NewDashboardOverviewRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	succeededAt := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

	txIDs := overviewSeedTransactions(t, ctx, pool, merchantID, "ref", []overviewTxSeed{
		{status: "PAID", createdAt: createdAt, paidAt: &createdAt},
		{status: "PAID", createdAt: createdAt, paidAt: &createdAt},
	})
	// SUCCEEDED inside the window (by succeeded_at) → counted.
	if _, err := pool.Exec(ctx, `
		INSERT INTO refunds (merchant_id, transaction_id, amount, currency, status, provider, succeeded_at)
		VALUES ($1, $2, 4000, 'IDR', 'SUCCEEDED', 'MOCK', $3)`,
		merchantID, txIDs[0], succeededAt,
	); err != nil {
		t.Fatalf("seed succeeded refund: %v", err)
	}
	// Still PENDING → never counted.
	if _, err := pool.Exec(ctx, `
		INSERT INTO refunds (merchant_id, transaction_id, amount, currency, status, provider)
		VALUES ($1, $2, 4000, 'IDR', 'PENDING', 'MOCK')`,
		merchantID, txIDs[1],
	); err != nil {
		t.Fatalf("seed pending refund: %v", err)
	}

	count, amount, _, err := repo.SumSucceededRefunds(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("SumSucceededRefunds: %v", err)
	}
	if count != 1 || amount != 4000 {
		t.Fatalf("refunds = (%d, %d), want (1, 4000)", count, amount)
	}
}
