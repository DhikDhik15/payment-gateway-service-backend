package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
)

// Phase 10J follow-up: prove the exact filter the overview service passes for
// Recent Payments (page 1, limit 10, half-open created_at window) returns only
// in-window rows from real SQL. Reuses the overview pg-test seeds/helpers.
//
// Requires TEST_DATABASE_URL pointing at a database migrated through 000024.
// Skips otherwise, like the other *_pg_test.go suites.

func overviewRecentFilter(from, to time.Time) model.TransactionListFilter {
	return model.TransactionListFilter{
		Page:        1,
		Limit:       10,
		CreatedFrom: &from,
		CreatedTo:   &to,
	}
}

func TestDashboardOverviewRecentPG_WindowIncludesFinalDayExcludesOutside(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	txRepo := repository.NewTransactionRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	// Selected range: October 8 through October 14 (inclusive) →
	// half-open window [2026-10-08, 2026-10-15).
	from := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	overviewSeedTransactions(t, ctx, pool, merchantID, "rec", []overviewTxSeed{
		{status: "PAID", createdAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},     // first day → in
		{status: "FAILED", createdAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}, // mid → in
		{status: "PAID", createdAt: time.Date(2026, 10, 14, 23, 59, 59, 0, time.UTC)}, // final day → in
		{status: "PAID", createdAt: time.Date(2026, 10, 7, 23, 59, 59, 0, time.UTC)},  // before → out
		{status: "PAID", createdAt: to},                                               // exclusive end → out
	})

	txs, err := txRepo.List(ctx, merchantID, overviewRecentFilter(from, to))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(txs) != 3 {
		t.Fatalf("got %d rows, want 3 in-window rows", len(txs))
	}
	// Newest first (created_at DESC, id DESC).
	for i := 1; i < len(txs); i++ {
		if txs[i].CreatedAt.After(txs[i-1].CreatedAt) {
			t.Fatalf("rows not newest-first: %v then %v", txs[i-1].CreatedAt, txs[i].CreatedAt)
		}
	}
	if !txs[0].CreatedAt.Equal(time.Date(2026, 10, 14, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("first row must be the final-day payment, got %v", txs[0].CreatedAt)
	}
}

func TestDashboardOverviewRecentPG_RowLimitAndIsolation(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	txRepo := repository.NewTransactionRepository(pool)
	merchantA := overviewSeedMerchant(t, ctx, pool)
	merchantB := overviewSeedMerchant(t, ctx, pool)

	from := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	seeds := make([]overviewTxSeed, 0, 12)
	for i := 0; i < 12; i++ {
		seeds = append(seeds, overviewTxSeed{status: "PAID", createdAt: mid.Add(time.Duration(i) * time.Second)})
	}
	overviewSeedTransactions(t, ctx, pool, merchantA, "lim-a", seeds)
	overviewSeedTransactions(t, ctx, pool, merchantB, "lim-b", []overviewTxSeed{
		{status: "PAID", createdAt: mid},
	})

	txs, err := txRepo.List(ctx, merchantA, overviewRecentFilter(from, to))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(txs) != 10 {
		t.Fatalf("got %d rows, want overview row limit of 10", len(txs))
	}
	for _, tx := range txs {
		if tx.MerchantID != merchantA {
			t.Fatalf("foreign-merchant row leaked: %v", tx.MerchantID)
		}
	}

	total, err := txRepo.CountList(ctx, merchantA, overviewRecentFilter(from, to))
	if err != nil {
		t.Fatalf("CountList: %v", err)
	}
	if total != 12 {
		t.Fatalf("count = %d, want 12 (limit applies to rows, not the count)", total)
	}
}

func TestDashboardOverviewRecentPG_EmptyPeriodReturnsNoRows(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	txRepo := repository.NewTransactionRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	from := time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2030, 5, 8, 0, 0, 0, 0, time.UTC)
	txs, err := txRepo.List(ctx, merchantID, overviewRecentFilter(from, to))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(txs) != 0 {
		t.Fatalf("empty period must return no rows, got %d", len(txs))
	}
}

// Regression for the reported screenshot: Today (2026-10-14) applied, metric
// cards at zero, but Recent Payments showed Oct 13 / Oct 12 / Oct 9 rows.
// Root cause was the overview service listing recent payments without the
// period predicate (last-10 overall). With the [from, to) filter applied,
// older rows must never fill the table — an empty day returns no rows.
func TestDashboardOverviewRecentPG_TodayWindowHidesOlderPayments(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	txRepo := repository.NewTransactionRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	// Today window: [2026-10-14, 2026-10-15).
	from := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	// Only older payments exist — the exact screenshot setup.
	overviewSeedTransactions(t, ctx, pool, merchantID, "today-bug", []overviewTxSeed{
		{status: "PAID", createdAt: time.Date(2026, 10, 13, 15, 4, 0, 0, time.UTC)},
		{status: "FAILED", createdAt: time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)},
		{status: "EXPIRED", createdAt: time.Date(2026, 10, 9, 18, 45, 0, 0, time.UTC)},
	})

	txs, err := txRepo.List(ctx, merchantID, overviewRecentFilter(from, to))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(txs) != 0 {
		t.Fatalf("Today window must hide older payments, got %d rows (first: %v)", len(txs), txs[0].CreatedAt)
	}

	// And the counts window agrees: zero in-window transactions.
	countRepo := repository.NewDashboardOverviewRepository(pool)
	stats, err := countRepo.CountPaymentsByStatus(ctx, merchantID, from, to)
	if err != nil {
		t.Fatalf("CountPaymentsByStatus: %v", err)
	}
	if stats.Total != 0 || stats.UnsuccessfulTransactions != 0 {
		t.Fatalf("Today counts must be zero alongside empty recent: %+v", stats)
	}
}
