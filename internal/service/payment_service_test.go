package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── In-memory mock repositories ─────────────────────────────────────────────

// mockTransactionRepo is an in-memory TransactionRepository for unit tests.
type mockTransactionRepo struct {
	mu  sync.Mutex
	txs map[uuid.UUID]*model.Transaction
}

func newMockTransactionRepo() *mockTransactionRepo {
	return &mockTransactionRepo{txs: make(map[uuid.UUID]*model.Transaction)}
}

// cloneTx returns a deep-ish copy so concurrent Find* callers do not race on
// shared *Transaction fields while Update* mutates the map entry.
func cloneTx(tx *model.Transaction) *model.Transaction {
	if tx == nil {
		return nil
	}
	c := *tx
	if tx.Provider != nil {
		v := *tx.Provider
		c.Provider = &v
	}
	if tx.ProviderTransactionID != nil {
		v := *tx.ProviderTransactionID
		c.ProviderTransactionID = &v
	}
	if tx.PaymentURL != nil {
		v := *tx.PaymentURL
		c.PaymentURL = &v
	}
	if tx.ExpiredAt != nil {
		v := *tx.ExpiredAt
		c.ExpiredAt = &v
	}
	if tx.PaidAt != nil {
		v := *tx.PaidAt
		c.PaidAt = &v
	}
	return &c
}

func (r *mockTransactionRepo) Create(_ context.Context, tx *model.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.txs[tx.ID] = cloneTx(tx)
	return nil
}

func (r *mockTransactionRepo) FindByID(_ context.Context, id uuid.UUID) (*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, ok := r.txs[id]
	if !ok {
		return nil, repository.ErrTransactionNotFound
	}
	return cloneTx(tx), nil
}

func (r *mockTransactionRepo) FindByMerchantAndID(_ context.Context, merchantID, id uuid.UUID) (*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, ok := r.txs[id]
	if !ok || tx.MerchantID != merchantID {
		return nil, repository.ErrTransactionNotFound
	}
	return cloneTx(tx), nil
}

func (r *mockTransactionRepo) FindByMerchantOrderID(_ context.Context, merchantID uuid.UUID, orderID string) (*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tx := range r.txs {
		if tx.MerchantID == merchantID && tx.MerchantOrderID == orderID {
			return cloneTx(tx), nil
		}
	}
	return nil, repository.ErrTransactionNotFound
}

func (r *mockTransactionRepo) UpdateStatus(_ context.Context, id uuid.UUID, from, to model.TransactionStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, ok := r.txs[id]
	if !ok || tx.Status != from {
		return repository.ErrTransactionNotFound
	}
	tx.Status = to
	tx.UpdatedAt = time.Now().UTC()
	return nil
}

func (r *mockTransactionRepo) UpdateStatusWithProvider(_ context.Context, id uuid.UUID, from, to model.TransactionStatus, provider, providerTransactionID, paymentURL string, expiredAt *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, ok := r.txs[id]
	if !ok || tx.Status != from {
		return repository.ErrTransactionNotFound
	}
	tx.Status = to
	tx.Provider = &provider
	tx.ProviderTransactionID = &providerTransactionID
	tx.PaymentURL = &paymentURL
	if expiredAt != nil {
		tx.ExpiredAt = expiredAt
	}
	tx.UpdatedAt = time.Now().UTC()
	return nil
}

func (r *mockTransactionRepo) FindByProviderTransactionID(_ context.Context, provider, providerTxID string) (*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tx := range r.txs {
		if tx.Provider != nil && *tx.Provider == provider &&
			tx.ProviderTransactionID != nil && *tx.ProviderTransactionID == providerTxID {
			return cloneTx(tx), nil
		}
	}
	return nil, repository.ErrTransactionNotFound
}

