package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── In-memory WebhookEventRepository ────────────────────────────────────────

type memWebhookRepo struct {
	mu     sync.Mutex
	events map[string]*model.WebhookEvent // key: "provider:event_id"
	byID   map[uuid.UUID]*model.WebhookEvent
}

func newMemWebhookRepo() *memWebhookRepo {
	return &memWebhookRepo{
		events: make(map[string]*model.WebhookEvent),
		byID:   make(map[uuid.UUID]*model.WebhookEvent),
	}
}

func (r *memWebhookRepo) Create(_ context.Context, ev *model.WebhookEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ev.Provider + ":" + ev.EventID
	if _, exists := r.events[key]; exists {
		return repository.ErrWebhookEventDuplicate
	}
	r.events[key] = ev
	r.byID[ev.ID] = ev
	return nil
}

func (r *memWebhookRepo) FindByProviderAndEventID(_ context.Context, provider, eventID string) (*model.WebhookEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := provider + ":" + eventID
	ev, ok := r.events[key]
	if !ok {
		return nil, repository.ErrWebhookEventNotFound
	}
	return ev, nil
}

func (r *memWebhookRepo) UpdateStatus(_ context.Context, id uuid.UUID, status model.WebhookEventStatus, errMsg *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.byID[id]
	if !ok {
		return repository.ErrWebhookEventNotFound
	}
	ev.Status = status
	ev.ErrorMessage = errMsg
	if status == model.WebhookEventStatusProcessed {
		now := time.Now().UTC()
		ev.ProcessedAt = &now
	}
	return nil
}

var _ repository.WebhookEventRepository = (*memWebhookRepo)(nil)

// ─── In-memory txRepo with forceError support ─────────────────────────────────

// errTxRepo wraps mockTransactionRepo and can inject DB errors on UpdateStatus.
type errTxRepo struct {
	*mockTransactionRepo
	updateErr error // if non-nil, UpdateStatus and UpdateStatusWithPaidAt return this
}

func (r *errTxRepo) UpdateStatus(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	return r.mockTransactionRepo.UpdateStatus(ctx, id, from, to)
}
func (r *errTxRepo) UpdateStatusWithPaidAt(ctx context.Context, id uuid.UUID, from model.TransactionStatus) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	return r.mockTransactionRepo.UpdateStatusWithPaidAt(ctx, id, from)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

const testSecret = "test-secret-key"

func buildWebhookService(txRepo repository.TransactionRepository, webhookRepo repository.WebhookEventRepository) service.WebhookService {
	parser := service.NewMockWebhookParser(testSecret)
	return service.NewWebhookService(txRepo, webhookRepo, parser)
}

// makeSignedPayload returns a JSON payload and its valid HMAC-SHA256 signature.
func makeSignedPayload(t *testing.T, fields map[string]any) ([]byte, string) {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("makeSignedPayload marshal: %v", err)
	}
	sig := service.SignMockWebhookPayload(b, testSecret)
	return b, sig
}

// insertPendingTx puts a PENDING transaction directly into txRepo.
func insertPendingTx(r *mockTransactionRepo, providerTxID string) *model.Transaction {
	provider := "MOCK"
	expiry := time.Now().UTC().Add(30 * time.Minute)
	tx := &model.Transaction{
		ID:                    uuid.New(),
		MerchantID:            uuid.New(),
		MerchantOrderID:       "ORDER-" + uuid.New().String()[:8],
		Amount:                50000,
		Currency:              "IDR",
		PaymentMethod:         "QRIS",
		Provider:              &provider,
		ProviderTransactionID: &providerTxID,
		Status:                model.TransactionStatusPending,
		ExpiredAt:             &expiry,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	r.txs[tx.ID] = tx
	return tx
}

// ─── WebhookService — PAID tests ──────────────────────────────────────────────

func TestWebhookService_ValidPaidWebhook(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	tx := insertPendingTx(txRepo, "MOCK-TXN-abc001")

	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":                "evt-paid-001",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-abc001",
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "PAID",
		"amount":                  50000,
		"currency":                "IDR",
	})

	result, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Status != model.WebhookEventStatusProcessed {
		t.Errorf("expected PROCESSED, got %s", result.Status)
	}

	// Transaction must now be PAID.
	updated := txRepo.txs[tx.ID]
	if updated.Status != model.TransactionStatusPaid {
		t.Errorf("expected transaction PAID, got %s", updated.Status)
	}
	// paid_at must be set.
	if updated.PaidAt == nil {
		t.Error("expected paid_at to be set")
	}
}

