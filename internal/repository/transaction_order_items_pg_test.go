package repository_test

// PostgreSQL coverage for GetOrderItems: the single merchant-scoped query
// behind DashboardPaymentDetailResponse.order_items. Requires
// TEST_DATABASE_URL pointing at a database migrated through 000022; skips
// otherwise, like the other pg tests. Fixture rows are deleted on cleanup.
//
// Product rows carry deliberately different names/prices than the snapshots
// so the test proves historical values come from order_items, never the live
// catalog. Product UUIDs are fixed so the canonical
// (product_id NULLS LAST, id) order is deterministic.

import (
	"context"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func itemsFixtureOrder(t *testing.T, pool *pgxpool.Pool, merchant uuid.UUID, items ...[4]any) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	orderID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO orders(id,merchant_id,status,currency,subtotal,total_amount,idempotency_key,request_hash)
		VALUES ($1,$2,'CREATED','IDR',$3,$3,$4,$5)`,
		orderID, merchant, int64(1000*len(items)), "items-"+orderID.String(),
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	// items are [product_id, snapshot_name, unit_price, quantity]; the live
	// product row intentionally disagrees to prove snapshot semantics. The
	// stored subtotal follows the backend pricing rule (unit_price*quantity).
	for _, item := range items {
		pid := uuid.MustParse(item[0].(string))
		name := item[1].(string)
		unitPrice := item[2].(int64)
		quantity := item[3].(int64)
		if _, err := pool.Exec(ctx, `INSERT INTO products(id,merchant_id,name,price,currency,active)
			VALUES ($1,$2,$3,$4,'IDR',true)`, pid, merchant, name+" (live)", unitPrice+1000); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO order_items(id,merchant_id,order_id,product_id,product_name,quantity,unit_price,subtotal)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.New(), merchant, orderID, pid, name, quantity, unitPrice, unitPrice*quantity); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM order_items WHERE order_id=$1`, orderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders WHERE id=$1`, orderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM products WHERE merchant_id=$1 AND name LIKE '% (live)'`, merchant)
	})
	return orderID
}

func TestGetOrderItems(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	repo := repository.NewTransactionRepository(pool)

	merchant := uuid.New()
	otherMerchant := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO merchants(id,name,code,status) VALUES ($1,'Items test merchant',$2,'ACTIVE')`,
		merchant, "items-"+merchant.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id=$1`, merchant) })

	// Fixed product UUIDs sort ascending: canonical order is deterministic.
	two := itemsFixtureOrder(t, pool, merchant,
		[4]any{"11111111-1111-4111-8111-111111111111", "Kopi Arabika", int64(35000), int64(2)},
		[4]any{"22222222-2222-4222-8222-222222222222", "Teh Hijau", int64(20000), int64(1)},
	)
	empty := itemsFixtureOrder(t, pool, merchant)
	unknown := uuid.New()

	items, err := repo.(repository.OrderItemsLookup).GetOrderItems(ctx, merchant, two)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	first, second := items[0], items[1]
	if first.ProductName != "Kopi Arabika" || first.UnitPrice != 35000 || first.Quantity != 2 || first.Subtotal != 70000 {
		t.Errorf("first snapshot mismatch (must ignore the live catalog): %+v", first)
	}
	if second.ProductName != "Teh Hijau" || second.UnitPrice != 20000 || second.Quantity != 1 || second.Subtotal != 20000 {
		t.Errorf("second snapshot mismatch (must ignore the live catalog): %+v", second)
	}
	for _, item := range items {
		if item.MerchantID != merchant || item.OrderID != two {
			t.Errorf("item scoping wrong: %+v", item)
		}
	}

	// Empty and unknown orders yield no rows (callers omit the breakdown).
	got, err := repo.(repository.OrderItemsLookup).GetOrderItems(ctx, merchant, empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty order must yield no items, got %+v", got)
	}
	got, err = repo.(repository.OrderItemsLookup).GetOrderItems(ctx, merchant, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown order must yield no items, got %+v", got)
	}

	// Cross-merchant access yields no rows: merchant isolation holds.
	got, err = repo.(repository.OrderItemsLookup).GetOrderItems(ctx, otherMerchant, two)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("cross-merchant lookup must yield no items, got %+v", got)
	}
}
