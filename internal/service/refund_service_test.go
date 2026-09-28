package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── In-memory RefundRepository ──────────────────────────────────────────────

type memRefundRepo struct {
	mu      sync.Mutex
	refunds map[uuid.UUID]*model.Refund
	txRepo  *mockTransactionRepo // shared pointer to update tx counters
}

func newMemRefundRepo(txRepo *mockTransactionRepo) *memRefundRepo {
	return &memRefundRepo{
		refunds: make(map[uuid.UUID]*model.Refund),
		txRepo:  txRepo,
	}
}

func (r *memRefundRepo) FindByID(_ context.Context, id uuid.UUID) (*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.refunds[id]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	cp := *ref
	return &cp, nil
}

func (r *memRefundRepo) FindByMerchantAndID(_ context.Context, merchantID, refundID uuid.UUID) (*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.refunds[refundID]
	if !ok || ref.MerchantID != merchantID {
		return nil, repository.ErrRefundNotFound
	}
	cp := *ref
	return &cp, nil
}

func (r *memRefundRepo) FindByProviderRefundID(_ context.Context, provider, providerRefundID string) (*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ref := range r.refunds {
		if ref.Provider == provider && ref.ProviderRefundID != nil && *ref.ProviderRefundID == providerRefundID {
			cp := *ref
			return &cp, nil
		}
	}
	return nil, repository.ErrRefundNotFound
}

