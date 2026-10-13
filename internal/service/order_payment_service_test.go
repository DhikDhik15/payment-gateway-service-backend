package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── Order-scoped transaction repository double ──────────────────────────────
//
// orderTxRepo reproduces the two guarantees migration 000022 adds to
// PostgreSQL and additionally rejects any status update the domain state
// machine does not allow, so a disagreement between
// PaymentService.transitionXxx and TransactionStatus.CanTransitionTo fails
// loudly instead of being silently absorbed by an in-memory fake.

type orderTxRepo struct {
	*mockTransactionRepo
}

func newOrderTxRepo() *orderTxRepo {
	return &orderTxRepo{mockTransactionRepo: newMockTransactionRepo()}
}

// FindByMerchantAndOrderID implements repository.OrderPaymentLookup.
func (r *orderTxRepo) FindByMerchantAndOrderID(_ context.Context, merchantID, orderID uuid.UUID) ([]*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.Transaction, 0)
	for _, tx := range r.txs {
		if tx == nil || tx.OrderID == nil || *tx.OrderID != orderID || tx.MerchantID != merchantID {
			continue
		}
		out = append(out, cloneTx(tx))
	}
	// Mirror the SQL ORDER BY created_at DESC, id DESC.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID.String() > out[j].ID.String()
	})
	return out, nil
}

// Create enforces uq_transactions_order_active and uq_transactions_order_paid.
// The check and the insert share the map lock so concurrent creators cannot
// both pass the guard.
func (r *orderTxRepo) Create(_ context.Context, tx *model.Transaction) error {
	if tx.OrderID == nil {
		return r.mockTransactionRepo.Create(context.Background(), tx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.txs {
		if existing == nil || existing.OrderID == nil {
			continue // legacy payment: order_id NULL never conflicts
		}
		if existing.MerchantID != tx.MerchantID || *existing.OrderID != *tx.OrderID {
			continue
		}
		switch existing.Status {
		case model.TransactionStatusCreated, model.TransactionStatusPending, model.TransactionStatusPaid:
			return repository.ErrOrderPaymentConflict
		}
	}
	r.txs[tx.ID] = cloneTx(tx)
	return nil
}

func (r *orderTxRepo) UpdateStatus(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus) error {
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("state machine rejects %s -> %s", from, to)
	}
	return r.mockTransactionRepo.UpdateStatus(ctx, id, from, to)
}

func (r *orderTxRepo) UpdateStatusWithProvider(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus, provider, providerTransactionID, paymentURL string, expiredAt *time.Time) error {
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("state machine rejects %s -> %s", from, to)
	}
	return r.mockTransactionRepo.UpdateStatusWithProvider(ctx, id, from, to, provider, providerTransactionID, paymentURL, expiredAt)
}

func (r *orderTxRepo) UpdateStatusWithPaidAt(ctx context.Context, id uuid.UUID, from model.TransactionStatus) error {
	if !from.CanTransitionTo(model.TransactionStatusPaid) {
		return fmt.Errorf("state machine rejects %s -> PAID", from)
	}
	return r.mockTransactionRepo.UpdateStatusWithPaidAt(ctx, id, from)
}

// safeAttemptRepo adds the locking the production repository has; the shared
// in-memory attempt double is only used single-threaded by older tests.
type safeAttemptRepo struct {
	mu    sync.Mutex
	inner *mockPaymentAttemptRepo
}

func (r *safeAttemptRepo) Create(ctx context.Context, a *model.PaymentAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.Create(ctx, a)
}

func (r *safeAttemptRepo) FindByTransactionID(ctx context.Context, id uuid.UUID) ([]model.PaymentAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.FindByTransactionID(ctx, id)
}

func (r *safeAttemptRepo) CountByTransactionID(ctx context.Context, id uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.CountByTransactionID(ctx, id)
}

// ─── Scriptable provider ─────────────────────────────────────────────────────

type scriptedProvider struct {
	mu          sync.Mutex
	createCalls int
	getCalls    int
	onCreate    func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error)
	onGet       func(id string) (*service.ProviderPaymentResponse, error)
}

func newScriptedProvider() *scriptedProvider {
	return &scriptedProvider{}
}

func (p *scriptedProvider) Name() string { return "MOCK" }

func (p *scriptedProvider) CreatePayment(_ context.Context, req service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
	p.mu.Lock()
	p.createCalls++
	fn := p.onCreate
	p.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return pendingResponse(req.TransactionID), nil
}

