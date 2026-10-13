package service_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── In-memory order repository ──────────────────────────────────────────────
//
// fakeOrderRepo mimics the PostgreSQL implementation closely enough to exercise
// the service contract: every read is merchant-scoped, LockIdempotencyKey
// serializes same merchant/key transactions until commit/rollback (like
// pg_advisory_xact_lock), and staged inserts are only published when the
// transaction callback returns nil (rollback otherwise).

type fakeOrderRepo struct {
	mu       sync.Mutex
	products map[uuid.UUID]model.Product // keyed by product ID; merchant checked on read
	orders   map[uuid.UUID]*model.Order  // committed orders by ID
	byKey    map[string]uuid.UUID        // merchant:key -> order ID

	keyLocksMu sync.Mutex
	keyLocks   map[string]*sync.Mutex

	// fault injection
	lockErr       error
	findErr       error
	productErr    error
	productErrFor uuid.UUID
	createErr     error

	// observation
	txCount         int
	productLookups  []uuid.UUID
	lookupMerchants []uuid.UUID
	createCalls     int
	getMerchant     uuid.UUID
	listMerchant    uuid.UUID
	listPage        int
	listLimit       int
	listCalls       int
}

func newFakeOrderRepo() *fakeOrderRepo {
	return &fakeOrderRepo{
		products: map[uuid.UUID]model.Product{},
		orders:   map[uuid.UUID]*model.Order{},
		byKey:    map[string]uuid.UUID{},
		keyLocks: map[string]*sync.Mutex{},
	}
}

func (r *fakeOrderRepo) addProduct(merchantID uuid.UUID, name string, price int64, currency string, active bool) uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := uuid.New()
	r.products[id] = model.Product{ID: id, MerchantID: merchantID, Name: name, Price: price, Currency: currency, Active: active}
	return id
}

func (r *fakeOrderRepo) setPrice(id uuid.UUID, price int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.products[id]
	p.Price = price
	r.products[id] = p
}

func (r *fakeOrderRepo) orderCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.orders)
}

type fakeOrderTx struct {
	repo   *fakeOrderRepo
	staged *model.Order
	locks  []*sync.Mutex
}

func (r *fakeOrderRepo) InTransaction(ctx context.Context, fn func(repository.OrderTransaction) error) error {
	r.mu.Lock()
	r.txCount++
	r.mu.Unlock()
	tx := &fakeOrderTx{repo: r}
	defer func() {
		for i := len(tx.locks) - 1; i >= 0; i-- {
			tx.locks[i].Unlock()
		}
	}()
	if err := fn(tx); err != nil {
		return err // rollback: staged order discarded
	}
	if tx.staged != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		k := tx.staged.MerchantID.String() + ":" + tx.staged.IdempotencyKey
		if _, dup := r.byKey[k]; dup {
			return errors.New("unique violation: orders(merchant_id, idempotency_key)")
		}
		r.orders[tx.staged.ID] = tx.staged
		r.byKey[k] = tx.staged.ID
	}
	return nil
}

func (t *fakeOrderTx) LockIdempotencyKey(_ context.Context, merchantID uuid.UUID, key string) error {
	if t.repo.lockErr != nil {
		return t.repo.lockErr
	}
	k := merchantID.String() + ":" + key
	t.repo.keyLocksMu.Lock()
	m, ok := t.repo.keyLocks[k]
	if !ok {
		m = &sync.Mutex{}
		t.repo.keyLocks[k] = m
	}
	t.repo.keyLocksMu.Unlock()
	m.Lock()
	t.locks = append(t.locks, m)
	return nil
}

func cloneOrder(o *model.Order) *model.Order {
	c := *o
	c.Items = append([]model.OrderItem(nil), o.Items...)
	return &c
}

func (t *fakeOrderTx) FindByIdempotencyKey(_ context.Context, merchantID uuid.UUID, key string) (*model.Order, error) {
	if t.repo.findErr != nil {
		return nil, t.repo.findErr
	}
	t.repo.mu.Lock()
	defer t.repo.mu.Unlock()
	id, ok := t.repo.byKey[merchantID.String()+":"+key]
	if !ok {
		return nil, repository.ErrOrderNotFound
	}
	return cloneOrder(t.repo.orders[id]), nil
}

func (t *fakeOrderTx) GetProductForOrder(_ context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	t.repo.mu.Lock()
	defer t.repo.mu.Unlock()
	t.repo.productLookups = append(t.repo.productLookups, productID)
	t.repo.lookupMerchants = append(t.repo.lookupMerchants, merchantID)
	if t.repo.productErr != nil && (t.repo.productErrFor == uuid.Nil || t.repo.productErrFor == productID) {
		return nil, t.repo.productErr
	}
	p, ok := t.repo.products[productID]
	if !ok || p.MerchantID != merchantID {
		return nil, repository.ErrProductNotFound
	}
	return &p, nil
}