func (r *memRefundRepo) ListByTransaction(_ context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Refund
	for _, ref := range r.refunds {
		if ref.MerchantID == merchantID && ref.TransactionID == transactionID {
			cp := *ref
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memRefundRepo) CountByTransaction(_ context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, ref := range r.refunds {
		if ref.MerchantID == merchantID && ref.TransactionID == transactionID {
			n++
		}
	}
	return n, nil
}

func (r *memRefundRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Refund
	for _, ref := range r.refunds {
		if ref.MerchantID != merchantID {
			continue
		}
		if filter.TransactionID != nil && ref.TransactionID != *filter.TransactionID {
			continue
		}
		if filter.Status != nil && ref.Status != *filter.Status {
			continue
		}
		cp := *ref
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memRefundRepo) CountByMerchant(_ context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (int64, error) {
	rows, err := r.ListByMerchant(context.Background(), merchantID, filter)
	if err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}

// ReserveAndCreate implements the critical concurrency path with a mutex.
// It locks the transaction, checks the refundable balance, inserts PENDING refund,
// and increments reserved_refund_amount.
func (r *memRefundRepo) ReserveAndCreate(ctx context.Context, merchantID, transactionID uuid.UUID, refund *model.Refund, outbox repository.RefundOutboxHook, createdEvent model.MerchantWebhookEventType) (*model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.txRepo.mu.Lock()
	defer r.txRepo.mu.Unlock()

	tx, ok := r.txRepo.txs[transactionID]
	if !ok || tx.MerchantID != merchantID {
		return nil, repository.ErrTransactionNotFound
	}
	if tx.Status != model.TransactionStatusPaid {
		return nil, repository.ErrRefundNotAllowed
	}
	if refund.Currency != tx.Currency {
		return nil, repository.ErrRefundCurrencyMismatch
	}
	available := tx.Amount - tx.RefundedAmount - tx.ReservedRefundAmount
	if refund.Amount > available {
		return nil, repository.ErrRefundAmountExceeded
	}

	// Insert PENDING refund.
	cp := *refund
	r.refunds[refund.ID] = &cp

	// Reserve amount.
	tx.ReservedRefundAmount += refund.Amount
	tx.UpdatedAt = time.Now().UTC()

	txCp := *tx
	if outbox != nil && createdEvent != "" {
		if err := outbox(ctx, nil, &cp, &txCp, createdEvent); err != nil {
			return nil, err
		}
	}
	return &txCp, nil
}

// FinalizeAfterProvider updates refund status and adjusts transaction counters atomically.
func (r *memRefundRepo) FinalizeAfterProvider(
	ctx context.Context,
	refundID uuid.UUID,
	newStatus model.RefundStatus,
	providerRefundID *string,
	failureCode, failureMessage *string,
	outbox repository.RefundOutboxHook,
	eventType model.MerchantWebhookEventType,
) (*model.Refund, *model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.txRepo.mu.Lock()
	defer r.txRepo.mu.Unlock()

	ref, ok := r.refunds[refundID]
	if !ok {
		return nil, nil, repository.ErrRefundNotFound
	}
	// SUCCEEDED is terminal — never regress.
	if ref.Status == model.RefundStatusSucceeded {
		tx := r.txRepo.txs[ref.TransactionID]
		txCp := *tx
		refCp := *ref
		return &refCp, &txCp, nil
	}

	tx := r.txRepo.txs[ref.TransactionID]
	now := time.Now().UTC()

	switch newStatus {
	case model.RefundStatusProcessing:
		ref.Status = model.RefundStatusProcessing
		ref.ProviderRefundID = providerRefundID
		ref.UpdatedAt = now
	case model.RefundStatusSucceeded:
		tx.ReservedRefundAmount -= ref.Amount
		tx.RefundedAmount += ref.Amount
		tx.UpdatedAt = now
		ref.Status = model.RefundStatusSucceeded
		ref.ProviderRefundID = providerRefundID
		ref.SucceededAt = &now
		ref.UpdatedAt = now
	case model.RefundStatusFailed:
		// Release reservation only if was PENDING or PROCESSING.
		if ref.Status == model.RefundStatusPending || ref.Status == model.RefundStatusProcessing {
			tx.ReservedRefundAmount -= ref.Amount
			tx.UpdatedAt = now
		}
		ref.Status = model.RefundStatusFailed
		ref.FailureCode = failureCode
		ref.FailureMessage = failureMessage
		ref.FailedAt = &now
		ref.UpdatedAt = now
	}

	txCp := *tx
	refCp := *ref
	if outbox != nil && eventType != "" {
		if err := outbox(ctx, nil, &refCp, &txCp, eventType); err != nil {
			return nil, nil, err
		}
	}
	return &refCp, &txCp, nil
}

func (r *memRefundRepo) ProcessRefundWebhookAtomically(
	ctx context.Context,
	webhook *model.WebhookEvent,
	provider, providerRefundID string,
	amount int64,
	currency string,
	targetStatus model.RefundStatus,
	outbox repository.RefundOutboxHook,
	eventType model.MerchantWebhookEventType,
) (model.WebhookEventStatus, error) {
	// Simplified for unit tests.
	ref, err := r.FindByProviderRefundID(ctx, provider, providerRefundID)
	if err != nil {
		return model.WebhookEventStatusFailed, err
	}
	if amount > 0 && amount != ref.Amount {
		return model.WebhookEventStatusIgnored, nil
	}
	if ref.Status == model.RefundStatusSucceeded {
		return model.WebhookEventStatusIgnored, nil
	}
	_, _, err = r.FinalizeAfterProvider(ctx, ref.ID, targetStatus, &providerRefundID, nil, nil, outbox, eventType)
	if err != nil {
		return model.WebhookEventStatusFailed, err
	}
	return model.WebhookEventStatusProcessed, nil
}

// ─── In-memory RefundAttemptRepository ───────────────────────────────────────

type memRefundAttemptRepo struct {
	mu       sync.Mutex
	attempts []*model.RefundAttempt
}

func newMemRefundAttemptRepo() *memRefundAttemptRepo {
	return &memRefundAttemptRepo{}
}

func (r *memRefundAttemptRepo) Create(_ context.Context, a *model.RefundAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.attempts = append(r.attempts, &cp)
	return nil
}

func (r *memRefundAttemptRepo) CountByRefundID(_ context.Context, refundID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.attempts {
		if a.RefundID == refundID {
			n++
		}
	}
	return n, nil
}

// ─── In-memory IdempotencyKeyRepository ──────────────────────────────────────

type memRefundIdempotencyRepo struct {
	mu      sync.Mutex
	records map[string]*model.IdempotencyKey // key: merchantID+key
}

func newMemRefundIdempotencyRepo() *memRefundIdempotencyRepo {
	return &memRefundIdempotencyRepo{records: make(map[string]*model.IdempotencyKey)}
}

func repoKey(merchantID uuid.UUID, key string) string {
	return merchantID.String() + ":" + key
}

func (r *memRefundIdempotencyRepo) Reserve(_ context.Context, k *model.IdempotencyKey) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk := repoKey(k.MerchantID, k.Key)
	if existing, ok := r.records[rk]; ok && existing.ExpiresAt.After(time.Now().UTC()) {
		return false, nil
	}
	cp := *k
	r.records[rk] = &cp
	return true, nil
}

func (r *memRefundIdempotencyRepo) GetByMerchantAndKey(_ context.Context, merchantID uuid.UUID, key string) (*model.IdempotencyKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk := repoKey(merchantID, key)
	rec, ok := r.records[rk]
	if !ok {
		return nil, repository.ErrIdempotencyNotFound
	}
	cp := *rec
	return &cp, nil
}

func (r *memRefundIdempotencyRepo) Create(_ context.Context, k *model.IdempotencyKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk := repoKey(k.MerchantID, k.Key)
	r.records[rk] = k
	return nil
}

func (r *memRefundIdempotencyRepo) UpdateProcessing(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.ID == id {
			rec.Status = model.IdempotencyStatusProcessing
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

func (r *memRefundIdempotencyRepo) AttachRefund(_ context.Context, id, refundID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.ID == id {
			rec.RefundID = &refundID
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

func (r *memRefundIdempotencyRepo) Complete(_ context.Context, id uuid.UUID, status int, body []byte, txID *uuid.UUID, refundID *uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.ID == id {
			rec.Status = model.IdempotencyStatusCompleted
			rec.ResponseStatus = &status
			rec.ResponseBody = append([]byte(nil), body...)
			rec.TransactionID = txID
			rec.RefundID = refundID
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

func (r *memRefundIdempotencyRepo) Fail(_ context.Context, id uuid.UUID, status int, body []byte, txID *uuid.UUID, refundID *uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.ID == id {
			rec.Status = model.IdempotencyStatusFailed
			rec.ResponseStatus = &status
			rec.ResponseBody = append([]byte(nil), body...)
			rec.TransactionID = txID
			rec.RefundID = refundID
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

func paidTransaction(merchantID uuid.UUID, amount int64) (*mockTransactionRepo, *model.Transaction) {
	txRepo := newMockTransactionRepo()
	provider := "MOCK"
	pProviderTxID := "MOCK-TXN-paid"
	now := time.Now().UTC()
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: merchantID,
		MerchantOrderID: "ORDER-" + uuid.New().String()[:8],
		Amount:          amount, Currency: "IDR", PaymentMethod: "QRIS",
		Status:   model.TransactionStatusPaid,
		Provider: &provider, ProviderTransactionID: &pProviderTxID,
		CreatedAt: now, UpdatedAt: now,
	}
	now2 := time.Now().UTC()
	tx.PaidAt = &now2
	txRepo.mu.Lock()
	txRepo.txs[tx.ID] = tx
	txRepo.mu.Unlock()
	return txRepo, tx
}

func buildRefundService(txRepo *mockTransactionRepo, provider *service.MockRefundProvider, configuredProvider ...string) (service.RefundService, *memRefundRepo, *memRefundAttemptRepo, *memRefundIdempotencyRepo) {
	refundRepo := newMemRefundRepo(txRepo)
	attemptRepo := newMemRefundAttemptRepo()
	idempRepo := newMemRefundIdempotencyRepo()
	svc := service.NewRefundService(txRepo, refundRepo, attemptRepo, idempRepo, provider, nil, 24*time.Hour, configuredProvider...)
	return svc, refundRepo, attemptRepo, idempRepo
}

func refundReq(amount int64) model.CreateRefundRequest {
	return model.CreateRefundRequest{Amount: amount, Currency: "IDR"}
}

func TestRefundService_AllowsMockProviderInDevelopmentWiring(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	svc, _, _, _ := buildRefundService(txRepo, provider, "mock")
	if _, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(100_000), "mock-provider-supported"); err != nil {
		t.Fatalf("mock refund failed: %v", err)
	}
	if provider.CreateRefundCallCount() != 1 {
		t.Fatalf("mock provider call count = %d, want 1", provider.CreateRefundCallCount())
	}
}

func TestRefundService_RejectsNonMockProviderBeforeReservation(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	svc, _, _, idempotency := buildRefundService(txRepo, provider, "midtrans")

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(100_000), "unsupported-provider")
	if !errors.Is(err, service.ErrRefundProviderUnsupported) {
		t.Fatalf("error = %v, want ErrRefundProviderUnsupported", err)
	}
	if provider.CreateRefundCallCount() != 0 {
		t.Fatalf("unsupported provider called mock adapter %d times", provider.CreateRefundCallCount())
	}
	if idempotency == nil {
		t.Fatal("idempotency repository unexpectedly nil")
	}
}

// ─── Create refund — happy path ───────────────────────────────────────────────

func TestRefundService_CreateFullRefund_Succeeds(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	svc, refundRepo, _, _ := buildRefundService(txRepo, provider)

	resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(100_000), "key-full")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != model.RefundStatusSucceeded {
		t.Errorf("want SUCCEEDED, got %s", resp.Status)
	}
	if resp.Amount != 100_000 {
		t.Errorf("amount mismatch: %d", resp.Amount)
	}
	if resp.TransactionAmount != 100_000 {
		t.Errorf("transaction_amount mismatch: %d", resp.TransactionAmount)
	}
	if resp.RefundedAmount != 100_000 {
		t.Errorf("refunded_amount should be 100000, got %d", resp.RefundedAmount)
	}
	if resp.RefundableAmount != 0 {
		t.Errorf("refundable_amount should be 0, got %d", resp.RefundableAmount)
	}
	// Provider called once.
	if provider.CreateRefundCallCount() != 1 {
		t.Errorf("provider calls: want 1, got %d", provider.CreateRefundCallCount())
	}
	// Refund stored.
	ref, err := refundRepo.FindByMerchantAndID(context.Background(), mid, resp.RefundID)
	if err != nil || ref.Status != model.RefundStatusSucceeded {
		t.Errorf("stored refund status: %v %v", ref, err)
	}
}

func TestRefundService_CreatePartialRefund_Succeeds(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(30_000), "key-partial")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != model.RefundStatusSucceeded {
		t.Errorf("want SUCCEEDED, got %s", resp.Status)
	}
	if resp.RefundedAmount != 30_000 {
		t.Errorf("refunded_amount: want 30000, got %d", resp.RefundedAmount)
	}
	if resp.RefundableAmount != 70_000 {
		t.Errorf("refundable_amount: want 70000, got %d", resp.RefundableAmount)
	}
}

func TestRefundService_MultiplePartialRefunds_Succeed(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// 3 partial refunds: 20k + 30k + 50k = 100k.
	amounts := []int64{20_000, 30_000, 50_000}
	keys := []string{"key-m1", "key-m2", "key-m3"}
	for i, amt := range amounts {
		resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(amt), keys[i])
		if err != nil {
			t.Fatalf("refund %d: unexpected error: %v", i+1, err)
		}
		if resp.Status != model.RefundStatusSucceeded {
			t.Errorf("refund %d: want SUCCEEDED, got %s", i+1, resp.Status)
		}
	}

	// Final state: refunded=100k, available=0.
	txRepo.mu.Lock()
	finalTx := txRepo.txs[tx.ID]
	txRepo.mu.Unlock()
	if finalTx.RefundedAmount != 100_000 {
		t.Errorf("final refunded_amount: want 100000, got %d", finalTx.RefundedAmount)
	}
	if finalTx.ReservedRefundAmount != 0 {
		t.Errorf("final reserved: want 0, got %d", finalTx.ReservedRefundAmount)
	}
}

func TestRefundService_ExactRemainingAmount_Succeeds(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// Refund 70k first.
	_, _ = svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(70_000), "key-ex1")
	// Refund exactly remaining 30k.
	resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(30_000), "key-ex2")
	if err != nil {
		t.Fatalf("exact remaining refund failed: %v", err)
	}
	if resp.RefundableAmount != 0 {
		t.Errorf("want 0 refundable, got %d", resp.RefundableAmount)
	}
}

// ─── Over-refund protection ───────────────────────────────────────────────────

func TestRefundService_OverRefund_Rejected(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(100_001), "key-over")
	if !errors.Is(err, service.ErrRefundAmountExceeded) {
		t.Errorf("want ErrRefundAmountExceeded, got %v", err)
	}
}

func TestRefundService_OverRefundAfterPartial_Rejected(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// Refund 80k first.
	_, _ = svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(80_000), "key-po1")
	// Attempt 30k more (would exceed 100k).
	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(30_000), "key-po2")
	if !errors.Is(err, service.ErrRefundAmountExceeded) {
		t.Errorf("want ErrRefundAmountExceeded, got %v", err)
	}
}

