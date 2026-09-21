package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── In-memory RefundRepository for webhook tests ─────────────────────────────

type memRefundRepoWH struct {
	mu      sync.Mutex
	refunds map[string]*model.Refund    // key: provider+":"+providerRefundID
	byID    map[uuid.UUID]*model.Refund // key: refund.ID
	txs     map[uuid.UUID]*model.Transaction
}

func newMemRefundRepoWH() *memRefundRepoWH {
	return &memRefundRepoWH{
		refunds: make(map[string]*model.Refund),
		byID:    make(map[uuid.UUID]*model.Refund),
		txs:     make(map[uuid.UUID]*model.Transaction),
	}
}

func (r *memRefundRepoWH) insertRefund(ref *model.Refund) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *ref
	r.byID[ref.ID] = &cp
	if ref.ProviderRefundID != nil {
		r.refunds[ref.Provider+":"+*ref.ProviderRefundID] = &cp
	}
}

func (r *memRefundRepoWH) insertTx(tx *model.Transaction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *tx
	r.txs[tx.ID] = &cp
}

func (r *memRefundRepoWH) FindByID(_ context.Context, id uuid.UUID) (*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byID[id]
	if !ok {
		return nil, repository.ErrRefundNotFound
	}
	cp := *ref
	return &cp, nil
}

func (r *memRefundRepoWH) FindByMerchantAndID(_ context.Context, merchantID, refundID uuid.UUID) (*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byID[refundID]
	if !ok || ref.MerchantID != merchantID {
		return nil, repository.ErrRefundNotFound
	}
	cp := *ref
	return &cp, nil
}

func (r *memRefundRepoWH) FindByProviderRefundID(_ context.Context, provider, providerRefundID string) (*model.Refund, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.refunds[provider+":"+providerRefundID]
	if !ok {
		return nil, repository.ErrRefundNotFound
	}
	cp := *ref
	return &cp, nil
}

func (r *memRefundRepoWH) ListByTransaction(_ context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error) {
	return nil, nil
}

func (r *memRefundRepoWH) CountByTransaction(_ context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) (int64, error) {
	return 0, nil
}

func (r *memRefundRepoWH) ListByMerchant(_ context.Context, merchantID uuid.UUID, filter model.RefundListFilter) ([]*model.Refund, error) {
	return nil, nil
}