func (t *fakeOrderTx) CreateOrder(_ context.Context, order *model.Order) error {
	t.repo.mu.Lock()
	t.repo.createCalls++
	t.repo.mu.Unlock()
	if t.repo.createErr != nil {
		return t.repo.createErr
	}
	t.staged = cloneOrder(order)
	return nil
}

func (r *fakeOrderRepo) GetOrderByID(_ context.Context, merchantID, orderID uuid.UUID) (*model.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMerchant = merchantID
	o, ok := r.orders[orderID]
	if !ok || o.MerchantID != merchantID {
		return nil, repository.ErrOrderNotFound
	}
	return cloneOrder(o), nil
}

func (r *fakeOrderRepo) ListOrders(_ context.Context, merchantID uuid.UUID, page, limit int) (model.OrderListResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	r.listMerchant, r.listPage, r.listLimit = merchantID, page, limit
	res := model.OrderListResult{Orders: []model.Order{}, Page: page, Limit: limit}
	for _, o := range r.orders {
		if o.MerchantID == merchantID {
			res.Orders = append(res.Orders, *cloneOrder(o))
		}
	}
	res.Total = int64(len(res.Orders))
	return res, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func orderReq(items ...model.CreateOrderItemRequest) model.CreateOrderRequest {
	return model.CreateOrderRequest{Items: items}
}

func orderItem(id uuid.UUID, qty int64) model.CreateOrderItemRequest {
	return model.CreateOrderItemRequest{ProductID: id, Quantity: qty}
}

func newOrderFixture() (*fakeOrderRepo, service.OrderService, uuid.UUID) {
	repo := newFakeOrderRepo()
	return repo, service.NewOrderService(repo), uuid.New()
}

// ─── Idempotency key validation ──────────────────────────────────────────────

func TestCreateOrder_InvalidIdempotencyKey(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"too long":         strings.Repeat("a", 256),
		"leading space":    " key",
		"trailing space":   "key ",
		"trailing newline": "key\n",
		"inner space":      "my key",
		"tab":              "my\tkey",
		"control char":     "key\x00",
		"DEL":              "key\x7f",
		"non-ascii":        "kéy",
		"emoji":            "key😀",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			repo, svc, merchant := newOrderFixture()
			pid := repo.addProduct(merchant, "Coffee", 1000, "IDR", true)
			_, _, err := svc.CreateOrder(context.Background(), merchant, key, orderReq(orderItem(pid, 1)))
			if !errors.Is(err, service.ErrOrderKey) {
				t.Fatalf("err = %v, want ErrOrderKey", err)
			}
			if repo.txCount != 0 {
				t.Fatalf("transaction started for invalid key")
			}
		})
	}
}

func TestCreateOrder_ValidIdempotencyKeyBoundaries(t *testing.T) {
	for name, key := range map[string]string{
		"single char":     "x",
		"255 chars":       strings.Repeat("k", 255),
		"printable punct": "!~order-1_2.3:4/5#{}",
		"uuid":            uuid.NewString(),
	} {
		t.Run(name, func(t *testing.T) {
			repo, svc, merchant := newOrderFixture()
			pid := repo.addProduct(merchant, "Coffee", 1000, "IDR", true)
			order, replay, err := svc.CreateOrder(context.Background(), merchant, key, orderReq(orderItem(pid, 1)))
			if err != nil || replay || order == nil {
				t.Fatalf("got order=%v replay=%v err=%v", order, replay, err)
			}
			if order.IdempotencyKey != key {
				t.Fatalf("key = %q, want %q", order.IdempotencyKey, key)
			}
		})
	}
}

// ─── Item validation ─────────────────────────────────────────────────────────

