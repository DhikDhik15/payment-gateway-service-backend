package repository_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
)

// Phase 10K: the Payments page composes date + status + search filters in one
// backend query. Proves at SQL level that combined filters intersect within
// the merchant scope and that pagination metadata reflects the filtered set.
//
// Requires TEST_DATABASE_URL pointing at a database migrated through 000024.
// Skips otherwise, like the other *_pg_test.go suites.

func TestTransactionListPG_DateStatusSearchCompose(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	txRepo := repository.NewTransactionRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	mid := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	overviewSeedTransactions(t, ctx, pool, merchantID, "COMP", []overviewTxSeed{
		{status: "PAID", createdAt: mid},
		{status: "PAID", createdAt: mid.Add(time.Hour)},
		{status: "FAILED", createdAt: mid.Add(2 * time.Hour)},
		{status: "PAID", createdAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)},
	})

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	paid := model.TransactionStatusPaid
	search := "comp"

	filter := model.TransactionListFilter{
		Page: 1, Limit: 10,
		Status:      &paid,
		Search:      &search,
		CreatedFrom: &from,
		CreatedTo:   &to,
	}
	txs, err := txRepo.List(ctx, merchantID, filter)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d rows, want 2 (in-window PAID matching search)", len(txs))
	}
	for _, tx := range txs {
		if tx.Status != model.TransactionStatusPaid {
			t.Fatalf("status leaked: %v", tx.Status)
		}
		if !strings.Contains(strings.ToLower(tx.MerchantOrderID), "comp") {
			t.Fatalf("search leaked: %v", tx.MerchantOrderID)
		}
		if tx.CreatedAt.Before(from) || !tx.CreatedAt.Before(to) {
			t.Fatalf("date leaked: %v", tx.CreatedAt)
		}
	}

	total, err := txRepo.CountList(ctx, merchantID, filter)
	if err != nil {
		t.Fatalf("CountList: %v", err)
	}
	if total != 2 {
		t.Fatalf("filtered count = %d, want 2", total)
	}
}

func TestTransactionListPG_PaginationMetadataReflectsFilteredSet(t *testing.T) {
	pool := overviewTestPool(t)
	ctx := context.Background()
	txRepo := repository.NewTransactionRepository(pool)
	merchantID := overviewSeedMerchant(t, ctx, pool)

	mid := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	seeds := make([]overviewTxSeed, 0, 6)
	for i := 0; i < 5; i++ {
		seeds = append(seeds, overviewTxSeed{status: "PAID", createdAt: mid.Add(time.Duration(i) * time.Minute)})
	}
	// Outside the window: must not shift page counts.
	seeds = append(seeds, overviewTxSeed{status: "PAID", createdAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)})
	overviewSeedTransactions(t, ctx, pool, merchantID, "PGM", seeds)

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	total, err := txRepo.CountList(ctx, merchantID, model.TransactionListFilter{
		Page: 1, Limit: 2, CreatedFrom: &from, CreatedTo: &to,
	})
	if err != nil {
		t.Fatalf("CountList: %v", err)
	}
	if total != 5 {
		t.Fatalf("filtered total = %d, want 5", total)
	}

	page2, err := txRepo.List(ctx, merchantID, model.TransactionListFilter{
		Page: 2, Limit: 2, CreatedFrom: &from, CreatedTo: &to,
	})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page 2 holds %d rows, want 2 (5 filtered rows, limit 2)", len(page2))
	}
}