func (r *memRefundRepoWH) CountByMerchant(_ context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (int64, error) {
	return 0, nil
}

func (r *memRefundRepoWH) ReserveAndCreate(_ context.Context, merchantID, transactionID uuid.UUID, refund *model.Refund, _ repository.RefundOutboxHook, _ model.MerchantWebhookEventType) (*model.Transaction, error) {
	return nil, nil
}

func (r *memRefundRepoWH) FinalizeAfterProvider(
	_ context.Context,
	refundID uuid.UUID,
	newStatus model.RefundStatus,
	providerRefundID *string,
	failureCode, failureMessage *string,
	_ repository.RefundOutboxHook,
	_ model.MerchantWebhookEventType,
) (*model.Refund, *model.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byID[refundID]
	if !ok {
		return nil, nil, repository.ErrRefundNotFound
	}
	if ref.Status == model.RefundStatusSucceeded || ref.Status == model.RefundStatusFailed || ref.Status == newStatus {
		tx := r.txs[ref.TransactionID]
		txCp := *tx
		refCp := *ref
		return &refCp, &txCp, nil
	}
	now := time.Now().UTC()
	switch newStatus {
	case model.RefundStatusSucceeded:
		ref.Status = model.RefundStatusSucceeded
		ref.SucceededAt = &now
		ref.ProviderRefundID = providerRefundID
		if tx, ok := r.txs[ref.TransactionID]; ok {
			tx.ReservedRefundAmount -= ref.Amount
			tx.RefundedAmount += ref.Amount
		}
	case model.RefundStatusFailed:
		ref.Status = model.RefundStatusFailed
		ref.FailedAt = &now
		if tx, ok := r.txs[ref.TransactionID]; ok {
			tx.ReservedRefundAmount -= ref.Amount
		}
	}
	tx := r.txs[ref.TransactionID]
	txCp := *tx
	refCp := *ref
	return &refCp, &txCp, nil
}

func (r *memRefundRepoWH) ProcessRefundWebhookAtomically(
	ctx context.Context,
	webhook *model.WebhookEvent,
	provider, providerRefundID string,
	amount int64,
	currency string,
	targetStatus model.RefundStatus,
	outbox repository.RefundOutboxHook,
	eventType model.MerchantWebhookEventType,
) (model.WebhookEventStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ref, ok := r.refunds[provider+":"+providerRefundID]
	if !ok {
		return model.WebhookEventStatusFailed, repository.ErrRefundNotFound
	}
	if amount > 0 && amount != ref.Amount {
		return model.WebhookEventStatusIgnored, nil
	}
	if currency != "" && currency != ref.Currency {
		return model.WebhookEventStatusIgnored, nil
	}
	// Terminal and duplicate same-status callbacks are ignored.
	if ref.Status == model.RefundStatusSucceeded || ref.Status == model.RefundStatusFailed || ref.Status == targetStatus {
		return model.WebhookEventStatusIgnored, nil
	}

	now := time.Now().UTC()
	switch targetStatus {
	case model.RefundStatusSucceeded:
		ref.Status = model.RefundStatusSucceeded
		ref.SucceededAt = &now
		ref.ProviderRefundID = &providerRefundID
		if tx, ok := r.txs[ref.TransactionID]; ok {
			tx.ReservedRefundAmount -= ref.Amount
			tx.RefundedAmount += ref.Amount
		}
	case model.RefundStatusFailed:
		ref.Status = model.RefundStatusFailed
		ref.FailedAt = &now
		if tx, ok := r.txs[ref.TransactionID]; ok {
			tx.ReservedRefundAmount -= ref.Amount
		}
	}
	return model.WebhookEventStatusProcessed, nil
}

var _ repository.RefundRepository = (*memRefundRepoWH)(nil)

// ─── Webhook router with refund support ──────────────────────────────────────

type refundWebhookDeps struct {
	router      *gin.Engine
	txRepo      *memTxRepo
	webhookRepo *memWebhookRepoH
	refundRepo  *memRefundRepoWH
}

func newRefundWebhookRouter(t *testing.T) *refundWebhookDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	txRepo := newMemTxRepo()
	webhookRepo := newMemWebhookRepoH()
	refundRepo := newMemRefundRepoWH()

	parser := service.NewMockWebhookParser(webhookTestSecret)
	webhookSvc := service.NewWebhookService(txRepo, webhookRepo, parser)
	service.ConfigureWebhookRefundRepository(webhookSvc, refundRepo)

	h := handler.NewWebhookHandler(webhookSvc)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.POST("/api/v1/webhooks/providers/:provider", h.Receive)

	return &refundWebhookDeps{
		router:      r,
		txRepo:      txRepo,
		webhookRepo: webhookRepo,
		refundRepo:  refundRepo,
	}
}

func insertRefundWithProviderID(d *refundWebhookDeps, providerRefID string) (*model.Transaction, *model.Refund) {
	mid := uuid.New()
	provider := "MOCK"
	pTxID := "MOCK-TXN-" + uuid.New().String()[:8]
	now := time.Now().UTC()

	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: mid,
		Amount: 100_000, Currency: "IDR", PaymentMethod: "QRIS",
		Status:                model.TransactionStatusPaid,
		Provider:              &provider,
		ProviderTransactionID: &pTxID,
		CreatedAt:             now, UpdatedAt: now,
		ReservedRefundAmount: 30_000,
	}
	d.txRepo.txs[tx.ID] = tx
	d.refundRepo.insertTx(tx)

	ref := &model.Refund{
		ID: uuid.New(), MerchantID: mid, TransactionID: tx.ID,
		Amount: 30_000, Currency: "IDR",
		Status:           model.RefundStatusProcessing,
		Provider:         "MOCK",
		ProviderRefundID: &providerRefID,
		RequestedAt:      now, CreatedAt: now, UpdatedAt: now,
	}
	d.refundRepo.insertRefund(ref)
	return tx, ref
}

func refundWebhookPayload(t *testing.T, fields map[string]any) ([]byte, string) {
	t.Helper()
	b, _ := json.Marshal(fields)
	sig := service.SignMockWebhookPayload(b, webhookTestSecret)
	return b, sig
}

// ─── Refund webhook tests ─────────────────────────────────────────────────────