func TestCreateOrder_InvalidItems(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	p1 := repo.addProduct(merchant, "A", 100, "IDR", true)
	p2 := repo.addProduct(merchant, "B", 100, "IDR", true)

	tooMany := make([]model.CreateOrderItemRequest, service.MaxOrderItems+1)
	for i := range tooMany {
		tooMany[i] = orderItem(uuid.New(), 1)
	}

	cases := map[string]model.CreateOrderRequest{
		"nil items":          {},
		"empty items":        {Items: []model.CreateOrderItemRequest{}},
		"too many items":     {Items: tooMany},
		"zero quantity":      orderReq(orderItem(p1, 0)),
		"negative quantity":  orderReq(orderItem(p1, -1)),
		"min int quantity":   orderReq(orderItem(p1, math.MinInt64)),
		"quantity over max":  orderReq(orderItem(p1, service.MaxOrderQuantity+1)),
		"huge quantity":      orderReq(orderItem(p1, math.MaxInt64)),
		"nil product id":     orderReq(orderItem(uuid.Nil, 1)),
		"duplicate adjacent": orderReq(orderItem(p1, 1), orderItem(p1, 2)),
		"duplicate split":    orderReq(orderItem(p1, 1), orderItem(p2, 1), orderItem(p1, 1)),
		"one bad among good": orderReq(orderItem(p1, 1), orderItem(p2, 0)),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			before := repo.txCount
			_, _, err := svc.CreateOrder(context.Background(), merchant, "key-"+strings.ReplaceAll(name, " ", "-"), req)
			if !errors.Is(err, service.ErrInvalidOrder) {
				t.Fatalf("err = %v, want ErrInvalidOrder", err)
			}
			if repo.txCount != before {
				t.Fatalf("transaction started for invalid request")
			}
		})
	}
	if repo.orderCount() != 0 {
		t.Fatalf("orders persisted for invalid requests")
	}
}

func TestCreateOrder_ItemBoundariesAccepted(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	p := repo.addProduct(merchant, "A", 7, "IDR", true)
	o, _, err := svc.CreateOrder(context.Background(), merchant, "qty-max", orderReq(orderItem(p, service.MaxOrderQuantity)))
	if err != nil {
		t.Fatalf("max quantity rejected: %v", err)
	}
	if o.TotalAmount != 7*service.MaxOrderQuantity {
		t.Fatalf("total = %d", o.TotalAmount)
	}

	items := make([]model.CreateOrderItemRequest, service.MaxOrderItems)
	for i := range items {
		items[i] = orderItem(repo.addProduct(merchant, fmt.Sprintf("P%d", i), 1, "IDR", true), 1)
	}
	o, _, err = svc.CreateOrder(context.Background(), merchant, "items-max", orderReq(items...))
	if err != nil {
		t.Fatalf("max items rejected: %v", err)
	}
	if len(o.Items) != service.MaxOrderItems || o.TotalAmount != int64(service.MaxOrderItems) {
		t.Fatalf("items=%d total=%d", len(o.Items), o.TotalAmount)
	}
}

// ─── Pricing / snapshots ─────────────────────────────────────────────────────

