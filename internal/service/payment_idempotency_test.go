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

type memoryIdempotencyRepo struct {
	mu      sync.Mutex
	records map[string]*model.IdempotencyKey
}

func newMemoryIdempotencyRepo() *memoryIdempotencyRepo {
	return &memoryIdempotencyRepo{records: make(map[string]*model.IdempotencyKey)}
}

func (r *memoryIdempotencyRepo) key(merchantID uuid.UUID, key string) string {
	return merchantID.String() + ":" + key
}

func (r *memoryIdempotencyRepo) GetByMerchantAndKey(_ context.Context, merchantID uuid.UUID, key string) (*model.IdempotencyKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[r.key(merchantID, key)]
	if !ok {
		return nil, repository.ErrIdempotencyNotFound
	}
	copy := *record
	return &copy, nil
}

func (r *memoryIdempotencyRepo) Create(_ context.Context, record *model.IdempotencyKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.key(record.MerchantID, record.Key)
	if _, ok := r.records[key]; ok {
		return repository.ErrIdempotencyConflict
	}
	copy := *record
	r.records[key] = &copy
	return nil
}

func (r *memoryIdempotencyRepo) Reserve(_ context.Context, record *model.IdempotencyKey) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.key(record.MerchantID, record.Key)
	if existing, ok := r.records[key]; ok && existing.ExpiresAt.After(time.Now().UTC()) {
		return false, nil
	}
	copy := *record
	r.records[key] = &copy
	return true, nil
}

func (r *memoryIdempotencyRepo) UpdateProcessing(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusProcessing
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

func (r *memoryIdempotencyRepo) AttachRefund(_ context.Context, id, refundID uuid.UUID) error {
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

func (r *memoryIdempotencyRepo) Complete(_ context.Context, id uuid.UUID, status int, body []byte, transactionID *uuid.UUID, refundID *uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusCompleted
			record.ResponseStatus = &status
			record.ResponseBody = append([]byte(nil), body...)
			record.TransactionID = transactionID
			record.RefundID = refundID
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

func (r *memoryIdempotencyRepo) Fail(_ context.Context, id uuid.UUID, status int, body []byte, transactionID *uuid.UUID, refundID *uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusFailed
			record.ResponseStatus = &status
			record.ResponseBody = append([]byte(nil), body...)
			record.TransactionID = transactionID
			record.RefundID = refundID
			return nil
		}
	}
	return repository.ErrIdempotencyNotFound
}

func buildIdempotentService(provider service.PaymentProvider, repo *memoryIdempotencyRepo) service.PaymentService {
	txRepo := newMockTransactionRepo()
	attemptRepo := newMockAttemptRepo()
	return service.NewPaymentServiceWithIdempotency(txRepo, attemptRepo, provider, repo, time.Hour)
}

func TestCreatePayment_IdempotentReplayAndMismatch(t *testing.T) {
	repo := newMemoryIdempotencyRepo()
	provider := service.NewMockPaymentProvider()
	svc := buildIdempotentService(provider, repo)
	req := validCreateReq("IDEMP-001")

	first, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "KEY-001")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	replay, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "KEY-001")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.TransactionID != first.TransactionID || provider.CreatePaymentCallCount() != 1 {
		t.Fatalf("replay created new payment: first=%s replay=%s calls=%d", first.TransactionID, replay.TransactionID, provider.CreatePaymentCallCount())
	}

	req.Amount++
	if _, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "KEY-001"); !errors.Is(err, service.ErrIdempotencyKeyReused) {
		t.Fatalf("expected key reuse error, got %v", err)
	}
	if provider.CreatePaymentCallCount() != 1 {
		t.Fatalf("mismatch called provider: %d", provider.CreatePaymentCallCount())
	}
}