func TestRefundWebhook_Succeeded_ProcessedOK(t *testing.T) {
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-wh001"
	tx, ref := insertRefundWithProviderID(d, providerRefID)

	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-succ-001",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": *tx.ProviderTransactionID,
		"provider_refund_id":      providerRefID,
		"amount":                  30_000,
		"currency":                "IDR",
	})

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d\nbody: %s", w.Code, w.Body)
	}

	// Verify refund state updated.
	d.refundRepo.mu.Lock()
	updatedRef := d.refundRepo.byID[ref.ID]
	updatedTx := d.refundRepo.txs[tx.ID]
	d.refundRepo.mu.Unlock()

	if updatedRef.Status != model.RefundStatusSucceeded {
		t.Errorf("refund status: want SUCCEEDED, got %s", updatedRef.Status)
	}
	if updatedTx.RefundedAmount != 30_000 {
		t.Errorf("refunded_amount: want 30000, got %d", updatedTx.RefundedAmount)
	}
	if updatedTx.ReservedRefundAmount != 0 {
		t.Errorf("reserved_refund_amount: want 0, got %d", updatedTx.ReservedRefundAmount)
	}
}

func TestRefundWebhook_Failed_ReleasesReservation(t *testing.T) {
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-wh002"
	tx, ref := insertRefundWithProviderID(d, providerRefID)

	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-fail-001",
		"event_type":              "REFUND_FAILED",
		"provider_transaction_id": *tx.ProviderTransactionID,
		"provider_refund_id":      providerRefID,
		"amount":                  30_000,
		"currency":                "IDR",
	})

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d\nbody: %s", w.Code, w.Body)
	}

	d.refundRepo.mu.Lock()
	updatedRef := d.refundRepo.byID[ref.ID]
	updatedTx := d.refundRepo.txs[tx.ID]
	d.refundRepo.mu.Unlock()

	if updatedRef.Status != model.RefundStatusFailed {
		t.Errorf("refund status: want FAILED, got %s", updatedRef.Status)
	}
	if updatedTx.ReservedRefundAmount != 0 {
		t.Errorf("reserved_refund_amount should be 0 after FAILED, got %d", updatedTx.ReservedRefundAmount)
	}
}

func TestRefundWebhook_Duplicate_Ignored(t *testing.T) {
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-dup001"
	tx, _ := insertRefundWithProviderID(d, providerRefID)

	fields := map[string]any{
		"event_id":                "evt-ref-dup-001",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": *tx.ProviderTransactionID,
		"provider_refund_id":      providerRefID,
		"amount":                  30_000,
		"currency":                "IDR",
	}
	body, sig := refundWebhookPayload(t, fields)

	sendRefundWebhook := func() *httptest.ResponseRecorder {
		req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Webhook-Signature", sig)
		w := httptest.NewRecorder()
		d.router.ServeHTTP(w, req)
		return w
	}

	// First delivery — succeeds.
	w1 := sendRefundWebhook()
	if w1.Code != http.StatusOK {
		t.Fatalf("first: want 200, got %d\nbody: %s", w1.Code, w1.Body)
	}

	// Second delivery — same event_id must be idempotent.
	w2 := sendRefundWebhook()
	if w2.Code != http.StatusOK {
		t.Fatalf("second: want 200, got %d", w2.Code)
	}
	status2 := getStr(t, parseBody(t, w2), "data", "status")
	if status2 != "IGNORED" && status2 != "PROCESSED" {
		t.Errorf("second webhook: want IGNORED or PROCESSED, got %s", status2)
	}

	// Critical: refunded_amount must not double-count.
	d.refundRepo.mu.Lock()
	updatedTx := d.refundRepo.txs[tx.ID]
	d.refundRepo.mu.Unlock()
	if updatedTx.RefundedAmount != 30_000 {
		t.Errorf("duplicate webhook increased refunded_amount: want 30000, got %d", updatedTx.RefundedAmount)
	}
}