func (p *scriptedProvider) GetPayment(_ context.Context, id string) (*service.ProviderPaymentResponse, error) {
	p.mu.Lock()
	p.getCalls++
	fn := p.onGet
	p.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	return pendingResponse(id), nil
}

func (p *scriptedProvider) CancelPayment(context.Context, string) error { return nil }

func (p *scriptedProvider) creates() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createCalls
}

func (p *scriptedProvider) gets() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.getCalls
}

func pendingResponse(id string) *service.ProviderPaymentResponse {
	expiry := time.Now().UTC().Add(30 * time.Minute)
	return &service.ProviderPaymentResponse{
		Provider:              "MOCK",
		ProviderTransactionID: id,
		Status:                "PENDING",
		PaymentURL:            "https://mock-provider.test/pay/" + id,
		ExpiredAt:             &expiry,
	}
}

// ─── Harness ─────────────────────────────────────────────────────────────────

type orderPaymentHarness struct {
	t           *testing.T
	svc         service.OrderPaymentService
	txRepo      *orderTxRepo
	attemptRepo *safeAttemptRepo
	orders      *fakeOrderRepo
	provider    *scriptedProvider
	idem        *memoryIdempotencyRepo
	merchantID  uuid.UUID
}

func newOrderPaymentHarness(t *testing.T, merchantID uuid.UUID) *orderPaymentHarness {
	t.Helper()
	provider := newScriptedProvider()
	txRepo := newOrderTxRepo()
	attemptRepo := &safeAttemptRepo{inner: newMockAttemptRepo()}
	idem := newMemoryIdempotencyRepo()
	payments := service.NewPaymentServiceWithIdempotency(txRepo, attemptRepo, provider, idem, time.Hour)
	orders := newFakeOrderRepo()

	return &orderPaymentHarness{
		t:           t,
		svc:         service.NewOrderPaymentService(payments, orders),
		txRepo:      txRepo,
		attemptRepo: attemptRepo,
		orders:      orders,
		provider:    provider,
		idem:        idem,
		merchantID:  merchantID,
	}
}

// addOrder seeds an order the way OrderService persists it (Phase 8A model:
// CREATED status, immutable price snapshot).
func (h *orderPaymentHarness) addOrder(merchantID uuid.UUID, currency string, total int64) uuid.UUID {
	h.t.Helper()
	now := time.Now().UTC()
	order := &model.Order{
		ID:          uuid.New(),
		MerchantID:  merchantID,
		Status:      model.OrderStatusCreated,
		Currency:    currency,
		Subtotal:    total,
		TotalAmount: total,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	h.orders.mu.Lock()
	h.orders.orders[order.ID] = order
	h.orders.mu.Unlock()
	return order.ID
}

func (h *orderPaymentHarness) seedAttempt(orderID, merchantID uuid.UUID, status model.TransactionStatus, age time.Duration) uuid.UUID {
	h.t.Helper()
	now := time.Now().UTC()
	tx := &model.Transaction{
		ID:              uuid.New(),
		MerchantID:      merchantID,
		OrderID:         &orderID,
		MerchantOrderID: fmt.Sprintf("order-%s-%s", orderID, uuid.NewString()),
		Amount:          15000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
		Status:          status,
		CreatedAt:       now.Add(-age),
		UpdatedAt:       now,
	}
	if err := h.txRepo.Create(context.Background(), tx); err != nil {
		h.t.Fatalf("seed attempt: %v", err)
	}
	return tx.ID
}

func (h *orderPaymentHarness) findTransaction(id uuid.UUID) *model.Transaction {
	h.t.Helper()
	tx, err := h.txRepo.FindByID(context.Background(), id)
	if err != nil {
		h.t.Fatalf("transaction %s: %v", id, err)
	}
	return tx
}

func (h *orderPaymentHarness) orderTransactions(orderID uuid.UUID) []*model.Transaction {
	h.t.Helper()
	out, err := h.txRepo.FindByMerchantAndOrderID(context.Background(), h.merchantID, orderID)
	if err != nil {
		h.t.Fatalf("find order transactions: %v", err)
	}
	return out
}

// ageOrderAttempts backdates persisted attempts so a retry treats them as no
// longer possibly in flight at the provider.
func (h *orderPaymentHarness) ageOrderAttempts(orderID uuid.UUID, age time.Duration) {
	h.t.Helper()
	h.txRepo.mu.Lock()
	defer h.txRepo.mu.Unlock()
	for _, tx := range h.txRepo.txs {
		if tx.OrderID == nil || *tx.OrderID != orderID {
			continue
		}
		tx.CreatedAt = tx.CreatedAt.Add(-age)
	}
}

func (h *orderPaymentHarness) create(orderID uuid.UUID, method, key string) (*model.CreatePaymentResponse, error) {
	h.t.Helper()
	return h.svc.CreatePaymentForOrder(context.Background(), h.merchantID, orderID,
		model.CreateOrderPaymentRequest{PaymentMethod: method}, key)
}

// ─── Happy path ──────────────────────────────────────────────────────────────

func TestCreatePaymentForOrder_DerivesServerSidePricing(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 77000)

	resp, err := h.create(orderID, "QRIS", "idem-happy-1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if resp.Amount != 77000 {
		t.Errorf("amount: expected order.total_amount 77000, got %d", resp.Amount)
	}
	if resp.Currency != "IDR" {
		t.Errorf("currency: expected order.currency IDR, got %q", resp.Currency)
	}
	if resp.OrderID == nil || *resp.OrderID != orderID {
		t.Errorf("order_id: expected %s, got %v", orderID, resp.OrderID)
	}
	if resp.Status != model.TransactionStatusPending {
		t.Errorf("status: expected PENDING, got %s", resp.Status)
	}
	if resp.PaymentMethod != "QRIS" {
		t.Errorf("payment method: expected QRIS, got %q", resp.PaymentMethod)
	}
	if resp.PaymentURL == "" {
		t.Error("expected a backend-provided payment_url")
	}

	// Persisted row.
	tx := h.findTransaction(resp.TransactionID)
	if tx.OrderID == nil || *tx.OrderID != orderID {
		t.Errorf("stored order_id: expected %s, got %v", orderID, tx.OrderID)
	}
	if tx.Amount != 77000 || tx.Currency != "IDR" {
		t.Errorf("stored pricing: got %d %s, want 77000 IDR", tx.Amount, tx.Currency)
	}
	if tx.MerchantID != merchantAID {
		t.Errorf("stored merchant: expected %s, got %s", merchantAID, tx.MerchantID)
	}
	if tx.Status != model.TransactionStatusPending {
		t.Errorf("stored status: expected PENDING, got %s", tx.Status)
	}
	// merchant_order_id keeps its external meaning: unique per attempt, never
	// the internal Order UUID.
	if tx.MerchantOrderID == orderID.String() {
		t.Error("merchant_order_id must not be the internal Order UUID")
	}
	if len(tx.MerchantOrderID) == 0 || len(tx.MerchantOrderID) > 100 {
		t.Errorf("merchant_order_id length out of contract: %q", tx.MerchantOrderID)
	}
}