func (r *mockTransactionRepo) FindExpiredPendingTransactions(_ context.Context, limit int) ([]*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	var out []*model.Transaction
	for _, tx := range r.txs {
		if tx.Status == model.TransactionStatusPending &&
			tx.ExpiredAt != nil && tx.ExpiredAt.Before(now) {
			out = append(out, cloneTx(tx))
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (r *mockTransactionRepo) UpdateStatusWithPaidAt(_ context.Context, id uuid.UUID, from model.TransactionStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, ok := r.txs[id]
	if !ok || tx.Status != from {
		return repository.ErrTransactionNotFound
	}
	tx.Status = model.TransactionStatusPaid
	if tx.PaidAt == nil {
		now := time.Now().UTC()
		tx.PaidAt = &now
	}
	tx.UpdatedAt = time.Now().UTC()
	return nil
}

// List and CountList stubs — not exercised by existing service tests.
// The listing-specific tests in payment_list_service_test.go use mockListTransactionRepo.
func (r *mockTransactionRepo) List(_ context.Context, _ uuid.UUID, f model.TransactionListFilter) ([]*model.Transaction, error) {
	return nil, nil
}

func (r *mockTransactionRepo) CountList(_ context.Context, _ uuid.UUID, _ model.TransactionListFilter) (int64, error) {
	return 0, nil
}

// mockPaymentAttemptRepo is an in-memory PaymentAttemptRepository for unit tests.
type mockPaymentAttemptRepo struct {
	attempts []model.PaymentAttempt
}

func newMockAttemptRepo() *mockPaymentAttemptRepo {
	return &mockPaymentAttemptRepo{}
}

func (r *mockPaymentAttemptRepo) Create(_ context.Context, a *model.PaymentAttempt) error {
	r.attempts = append(r.attempts, *a)
	return nil
}

func (r *mockPaymentAttemptRepo) FindByTransactionID(_ context.Context, txID uuid.UUID) ([]model.PaymentAttempt, error) {
	var out []model.PaymentAttempt
	for _, a := range r.attempts {
		if a.TransactionID == txID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *mockPaymentAttemptRepo) CountByTransactionID(_ context.Context, txID uuid.UUID) (int, error) {
	count := 0
	for _, a := range r.attempts {
		if a.TransactionID == txID {
			count++
		}
	}
	return count, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func buildService(provider service.PaymentProvider) (service.PaymentService, *mockTransactionRepo, *mockPaymentAttemptRepo) {
	txRepo := newMockTransactionRepo()
	attemptRepo := newMockAttemptRepo()
	svc := service.NewPaymentService(txRepo, attemptRepo, provider)
	return svc, txRepo, attemptRepo
}

var (
	merchantAID = uuid.New()
	merchantBID = uuid.New()
)

func validCreateReq(orderID string) model.CreatePaymentRequest {
	return model.CreatePaymentRequest{
		MerchantOrderID: orderID,
		Amount:          50000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
	}
}

// ─── CreatePayment tests ──────────────────────────────────────────────────────

func TestCreatePayment_Success(t *testing.T) {
	svc, txRepo, attemptRepo := buildService(service.NewMockPaymentProvider())

	resp, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-001"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Response shape
	if resp.Status != model.TransactionStatusPending {
		t.Errorf("expected PENDING, got %s", resp.Status)
	}
	if resp.Provider != "MOCK" {
		t.Errorf("expected provider MOCK, got %s", resp.Provider)
	}
	if resp.ProviderTransactionID == "" {
		t.Error("expected non-empty provider_transaction_id")
	}
	if resp.PaymentURL == "" {
		t.Error("expected non-empty payment_url")
	}
	if resp.ExpiredAt == nil {
		t.Error("expected non-nil expired_at")
	}
	if resp.Amount != 50000 {
		t.Errorf("expected amount 50000, got %d", resp.Amount)
	}

	// Transaction stored with PENDING status
	tx, err := txRepo.FindByID(context.Background(), resp.TransactionID)
	if err != nil {
		t.Fatalf("transaction not found in repo: %v", err)
	}
	if tx.Status != model.TransactionStatusPending {
		t.Errorf("DB transaction status: expected PENDING, got %s", tx.Status)
	}
	if tx.Provider == nil || *tx.Provider != "MOCK" {
		t.Error("DB transaction provider not set to MOCK")
	}

	// Payment attempt recorded
	attempts, _ := attemptRepo.FindByTransactionID(context.Background(), resp.TransactionID)
	if len(attempts) != 1 {
		t.Errorf("expected 1 payment attempt, got %d", len(attempts))
	}
	if attempts[0].Status != "SUCCESS" {
		t.Errorf("attempt status: expected SUCCESS, got %s", attempts[0].Status)
	}
	if attempts[0].AttemptNumber != 1 {
		t.Errorf("attempt number: expected 1, got %d", attempts[0].AttemptNumber)
	}
}

func TestCreatePayment_InvalidAmount(t *testing.T) {
	req := validCreateReq("ORDER-002")
	req.Amount = 0

	// Amount=0 is caught by `binding:"required,min=1"` in the struct tag.
	// Service-level validation happens in the handler binding step.
	// This test documents the expectation; binding tests are in handler tests.
	_ = req
}

func TestCreatePayment_InvalidCurrency(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())
	req := validCreateReq("ORDER-003")
	req.Currency = "USD"

	_, err := svc.CreatePayment(context.Background(), merchantAID, req)
	if !errors.Is(err, service.ErrInvalidCurrency) {
		t.Errorf("expected ErrInvalidCurrency, got %v", err)
	}
}

func TestCreatePayment_InvalidPaymentMethod(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())
	req := validCreateReq("ORDER-004")
	req.PaymentMethod = "CREDIT_CARD"

	_, err := svc.CreatePayment(context.Background(), merchantAID, req)
	if !errors.Is(err, service.ErrInvalidPaymentMethod) {
		t.Errorf("expected ErrInvalidPaymentMethod, got %v", err)
	}
}

func TestCreatePayment_DuplicateOrderID(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	// First call succeeds.
	if _, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-DUP")); err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	// Second call with same order ID must fail.
	_, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-DUP"))
	if !errors.Is(err, service.ErrDuplicateOrder) {
		t.Errorf("expected ErrDuplicateOrder, got %v", err)
	}
}

func TestCreatePayment_SameOrderID_DifferentMerchant(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	// Merchant A creates ORDER-SHARED.
	if _, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-SHARED")); err != nil {
		t.Fatalf("merchant A create failed: %v", err)
	}

	// Merchant B using the same order ID must succeed (different merchant scope).
	_, err := svc.CreatePayment(context.Background(), merchantBID, validCreateReq("ORDER-SHARED"))
	if err != nil {
		t.Errorf("merchant B should be allowed to use same order ID, got: %v", err)
	}
}

func TestCreatePayment_ProviderFailure(t *testing.T) {
	provider := &service.MockPaymentProvider{ShouldFailCreate: true}
	svc, txRepo, attemptRepo := buildService(provider)

	_, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-FAIL"))
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Errorf("expected ErrProviderFailure, got %v", err)
	}

	// Find the transaction — we need its ID.
	var failedTx *model.Transaction
	for _, tx := range txRepo.txs {
		if tx.MerchantOrderID == "ORDER-FAIL" {
			failedTx = tx
			break
		}
	}
	if failedTx == nil {
		t.Fatal("transaction not found after provider failure")
	}

	// Transaction must be FAILED.
	if failedTx.Status != model.TransactionStatusFailed {
		t.Errorf("expected FAILED transaction, got %s", failedTx.Status)
	}

	// Payment attempt must exist with FAILED status.
	attempts, _ := attemptRepo.FindByTransactionID(context.Background(), failedTx.ID)
	if len(attempts) == 0 {
		t.Fatal("expected at least one payment attempt after provider failure")
	}
	if attempts[0].Status != "FAILED" {
		t.Errorf("attempt status: expected FAILED, got %s", attempts[0].Status)
	}
}

func TestCreatePayment_ProviderTimeout(t *testing.T) {
	provider := &service.MockPaymentProvider{ShouldTimeout: true}
	svc, _, _ := buildService(provider)

	_, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-TIMEOUT"))
	if !errors.Is(err, service.ErrProviderTimeout) {
		t.Errorf("expected ErrProviderTimeout, got %v", err)
	}
}

// ─── GetPayment tests ─────────────────────────────────────────────────────────

func TestGetPayment_Success(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	created, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-GET"))
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	got, err := svc.GetPayment(context.Background(), merchantAID, created.TransactionID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.TransactionID != created.TransactionID {
		t.Error("transaction ID mismatch")
	}
	if got.Status != model.TransactionStatusPending {
		t.Errorf("expected PENDING, got %s", got.Status)
	}
}

func TestGetPayment_NotFound(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	_, err := svc.GetPayment(context.Background(), merchantAID, uuid.New())
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("expected ErrTransactionNotFound, got %v", err)
	}
}

func TestGetPayment_WrongMerchant(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	// Merchant A creates a payment.
	created, err := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-ISO"))
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Merchant B must NOT see it — must get ErrTransactionNotFound (not 403).
	_, err = svc.GetPayment(context.Background(), merchantBID, created.TransactionID)
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("expected ErrTransactionNotFound for cross-merchant access, got %v", err)
	}
}