func TestCreateOrder_PricingFromProductSnapshot(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	coffee := repo.addProduct(merchant, "Coffee", 25000, "IDR", true)
	cake := repo.addProduct(merchant, "Cake", 17500, "IDR", true)
	free := repo.addProduct(merchant, "Water", 0, "IDR", true)

	order, replay, err := svc.CreateOrder(context.Background(), merchant, "price-1",
		orderReq(orderItem(coffee, 2), orderItem(cake, 3), orderItem(free, 5)))
	if err != nil || replay {
		t.Fatalf("err=%v replay=%v", err, replay)
	}
	want := int64(2*25000 + 3*17500)
	if order.Subtotal != want || order.TotalAmount != want {
		t.Fatalf("subtotal=%d total=%d want %d", order.Subtotal, order.TotalAmount, want)
	}
	if order.Currency != "IDR" || order.Status != model.OrderStatusCreated || order.MerchantID != merchant {
		t.Fatalf("unexpected header: %+v", order)
	}
	if order.ID == uuid.Nil || order.RequestHash == "" {
		t.Fatalf("missing id or hash")
	}
	if len(order.Items) != 3 {
		t.Fatalf("items = %d", len(order.Items))
	}
	byProduct := map[uuid.UUID]model.OrderItem{}
	var sum int64
	for _, it := range order.Items {
		if it.ProductID == nil {
			t.Fatalf("nil product id on new item")
		}
		if it.OrderID != order.ID || it.MerchantID != merchant || it.ID == uuid.Nil {
			t.Fatalf("item not linked to order/merchant: %+v", it)
		}
		if it.Subtotal != it.UnitPrice*it.Quantity {
			t.Fatalf("line subtotal mismatch: %+v", it)
		}
		sum += it.Subtotal
		byProduct[*it.ProductID] = it
	}
	if sum != order.Subtotal {
		t.Fatalf("sum of lines %d != subtotal %d", sum, order.Subtotal)
	}
	if it := byProduct[coffee]; it.ProductName != "Coffee" || it.UnitPrice != 25000 || it.Quantity != 2 {
		t.Fatalf("coffee snapshot wrong: %+v", it)
	}
	if it := byProduct[free]; it.UnitPrice != 0 || it.Subtotal != 0 {
		t.Fatalf("free snapshot wrong: %+v", it)
	}

	// Persisted snapshot is immune to later price changes.
	repo.setPrice(coffee, 99999)
	got, err := svc.GetOrder(context.Background(), merchant, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TotalAmount != want {
		t.Fatalf("stored total changed to %d", got.TotalAmount)
	}
}

func TestCreateOrder_ProductLookupsSortedAndScoped(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	ids := []uuid.UUID{
		repo.addProduct(merchant, "A", 1, "IDR", true),
		repo.addProduct(merchant, "B", 1, "IDR", true),
		repo.addProduct(merchant, "C", 1, "IDR", true),
		repo.addProduct(merchant, "D", 1, "IDR", true),
	}
	req := orderReq(orderItem(ids[2], 1), orderItem(ids[0], 1), orderItem(ids[3], 1), orderItem(ids[1], 1))
	original := append([]model.CreateOrderItemRequest(nil), req.Items...)
	if _, _, err := svc.CreateOrder(context.Background(), merchant, "sorted", req); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(repo.productLookups); i++ {
		if repo.productLookups[i-1].String() >= repo.productLookups[i].String() {
			t.Fatalf("product lookups not in stable sorted order: %v", repo.productLookups)
		}
	}
	for _, m := range repo.lookupMerchants {
		if m != merchant {
			t.Fatalf("product lookup used merchant %s, want %s", m, merchant)
		}
	}
	for i := range original {
		if req.Items[i] != original[i] {
			t.Fatalf("caller's request slice was mutated")
		}
	}
}

func TestCreateOrder_MixedCurrencyRejected(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 100, "IDR", true)
	b := repo.addProduct(merchant, "B", 100, "USD", true)
	_, _, err := svc.CreateOrder(context.Background(), merchant, "mixed", orderReq(orderItem(a, 1), orderItem(b, 1)))
	if !errors.Is(err, service.ErrOrderCurrency) {
		t.Fatalf("err = %v, want ErrOrderCurrency", err)
	}
	if repo.orderCount() != 0 || repo.createCalls != 0 {
		t.Fatalf("order persisted despite currency mismatch")
	}
}