func TestCreatePaymentForOrder_RequestCarriesOnlyPaymentMethod(t *testing.T) {
	// The wire contract accepts payment_method alone; pricing fields are not
	// part of CreateOrderPaymentRequest and cannot reach the service.
	var req model.CreateOrderPaymentRequest
	body := []byte(`{"payment_method":"QRIS","amount":1,"currency":"USD","merchant_id":"00000000-0000-0000-0000-000000000000","total_amount":1,"subtotal":1,"unit_price":1,"merchant_order_id":"x"}`)
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.PaymentMethod != "QRIS" {
		t.Fatalf("payment_method: expected QRIS, got %q", req.PaymentMethod)
	}

	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 90500)

	resp, err := h.svc.CreatePaymentForOrder(context.Background(), merchantAID, orderID, req, "idem-only-method")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if resp.Amount != 90500 || resp.Currency != "IDR" {
		t.Errorf("server pricing must win: got %d %s, want 90500 IDR", resp.Amount, resp.Currency)
	}
}

// ─── Security ────────────────────────────────────────────────────────────────

func TestCreatePaymentForOrder_MissingOrder(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)

	_, err := h.create(uuid.New(), "QRIS", "idem-missing")
	if !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
}

func TestCreatePaymentForOrder_ForeignOrderIsNotFound(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	// Merchant B's Order, requested by merchant A's identity.
	foreignOrderID := h.addOrder(merchantBID, "IDR", 12345)

	_, err := h.create(foreignOrderID, "QRIS", "idem-foreign")
	if !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound for a foreign Order, got %v", err)
	}
	if got := len(h.orderTransactions(foreignOrderID)); got != 0 {
		t.Errorf("no payment may be created for a foreign Order, found %d", got)
	}
}