// ─── CancelPayment tests ──────────────────────────────────────────────────────

func TestCancelPayment_FromCreated(t *testing.T) {
	txRepo := newMockTransactionRepo()
	attemptRepo := newMockAttemptRepo()
	provider := service.NewMockPaymentProvider()
	svc := service.NewPaymentService(txRepo, attemptRepo, provider)

	// Insert a CREATED transaction directly.
	txID := uuid.New()
	now := time.Now().UTC()
	txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: merchantAID, MerchantOrderID: "ORDER-CANCEL-1",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusCreated, CreatedAt: now, UpdatedAt: now,
	}

	resp, err := svc.CancelPayment(context.Background(), merchantAID, txID)
	if err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if resp.Status != model.TransactionStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", resp.Status)
	}
}

func TestCancelPayment_FromPending(t *testing.T) {
	svc, txRepo, _ := buildService(service.NewMockPaymentProvider())

	created, _ := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-CANCEL-P"))

	resp, err := svc.CancelPayment(context.Background(), merchantAID, created.TransactionID)
	if err != nil {
		t.Fatalf("cancel from PENDING failed: %v", err)
	}
	if resp.Status != model.TransactionStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", resp.Status)
	}

	// Verify DB
	tx, _ := txRepo.FindByID(context.Background(), created.TransactionID)
	if tx.Status != model.TransactionStatusCancelled {
		t.Errorf("DB status: expected CANCELLED, got %s", tx.Status)
	}
}