func TestWebhookService_ValidFailedWebhook(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	tx := insertPendingTx(txRepo, "MOCK-TXN-abc002")

	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":                "evt-failed-001",
		"event_type":              "PAYMENT_FAILED",
		"provider_transaction_id": "MOCK-TXN-abc002",
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "FAILED",
		"amount":                  50000,
		"currency":                "IDR",
	})

	result, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Status != model.WebhookEventStatusProcessed {
		t.Errorf("expected PROCESSED, got %s", result.Status)
	}
	if txRepo.txs[tx.ID].Status != model.TransactionStatusFailed {
		t.Errorf("expected FAILED, got %s", txRepo.txs[tx.ID].Status)
	}
}

// ─── WebhookService — signature tests ─────────────────────────────────────────

func TestWebhookService_InvalidSignature(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, _ := makeSignedPayload(t, map[string]any{
		"event_id": "evt-sig-001", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})

	_, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, "deadbeef0000")
	if !errors.Is(err, service.ErrWebhookInvalidSignature) {
		t.Errorf("expected ErrWebhookInvalidSignature, got %v", err)
	}
}

func TestWebhookService_MissingSignature(t *testing.T) {
	// Empty signature should also fail verification.
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, _ := makeSignedPayload(t, map[string]any{
		"event_id": "evt-nosig", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})

	_, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, "")
	if !errors.Is(err, service.ErrWebhookInvalidSignature) {
		t.Errorf("expected ErrWebhookInvalidSignature, got %v", err)
	}
}

func TestWebhookService_WrongSecret(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, _ := makeSignedPayload(t, map[string]any{
		"event_id": "evt-wrongkey", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})
	// Sign with a DIFFERENT secret.
	wrongSig := service.SignMockWebhookPayload(payload, "wrong-secret")

	_, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, wrongSig)
	if !errors.Is(err, service.ErrWebhookInvalidSignature) {
		t.Errorf("expected ErrWebhookInvalidSignature, got %v", err)
	}
}

func TestWebhookService_ModifiedPayload(t *testing.T) {
	// Sign payload A, then tamper with it.
	original := []byte(`{"event_id":"evt-tamper","event_type":"PAYMENT_PAID","provider_transaction_id":"MOCK-TXN-x"}`)
	goodSig := service.SignMockWebhookPayload(original, testSecret)

	tampered := []byte(`{"event_id":"evt-tamper","event_type":"PAYMENT_PAID","provider_transaction_id":"MOCK-TXN-DIFFERENT"}`)

	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	_, err := svc.ProcessWebhook(context.Background(), "MOCK", tampered, goodSig)
	if !errors.Is(err, service.ErrWebhookInvalidSignature) {
		t.Errorf("expected ErrWebhookInvalidSignature, got %v", err)
	}
}

// ─── WebhookService — payload validation tests ────────────────────────────────

func TestWebhookService_MalformedJSON(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	badPayload := []byte(`{not valid json`)
	sig := service.SignMockWebhookPayload(badPayload, testSecret)

	_, err := svc.ProcessWebhook(context.Background(), "MOCK", badPayload, sig)
	if !errors.Is(err, service.ErrWebhookMalformedPayload) {
		t.Errorf("expected ErrWebhookMalformedPayload, got %v", err)
	}
}

func TestWebhookService_MissingEventID(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		// event_id intentionally omitted
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})
	_, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if !errors.Is(err, service.ErrWebhookMissingFields) {
		t.Errorf("expected ErrWebhookMissingFields, got %v", err)
	}
}

func TestWebhookService_MissingProviderTransactionID(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":   "evt-noptx",
		"event_type": "PAYMENT_PAID",
		// provider_transaction_id omitted
	})
	_, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if !errors.Is(err, service.ErrWebhookMissingFields) {
		t.Errorf("expected ErrWebhookMissingFields, got %v", err)
	}
}

// ─── WebhookService — provider / transaction lookup tests ─────────────────────

func TestWebhookService_UnknownProvider(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id": "evt-unk", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "X-123",
	})
	_, err := svc.ProcessWebhook(context.Background(), "UNKNOWN_PROVIDER", payload, sig)
	if !errors.Is(err, service.ErrWebhookUnknownProvider) {
		t.Errorf("expected ErrWebhookUnknownProvider, got %v", err)
	}
}

func TestWebhookService_TransactionNotFound(t *testing.T) {
	txRepo := newMockTransactionRepo() // empty — no transactions
	webhookRepo := newMemWebhookRepo()
	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":                "evt-notx",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-doesnotexist",
	})
	_, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("expected ErrTransactionNotFound, got %v", err)
	}
}

// ─── WebhookService — idempotency tests ───────────────────────────────────────