func TestRefundService_ZeroAmount_Rejected(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// amount=0 fails binding validation before service; test at service level with unsupported currency
	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID,
		model.CreateRefundRequest{Amount: 0, Currency: "IDR"}, "key-zero")
	// binding:"min=1" on struct tag means amount=0 is rejected — service returns currency error because binding
	// happens in handler. At service level we validate currency first.
	// The service will reject it via ErrRefundCurrencyMismatch if currency is wrong,
	// or allow 0 through to DB which will reject via CHECK.
	// Either way it should not succeed.
	if err == nil {
		t.Error("expected error for zero amount refund")
	}
}

// ─── Transaction state validation ────────────────────────────────────────────

func TestRefundService_NonPaidTransaction_Rejected(t *testing.T) {
	mid := uuid.New()
	txRepo := newMockTransactionRepo()
	now := time.Now().UTC()
	for _, status := range []model.TransactionStatus{
		model.TransactionStatusCreated,
		model.TransactionStatusPending,
		model.TransactionStatusFailed,
		model.TransactionStatusExpired,
		model.TransactionStatusCancelled,
	} {
		tx := &model.Transaction{
			ID: uuid.New(), MerchantID: mid, Amount: 50_000, Currency: "IDR",
			MerchantOrderID: "O-" + string(status), PaymentMethod: "QRIS",
			Status: status, CreatedAt: now, UpdatedAt: now,
		}
		txRepo.mu.Lock()
		txRepo.txs[tx.ID] = tx
		txRepo.mu.Unlock()

		svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())
		_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(10_000), "key-"+string(status))
		if !errors.Is(err, service.ErrRefundTransactionNotPaid) {
			t.Errorf("status %s: want ErrRefundTransactionNotPaid, got %v", status, err)
		}
	}
}