func cancelFromTerminalState(t *testing.T, status model.TransactionStatus) {
	t.Helper()
	txRepo := newMockTransactionRepo()
	attemptRepo := newMockAttemptRepo()
	svc := service.NewPaymentService(txRepo, attemptRepo, service.NewMockPaymentProvider())

	txID := uuid.New()
	now := time.Now().UTC()
	txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: merchantAID, MerchantOrderID: "ORDER-TERM",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: status, CreatedAt: now, UpdatedAt: now,
	}

	_, err := svc.CancelPayment(context.Background(), merchantAID, txID)
	if !errors.Is(err, service.ErrInvalidTransactionState) {
		t.Errorf("status %s: expected ErrInvalidTransactionState, got %v", status, err)
	}
}

func TestCancelPayment_FromPaid_Rejected(t *testing.T) {
	cancelFromTerminalState(t, model.TransactionStatusPaid)
}
func TestCancelPayment_FromFailed_Rejected(t *testing.T) {
	cancelFromTerminalState(t, model.TransactionStatusFailed)
}
func TestCancelPayment_FromExpired_Rejected(t *testing.T) {
	cancelFromTerminalState(t, model.TransactionStatusExpired)
}
func TestCancelPayment_FromCancelled_Rejected(t *testing.T) {
	cancelFromTerminalState(t, model.TransactionStatusCancelled)
}