func TestCreatePaymentForOrder_UnsupportedPaymentMethod(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 10000)

	for _, method := range []string{"CREDIT_CARD", "VA", "gopay", ""} {
		_, err := h.create(orderID, method, "idem-method-"+method)
		if !errors.Is(err, service.ErrInvalidPaymentMethod) {
			t.Errorf("method %q: expected ErrInvalidPaymentMethod, got %v", method, err)
		}
	}
	if h.provider.creates() != 0 {
		t.Errorf("provider must never be called for a rejected method, got %d calls", h.provider.creates())
	}
	if got := len(h.orderTransactions(orderID)); got != 0 {
		t.Errorf("rejected request must not persist a payment, found %d", got)
	}
}

func TestCreatePaymentForOrder_UnsupportedCurrency(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "USD", 10000)

	_, err := h.create(orderID, "QRIS", "idem-currency")
	if !errors.Is(err, service.ErrInvalidCurrency) {
		t.Fatalf("expected ErrInvalidCurrency, got %v", err)
	}
}

func TestCreatePaymentForOrder_NonPayableOrderStatus(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 10000)

	h.orders.mu.Lock()
	h.orders.orders[orderID].Status = "PAID"
	h.orders.mu.Unlock()

	_, err := h.create(orderID, "QRIS", "idem-status")
	if !errors.Is(err, service.ErrOrderPaymentNotAvailable) {
		t.Fatalf("expected ErrOrderPaymentNotAvailable, got %v", err)
	}
}

// ─── Retry policy ────────────────────────────────────────────────────────────

func TestCreatePaymentForOrder_RetryPolicy(t *testing.T) {
	cases := []struct {
		name     string
		existing model.TransactionStatus
		wantErr  error
	}{
		{"FAILED allows a new attempt", model.TransactionStatusFailed, nil},
		{"CANCELLED allows a new attempt", model.TransactionStatusCancelled, nil},
		{"EXPIRED allows a new attempt", model.TransactionStatusExpired, nil},
		{"PENDING blocks a new attempt", model.TransactionStatusPending, service.ErrOrderPaymentConflict},
		{"PAID blocks a new attempt", model.TransactionStatusPaid, service.ErrOrderPaymentConflict},
		{"CREATED blocks a new attempt", model.TransactionStatusCreated, service.ErrOrderPaymentConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOrderPaymentHarness(t, merchantAID)
			orderID := h.addOrder(merchantAID, "IDR", 15000)
			seeded := h.seedAttempt(orderID, merchantAID, tc.existing, time.Minute)

			// A CREATED row left by an ambiguous timeout is resolved through a
			// provider status lookup before the retry decision is made.
			h.provider.onGet = func(id string) (*service.ProviderPaymentResponse, error) {
				resp := pendingResponse(id)
				resp.Status = string(tc.existing)
				return resp, nil
			}

			before := h.provider.creates()
			resp, err := h.create(orderID, "QRIS", "idem-retry-1")

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				if h.provider.creates() != before {
					t.Error("provider create must not be called while an active payment exists")
				}
				if got := len(h.orderTransactions(orderID)); got != 1 {
					t.Errorf("expected exactly 1 payment attempt, got %d", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("expected a new attempt to be allowed, got %v", err)
			}
			if resp.TransactionID == seeded {
				t.Error("a new attempt must be a new Transaction")
			}
			if got := len(h.orderTransactions(orderID)); got != 2 {
				t.Errorf("expected 2 payment attempts after retry, got %d", got)
			}
			// The Order amount is immutable across retries.
			if resp.Amount != 15000 {
				t.Errorf("retry changed the amount: got %d, want 15000", resp.Amount)
			}
		})
	}
}

func TestCreatePaymentForOrder_DefinitiveProviderFailureAllowsRetry(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 25000)

	h.provider.onCreate = func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderFailure
	}
	_, err := h.create(orderID, "QRIS", "idem-fail-1")
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("expected ErrProviderFailure, got %v", err)
	}

	rows := h.orderTransactions(orderID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(rows))
	}
	// Lifecycle: CREATED → FAILED on a definitive provider failure, and the
	// transition must be permitted by TransactionStatus.CanTransitionTo too.
	if rows[0].Status != model.TransactionStatusFailed {
		t.Fatalf("expected FAILED after definitive provider failure, got %s", rows[0].Status)
	}
	if !model.TransactionStatusCreated.CanTransitionTo(model.TransactionStatusFailed) {
		t.Fatal("state machine must allow CREATED -> FAILED")
	}

	// Retry with a fresh Idempotency-Key.
	h.provider.onCreate = nil
	resp, err := h.create(orderID, "QRIS", "idem-fail-2")
	if err != nil {
		t.Fatalf("retry after FAILED must be allowed, got %v", err)
	}
	if resp.Status != model.TransactionStatusPending {
		t.Errorf("expected retry to reach PENDING, got %s", resp.Status)
	}
	if got := len(h.orderTransactions(orderID)); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
	if h.provider.creates() != 2 {
		t.Errorf("expected 2 provider create calls, got %d", h.provider.creates())
	}
}