func TestWebhookService_DuplicateWebhook_ReturnsIgnored(t *testing.T) {
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	tx := insertPendingTx(txRepo, "MOCK-TXN-dup001")
	svc := buildWebhookService(txRepo, webhookRepo)

	fields := map[string]any{
		"event_id":                "evt-dup-001",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-dup001",
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "PAID",
		"amount":                  50000,
		"currency":                "IDR",
	}
	payload, sig := makeSignedPayload(t, fields)

	// First call succeeds.
	r1, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if r1.Status != model.WebhookEventStatusProcessed {
		t.Errorf("first call: expected PROCESSED, got %s", r1.Status)
	}

	// Second call with same event_id must return IGNORED (not an error).
	r2, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if err != nil {
		t.Fatalf("second call returned error: %v", err)
	}
	if r2.Status != model.WebhookEventStatusIgnored {
		t.Errorf("second call: expected IGNORED, got %s", r2.Status)
	}

	// Transaction must still be PAID (not double-processed).
	if txRepo.txs[tx.ID].Status != model.TransactionStatusPaid {
		t.Errorf("transaction should remain PAID after duplicate")
	}
}

func TestWebhookService_DuplicatePaidEvent_AlreadyPaid_Ignored(t *testing.T) {
	// Transaction is already PAID; receiving another PAID event should return IGNORED.
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()

	providerTxID := "MOCK-TXN-alreadypaid"
	provider := "MOCK"
	paidAt := time.Now().UTC()
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-PAID",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Provider: &provider, ProviderTransactionID: &providerTxID,
		Status: model.TransactionStatusPaid, PaidAt: &paidAt,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":                "evt-paid-dup-002",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": providerTxID,
		"status":                  "PAID",
		"amount":                  50000,
		"currency":                "IDR",
	})

	result, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if err != nil {
		t.Fatalf("expected no error for already-PAID, got: %v", err)
	}
	if result.Status != model.WebhookEventStatusIgnored {
		t.Errorf("expected IGNORED for already-PAID, got %s", result.Status)
	}
}

// ─── WebhookService — invalid state transitions ───────────────────────────────

func TestWebhookService_PAIDtoFAILED_Ignored(t *testing.T) {
	// PAID → FAILED is not allowed; webhook should be IGNORED (not error).
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()

	providerTxID := "MOCK-TXN-paidfailed"
	provider := "MOCK"
	paidAt := time.Now().UTC()
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-PF",
		Provider: &provider, ProviderTransactionID: &providerTxID,
		Status: model.TransactionStatusPaid, PaidAt: &paidAt,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildWebhookService(txRepo, webhookRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":                "evt-pf-001",
		"event_type":              "PAYMENT_FAILED",
		"provider_transaction_id": providerTxID,
	})
	result, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Status != model.WebhookEventStatusIgnored {
		t.Errorf("expected IGNORED for invalid transition PAID→FAILED, got %s", result.Status)
	}
	// Transaction must remain PAID.
	if txRepo.txs[tx.ID].Status != model.TransactionStatusPaid {
		t.Errorf("transaction must remain PAID, got %s", txRepo.txs[tx.ID].Status)
	}
}

// ─── WebhookService — concurrent duplicate tests ──────────────────────────────

func TestWebhookService_ConcurrentDuplicateWebhooks_OnlyOneProcessed(t *testing.T) {
	// Simulate two goroutines receiving the same event_id simultaneously.
	// The database UNIQUE constraint (modeled by memWebhookRepo.mu) ensures
	// exactly one INSERT succeeds; the other returns ErrWebhookEventDuplicate.
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()
	tx := insertPendingTx(txRepo, "MOCK-TXN-concurrent")
	svc := buildWebhookService(txRepo, webhookRepo)

	fields := map[string]any{
		"event_id":                "evt-concurrent-001",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-concurrent",
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "PAID",
		"amount":                  50000,
		"currency":                "IDR",
	}
	payload, sig := makeSignedPayload(t, fields)

	var wg sync.WaitGroup
	results := make([]model.WebhookEventStatus, 2)
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := svc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
			errs[i] = err
			if r != nil {
				results[i] = r.Status
			}
		}()
	}
	wg.Wait()

	// No errors.
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d returned error: %v", i, err)
		}
	}

	// Count outcomes.
	processed, ignored := 0, 0
	for _, s := range results {
		switch s {
		case model.WebhookEventStatusProcessed:
			processed++
		case model.WebhookEventStatusIgnored:
			ignored++
		}
	}
	if processed != 1 {
		t.Errorf("expected exactly 1 PROCESSED, got %d", processed)
	}
	if ignored != 1 {
		t.Errorf("expected exactly 1 IGNORED, got %d", ignored)
	}
	// Transaction must be PAID exactly once.
	if txRepo.txs[tx.ID].Status != model.TransactionStatusPaid {
		t.Errorf("transaction should be PAID, got %s", txRepo.txs[tx.ID].Status)
	}
}