// ─── Currency mismatch ────────────────────────────────────────────────────────

func TestRefundService_CurrencyMismatch_Rejected(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000) // IDR transaction
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID,
		model.CreateRefundRequest{Amount: 10_000, Currency: "USD"}, "key-cur")
	if !errors.Is(err, service.ErrRefundCurrencyMismatch) {
		t.Errorf("want ErrRefundCurrencyMismatch, got %v", err)
	}
}

// ─── Merchant isolation ───────────────────────────────────────────────────────

func TestRefundService_WrongMerchant_Rejected(t *testing.T) {
	midA := uuid.New()
	midB := uuid.New()
	txRepo, tx := paidTransaction(midA, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// Merchant B tries to refund Merchant A's transaction.
	_, err := svc.CreateRefundWithIdempotency(context.Background(), midB, tx.ID, refundReq(10_000), "key-iso")
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("want ErrTransactionNotFound for wrong merchant, got %v", err)
	}
}

func TestRefundService_GetRefund_WrongMerchant_404(t *testing.T) {
	midA := uuid.New()
	midB := uuid.New()
	txRepo, tx := paidTransaction(midA, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// Create refund for merchant A.
	resp, _ := svc.CreateRefundWithIdempotency(context.Background(), midA, tx.ID, refundReq(10_000), "key-iso-get")
	// Merchant B tries to get it.
	_, err := svc.GetRefund(context.Background(), midB, resp.RefundID)
	if !errors.Is(err, service.ErrRefundNotFound) {
		t.Errorf("want ErrRefundNotFound for wrong merchant get, got %v", err)
	}
}

// ─── Provider failure scenarios ───────────────────────────────────────────────

func TestRefundService_ProviderFailure_RefundFailed(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := &service.MockRefundProvider{ShouldFail: true}
	svc, refundRepo, _, _ := buildRefundService(txRepo, provider)

	resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), "key-fail")
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Errorf("want ErrProviderFailure, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response even on provider failure")
	}
	// Refund should be FAILED.
	if resp.Status != model.RefundStatusFailed {
		t.Errorf("want FAILED, got %s", resp.Status)
	}
	// Reserved amount should be released.
	txRepo.mu.Lock()
	finalTx := txRepo.txs[tx.ID]
	txRepo.mu.Unlock()
	if finalTx.ReservedRefundAmount != 0 {
		t.Errorf("reserved should be 0 after failure, got %d", finalTx.ReservedRefundAmount)
	}
	// Provider called once.
	if provider.CreateRefundCallCount() != 1 {
		t.Errorf("provider calls: want 1, got %d", provider.CreateRefundCallCount())
	}
	// Verify stored state.
	stored, _ := refundRepo.FindByMerchantAndID(context.Background(), mid, resp.RefundID)
	if stored.Status != model.RefundStatusFailed {
		t.Errorf("stored refund: want FAILED, got %s", stored.Status)
	}
}

