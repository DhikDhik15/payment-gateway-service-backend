package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
)

// ─── In-memory WebhookEventRepository (for handler tests) ────────────────────

type memWebhookRepoH struct {
	events map[string]*model.WebhookEvent
	byID   map[uuid.UUID]*model.WebhookEvent
}

func newMemWebhookRepoH() *memWebhookRepoH {
	return &memWebhookRepoH{
		events: make(map[string]*model.WebhookEvent),
		byID:   make(map[uuid.UUID]*model.WebhookEvent),
	}
}

func (r *memWebhookRepoH) Create(_ context.Context, ev *model.WebhookEvent) error {
	key := ev.Provider + ":" + ev.EventID
	if _, exists := r.events[key]; exists {
		return repository.ErrWebhookEventDuplicate
	}
	r.events[key] = ev
	r.byID[ev.ID] = ev
	return nil
}

func (r *memWebhookRepoH) FindByProviderAndEventID(_ context.Context, provider, eventID string) (*model.WebhookEvent, error) {
	key := provider + ":" + eventID
	ev, ok := r.events[key]
	if !ok {
		return nil, repository.ErrWebhookEventNotFound
	}
	return ev, nil
}

func (r *memWebhookRepoH) UpdateStatus(_ context.Context, id uuid.UUID, status model.WebhookEventStatus, errMsg *string) error {
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

var _ repository.WebhookEventRepository = (*memWebhookRepoH)(nil)

// ─── Helper: build webhook test router ───────────────────────────────────────

const webhookTestSecret = "webhook-test-secret"

type webhookTestDeps struct {
	router      *gin.Engine
	txRepo      *memTxRepo
	webhookRepo *memWebhookRepoH
}

func newWebhookRouter(t *testing.T) *webhookTestDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	txRepo := newMemTxRepo()
	webhookRepo := newMemWebhookRepoH()

	parser := service.NewMockWebhookParser(webhookTestSecret)
	svc := service.NewWebhookService(txRepo, webhookRepo, parser)
	h := handler.NewWebhookHandler(svc)

	r := gin.New()
	r.Use(middleware.RequestID())
	r.POST("/api/v1/webhooks/providers/:provider", h.Receive)

	return &webhookTestDeps{router: r, txRepo: txRepo, webhookRepo: webhookRepo}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func signedWebhookRequest(t *testing.T, router *gin.Engine, provider string, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/"+provider, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Webhook-Signature", sig)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func webhookPayload(t *testing.T, fields map[string]any) ([]byte, string) {
	t.Helper()
	b, _ := json.Marshal(fields)
	sig := service.SignMockWebhookPayload(b, webhookTestSecret)
	return b, sig
}

// insertPendingTxH inserts a PENDING transaction into the handler test's memTxRepo.
func insertPendingTxH(r *memTxRepo, providerTxID string) *model.Transaction {
	provider := "MOCK"
	expiry := time.Now().UTC().Add(30 * time.Minute)
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: uuid.New(),
		MerchantOrderID: "ORDER-WH-" + uuid.New().String()[:8],
		Provider:        &provider, ProviderTransactionID: &providerTxID,
		Status: model.TransactionStatusPending, ExpiredAt: &expiry,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	r.txs[tx.ID] = tx
	return tx
}

// ─── Webhook handler tests ────────────────────────────────────────────────────

func TestWebhookHandler_ValidPAID(t *testing.T) {
	d := newWebhookRouter(t)
	tx := insertPendingTxH(d.txRepo, "MOCK-TXN-wh001")

	body, sig := webhookPayload(t, map[string]any{
		"event_id":                "evt-wh-paid-001",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-wh001",
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "PAID",
		"amount":                  50000,
		"currency":                "IDR",
	})
	w := signedWebhookRequest(t, d.router, "mock", body, sig)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	resp := parseBody(t, w)
	if getStr(t, resp, "data", "status") != "PROCESSED" {
		t.Errorf("expected PROCESSED, got %s", getStr(t, resp, "data", "status"))
	}
	if getStr(t, resp, "meta", "request_id") == "" {
		t.Error("expected non-empty request_id in meta")
	}
}

func TestWebhookHandler_ValidFAILED(t *testing.T) {
	d := newWebhookRouter(t)
	insertPendingTxH(d.txRepo, "MOCK-TXN-wh002")

	body, sig := webhookPayload(t, map[string]any{
		"event_id":                "evt-wh-fail-001",
		"event_type":              "PAYMENT_FAILED",
		"provider_transaction_id": "MOCK-TXN-wh002",
		"status":                  "FAILED",
		"amount":                  50000,
		"currency":                "IDR",
	})
	w := signedWebhookRequest(t, d.router, "mock", body, sig)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "data", "status") != "PROCESSED" {
		t.Error("expected PROCESSED")
	}
}