// ─── Idempotency ─────────────────────────────────────────────────────────────

func TestCreatePaymentForOrder_SameKeySameRequestReplays(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 64000)

	first, err := h.create(orderID, "QRIS", "idem-stable")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := h.create(orderID, "QRIS", "idem-stable")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if second.TransactionID != first.TransactionID {
		t.Errorf("replay must return the original transaction: %s != %s", second.TransactionID, first.TransactionID)
	}
	if second.Amount != first.Amount || second.Currency != first.Currency {
		t.Error("replay must return the original payment values")
	}
	if first.OrderID == nil || second.OrderID == nil || *second.OrderID != *first.OrderID {
		t.Errorf("replay must return the original order link: %v != %v", first.OrderID, second.OrderID)
	}
	if h.provider.creates() != 1 {
		t.Errorf("provider must be called exactly once, got %d", h.provider.creates())
	}
	if got := len(h.orderTransactions(orderID)); got != 1 {
		t.Errorf("replay must not create a second payment, found %d", got)
	}
}

func TestCreatePaymentForOrder_SameKeyDifferentRequestConflicts(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	firstOrder := h.addOrder(merchantAID, "IDR", 10000)
	secondOrder := h.addOrder(merchantAID, "IDR", 20000)

	if _, err := h.create(firstOrder, "QRIS", "idem-shared"); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Same merchant-scoped key bound to a different Order is a different
	// logical request and must be rejected, never silently honoured.
	_, err := h.create(secondOrder, "QRIS", "idem-shared")
	if !errors.Is(err, service.ErrIdempotencyKeyReused) {
		t.Fatalf("expected ErrIdempotencyKeyReused, got %v", err)
	}
	if got := len(h.orderTransactions(secondOrder)); got != 0 {
		t.Errorf("conflicting request must not create a payment, found %d", got)
	}
	if h.provider.creates() != 1 {
		t.Errorf("provider must be called once, got %d", h.provider.creates())
	}
}

func TestCreatePaymentForOrder_KeyIsMerchantScoped(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderA := h.addOrder(merchantAID, "IDR", 10000)
	orderB := h.addOrder(merchantBID, "IDR", 10000)

	// Merchant A uses the key for its own Order.
	if _, err := h.create(orderA, "QRIS", "idem-scoped"); err != nil {
		t.Fatalf("merchant A create: %v", err)
	}

	// Merchant B reusing the same literal key for its own Order is a separate
	// idempotency scope and must succeed.
	resp, err := h.svc.CreatePaymentForOrder(context.Background(), merchantBID, orderB,
		model.CreateOrderPaymentRequest{PaymentMethod: "QRIS"}, "idem-scoped")
	if err != nil {
		t.Fatalf("merchant B must own its idempotency scope, got %v", err)
	}
	if resp.OrderID == nil || *resp.OrderID != orderB {
		t.Errorf("expected merchant B's order, got %v", resp.OrderID)
	}
}

func TestCreatePaymentForOrder_ConcurrentSameKeySinglePayment(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 48000)

	const workers = 6
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		success  []*model.CreatePaymentResponse
		failures []error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := h.create(orderID, "QRIS", "idem-concurrent")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success = append(success, resp)
			} else {
				failures = append(failures, err)
			}
		}()
	}
	wg.Wait()

	for _, err := range failures {
		if !errors.Is(err, service.ErrIdempotencyInProgress) {
			t.Errorf("only in-flight conflicts are acceptable, got %v", err)
		}
	}
	if len(success) == 0 {
		t.Fatal("expected at least one successful initiation")
	}
	for _, s := range success {
		if s.TransactionID != success[0].TransactionID {
			t.Errorf("all replays must share one transaction: %s != %s", s.TransactionID, success[0].TransactionID)
		}
	}
	if got := len(h.orderTransactions(orderID)); got != 1 {
		t.Errorf("exactly one payment must exist, found %d", got)
	}
	if h.provider.creates() != 1 {
		t.Errorf("provider must be called exactly once, got %d", h.provider.creates())
	}
}

