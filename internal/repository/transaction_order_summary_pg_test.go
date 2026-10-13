package repository_test

// PostgreSQL coverage for GetOrderSummaries: the single aggregate query behind
// PaymentResponse.order_summary. Requires TEST_DATABASE_URL pointing at a
// database migrated through 000022; skips otherwise, like the other pg tests.
// It never commits schema changes — fixture rows are deleted on cleanup.

import (
	"context"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func summaryFixtureOrder(t *testing.T, pool *pgxpool.Pool, merchant uuid.UUID, names ...string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	orderID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO orders(id,merchant_id,status,currency,subtotal,total_amount,idempotency_key,request_hash)
		VALUES ($1,$2,'CREATED','IDR',$3,$3,$4,$5)`,
		orderID, merchant, int64(1000*len(names)), "summary-"+orderID.String(),
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if _, err := pool.Exec(ctx, `INSERT INTO order_items(id,merchant_id,order_id,product_name,quantity,unit_price,subtotal)
			VALUES ($1,$2,$3,$4,1,1000,1000)`, uuid.New(), merchant, orderID, name); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM order_items WHERE order_id=$1`, orderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders WHERE id=$1`, orderID)
	})
	return orderID
}

func TestGetOrderSummaries(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	repo := repository.NewTransactionRepository(pool)

	merchant := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO merchants(id,name,code,status) VALUES ($1,'Summary test merchant',$2,'ACTIVE')`,
		merchant, "summary-"+merchant.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id=$1`, merchant) })

	one := summaryFixtureOrder(t, pool, merchant, "Mechanical Keyboard")
	three := summaryFixtureOrder(t, pool, merchant, "Mechanical Keyboard", "Mouse", "Monitor")
	empty := summaryFixtureOrder(t, pool, merchant)
	unknown := uuid.New()

	summaries, err := repo.(repository.OrderSummaryLookup).GetOrderSummaries(
		ctx, merchant, []uuid.UUID{one, three, empty, unknown, one},
	)
	if err != nil {
		t.Fatal(err)
	}

	// One product: the snapshot name, count 1.
	s, ok := summaries[one]
	if !ok || len(s.ProductNames) != 1 || s.ProductNames[0] != "Mechanical Keyboard" || s.ProductCount != 1 {
		t.Fatalf("one-product summary wrong: %+v present=%v", s, ok)
	}
	// Three products: first two names, full count — clients render "A + 2 others".
	s, ok = summaries[three]
	if !ok || len(s.ProductNames) != 2 || s.ProductNames[0] != "Mechanical Keyboard" ||
		s.ProductNames[1] != "Mouse" || s.ProductCount != 3 {
		t.Fatalf("three-product summary wrong: %+v present=%v", s, ok)
	}
	// Empty and unknown orders are absent: clients use their fallback.
	if _, ok := summaries[empty]; ok {
		t.Fatal("empty order must be absent from the map")
	}
	if _, ok := summaries[unknown]; ok {
		t.Fatal("unknown order must be absent from the map")
	}

	// Empty input issues no query and returns an empty map.
	none, err := repo.(repository.OrderSummaryLookup).GetOrderSummaries(ctx, merchant, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty input must return an empty map: %+v err=%v", none, err)
	}
}
