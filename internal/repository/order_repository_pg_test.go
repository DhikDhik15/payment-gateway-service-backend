package repository_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Set TEST_DATABASE_URL to a disposable PostgreSQL database migrated through
// 000022. The regular unit suite does not require PostgreSQL.
func orderTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	var version int
	var dirty bool
	if err := pool.QueryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil || dirty || version < 22 {
		pool.Close()
		t.Fatalf("orders and order payments require a clean migration 22 database: version=%d dirty=%v err=%v", version, dirty, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func orderFixture(t *testing.T, pool *pgxpool.Pool, price int64, currency string, active bool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	merchant, product := uuid.New(), uuid.New()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO merchants(id,name,code,status) VALUES ($1,'Orders test merchant',$2,'ACTIVE')`, merchant, "orders-"+merchant.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id=$1`, merchant) })
	if _, err := pool.Exec(ctx, `INSERT INTO products(id,merchant_id,name,price,currency,active) VALUES ($1,$2,'Snapshot name',$3,$4,$5)`, product, merchant, price, currency, active); err != nil {
		t.Fatal(err)
	}
	return merchant, product
}

func TestOrderPostgresIsolationSnapshotsAndIdempotency(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	merchantA, productA := orderFixture(t, pool, 750, "IDR", true)
	merchantB, productB := orderFixture(t, pool, 100, "IDR", true)
	svc := service.NewOrderService(repository.NewOrderRepository(pool))
	reqA := model.CreateOrderRequest{Items: []model.CreateOrderItemRequest{{ProductID: productA, Quantity: 3}}}
	order, replay, err := svc.CreateOrder(ctx, merchantA, "same-key", reqA)
	if err != nil || replay || order.Subtotal != 2250 || order.TotalAmount != 2250 || order.Status != model.OrderStatusCreated || order.Currency != "IDR" || len(order.Items) != 1 || order.Items[0].ProductName != "Snapshot name" || order.Items[0].UnitPrice != 750 {
		t.Fatalf("created order=%+v replay=%v err=%v", order, replay, err)
	}
	again, replay, err := svc.CreateOrder(ctx, merchantA, "same-key", reqA)
	if err != nil || !replay || again.ID != order.ID {
		t.Fatalf("replay=%+v flag=%v err=%v", again, replay, err)
	}
	_, _, err = svc.CreateOrder(ctx, merchantA, "same-key", model.CreateOrderRequest{Items: []model.CreateOrderItemRequest{{ProductID: productA, Quantity: 2}}})
	if !errors.Is(err, service.ErrOrderKeyConflict) {
		t.Fatalf("different request with same key: %v", err)
	}
	if _, _, err = svc.CreateOrder(ctx, merchantA, "foreign-product", model.CreateOrderRequest{Items: []model.CreateOrderItemRequest{{ProductID: productB, Quantity: 1}}}); !errors.Is(err, service.ErrOrderProductMissing) {
		t.Fatalf("foreign product: %v", err)
	}
	other, _, err := svc.CreateOrder(ctx, merchantB, "same-key", model.CreateOrderRequest{Items: []model.CreateOrderItemRequest{{ProductID: productB, Quantity: 1}}})
	if err != nil || other.ID == order.ID {
		t.Fatalf("different merchant's identical key: %+v %v", other, err)
	}
	// A caller bypassing the service still cannot cross-wire order/product
	// ownership: both foreign keys include merchant_id.
	for _, forged := range []struct {
		merchant uuid.UUID
		order    uuid.UUID
		product  uuid.UUID
	}{
		{merchantA, order.ID, productB},
		{merchantB, order.ID, productB},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO order_items(id,merchant_id,order_id,product_id,product_name,quantity,unit_price,subtotal)
			VALUES ($1,$2,$3,$4,'forged',1,100,100)`, uuid.New(), forged.merchant, forged.order, forged.product)
		if err == nil {
			t.Fatal("composite tenant FK allowed a cross-merchant item")
		}
	}
	if _, err := svc.GetOrder(ctx, merchantB, order.ID); !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("cross-tenant get must be not found: %v", err)
	}
	list, err := svc.ListOrders(ctx, merchantA, 1, 20)
	if err != nil || list.Total != 1 || len(list.Orders) != 1 || list.Orders[0].ID != order.ID {
		t.Fatalf("cross-tenant list leaked or lost orders: %+v %v", list, err)
	}
	// A hard product delete must not erase item snapshots or merchant_id.
	if _, err := pool.Exec(ctx, `DELETE FROM products WHERE id=$1 AND merchant_id=$2`, productA, merchantA); err != nil {
		t.Fatal(err)
	}
	persisted, err := svc.GetOrder(ctx, merchantA, order.ID)
	if err != nil || len(persisted.Items) != 1 || persisted.Items[0].ProductID != nil || persisted.Items[0].ProductName != "Snapshot name" || persisted.Items[0].UnitPrice != 750 || persisted.Items[0].Subtotal != 2250 {
		t.Fatalf("history after product deletion: %+v %v", persisted, err)
	}
}

func TestOrderPostgresConcurrentSameKeyAndRollback(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	merchant, product := orderFixture(t, pool, 100, "IDR", true)
	repo := repository.NewOrderRepository(pool)
	svc := service.NewOrderService(repo)
	req := model.CreateOrderRequest{Items: []model.CreateOrderItemRequest{{ProductID: product, Quantity: 1}}}
	const callers = 8
	var wg sync.WaitGroup
	results := make(chan uuid.UUID, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, _, err := svc.CreateOrder(ctx, merchant, "concurrent-key", req)
			if err != nil {
				errs <- err
			} else {
				results <- order.ID
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent create: %v", err)
	}
	var first uuid.UUID
	for id := range results {
		if first == uuid.Nil {
			first = id
		} else if id != first {
			t.Errorf("duplicate order %v vs %v", id, first)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE merchant_id=$1 AND idempotency_key='concurrent-key'`, merchant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotency DB count=%d err=%v", count, err)
	}
	// Fail the second item insert (duplicate item primary key) after the order
	// and first item have already been inserted. The transaction must roll back.
	badID, orderID := uuid.New(), uuid.New()
	productID := product
	bad := &model.Order{
		ID: orderID, MerchantID: merchant, Status: model.OrderStatusCreated, Currency: "IDR",
		Subtotal: 200, TotalAmount: 200, IdempotencyKey: "rollback-key",
		RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Items: []model.OrderItem{
			{ID: badID, ProductID: &productID, ProductName: "Snapshot name", Quantity: 1, UnitPrice: 100, Subtotal: 100},
			{ID: badID, ProductID: &productID, ProductName: "Snapshot name", Quantity: 1, UnitPrice: 100, Subtotal: 100},
		},
	}
	if err := repo.InTransaction(ctx, func(tx repository.OrderTransaction) error { return tx.CreateOrder(ctx, bad) }); err == nil {
		t.Fatal("second item insert should fail")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE id=$1`, orderID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("order survived rollback: count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM order_items WHERE order_id=$1`, orderID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("items survived rollback: count=%d err=%v", count, err)
	}
}