func TestCreateOrder_CurrencyFromProduct(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 150, "USD", true)
	o, _, err := svc.CreateOrder(context.Background(), merchant, "usd", orderReq(orderItem(a, 2)))
	if err != nil || o.Currency != "USD" || o.TotalAmount != 300 {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

func TestCreateOrder_Overflow(t *testing.T) {
	t.Run("line overflow", func(t *testing.T) {
		repo, svc, merchant := newOrderFixture()
		p := repo.addProduct(merchant, "Big", math.MaxInt64/2+1, "IDR", true)
		_, _, err := svc.CreateOrder(context.Background(), merchant, "k", orderReq(orderItem(p, 2)))
		if !errors.Is(err, service.ErrOrderOverflow) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sum overflow", func(t *testing.T) {
		repo, svc, merchant := newOrderFixture()
		a := repo.addProduct(merchant, "A", math.MaxInt64-10, "IDR", true)
		b := repo.addProduct(merchant, "B", 11, "IDR", true)
		_, _, err := svc.CreateOrder(context.Background(), merchant, "k", orderReq(orderItem(a, 1), orderItem(b, 1)))
		if !errors.Is(err, service.ErrOrderOverflow) {
			t.Fatalf("err = %v", err)
		}
		if repo.orderCount() != 0 {
			t.Fatalf("overflowing order persisted")
		}
	})
	t.Run("negative price", func(t *testing.T) {
		repo, svc, merchant := newOrderFixture()
		p := repo.addProduct(merchant, "Neg", -1, "IDR", true)
		_, _, err := svc.CreateOrder(context.Background(), merchant, "k", orderReq(orderItem(p, 1)))
		if !errors.Is(err, service.ErrOrderOverflow) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("exact max accepted", func(t *testing.T) {
		repo, svc, merchant := newOrderFixture()
		a := repo.addProduct(merchant, "A", math.MaxInt64-10, "IDR", true)
		b := repo.addProduct(merchant, "B", 10, "IDR", true)
		o, _, err := svc.CreateOrder(context.Background(), merchant, "k", orderReq(orderItem(a, 1), orderItem(b, 1)))
		if err != nil || o.TotalAmount != math.MaxInt64 {
			t.Fatalf("o=%v err=%v", o, err)
		}
	})
	t.Run("exact line max accepted", func(t *testing.T) {
		repo, svc, merchant := newOrderFixture()
		p := repo.addProduct(merchant, "A", math.MaxInt64/1000, "IDR", true)
		o, _, err := svc.CreateOrder(context.Background(), merchant, "k", orderReq(orderItem(p, 1000)))
		if err != nil || o.TotalAmount != (math.MaxInt64/1000)*1000 {
			t.Fatalf("o=%v err=%v", o, err)
		}
	})
}

func TestCreateOrder_MissingOrInactiveProduct(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	active := repo.addProduct(merchant, "A", 100, "IDR", true)
	inactive := repo.addProduct(merchant, "I", 100, "IDR", false)
	for name, req := range map[string]model.CreateOrderRequest{
		"unknown":  orderReq(orderItem(uuid.New(), 1)),
		"inactive": orderReq(orderItem(inactive, 1)),
		"mixed":    orderReq(orderItem(active, 1), orderItem(inactive, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := svc.CreateOrder(context.Background(), merchant, "k-"+name, req)
			if !errors.Is(err, service.ErrOrderProductMissing) {
				t.Fatalf("err = %v, want ErrOrderProductMissing", err)
			}
		})
	}
	if repo.orderCount() != 0 || repo.createCalls != 0 {
		t.Fatalf("order persisted with missing product")
	}
}

// ─── Tenant isolation ────────────────────────────────────────────────────────

func TestCreateOrder_CrossMerchantProductRejected(t *testing.T) {
	repo, svc, merchantA := newOrderFixture()
	merchantB := uuid.New()
	foreign := repo.addProduct(merchantB, "B's product", 100, "IDR", true)
	own := repo.addProduct(merchantA, "A's product", 100, "IDR", true)

	_, _, err := svc.CreateOrder(context.Background(), merchantA, "k", orderReq(orderItem(own, 1), orderItem(foreign, 1)))
	if !errors.Is(err, service.ErrOrderProductMissing) {
		t.Fatalf("err = %v, want ErrOrderProductMissing (indistinguishable from missing)", err)
	}
	for _, m := range repo.lookupMerchants {
		if m != merchantA {
			t.Fatalf("lookup leaked merchant %s", m)
		}
	}
	if repo.orderCount() != 0 {
		t.Fatalf("order persisted with foreign product")
	}
}

func TestCreateOrder_IdempotencyKeyIsMerchantScoped(t *testing.T) {
	repo, svc, merchantA := newOrderFixture()
	merchantB := uuid.New()
	pa := repo.addProduct(merchantA, "A", 100, "IDR", true)
	pb := repo.addProduct(merchantB, "B", 200, "IDR", true)

	oa, replayA, err := svc.CreateOrder(context.Background(), merchantA, "shared-key", orderReq(orderItem(pa, 1)))
	if err != nil || replayA {
		t.Fatalf("A: err=%v replay=%v", err, replayA)
	}
	// Different body under the same key is NOT a conflict for another merchant.
	ob, replayB, err := svc.CreateOrder(context.Background(), merchantB, "shared-key", orderReq(orderItem(pb, 3)))
	if err != nil || replayB {
		t.Fatalf("B: err=%v replay=%v", err, replayB)
	}
	if oa.ID == ob.ID || ob.MerchantID != merchantB || ob.TotalAmount != 600 {
		t.Fatalf("merchant B got A's order or wrong data: %+v", ob)
	}
	if repo.orderCount() != 2 {
		t.Fatalf("orders = %d, want 2", repo.orderCount())
	}
}

func TestGetOrder_TenantIsolation(t *testing.T) {
	repo, svc, merchantA := newOrderFixture()
	merchantB := uuid.New()
	p := repo.addProduct(merchantA, "A", 100, "IDR", true)
	o, _, err := svc.CreateOrder(context.Background(), merchantA, "k", orderReq(orderItem(p, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetOrder(context.Background(), merchantB, o.ID); !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("cross-merchant get err = %v, want ErrOrderNotFound", err)
	}
	if repo.getMerchant != merchantB {
		t.Fatalf("repository received merchant %s", repo.getMerchant)
	}
	got, err := svc.GetOrder(context.Background(), merchantA, o.ID)
	if err != nil || got.ID != o.ID {
		t.Fatalf("own get failed: %v", err)
	}
	if _, err := svc.GetOrder(context.Background(), merchantA, uuid.New()); !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("missing get err = %v", err)
	}
}

func TestListOrders_TenantIsolationAndValidation(t *testing.T) {
	repo, svc, merchantA := newOrderFixture()
	merchantB := uuid.New()
	pa := repo.addProduct(merchantA, "A", 100, "IDR", true)
	pb := repo.addProduct(merchantB, "B", 100, "IDR", true)
	for i := 0; i < 3; i++ {
		if _, _, err := svc.CreateOrder(context.Background(), merchantA, fmt.Sprintf("a-%d", i), orderReq(orderItem(pa, 1))); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := svc.CreateOrder(context.Background(), merchantB, "b-0", orderReq(orderItem(pb, 1))); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ListOrders(context.Background(), merchantA, 2, 50)
	if err != nil {
		t.Fatal(err)
	}
	if repo.listMerchant != merchantA || repo.listPage != 2 || repo.listLimit != 50 {
		t.Fatalf("repo got merchant=%s page=%d limit=%d", repo.listMerchant, repo.listPage, repo.listLimit)
	}
	if res.Total != 3 {
		t.Fatalf("total = %d, want 3", res.Total)
	}
	for _, o := range res.Orders {
		if o.MerchantID != merchantA {
			t.Fatalf("foreign order leaked")
		}
	}

	calls := repo.listCalls
	for name, pl := range map[string][2]int{
		"page zero":       {0, 20},
		"negative page":   {-1, 20},
		"limit zero":      {1, 0},
		"negative limit":  {1, -5},
		"limit over max":  {1, model.MaxLimit + 1},
		"offset overflow": {math.MaxInt, 100},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.ListOrders(context.Background(), merchantA, pl[0], pl[1]); !errors.Is(err, service.ErrInvalidOrder) {
				t.Fatalf("err = %v, want ErrInvalidOrder", err)
			}
		})
	}
	if repo.listCalls != calls {
		t.Fatalf("repository called for invalid pagination")
	}
	if _, err := svc.ListOrders(context.Background(), merchantA, 1, model.MaxLimit); err != nil {
		t.Fatalf("max limit rejected: %v", err)
	}
}

// ─── Idempotency ─────────────────────────────────────────────────────────────

func TestCreateOrder_IdempotentReplay(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 1000, "IDR", true)
	b := repo.addProduct(merchant, "B", 500, "IDR", true)

	first, replay, err := svc.CreateOrder(context.Background(), merchant, "idem", orderReq(orderItem(a, 1), orderItem(b, 2)))
	if err != nil || replay {
		t.Fatalf("first: err=%v replay=%v", err, replay)
	}
	lookups := len(repo.productLookups)

	// Price changes and product deactivation must not affect the replay.
	repo.setPrice(a, 5)
	repo.mu.Lock()
	pb := repo.products[b]
	pb.Active = false
	repo.products[b] = pb
	repo.mu.Unlock()

	// Same items in a different order are an equivalent request.
	second, replay, err := svc.CreateOrder(context.Background(), merchant, "idem", orderReq(orderItem(b, 2), orderItem(a, 1)))
	if err != nil || !replay {
		t.Fatalf("second: err=%v replay=%v", err, replay)
	}
	if second.ID != first.ID || second.TotalAmount != first.TotalAmount || second.TotalAmount != 2000 {
		t.Fatalf("replay returned different order: first=%+v second=%+v", first, second)
	}
	if len(second.Items) != 2 {
		t.Fatalf("replay items = %d", len(second.Items))
	}
	if len(repo.productLookups) != lookups {
		t.Fatalf("replay re-read products")
	}
	if repo.orderCount() != 1 || repo.createCalls != 1 {
		t.Fatalf("orders=%d creates=%d, want 1/1", repo.orderCount(), repo.createCalls)
	}
}

func TestCreateOrder_IdempotencyConflict(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 1000, "IDR", true)
	b := repo.addProduct(merchant, "B", 500, "IDR", true)
	if _, _, err := svc.CreateOrder(context.Background(), merchant, "idem", orderReq(orderItem(a, 1))); err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]model.CreateOrderRequest{
		"different quantity": orderReq(orderItem(a, 2)),
		"different product":  orderReq(orderItem(b, 1)),
		"extra item":         orderReq(orderItem(a, 1), orderItem(b, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			o, replay, err := svc.CreateOrder(context.Background(), merchant, "idem", req)
			if !errors.Is(err, service.ErrOrderKeyConflict) || o != nil || replay {
				t.Fatalf("o=%v replay=%v err=%v, want ErrOrderKeyConflict", o, replay, err)
			}
		})
	}
	if repo.orderCount() != 1 {
		t.Fatalf("orders = %d", repo.orderCount())
	}
}