func TestCreatePaymentForOrder_ConcurrentDistinctKeysOnlyOneActivePayment(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 55000)

	const workers = 6
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		success  int
		conflict int
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.create(orderID, "QRIS", fmt.Sprintf("idem-race-%d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, service.ErrOrderPaymentConflict), errors.Is(err, repository.ErrOrderPaymentConflict):
				conflict++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if success != 1 {
		t.Errorf("exactly one initiation must win, got %d", success)
	}
	if success+conflict != workers {
		t.Errorf("every loser must be a conflict: success=%d conflict=%d", success, conflict)
	}
	if got := len(h.orderTransactions(orderID)); got != 1 {
		t.Errorf("exactly one active payment must exist, found %d", got)
	}
}

// ─── Provider timeout / ambiguous outcome ────────────────────────────────────

func TestCreatePaymentForOrder_AmbiguousTimeoutKeepsAttemptUnresolved(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 70000)

	h.provider.onCreate = func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderTimeout
	}
	h.provider.onGet = func(string) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderTimeout
	}

	_, err := h.create(orderID, "QRIS", "idem-ambiguous-1")
	if !errors.Is(err, service.ErrProviderTimeout) {
		t.Fatalf("expected ErrProviderTimeout, got %v", err)
	}

	rows := h.orderTransactions(orderID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(rows))
	}
	if rows[0].Status != model.TransactionStatusCreated {
		t.Errorf("an unresolved attempt must stay CREATED so the active guard holds, got %s", rows[0].Status)
	}
	if h.provider.creates() != 1 {
		t.Errorf("a create must never be retried blindly, got %d calls", h.provider.creates())
	}

	// While the attempt is fresh it may still be in flight: a second attempt
	// is refused outright, without even touching the provider.
	_, err = h.create(orderID, "QRIS", "idem-ambiguous-2")
	if !errors.Is(err, service.ErrOrderPaymentConflict) {
		t.Fatalf("expected the fresh unresolved attempt to block a retry, got %v", err)
	}
	if h.provider.creates() != 1 {
		t.Errorf("provider create must still be called once, got %d", h.provider.creates())
	}
	if h.provider.gets() != 1 {
		t.Errorf("expected only the original reconciliation lookup, got %d", h.provider.gets())
	}

	// Once the attempt is older than the in-flight grace window a retry is
	// allowed to resolve it — through a status lookup, never a new create.
	h.ageOrderAttempts(orderID, time.Hour)
	_, err = h.create(orderID, "QRIS", "idem-ambiguous-3")
	if !errors.Is(err, service.ErrProviderTimeout) {
		t.Fatalf("expected the lookup to stay unresolved, got %v", err)
	}
	if h.provider.creates() != 1 {
		t.Errorf("a create must never be retried blindly, got %d", h.provider.creates())
	}
	if got := len(h.orderTransactions(orderID)); got != 1 {
		t.Errorf("expected 1 attempt, found %d", got)
	}
}

func TestCreatePaymentForOrder_FreshCreatedAttemptBlocksRetryWithoutLookup(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 61000)
	h.seedAttempt(orderID, merchantAID, model.TransactionStatusCreated, 0)

	_, err := h.create(orderID, "QRIS", "idem-fresh-created")
	if !errors.Is(err, service.ErrOrderPaymentConflict) {
		t.Fatalf("expected ErrOrderPaymentConflict, got %v", err)
	}
	if h.provider.creates() != 0 || h.provider.gets() != 0 {
		t.Errorf("a possibly in-flight attempt must not be touched: creates=%d gets=%d",
			h.provider.creates(), h.provider.gets())
	}
	if got := len(h.orderTransactions(orderID)); got != 1 {
		t.Errorf("expected 1 attempt, found %d", got)
	}
}

func TestCreatePaymentForOrder_AmbiguousTimeoutReconciledToPending(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 81000)

	h.provider.onCreate = func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderTimeout
	}
	h.provider.onGet = func(id string) (*service.ProviderPaymentResponse, error) {
		return pendingResponse(id), nil
	}

	resp, err := h.create(orderID, "QRIS", "idem-reconcile-pending")
	if err != nil {
		t.Fatalf("expected reconciliation to resolve the attempt, got %v", err)
	}
	if resp.Status != model.TransactionStatusPending {
		t.Errorf("expected PENDING after lookup, got %s", resp.Status)
	}
	if h.provider.creates() != 1 {
		t.Errorf("no blind create retry allowed, got %d", h.provider.creates())
	}
	if h.provider.gets() == 0 {
		t.Error("expected a GetPayment lookup after the ambiguous timeout")
	}

	// The attempt is now active: another initiation must be refused.
	_, err = h.create(orderID, "QRIS", "idem-reconcile-pending-2")
	if !errors.Is(err, service.ErrOrderPaymentConflict) {
		t.Fatalf("expected ErrOrderPaymentConflict, got %v", err)
	}
}