// ─── ExpiryService tests ──────────────────────────────────────────────────────

func buildExpiryService(txRepo repository.TransactionRepository) service.ExpiryService {
	return service.NewExpiryService(txRepo)
}

func TestExpiryService_PendingExpiredBecomesExpired(t *testing.T) {
	txRepo := newMockTransactionRepo()
	provider := "MOCK"
	past := time.Now().UTC().Add(-1 * time.Minute)
	ptxID := "MOCK-TXN-exp001"
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-EXP",
		Provider: &provider, ProviderTransactionID: &ptxID,
		Status: model.TransactionStatusPending, ExpiredAt: &past,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildExpiryService(txRepo)
	n, err := svc.ProcessExpired(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessExpired error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 processed, got %d", n)
	}
	if txRepo.txs[tx.ID].Status != model.TransactionStatusExpired {
		t.Errorf("expected EXPIRED, got %s", txRepo.txs[tx.ID].Status)
	}
}

func TestExpiryService_PendingNotExpiredRemainsUnchanged(t *testing.T) {
	txRepo := newMockTransactionRepo()
	provider := "MOCK"
	future := time.Now().UTC().Add(30 * time.Minute)
	ptxID := "MOCK-TXN-notexp"
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-NOTEXP",
		Provider: &provider, ProviderTransactionID: &ptxID,
		Status: model.TransactionStatusPending, ExpiredAt: &future,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildExpiryService(txRepo)
	n, err := svc.ProcessExpired(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessExpired error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 processed, got %d", n)
	}
	if txRepo.txs[tx.ID].Status != model.TransactionStatusPending {
		t.Errorf("non-expired transaction should remain PENDING, got %s", txRepo.txs[tx.ID].Status)
	}
}

func TestExpiryService_PAIDNotExpired(t *testing.T) {
	txRepo := newMockTransactionRepo()
	provider := "MOCK"
	past := time.Now().UTC().Add(-1 * time.Minute)
	paidAt := time.Now().UTC()
	ptxID := "MOCK-TXN-paid"
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-PAID",
		Provider: &provider, ProviderTransactionID: &ptxID,
		Status: model.TransactionStatusPaid, ExpiredAt: &past, PaidAt: &paidAt,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildExpiryService(txRepo)
	n, _ := svc.ProcessExpired(context.Background(), 100)
	if n != 0 {
		t.Errorf("PAID transaction must not be expired, got %d processed", n)
	}
	if txRepo.txs[tx.ID].Status != model.TransactionStatusPaid {
		t.Error("PAID transaction must remain PAID")
	}
}

func TestExpiryService_CancelledNotExpired(t *testing.T) {
	txRepo := newMockTransactionRepo()
	provider := "MOCK"
	past := time.Now().UTC().Add(-1 * time.Minute)
	ptxID := "MOCK-TXN-cancelled"
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-CANC",
		Provider: &provider, ProviderTransactionID: &ptxID,
		Status: model.TransactionStatusCancelled, ExpiredAt: &past,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildExpiryService(txRepo)
	n, _ := svc.ProcessExpired(context.Background(), 100)
	if n != 0 {
		t.Errorf("CANCELLED transaction must not be expired, got %d", n)
	}
}