func TestCreateOrder_RequestHashCanonical(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 1, "IDR", true)
	b := repo.addProduct(merchant, "B", 1, "IDR", true)
	o1, _, err := svc.CreateOrder(context.Background(), merchant, "h1", orderReq(orderItem(a, 1), orderItem(b, 2)))
	if err != nil {
		t.Fatal(err)
	}
	o2, _, err := svc.CreateOrder(context.Background(), merchant, "h2", orderReq(orderItem(b, 2), orderItem(a, 1)))
	if err != nil {
		t.Fatal(err)
	}
	o3, _, err := svc.CreateOrder(context.Background(), merchant, "h3", orderReq(orderItem(b, 3), orderItem(a, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if o1.RequestHash != o2.RequestHash {
		t.Fatalf("permuted request hashed differently")
	}
	if o1.RequestHash == o3.RequestHash {
		t.Fatalf("different request produced identical hash")
	}
	if len(o1.RequestHash) != 64 {
		t.Fatalf("hash length %d, want hex sha256", len(o1.RequestHash))
	}
}

// ─── Rollback / error propagation ────────────────────────────────────────────

func TestCreateOrder_RollbackOnLaterItemFailure(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 100, "IDR", true)
	b := repo.addProduct(merchant, "B", 100, "IDR", true)
	boom := errors.New("db read failed")
	repo.productErr, repo.productErrFor = boom, b

	_, _, err := svc.CreateOrder(context.Background(), merchant, "rb", orderReq(orderItem(a, 1), orderItem(b, 1)))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped repo error", err)
	}
	if repo.orderCount() != 0 || repo.createCalls != 0 {
		t.Fatalf("partial order persisted")
	}

	// After the fault clears, the same key creates a fresh order (no stale replay).
	repo.productErr = nil
	o, replay, err := svc.CreateOrder(context.Background(), merchant, "rb", orderReq(orderItem(a, 1), orderItem(b, 1)))
	if err != nil || replay || o.TotalAmount != 200 {
		t.Fatalf("retry: o=%v replay=%v err=%v", o, replay, err)
	}
}