func TestOrderPostgresRejectedProductsLeaveNoOrder(t *testing.T) {
	pool := orderTestPool(t)
	ctx := context.Background()
	merchant, activeID := orderFixture(t, pool, 100, "IDR", true)
	inactiveID, usdID, expensiveID := uuid.New(), uuid.New(), uuid.New()
	for _, p := range []struct {
		id       uuid.UUID
		price    int64
		currency string
		active   bool
	}{
		{inactiveID, 100, "IDR", false},
		{usdID, 100, "USD", true},
		{expensiveID, 9223372036854775807, "IDR", true},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO products(id,merchant_id,name,price,currency,active)
			VALUES($1,$2,'validation product',$3,$4,$5)`, p.id, merchant, p.price, p.currency, p.active); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.NewOrderService(repository.NewOrderRepository(pool))
	for _, tc := range []struct {
		name  string
		items []model.CreateOrderItemRequest
		want  error
	}{
		{"inactive", []model.CreateOrderItemRequest{{ProductID: inactiveID, Quantity: 1}}, service.ErrOrderProductMissing},
		{"mixed-currency", []model.CreateOrderItemRequest{{ProductID: activeID, Quantity: 1}, {ProductID: usdID, Quantity: 1}}, service.ErrOrderCurrency},
		{"overflow", []model.CreateOrderItemRequest{{ProductID: expensiveID, Quantity: 2}}, service.ErrOrderOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := svc.CreateOrder(ctx, merchant, tc.name, model.CreateOrderRequest{Items: tc.items}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE merchant_id=$1`, merchant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid orders persisted: count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM order_items WHERE merchant_id=$1`, merchant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid order items persisted: count=%d err=%v", count, err)
	}
}