func TestRefundService_ProviderTimeout_RefundPending(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := &service.MockRefundProvider{ShouldTimeout: true}
	svc, _, _, _ := buildRefundService(txRepo, provider)

	resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), "key-timeout")
	if !errors.Is(err, service.ErrProviderTimeout) {
		t.Errorf("want ErrProviderTimeout, got %v", err)
	}
	// On timeout refund stays PENDING (reservation held; result uncertain).
	if resp != nil && resp.Status == model.RefundStatusFailed {
		t.Error("on timeout, refund must not be FAILED — reservation must remain")
	}
	// Reserved amount must still be held.
	txRepo.mu.Lock()
	finalTx := txRepo.txs[tx.ID]
	txRepo.mu.Unlock()
	if finalTx.ReservedRefundAmount != 20_000 {
		t.Errorf("reserved should remain 20000 on timeout, got %d", finalTx.ReservedRefundAmount)
	}
}

func TestRefundService_ProviderTimeout_RetryReplaysExistingRefund(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := &service.MockRefundProvider{ShouldTimeout: true}
	svc, _, _, idempotencyRepo := buildRefundService(txRepo, provider)

	key := "key-timeout-retry"
	first, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), key)
	if !errors.Is(err, service.ErrProviderTimeout) {
		t.Fatalf("first call: want timeout, got %v", err)
	}
	if first == nil {
		t.Fatal("first timeout should return the created refund")
	}

	replayed, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), key)
	if err != nil {
		t.Fatalf("retry: want existing refund replay, got %v", err)
	}
	if replayed.RefundID != first.RefundID || replayed.Status != model.RefundStatusPending {
		t.Fatalf("retry returned a different refund or status: first=%+v retry=%+v", first, replayed)
	}
	if provider.CreateRefundCallCount() != 1 {
		t.Fatalf("retry must not call provider again: got %d calls", provider.CreateRefundCallCount())
	}
	record, err := idempotencyRepo.GetByMerchantAndKey(context.Background(), mid, key)
	if err != nil {
		t.Fatalf("get idempotency record: %v", err)
	}
	if record.Status != model.IdempotencyStatusProcessing {
		t.Fatalf("uncertain timeout must remain PROCESSING, got %s", record.Status)
	}
}

func TestRefundService_ProviderProcessing_RefundProcessing(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := &service.MockRefundProvider{ReturnProcessing: true}
	svc, _, _, _ := buildRefundService(txRepo, provider)

	resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(40_000), "key-proc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != model.RefundStatusProcessing {
		t.Errorf("want PROCESSING, got %s", resp.Status)
	}
	// Reserved must still be held (provider hasn't confirmed yet).
	txRepo.mu.Lock()
	finalTx := txRepo.txs[tx.ID]
	txRepo.mu.Unlock()
	if finalTx.ReservedRefundAmount != 40_000 {
		t.Errorf("reserved should be 40000 while PROCESSING, got %d", finalTx.ReservedRefundAmount)
	}
}

// ─── Idempotency ──────────────────────────────────────────────────────────────

func TestRefundService_Idempotency_SameKeyReplay(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	svc, _, _, _ := buildRefundService(txRepo, provider)

	req := refundReq(30_000)
	resp1, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, req, "idemp-key-1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Replay — same key, same payload.
	resp2, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, req, "idemp-key-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if resp2.RefundID != resp1.RefundID {
		t.Errorf("replay must return same refund_id: got %s, want %s", resp2.RefundID, resp1.RefundID)
	}
	// Provider called only once.
	if provider.CreateRefundCallCount() != 1 {
		t.Errorf("provider calls: want 1, got %d", provider.CreateRefundCallCount())
	}
}

func TestRefundService_Idempotency_DifferentPayload_Rejected(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(30_000), "idemp-key-2")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Same key, different amount.
	_, err = svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(40_000), "idemp-key-2")
	if !errors.Is(err, service.ErrIdempotencyKeyReused) {
		t.Errorf("want ErrIdempotencyKeyReused, got %v", err)
	}
}