func TestCreateOrder_RollbackOnInsertFailure(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 100, "IDR", true)
	boom := errors.New("insert order item failed")
	repo.createErr = boom
	o, replay, err := svc.CreateOrder(context.Background(), merchant, "ins", orderReq(orderItem(a, 1)))
	if !errors.Is(err, boom) || o != nil || replay {
		t.Fatalf("o=%v replay=%v err=%v", o, replay, err)
	}
	if repo.orderCount() != 0 {
		t.Fatalf("order committed after insert failure")
	}
	repo.createErr = nil
	if _, replay, err := svc.CreateOrder(context.Background(), merchant, "ins", orderReq(orderItem(a, 1))); err != nil || replay {
		t.Fatalf("retry: replay=%v err=%v", replay, err)
	}
}

func TestCreateOrder_RollbackOnBusinessErrorAfterPartialPricing(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 100, "IDR", true)
	b := repo.addProduct(merchant, "B", 100, "SGD", true)
	if _, _, err := svc.CreateOrder(context.Background(), merchant, "biz", orderReq(orderItem(a, 1), orderItem(b, 1))); !errors.Is(err, service.ErrOrderCurrency) {
		t.Fatalf("err = %v", err)
	}
	// Key was not consumed: a valid request under the same key now succeeds.
	o, replay, err := svc.CreateOrder(context.Background(), merchant, "biz", orderReq(orderItem(a, 1)))
	if err != nil || replay || o.TotalAmount != 100 {
		t.Fatalf("o=%v replay=%v err=%v", o, replay, err)
	}
}

func TestCreateOrder_RepositoryErrorsPropagate(t *testing.T) {
	boom := errors.New("connection reset")
	for name, set := range map[string]func(*fakeOrderRepo){
		"lock":    func(r *fakeOrderRepo) { r.lockErr = boom },
		"find":    func(r *fakeOrderRepo) { r.findErr = boom },
		"product": func(r *fakeOrderRepo) { r.productErr = boom },
		"create":  func(r *fakeOrderRepo) { r.createErr = boom },
	} {
		t.Run(name, func(t *testing.T) {
			repo, svc, merchant := newOrderFixture()
			p := repo.addProduct(merchant, "A", 1, "IDR", true)
			set(repo)
			o, replay, err := svc.CreateOrder(context.Background(), merchant, "k", orderReq(orderItem(p, 1)))
			if !errors.Is(err, boom) || o != nil || replay {
				t.Fatalf("o=%v replay=%v err=%v", o, replay, err)
			}
			for _, sentinel := range []error{service.ErrOrderKeyConflict, service.ErrOrderProductMissing, service.ErrInvalidOrder} {
				if errors.Is(err, sentinel) {
					t.Fatalf("infra error misclassified as %v", sentinel)
				}
			}
			if repo.orderCount() != 0 {
				t.Fatalf("order committed")
			}
		})
	}
}