func TestCreatePaymentForOrder_AmbiguousTimeoutReconciledToPaid(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 99000)

	h.provider.onCreate = func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderTimeout
	}
	h.provider.onGet = func(id string) (*service.ProviderPaymentResponse, error) {
		resp := pendingResponse(id)
		resp.Status = "settlement"
		return resp, nil
	}

	resp, err := h.create(orderID, "QRIS", "idem-reconcile-paid")
	if err != nil {
		t.Fatalf("expected reconciliation to resolve the attempt, got %v", err)
	}
	if resp.Status != model.TransactionStatusPaid {
		t.Errorf("expected PAID after lookup, got %s", resp.Status)
	}
	rows := h.orderTransactions(orderID)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 transaction, found %d", len(rows))
	}
	if rows[0].PaidAt == nil {
		t.Error("PAID must record paid_at")
	}
	if h.provider.creates() != 1 {
		t.Errorf("expected 1 provider create, got %d", h.provider.creates())
	}

	// A paid Order never accepts another payment.
	_, err = h.create(orderID, "QRIS", "idem-reconcile-paid-2")
	if !errors.Is(err, service.ErrOrderPaymentConflict) {
		t.Fatalf("expected ErrOrderPaymentConflict for a paid Order, got %v", err)
	}
	if got := len(h.orderTransactions(orderID)); got != 1 {
		t.Errorf("expected 1 transaction, found %d", got)
	}
}

func TestCreatePaymentForOrder_AmbiguousTimeoutLookupNotFoundAllowsRetry(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 33000)

	h.provider.onCreate = func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderTimeout
	}
	h.provider.onGet = func(string) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderPaymentNotFound
	}

	_, err := h.create(orderID, "QRIS", "idem-notfound-1")
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("a definitive 'not found' lookup is a failure, got %v", err)
	}
	rows := h.orderTransactions(orderID)
	if len(rows) != 1 || rows[0].Status != model.TransactionStatusFailed {
		t.Fatalf("expected one FAILED attempt, got %+v", rows)
	}

	// The outcome is now definitive, so a fresh attempt is allowed.
	h.provider.onCreate = nil
	resp, err := h.create(orderID, "QRIS", "idem-notfound-2")
	if err != nil {
		t.Fatalf("retry after a definitive failure must be allowed, got %v", err)
	}
	if resp.Status != model.TransactionStatusPending {
		t.Errorf("expected PENDING, got %s", resp.Status)
	}
}

// ─── Isolation ───────────────────────────────────────────────────────────────

func TestCreatePaymentForOrder_MerchantIsolation(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderB := h.addOrder(merchantBID, "IDR", 44000)

	// Merchant A cannot initiate a payment for merchant B's Order.
	if _, err := h.create(orderB, "QRIS", "idem-iso-a"); !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}

	// Merchant B can, and merchant A still cannot read the resulting payment.
	h.merchantID = merchantBID
	resp, err := h.create(orderB, "QRIS", "idem-iso-b")
	if err != nil {
		t.Fatalf("merchant B create: %v", err)
	}
	if resp.MerchantOrderID == "" {
		t.Fatal("expected a merchant order reference")
	}

	payments := service.NewPaymentService(h.txRepo, h.attemptRepo, h.provider)
	if _, err := payments.GetPayment(context.Background(), merchantAID, resp.TransactionID); !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("cross-merchant payment read: expected ErrTransactionNotFound, got %v", err)
	}
	if _, err := payments.GetPayment(context.Background(), merchantBID, resp.TransactionID); err != nil {
		t.Errorf("owner must be able to read its payment, got %v", err)
	}
}

// ─── Lifecycle regression ────────────────────────────────────────────────────