func TestRefundService_Idempotency_MerchantIsolation(t *testing.T) {
	// Merchant A and B using the same key should be independent.
	midA := uuid.New()
	midB := uuid.New()
	txRepoA, txA := paidTransaction(midA, 100_000)
	txRepoB, txB := paidTransaction(midB, 100_000)

	// Build separate repos for each merchant.
	refundRepoA := newMemRefundRepo(txRepoA)
	refundRepoB := newMemRefundRepo(txRepoB)
	attemptRepo := newMemRefundAttemptRepo()
	idempRepo := newMemRefundIdempotencyRepo() // shared

	providerA := service.NewMockRefundProvider()
	providerB := service.NewMockRefundProvider()

	svcA := service.NewRefundService(txRepoA, refundRepoA, attemptRepo, idempRepo, providerA, nil, 24*time.Hour)
	svcB := service.NewRefundService(txRepoB, refundRepoB, attemptRepo, idempRepo, providerB, nil, 24*time.Hour)

	const sharedKey = "shared-idemp-key"
	respA, errA := svcA.CreateRefundWithIdempotency(context.Background(), midA, txA.ID, refundReq(10_000), sharedKey)
	respB, errB := svcB.CreateRefundWithIdempotency(context.Background(), midB, txB.ID, refundReq(20_000), sharedKey)

	if errA != nil || errB != nil {
		t.Fatalf("errA=%v errB=%v", errA, errB)
	}
	if respA.RefundID == respB.RefundID {
		t.Error("different merchants must get different refund IDs for same key")
	}
	if respA.Amount != 10_000 || respB.Amount != 20_000 {
		t.Errorf("amounts: A=%d B=%d", respA.Amount, respB.Amount)
	}
}

// ─── GetRefund / ListRefunds ──────────────────────────────────────────────────

func TestRefundService_GetRefund_Success(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	created, _ := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(25_000), "key-get")
	got, err := svc.GetRefund(context.Background(), mid, created.RefundID)
	if err != nil {
		t.Fatalf("GetRefund: %v", err)
	}
	if got.RefundID != created.RefundID {
		t.Errorf("refund_id mismatch")
	}
	if got.Amount != 25_000 {
		t.Errorf("amount: want 25000, got %d", got.Amount)
	}
}

func TestRefundService_ListRefunds_Success(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	// Create 2 refunds.
	_, _ = svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(10_000), "key-list1")
	_, _ = svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(15_000), "key-list2")

	result, err := svc.ListRefunds(context.Background(), mid, tx.ID, model.RefundListFilter{Page: 1, Limit: 20})
	if err != nil {
		t.Fatalf("ListRefunds: %v", err)
	}
	if len(result.Refunds) != 2 {
		t.Errorf("want 2 refunds, got %d", len(result.Refunds))
	}
	if result.Total != 2 {
		t.Errorf("total: want 2, got %d", result.Total)
	}
}

func TestRefundService_ListRefunds_TransactionNotFound(t *testing.T) {
	mid := uuid.New()
	txRepo := newMockTransactionRepo()
	svc, _, _, _ := buildRefundService(txRepo, service.NewMockRefundProvider())

	_, err := svc.ListRefunds(context.Background(), mid, uuid.New(), model.RefundListFilter{Page: 1, Limit: 20})
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("want ErrTransactionNotFound, got %v", err)
	}
}

// ─── Refund attempt tracking ──────────────────────────────────────────────────

func TestRefundService_AttemptRecorded_OnSuccess(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	svc, _, attemptRepo, _ := buildRefundService(txRepo, provider)

	resp, _ := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(10_000), "key-attempt")

	attemptRepo.mu.Lock()
	attempts := attemptRepo.attempts
	attemptRepo.mu.Unlock()

	// Find attempts for this refund.
	var refundAttempts []*model.RefundAttempt
	for _, a := range attempts {
		if a.RefundID == resp.RefundID {
			refundAttempts = append(refundAttempts, a)
		}
	}
	if len(refundAttempts) == 0 {
		t.Fatal("no refund attempts recorded")
	}
	if refundAttempts[0].Status != "SUCCESS" {
		t.Errorf("attempt status: want SUCCESS, got %s", refundAttempts[0].Status)
	}
	if refundAttempts[0].AttemptNumber != 1 {
		t.Errorf("attempt number: want 1, got %d", refundAttempts[0].AttemptNumber)
	}
}

func TestRefundService_AttemptRecorded_OnFailure(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := &service.MockRefundProvider{ShouldFail: true}
	svc, _, attemptRepo, _ := buildRefundService(txRepo, provider)

	resp, _ := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(10_000), "key-att-fail")

	attemptRepo.mu.Lock()
	attempts := attemptRepo.attempts
	attemptRepo.mu.Unlock()

	var refundAttempts []*model.RefundAttempt
	for _, a := range attempts {
		if a.RefundID == resp.RefundID {
			refundAttempts = append(refundAttempts, a)
		}
	}
	if len(refundAttempts) == 0 {
		t.Fatal("no refund attempts recorded on failure")
	}
	if refundAttempts[0].Status != "FAILED" {
		t.Errorf("attempt status: want FAILED, got %s", refundAttempts[0].Status)
	}
}

// ─── Concurrency test — critical financial invariant ─────────────────────────