func TestCreatePayment_IdempotencyMerchantIsolation(t *testing.T) {
	repo := newMemoryIdempotencyRepo()
	provider := service.NewMockPaymentProvider()
	svc := buildIdempotentService(provider, repo)
	req := validCreateReq("IDEMP-MERCHANT")

	first, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "SHARED-KEY")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantBID, req, "SHARED-KEY")
	if err != nil {
		t.Fatal(err)
	}
	if first.TransactionID == second.TransactionID || provider.CreatePaymentCallCount() != 2 {
		t.Fatalf("merchants were not isolated: first=%s second=%s calls=%d", first.TransactionID, second.TransactionID, provider.CreatePaymentCallCount())
	}
}

func TestCreatePayment_IdempotencyProcessingCompletedAndExpired(t *testing.T) {
	repo := newMemoryIdempotencyRepo()
	provider := service.NewMockPaymentProvider()
	svc := buildIdempotentService(provider, repo)
	req := validCreateReq("IDEMP-STATES")
	now := time.Now().UTC()
	processing := &model.IdempotencyKey{ID: uuid.New(), MerchantID: merchantAID, Key: "PROCESSING", RequestHash: model.PaymentRequestHash(req), Status: model.IdempotencyStatusProcessing, ExpiresAt: now.Add(time.Hour)}
	if err := repo.Create(context.Background(), processing); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "PROCESSING"); !errors.Is(err, service.ErrIdempotencyInProgress) {
		t.Fatalf("expected processing error, got %v", err)
	}
	if provider.CreatePaymentCallCount() != 0 {
		t.Fatal("processing replay called provider")
	}

	completed := &model.IdempotencyKey{ID: uuid.New(), MerchantID: merchantAID, Key: "COMPLETED", RequestHash: model.PaymentRequestHash(req), Status: model.IdempotencyStatusCompleted, ResponseBody: []byte(`{"transaction_id":"00000000-0000-0000-0000-000000000001","merchant_order_id":"IDEMP-STATES","amount":50000,"currency":"IDR","payment_method":"QRIS","provider":"MOCK","provider_transaction_id":"provider-1","status":"PENDING"}`), ExpiresAt: now.Add(time.Hour)}
	if err := repo.Create(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "COMPLETED")
	if err != nil || replay.ProviderTransactionID != "provider-1" {
		t.Fatalf("completed replay failed: response=%+v err=%v", replay, err)
	}

	expired := &model.IdempotencyKey{ID: uuid.New(), MerchantID: merchantAID, Key: "EXPIRED", RequestHash: model.PaymentRequestHash(req), Status: model.IdempotencyStatusCompleted, ResponseBody: completed.ResponseBody, ExpiresAt: now.Add(-time.Minute)}
	if err := repo.Create(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "EXPIRED"); err != nil {
		t.Fatalf("expired key should be reservable: %v", err)
	}
}

func TestCreatePayment_IdempotencyFailureReplay(t *testing.T) {
	repo := newMemoryIdempotencyRepo()
	provider := &service.MockPaymentProvider{ShouldFailCreate: true}
	svc := buildIdempotentService(provider, repo)
	req := validCreateReq("IDEMP-FAIL")
	if _, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "FAIL-KEY"); !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("first failure: %v", err)
	}
	if _, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "FAIL-KEY"); !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("failure replay: %v", err)
	}
	if provider.CreatePaymentCallCount() != 1 {
		t.Fatalf("failure replay called provider %d times", provider.CreatePaymentCallCount())
	}
}

func TestCreatePayment_IdempotencyConcurrentProviderOnce(t *testing.T) {
	repo := newMemoryIdempotencyRepo()
	provider := service.NewMockPaymentProvider()
	svc := buildIdempotentService(provider, repo)
	req := validCreateReq("IDEMP-CONCURRENT")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreatePaymentWithIdempotency(context.Background(), merchantAID, req, "CONCURRENT")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, service.ErrIdempotencyInProgress) {
			t.Fatalf("concurrent request failed unexpectedly: %v", err)
		}
	}
	if provider.CreatePaymentCallCount() != 1 {
		t.Fatalf("expected one provider call, got %d", provider.CreatePaymentCallCount())
	}
}