func TestWebhookHandler_InvalidSignature(t *testing.T) {
	d := newWebhookRouter(t)
	body, _ := webhookPayload(t, map[string]any{
		"event_id": "evt-badsig", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})
	w := signedWebhookRequest(t, d.router, "mock", body, "badhexsig00000000000000000000000000000000000000000000000000000000")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeUnauthorized) {
		t.Error("expected UNAUTHORIZED")
	}
}

func TestWebhookHandler_MissingSignature(t *testing.T) {
	d := newWebhookRouter(t)
	body, _ := webhookPayload(t, map[string]any{
		"event_id": "evt-nosig", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})
	w := signedWebhookRequest(t, d.router, "mock", body, "") // no sig header

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestWebhookHandler_MalformedJSON(t *testing.T) {
	d := newWebhookRouter(t)
	badBody := []byte(`{not valid json`)
	sig := service.SignMockWebhookPayload(badBody, webhookTestSecret)
	w := signedWebhookRequest(t, d.router, "mock", badBody, sig)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidRequest) {
		t.Error("expected INVALID_REQUEST")
	}
}

func TestWebhookHandler_UnknownProvider(t *testing.T) {
	d := newWebhookRouter(t)
	body, sig := webhookPayload(t, map[string]any{
		"event_id": "evt-unk", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "X-123",
	})
	w := signedWebhookRequest(t, d.router, "unknown_provider", body, sig)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidRequest) {
		t.Error("expected INVALID_REQUEST")
	}
}

func TestWebhookHandler_TransactionNotFound(t *testing.T) {
	d := newWebhookRouter(t) // no transactions in repo
	body, sig := webhookPayload(t, map[string]any{
		"event_id":                "evt-notx",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-doesnotexist",
	})
	w := signedWebhookRequest(t, d.router, "mock", body, sig)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeTransactionNotFound) {
		t.Error("expected TRANSACTION_NOT_FOUND")
	}
}

func TestWebhookHandler_DuplicateEvent_ReturnsIgnored(t *testing.T) {
	d := newWebhookRouter(t)
	tx := insertPendingTxH(d.txRepo, "MOCK-TXN-dup-wh")

	fields := map[string]any{
		"event_id":                "evt-dup-wh-001",
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-dup-wh",
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  "PAID",
		"amount":                  50000,
		"currency":                "IDR",
	}
	body, sig := webhookPayload(t, fields)

	// First call.
	w1 := signedWebhookRequest(t, d.router, "mock", body, sig)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d\nbody: %s", w1.Code, w1.Body)
	}
	if getStr(t, parseBody(t, w1), "data", "status") != "PROCESSED" {
		t.Error("first call: expected PROCESSED")
	}

	// Second call — same event_id → IGNORED (not an error).
	w2 := signedWebhookRequest(t, d.router, "mock", body, sig)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call: expected 200, got %d\nbody: %s", w2.Code, w2.Body)
	}
	if getStr(t, parseBody(t, w2), "data", "status") != "IGNORED" {
		t.Errorf("second call: expected IGNORED, got %s", getStr(t, parseBody(t, w2), "data", "status"))
	}
}

func TestWebhookHandler_RequestIDPropagation(t *testing.T) {
	const clientID = "req-webhook-test-123"
	d := newWebhookRouter(t)

	body, sig := webhookPayload(t, map[string]any{
		"event_id": "evt-reqid", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-reqid",
	})
	req, _ := http.NewRequest("POST", "/api/v1/webhooks/providers/mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	req.Header.Set("X-Request-ID", clientID)

	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	// Status is 404 (no matching tx) but request_id must be propagated regardless.
	gotHeader := w.Header().Get("X-Request-ID")
	if gotHeader != clientID {
		t.Errorf("X-Request-ID header: got %q, want %q", gotHeader, clientID)
	}
	resp := parseBody(t, w)
	gotMeta := getStr(t, resp, "meta", "request_id")
	if gotMeta != clientID {
		t.Errorf("meta.request_id: got %q, want %q", gotMeta, clientID)
	}
}

func TestWebhookHandler_MissingRequiredFields(t *testing.T) {
	d := newWebhookRouter(t)
	// event_type present but missing event_id
	body, sig := webhookPayload(t, map[string]any{
		"event_type":              "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
		// event_id missing
	})
	w := signedWebhookRequest(t, d.router, "mock", body, sig)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	code := getStr(t, parseBody(t, w), "error", "code")
	if code != string(response.CodeValidationError) {
		t.Errorf("expected VALIDATION_ERROR, got %s", code)
	}
}

func TestWebhookHandler_ValidSignatureButWrongSecret(t *testing.T) {
	// Sign with a different key than what the parser uses.
	d := newWebhookRouter(t)
	body, _ := webhookPayload(t, map[string]any{
		"event_id": "evt-wrongkey", "event_type": "PAYMENT_PAID",
		"provider_transaction_id": "MOCK-TXN-xxx",
	})
	wrongSig := service.SignMockWebhookPayload(body, "completely-wrong-secret")
	w := signedWebhookRequest(t, d.router, "mock", body, wrongSig)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