// TestRefundService_ConcurrentRefunds_NeverExceedsBalance fires 10 goroutines
// simultaneously against a 100,000 IDR transaction, each requesting a 20,000
// refund. At most 5 can succeed. The total must never exceed 100,000 and
// must never go negative. Run with -race.
func TestRefundService_ConcurrentRefunds_NeverExceedsBalance(t *testing.T) {
	const goroutines = 10
	const txAmount = int64(100_000)
	const refundAmount = int64(20_000)
	const maxSucceeded = txAmount / refundAmount // 5

	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, txAmount)
	provider := service.NewMockRefundProvider()
	svc, refundRepo, _, _ := buildRefundService(txRepo, provider)

	var (
		wg        sync.WaitGroup
		succeeded atomic.Int64
		exceeded  atomic.Int64
		errCount  atomic.Int64
	)

	barrier := make(chan struct{})

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-barrier // start all goroutines simultaneously
			key := "concurrent-key-" + string(rune('A'+idx))
			resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(refundAmount), key)
			if err != nil {
				if errors.Is(err, service.ErrRefundAmountExceeded) {
					exceeded.Add(1)
					return
				}
				errCount.Add(1)
				t.Logf("goroutine %d: unexpected error: %v", idx, err)
				return
			}
			if resp.Status == model.RefundStatusSucceeded {
				succeeded.Add(1)
			}
		}(i)
	}

	close(barrier) // release all goroutines at once
	wg.Wait()

	if errCount.Load() > 0 {
		t.Errorf("%d unexpected errors", errCount.Load())
	}

	// Verify succeeded count does not exceed max allowed.
	if succeeded.Load() > maxSucceeded {
		t.Errorf("succeeded refunds %d exceeds max %d", succeeded.Load(), maxSucceeded)
	}

	// Critical: total refunded + reserved must never exceed txAmount.
	txRepo.mu.Lock()
	finalTx := txRepo.txs[tx.ID]
	refundedAmt := finalTx.RefundedAmount
	reservedAmt := finalTx.ReservedRefundAmount
	txRepo.mu.Unlock()

	total := refundedAmt + reservedAmt
	if total > txAmount {
		t.Errorf("FINANCIAL INVARIANT VIOLATED: refunded(%d) + reserved(%d) = %d > txAmount(%d)",
			refundedAmt, reservedAmt, total, txAmount)
	}
	if refundedAmt < 0 || reservedAmt < 0 {
		t.Errorf("NEGATIVE BALANCE: refunded=%d reserved=%d", refundedAmt, reservedAmt)
	}

	// Check every individual refund for uniqueness.
	refundRepo.mu.Lock()
	seenIDs := make(map[uuid.UUID]struct{})
	for id := range refundRepo.refunds {
		if _, dup := seenIDs[id]; dup {
			t.Errorf("duplicate refund ID: %s", id)
		}
		seenIDs[id] = struct{}{}
	}
	refundRepo.mu.Unlock()

	t.Logf("concurrent test: succeeded=%d, exceeded=%d, total_refunded=%d, reserved=%d",
		succeeded.Load(), exceeded.Load(), refundedAmt, reservedAmt)
}

// ─── RefundRequestHash — idempotency hash stability ──────────────────────────

func TestRefundRequestHash_Stability(t *testing.T) {
	txID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	req := model.CreateRefundRequest{Amount: 50_000, Currency: "IDR"}

	h1 := model.RefundRequestHash(txID, req)
	h2 := model.RefundRequestHash(txID, req)
	if h1 != h2 {
		t.Errorf("hash not stable: %s vs %s", h1, h2)
	}
}

func TestRefundRequestHash_DifferentAmounts_DifferentHash(t *testing.T) {
	txID := uuid.New()
	h1 := model.RefundRequestHash(txID, model.CreateRefundRequest{Amount: 10_000, Currency: "IDR"})
	h2 := model.RefundRequestHash(txID, model.CreateRefundRequest{Amount: 20_000, Currency: "IDR"})
	if h1 == h2 {
		t.Error("different amounts must produce different hashes")
	}
}

func TestRefundRequestHash_DifferentTransactions_DifferentHash(t *testing.T) {
	txA := uuid.New()
	txB := uuid.New()
	req := model.CreateRefundRequest{Amount: 10_000, Currency: "IDR"}
	h1 := model.RefundRequestHash(txA, req)
	h2 := model.RefundRequestHash(txB, req)
	if h1 == h2 {
		t.Error("different transaction IDs must produce different hashes")
	}
}

// ─── Concurrent identical idempotency key ────────────────────────────────────