// ─── Concurrency ─────────────────────────────────────────────────────────────

func TestCreateOrder_ConcurrentSameKeySameBody(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 1000, "IDR", true)
	b := repo.addProduct(merchant, "B", 250, "IDR", true)

	const n = 32
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, n)
	replays := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := orderReq(orderItem(a, 1), orderItem(b, 4))
			if i%2 == 1 { // permuted but equivalent
				req = orderReq(orderItem(b, 4), orderItem(a, 1))
			}
			o, r, err := svc.CreateOrder(context.Background(), merchant, "race", req)
			errs[i], replays[i] = err, r
			if o != nil {
				ids[i] = o.ID
			}
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if !replays[i] {
			created++
		}
		if ids[i] != ids[0] {
			t.Fatalf("goroutine %d got order %s, want %s", i, ids[i], ids[0])
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1", created)
	}
	if repo.orderCount() != 1 || repo.createCalls != 1 {
		t.Fatalf("orders=%d creates=%d", repo.orderCount(), repo.createCalls)
	}
}

func TestCreateOrder_ConcurrentSameKeyDifferentBodies(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 10, "IDR", true)

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, conflicts := 0, 0
	var other []error
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(qty int64) {
			defer wg.Done()
			<-start
			_, replay, err := svc.CreateOrder(context.Background(), merchant, "race-diff", orderReq(orderItem(a, qty)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && !replay:
				created++
			case errors.Is(err, service.ErrOrderKeyConflict):
				conflicts++
			default:
				other = append(other, fmt.Errorf("replay=%v err=%v", replay, err))
			}
		}(int64(i + 1))
	}
	close(start)
	wg.Wait()
	if len(other) > 0 {
		t.Fatalf("unexpected outcomes: %v", other)
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("created=%d conflicts=%d", created, conflicts)
	}
	if repo.orderCount() != 1 {
		t.Fatalf("orders = %d", repo.orderCount())
	}
}

func TestCreateOrder_ConcurrentDistinctKeys(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	a := repo.addProduct(merchant, "A", 10, "IDR", true)
	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, replay, err := svc.CreateOrder(context.Background(), merchant, fmt.Sprintf("key-%d", i), orderReq(orderItem(a, 1)))
			if err == nil && replay {
				err = errors.New("unexpected replay")
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if repo.orderCount() != n {
		t.Fatalf("orders = %d, want %d", repo.orderCount(), n)
	}
}

func TestCreateOrder_PriceChangeNeverMutatesExistingOrders(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	coffee := repo.addProduct(merchant, "Coffee", 25000, "IDR", true)

	before, _, err := svc.CreateOrder(context.Background(), merchant, "snap-1",
		orderReq(orderItem(coffee, 2)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if before.TotalAmount != 50000 {
		t.Fatalf("total=%d want 50000", before.TotalAmount)
	}

	// Merchant edits the catalog price afterwards.
	repo.setPrice(coffee, 99999)

	// The stored order keeps its original totals and snapshots.
	stored, err := svc.GetOrder(context.Background(), merchant, before.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.TotalAmount != 50000 || stored.Subtotal != 50000 {
		t.Fatalf("stored totals changed: %+v", stored)
	}
	if len(stored.Items) != 1 || stored.Items[0].UnitPrice != 25000 || stored.Items[0].Subtotal != 50000 || stored.Items[0].ProductName != "Coffee" {
		t.Fatalf("stored snapshot changed: %+v", stored.Items)
	}

	// A new order reflects the new price.
	after, _, err := svc.CreateOrder(context.Background(), merchant, "snap-2",
		orderReq(orderItem(coffee, 2)))
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if after.TotalAmount != 199998 {
		t.Fatalf("new total=%d want 199998", after.TotalAmount)
	}
}

func TestCreateOrder_InactiveProductRejected(t *testing.T) {
	repo, svc, merchant := newOrderFixture()
	dead := repo.addProduct(merchant, "Retired", 10000, "IDR", false)

	if _, _, err := svc.CreateOrder(context.Background(), merchant, "inactive-1",
		orderReq(orderItem(dead, 1))); !errors.Is(err, service.ErrOrderProductMissing) {
		t.Fatalf("inactive product: got %v", err)
	}
	if repo.orderCount() != 0 {
		t.Fatalf("failed order must not persist")
	}
}