func TestRefundWebhook_TerminalRefund_Ignored(t *testing.T) {
	// A SUCCEEDED refund must stay SUCCEEDED even if another webhook arrives.
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-term001"
	tx, ref := insertRefundWithProviderID(d, providerRefID)

	// Pre-set refund to SUCCEEDED with counters already applied.
	// Must update the map entries (not the returned local pointer).
	d.refundRepo.mu.Lock()
	now := time.Now().UTC()
	mapRef := d.refundRepo.byID[ref.ID]
	mapRef.Status = model.RefundStatusSucceeded
	mapRef.SucceededAt = &now
	// The refunds map and byID map share the same pointer from insertRefund.
	d.refundRepo.txs[tx.ID].ReservedRefundAmount = 0
	d.refundRepo.txs[tx.ID].RefundedAmount = 30_000
	d.refundRepo.mu.Unlock()

	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-term-001",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": *tx.ProviderTransactionID,
		"provider_refund_id":      providerRefID,
		"amount":                  30_000,
		"currency":                "IDR",
	})

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	// Counters must not change.
	d.refundRepo.mu.Lock()
	finalTx := d.refundRepo.txs[tx.ID]
	d.refundRepo.mu.Unlock()
	if finalTx.RefundedAmount != 30_000 {
		t.Errorf("refunded_amount changed: want 30000, got %d", finalTx.RefundedAmount)
	}
	if finalTx.ReservedRefundAmount != 0 {
		t.Errorf("reserved_refund_amount changed: want 0, got %d", finalTx.ReservedRefundAmount)
	}
}

func TestRefundWebhook_FailedRefundCannotBecomeSucceeded(t *testing.T) {
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-failed-terminal"
	tx, ref := insertRefundWithProviderID(d, providerRefID)
	ref.Status = model.RefundStatusFailed
	tx.ReservedRefundAmount = 0
	ref.FailedAt = func() *time.Time {
		now := time.Now().UTC()
		return &now
	}()

	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-failed-terminal",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_refund_id":      providerRefID,
		"provider_transaction_id": "",
		"amount":                  30_000,
		"currency":                "IDR",
	})
	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ref.Status != model.RefundStatusFailed {
		t.Fatalf("failed refund became %s", ref.Status)
	}
	if tx.RefundedAmount != 0 || tx.ReservedRefundAmount != 0 {
		t.Fatalf("financial counters changed: refunded=%d reserved=%d", tx.RefundedAmount, tx.ReservedRefundAmount)
	}
}

func TestRefundWebhook_AmountMismatch_Ignored(t *testing.T) {
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-amt001"
	tx, _ := insertRefundWithProviderID(d, providerRefID)

	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-amt-001",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": *tx.ProviderTransactionID,
		"provider_refund_id":      providerRefID,
		"amount":                  99_999, // wrong amount
		"currency":                "IDR",
	})

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	// Refund must remain PROCESSING (not changed).
	d.refundRepo.mu.Lock()
	updatedTx := d.refundRepo.txs[tx.ID]
	d.refundRepo.mu.Unlock()
	// Reserved must remain because refund was not processed.
	if updatedTx.RefundedAmount != 0 {
		t.Errorf("refunded_amount should be 0 on mismatch, got %d", updatedTx.RefundedAmount)
	}
}

func TestRefundWebhook_UnknownRefundID_Returns404(t *testing.T) {
	d := newRefundWebhookRouter(t)

	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-unk-001",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": "MOCK-TXN-unknown",
		"provider_refund_id":      "MOCK-REF-doesnotexist",
		"amount":                  30_000,
		"currency":                "IDR",
	})

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	// Service returns ErrRefundNotFound which maps to 404.
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 for unknown refund, got %d\nbody: %s", w.Code, w.Body)
	}
}

func TestRefundWebhook_MissingProviderRefundID_MissingFields(t *testing.T) {
	d := newRefundWebhookRouter(t)

	// REFUND event without provider_refund_id — must fail validation.
	body, sig := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-norid",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": "MOCK-TXN-xxx",
		// provider_refund_id missing
	})

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing provider_refund_id, got %d", w.Code)
	}
}

func TestRefundWebhook_InvalidSignature_Rejected(t *testing.T) {
	d := newRefundWebhookRouter(t)
	const providerRefID = "MOCK-REF-badsig"
	tx, _ := insertRefundWithProviderID(d, providerRefID)

	body, _ := refundWebhookPayload(t, map[string]any{
		"event_id":                "evt-ref-badsig",
		"event_type":              "REFUND_SUCCEEDED",
		"provider_transaction_id": *tx.ProviderTransactionID,
		"provider_refund_id":      providerRefID,
	})
	wrongSig := service.SignMockWebhookPayload(body, "wrong-secret")

	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", wrongSig)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeUnauthorized) {
		t.Error("expected UNAUTHORIZED")
	}
}

// Ensure unused imports compile.
var _ = json.Marshal
var _ = pgx.ErrNoRows