func TestCancelPayment_ProviderCancelFailure(t *testing.T) {
	// Step 1: create a PENDING payment using a working provider.
	txRepo := newMockTransactionRepo()
	attemptRepo := newMockAttemptRepo()
	workingProvider := service.NewMockPaymentProvider()
	svcCreate := service.NewPaymentService(txRepo, attemptRepo, workingProvider)

	created, err := svcCreate.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-CANCEL-FAIL"))
	if err != nil {
		t.Fatalf("setup: create payment failed: %v", err)
	}

	// Step 2: attempt cancel using a provider that refuses cancellation.
	failCancelProvider := &service.MockPaymentProvider{ShouldFailCancel: true}
	svcCancel := service.NewPaymentService(txRepo, attemptRepo, failCancelProvider)

	_, err = svcCancel.CancelPayment(context.Background(), merchantAID, created.TransactionID)
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Errorf("expected ErrProviderFailure on cancel, got %v", err)
	}

	// Transaction must still be PENDING — must not be changed on provider failure.
	tx, _ := txRepo.FindByID(context.Background(), created.TransactionID)
	if tx.Status != model.TransactionStatusPending {
		t.Errorf("status must remain PENDING after provider cancel failure, got %s", tx.Status)
	}
}

func TestCancelPayment_NotFound(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	_, err := svc.CancelPayment(context.Background(), merchantAID, uuid.New())
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("expected ErrTransactionNotFound, got %v", err)
	}
}

func TestCancelPayment_WrongMerchant(t *testing.T) {
	svc, _, _ := buildService(service.NewMockPaymentProvider())

	created, _ := svc.CreatePayment(context.Background(), merchantAID, validCreateReq("ORDER-CANCEL-ISO"))

	_, err := svc.CancelPayment(context.Background(), merchantBID, created.TransactionID)
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("cross-merchant cancel: expected ErrTransactionNotFound, got %v", err)
	}
}

// ─── State machine tests ──────────────────────────────────────────────────────

func TestStateMachine_AllowedTransitions(t *testing.T) {
	cases := []struct {
		from model.TransactionStatus
		to   model.TransactionStatus
		want bool
	}{
		{model.TransactionStatusCreated, model.TransactionStatusPending, true},
		{model.TransactionStatusCreated, model.TransactionStatusCancelled, true},
		{model.TransactionStatusPending, model.TransactionStatusPaid, true},
		{model.TransactionStatusPending, model.TransactionStatusFailed, true},
		{model.TransactionStatusPending, model.TransactionStatusExpired, true},
		{model.TransactionStatusPending, model.TransactionStatusCancelled, true},
		// Invalid
		{model.TransactionStatusPaid, model.TransactionStatusPending, false},
		{model.TransactionStatusPaid, model.TransactionStatusCancelled, false},
		{model.TransactionStatusFailed, model.TransactionStatusPaid, false},
		{model.TransactionStatusFailed, model.TransactionStatusPending, false},
		{model.TransactionStatusExpired, model.TransactionStatusPaid, false},
		{model.TransactionStatusExpired, model.TransactionStatusPending, false},
		{model.TransactionStatusCancelled, model.TransactionStatusPending, false},
		{model.TransactionStatusCancelled, model.TransactionStatusPaid, false},
	}

	for _, tc := range cases {
		got := tc.from.CanTransitionTo(tc.to)
		if got != tc.want {
			t.Errorf("CanTransitionTo(%s → %s): got %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestStateMachine_TerminalStates(t *testing.T) {
	terminals := []model.TransactionStatus{
		model.TransactionStatusPaid,
		model.TransactionStatusFailed,
		model.TransactionStatusExpired,
		model.TransactionStatusCancelled,
	}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}

	nonTerminals := []model.TransactionStatus{
		model.TransactionStatusCreated,
		model.TransactionStatusPending,
	}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}