// TestRefundService_Idempotency_ConcurrentSameKey fires 10 goroutines with the
// same key and payload. Exactly one provider call and one logical refund.
func TestRefundService_Idempotency_ConcurrentSameKey(t *testing.T) {
	const goroutines = 10
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	svc, refundRepo, _, _ := buildRefundService(txRepo, provider)

	req := refundReq(25_000)
	const key = "concurrent-identical-key"

	type result struct {
		resp *model.RefundResponse
		err  error
	}
	results := make(chan result, goroutines)
	var wg sync.WaitGroup
	barrier := make(chan struct{})

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			resp, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, req, key)
			results <- result{resp: resp, err: err}
		}()
	}
	close(barrier)
	wg.Wait()
	close(results)

	var successIDs []uuid.UUID
	var inProgress, otherErr int
	for r := range results {
		switch {
		case r.err == nil && r.resp != nil:
			successIDs = append(successIDs, r.resp.RefundID)
		case errors.Is(r.err, service.ErrIdempotencyInProgress):
			inProgress++
		default:
			otherErr++
			t.Logf("unexpected concurrent result: err=%v", r.err)
		}
	}
	if otherErr > 0 {
		t.Errorf("%d unexpected errors", otherErr)
	}
	if provider.CreateRefundCallCount() != 1 {
		t.Errorf("provider calls: want 1, got %d", provider.CreateRefundCallCount())
	}
	if len(successIDs) == 0 {
		t.Fatal("expected at least one successful response")
	}
	first := successIDs[0]
	for _, id := range successIDs[1:] {
		if id != first {
			t.Errorf("multiple refund IDs returned: %s vs %s", first, id)
		}
	}
	refundRepo.mu.Lock()
	count := 0
	for _, ref := range refundRepo.refunds {
		if ref.TransactionID == tx.ID {
			count++
		}
	}
	refundRepo.mu.Unlock()
	if count != 1 {
		t.Errorf("want exactly 1 refund row, got %d", count)
	}
	t.Logf("concurrent identical key: success=%d in_progress=%d refund_id=%s",
		len(successIDs), inProgress, first)
}

// ─── Outbox event capture ────────────────────────────────────────────────────

type captureRefundPublisher struct {
	mu     sync.Mutex
	events []model.MerchantWebhookEventType
}

func (p *captureRefundPublisher) Enqueue(context.Context, *model.Transaction, model.MerchantWebhookEventType) error {
	return nil
}
func (p *captureRefundPublisher) EnqueueInTx(context.Context, pgx.Tx, *model.Transaction, model.MerchantWebhookEventType) error {
	return nil
}
func (p *captureRefundPublisher) BuildDelivery(context.Context, *model.Transaction, model.MerchantWebhookEventType) (*model.MerchantWebhookDelivery, error) {
	return nil, nil
}
func (p *captureRefundPublisher) EnqueueRefundInTx(_ context.Context, _ pgx.Tx, _ *model.Refund, _ *model.Transaction, eventType model.MerchantWebhookEventType) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, eventType)
	return nil
}
func (p *captureRefundPublisher) snapshot() []model.MerchantWebhookEventType {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]model.MerchantWebhookEventType, len(p.events))
	copy(out, p.events)
	return out
}

func TestRefundService_Outbox_CreatedAndSucceeded(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	refundRepo := newMemRefundRepo(txRepo)
	attemptRepo := newMemRefundAttemptRepo()
	idempRepo := newMemRefundIdempotencyRepo()
	pub := &captureRefundPublisher{}
	svc := service.NewRefundService(txRepo, refundRepo, attemptRepo, idempRepo, provider, pub, 24*time.Hour)

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), "outbox-ok")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	events := pub.snapshot()
	wantCreated, wantSucceeded := false, false
	for _, e := range events {
		if e == model.MerchantWebhookEventRefundCreated {
			wantCreated = true
		}
		if e == model.MerchantWebhookEventRefundSucceeded {
			wantSucceeded = true
		}
	}
	if !wantCreated {
		t.Errorf("missing refund.created; events=%v", events)
	}
	if !wantSucceeded {
		t.Errorf("missing refund.succeeded; events=%v", events)
	}
}

func TestRefundService_Outbox_Failed(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	provider.ShouldFail = true
	refundRepo := newMemRefundRepo(txRepo)
	attemptRepo := newMemRefundAttemptRepo()
	idempRepo := newMemRefundIdempotencyRepo()
	pub := &captureRefundPublisher{}
	svc := service.NewRefundService(txRepo, refundRepo, attemptRepo, idempRepo, provider, pub, 24*time.Hour)

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), "outbox-fail")
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("want ErrProviderFailure, got %v", err)
	}
	events := pub.snapshot()
	hasFailed := false
	for _, e := range events {
		if e == model.MerchantWebhookEventRefundFailed {
			hasFailed = true
		}
	}
	if !hasFailed {
		t.Errorf("missing refund.failed; events=%v", events)
	}
}

func TestRefundService_Outbox_Processing(t *testing.T) {
	mid := uuid.New()
	txRepo, tx := paidTransaction(mid, 100_000)
	provider := service.NewMockRefundProvider()
	provider.ReturnProcessing = true
	refundRepo := newMemRefundRepo(txRepo)
	attemptRepo := newMemRefundAttemptRepo()
	idempRepo := newMemRefundIdempotencyRepo()
	pub := &captureRefundPublisher{}
	svc := service.NewRefundService(txRepo, refundRepo, attemptRepo, idempRepo, provider, pub, 24*time.Hour)

	_, err := svc.CreateRefundWithIdempotency(context.Background(), mid, tx.ID, refundReq(20_000), "outbox-proc")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	events := pub.snapshot()
	hasProcessing := false
	for _, e := range events {
		if e == model.MerchantWebhookEventRefundProcessing {
			hasProcessing = true
		}
	}
	if !hasProcessing {
		t.Errorf("missing refund.processing; events=%v", events)
	}
}