// TestPaymentServiceCREATEDToFAILEDAllowedByStateMachine pins the Phase 9A fix:
// PaymentService.transitionFailed may perform CREATED → FAILED, so the state
// machine must allow the same transition. The orderTxRepo double rejects any
// update the state machine forbids, which is what couples both halves here.
func TestPaymentServiceCREATEDToFAILEDAllowedByStateMachine(t *testing.T) {
	if !model.TransactionStatusCreated.CanTransitionTo(model.TransactionStatusFailed) {
		t.Fatal("state machine must allow CREATED -> FAILED")
	}
	for _, to := range []model.TransactionStatus{
		model.TransactionStatusPending,
		model.TransactionStatusCancelled,
	} {
		if !model.TransactionStatusCreated.CanTransitionTo(to) {
			t.Errorf("state machine must allow CREATED -> %s", to)
		}
	}

	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 1200)
	h.provider.onCreate = func(service.ProviderCreateRequest) (*service.ProviderPaymentResponse, error) {
		return nil, service.ErrProviderFailure
	}

	_, err := h.create(orderID, "QRIS", "idem-created-failed")
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("expected ErrProviderFailure, got %v", err)
	}
	rows := h.orderTransactions(orderID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(rows))
	}
	if rows[0].Status != model.TransactionStatusFailed {
		t.Fatalf("transitionFailed must reach FAILED, got %s", rows[0].Status)
	}
}

func TestListPaymentsForOrderEmpty(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 10000)

	beforeTx := len(h.txRepo.txs)
	beforeCreates, beforeGets := h.provider.creates(), h.provider.gets()
	history, err := h.svc.ListPaymentsForOrder(context.Background(), merchantAID, orderID)
	if err != nil {
		t.Fatalf("empty history: %v", err)
	}
	if history == nil {
		t.Fatal("empty history must be [], not nil")
	}
	if len(history) != 0 {
		t.Fatalf("expected 0 attempts, got %d", len(history))
	}
	if len(h.txRepo.txs) != beforeTx {
		t.Fatal("history read must not create transactions")
	}
	if h.provider.creates() != beforeCreates || h.provider.gets() != beforeGets {
		t.Fatal("history read must not touch the provider")
	}
}

func TestListPaymentsForOrderFailedThenPending(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 10000)
	failedID := h.seedAttempt(orderID, merchantAID, model.TransactionStatusFailed, time.Hour)
	if _, err := h.create(orderID, "QRIS", "idem-history-new"); err != nil {
		t.Fatalf("create second attempt: %v", err)
	}

	history, err := h.svc.ListPaymentsForOrder(context.Background(), merchantAID, orderID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(history))
	}
	// Newest first: the live PENDING attempt precedes the older FAILED one.
	if history[0].Status != model.TransactionStatusPending {
		t.Errorf("first item must be PENDING, got %s", history[0].Status)
	}
	if history[1].Status != model.TransactionStatusFailed || history[1].TransactionID != failedID {
		t.Errorf("second item must be the seeded FAILED attempt, got %+v", history[1])
	}
	for _, item := range history {
		if item.OrderID == nil || *item.OrderID != orderID {
			t.Errorf("item missing order link: %+v", item)
		}
		if item.Amount != 10000 && item.Amount != 15000 {
			t.Errorf("item amount must be server-derived, got %d", item.Amount)
		}
		if item.PaymentMethod != "QRIS" || item.Currency != "IDR" {
			t.Errorf("item fields wrong: %+v", item)
		}
	}
}

func TestListPaymentsForOrderPaid(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	orderID := h.addOrder(merchantAID, "IDR", 10000)
	paidID := h.seedAttempt(orderID, merchantAID, model.TransactionStatusPaid, time.Hour)

	history, err := h.svc.ListPaymentsForOrder(context.Background(), merchantAID, orderID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 || history[0].TransactionID != paidID || history[0].Status != model.TransactionStatusPaid {
		t.Fatalf("expected the single PAID attempt, got %+v", history)
	}
}

func TestListPaymentsForOrderIsolation(t *testing.T) {
	h := newOrderPaymentHarness(t, merchantAID)
	mine := h.addOrder(merchantAID, "IDR", 10000)
	theirs := h.addOrder(merchantBID, "IDR", 10000)
	h.seedAttempt(theirs, merchantBID, model.TransactionStatusPending, time.Minute)

	if _, err := h.svc.ListPaymentsForOrder(context.Background(), merchantAID, theirs); !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("foreign order must be not found, got %v", err)
	}
	if _, err := h.svc.ListPaymentsForOrder(context.Background(), merchantAID, uuid.New()); !errors.Is(err, repository.ErrOrderNotFound) {
		t.Fatalf("unknown order must be not found, got %v", err)
	}
	history, err := h.svc.ListPaymentsForOrder(context.Background(), merchantAID, mine)
	if err != nil {
		t.Fatalf("own order: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("foreign attempts must not leak, got %d", len(history))
	}
	other, err := h.svc.ListPaymentsForOrder(context.Background(), merchantBID, theirs)
	if err != nil || len(other) != 1 {
		t.Fatalf("merchant B must see its own attempt, got %+v (%v)", other, err)
	}
}