func TestExpiryService_BatchLimitRespected(t *testing.T) {
	txRepo := newMockTransactionRepo()
	past := time.Now().UTC().Add(-1 * time.Minute)

	// Insert 5 expired PENDING transactions.
	for i := 0; i < 5; i++ {
		provider := "MOCK"
		ptxID := "MOCK-TXN-batch-" + uuid.New().String()[:8]
		tx := &model.Transaction{
			ID: uuid.New(), MerchantID: uuid.New(),
			MerchantOrderID: "ORDER-BATCH-" + ptxID,
			Provider:        &provider, ProviderTransactionID: &ptxID,
			Status: model.TransactionStatusPending, ExpiredAt: &past,
			Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		txRepo.txs[tx.ID] = tx
	}

	svc := buildExpiryService(txRepo)
	// Limit to 3.
	n, err := svc.ProcessExpired(context.Background(), 3)
	if err != nil {
		t.Fatalf("ProcessExpired error: %v", err)
	}
	// Should not process more than 3 (batch limit).
	if n > 3 {
		t.Errorf("batch limit not respected: processed %d > 3", n)
	}
}

func TestExpiryService_MultipleExpiredTransactions(t *testing.T) {
	txRepo := newMockTransactionRepo()
	past := time.Now().UTC().Add(-1 * time.Minute)
	ids := make([]uuid.UUID, 3)

	for i := 0; i < 3; i++ {
		provider := "MOCK"
		ptxID := "MOCK-TXN-multi-" + uuid.New().String()[:8]
		tx := &model.Transaction{
			ID: uuid.New(), MerchantID: uuid.New(),
			MerchantOrderID: "ORDER-MULTI-" + ptxID,
			Provider:        &provider, ProviderTransactionID: &ptxID,
			Status: model.TransactionStatusPending, ExpiredAt: &past,
			Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		txRepo.txs[tx.ID] = tx
		ids[i] = tx.ID
	}

	svc := buildExpiryService(txRepo)
	n, err := svc.ProcessExpired(context.Background(), 100)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 processed, got %d", n)
	}
	for _, id := range ids {
		if txRepo.txs[id].Status != model.TransactionStatusExpired {
			t.Errorf("tx %s should be EXPIRED", id)
		}
	}
}

func TestExpiryService_ConcurrentWebhookVsExpiry(t *testing.T) {
	// Both webhook (PAID) and expiry worker run concurrently on the same PENDING tx.
	// Exactly one must win; the other must NOT produce an error or data corruption.
	txRepo := newMockTransactionRepo()
	webhookRepo := newMemWebhookRepo()

	past := time.Now().UTC().Add(-1 * time.Minute)
	provider := "MOCK"
	ptxID := "MOCK-TXN-race001"
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-RACE",
		Provider: &provider, ProviderTransactionID: &ptxID,
		Status: model.TransactionStatusPending, ExpiredAt: &past,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	webhookSvc := buildWebhookService(txRepo, webhookRepo)
	expirySvc := buildExpiryService(txRepo)

	payload, sig := makeSignedPayload(t, map[string]any{
		"event_id":                "evt-race-001",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": ptxID,
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "PAID",
	})

	var wg sync.WaitGroup
	webhookErr := make(chan error, 1)
	expiryErr := make(chan error, 1)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := webhookSvc.ProcessWebhook(context.Background(), "MOCK", payload, sig)
		webhookErr <- err
	}()
	go func() {
		defer wg.Done()
		_, err := expirySvc.ProcessExpired(context.Background(), 100)
		expiryErr <- err
	}()
	wg.Wait()
	close(webhookErr)
	close(expiryErr)

	for err := range webhookErr {
		if err != nil {
			t.Errorf("webhook goroutine error: %v", err)
		}
	}
	for err := range expiryErr {
		if err != nil {
			t.Errorf("expiry goroutine error: %v", err)
		}
	}

	// Final status must be exactly one of PAID or EXPIRED — never anything else.
	finalStatus := txRepo.txs[tx.ID].Status
	if finalStatus != model.TransactionStatusPaid && finalStatus != model.TransactionStatusExpired {
		t.Errorf("unexpected final status: %s (expected PAID or EXPIRED)", finalStatus)
	}
}

// ─── ExpiryWorker tests ───────────────────────────────────────────────────────

func TestExpiryWorker_StopsOnContextCancel(t *testing.T) {
	txRepo := newMockTransactionRepo()
	svc := buildExpiryService(txRepo)
	worker := service.NewExpiryWorker(svc, 100*time.Millisecond, 10)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)

	// Let it run for a short time, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait must return (no goroutine leak).
	done := make(chan struct{})
	go func() {
		worker.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Error("ExpiryWorker did not stop within 2 seconds after context cancel")
	}
}

func TestExpiryWorker_ProcessesExpiredOnTick(t *testing.T) {
	txRepo := newMockTransactionRepo()
	past := time.Now().UTC().Add(-1 * time.Minute)
	provider := "MOCK"
	ptxID := "MOCK-TXN-worker001"
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "ORDER-WORKER",
		Provider: &provider, ProviderTransactionID: &ptxID,
		Status: model.TransactionStatusPending, ExpiredAt: &past,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	txRepo.txs[tx.ID] = tx

	svc := buildExpiryService(txRepo)
	// Short interval so the worker ticks quickly in tests.
	worker := service.NewExpiryWorker(svc, 50*time.Millisecond, 100)

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)

	// The worker runs once immediately on Start; allow a small window.
	time.Sleep(30 * time.Millisecond)
	cancel()
	worker.Wait()

	if txRepo.txs[tx.ID].Status != model.TransactionStatusExpired {
		t.Errorf("expected EXPIRED after worker tick, got %s", txRepo.txs[tx.ID].Status)
	}
}
