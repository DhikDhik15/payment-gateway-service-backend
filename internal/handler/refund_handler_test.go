package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// ─── Stub RefundService ───────────────────────────────────────────────────────

type stubRefundService struct {
	mu      sync.Mutex
	refunds map[uuid.UUID]*model.RefundResponse

	createErr  error
	createResp *model.RefundResponse
	getErr     error
	listResult *service.ListRefundsResult
	listErr    error
}

func newStubRefundService() *stubRefundService {
	return &stubRefundService{refunds: make(map[uuid.UUID]*model.RefundResponse)}
}

func (s *stubRefundService) CreateRefundWithIdempotency(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ model.CreateRefundRequest, _ string) (*model.RefundResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResp, nil
}

func (s *stubRefundService) GetRefund(_ context.Context, _ uuid.UUID, refundID uuid.UUID) (*model.RefundResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	if r, ok := s.refunds[refundID]; ok {
		return r, nil
	}
	return nil, service.ErrRefundNotFound
}

func (s *stubRefundService) ListRefunds(_ context.Context, _ uuid.UUID, _ uuid.UUID, filter model.RefundListFilter) (*service.ListRefundsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.listResult != nil {
		return s.listResult, nil
	}
	return &service.ListRefundsResult{
		Refunds:    []model.RefundResponse{},
		Total:      0,
		TotalPages: 0,
		Page:       filter.Page,
		Limit:      filter.Limit,
	}, nil
}

func (s *stubRefundService) ListRefundsByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (*service.ListRefundsResult, error) {
	return s.ListRefunds(ctx, merchantID, uuid.Nil, filter)
}

var _ service.RefundService = (*stubRefundService)(nil)

// ─── Router factory ───────────────────────────────────────────────────────────

type refundTestDeps struct {
	router   *gin.Engine
	merchant *model.Merchant
	svc      *stubRefundService
}

func newRefundRouter(t *testing.T) *refundTestDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	merchant := &model.Merchant{
		ID: uuid.New(), Name: "Test Merchant", Code: "TEST001",
		APIKey: "pk_test", Status: model.MerchantStatusActive,
	}
	svc := newStubRefundService()
	h := handler.NewRefundHandler(svc)

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(func(c *gin.Context) {
		c.Set(model.ContextKeyMerchant, merchant)
		c.Next()
	})

	r.POST("/api/v1/payments/:id/refunds", h.CreateRefund)
	r.GET("/api/v1/payments/:id/refunds", h.ListRefunds)
	r.GET("/api/v1/refunds/:id", h.GetRefund)

	return &refundTestDeps{router: r, merchant: merchant, svc: svc}
}

func makeRefundRequest(t *testing.T, method, url string, body any, idempKey string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	if idempKey != "" {
		req.Header.Set("Idempotency-Key", idempKey)
	}
	return req
}

func doRefundRequest(r *gin.Engine, method, url string, body any, idempKey string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, url, nil)
	if body != nil {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req, _ = http.NewRequest(method, url, &buf)
	}
	req.Header.Set("Content-Type", "application/json")
	if idempKey != "" {
		req.Header.Set("Idempotency-Key", idempKey)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sampleRefundResp(txID uuid.UUID) *model.RefundResponse {
	now := time.Now().UTC()
	refundID := uuid.New()
	return &model.RefundResponse{
		RefundID:          refundID,
		TransactionID:     txID,
		Amount:            30_000,
		Currency:          "IDR",
		Status:            model.RefundStatusSucceeded,
		Provider:          "MOCK",
		TransactionAmount: 100_000,
		RefundedAmount:    30_000,
		RefundableAmount:  70_000,
		RequestedAt:       now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// ─── POST /api/v1/payments/:id/refunds ───────────────────────────────────────

func TestRefundHandler_Create_Success(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createResp = sampleRefundResp(txID)
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 30000, "currency": "IDR"}, "idemp-key-1")

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	if getStr(t, body, "data", "status") != "SUCCEEDED" {
		t.Errorf("want SUCCEEDED, got %s", getStr(t, body, "data", "status"))
	}
	if getStr(t, body, "meta", "request_id") == "" {
		t.Error("expected non-empty request_id")
	}
}

func TestRefundHandler_Create_MissingIdempotencyKey(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 30000, "currency": "IDR"}, "") // no key

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidRequest) {
		t.Error("expected INVALID_REQUEST")
	}
}

func TestRefundHandler_Create_InvalidUUID(t *testing.T) {
	d := newRefundRouter(t)
	w := doRefundRequest(d.router, "POST", "/api/v1/payments/not-a-uuid/refunds",
		map[string]any{"amount": 30000, "currency": "IDR"}, "key-1")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRefundHandler_Create_MissingBody(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds", nil, "key-1")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRefundHandler_Create_TransactionNotPaid(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrRefundTransactionNotPaid
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, "key-notpaid")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeRefundTransactionNotPaid) {
		t.Error("expected REFUND_TRANSACTION_NOT_PAID")
	}
}

func TestRefundHandler_Create_AmountExceeded(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrRefundAmountExceeded
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 999999, "currency": "IDR"}, "key-exceeded")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeRefundAmountExceeded) {
		t.Error("expected REFUND_AMOUNT_EXCEEDED")
	}
}

func TestRefundHandler_Create_CurrencyMismatch(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrRefundCurrencyMismatch
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "USD"}, "key-cur")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeRefundCurrencyMismatch) {
		t.Error("expected REFUND_CURRENCY_MISMATCH")
	}
}

func TestRefundHandler_Create_IdempotencyKeyReused(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrIdempotencyKeyReused
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, "reused-key")

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeIdempotencyKeyReused) {
		t.Error("expected IDEMPOTENCY_KEY_REUSED")
	}
}

func TestRefundHandler_Create_IdempotencyInProgress(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrIdempotencyInProgress
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, "inprogress-key")

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeIdempotencyInProgress) {
		t.Error("expected IDEMPOTENCY_REQUEST_IN_PROGRESS")
	}
}

func TestRefundHandler_Create_TransactionNotFound(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = repository.ErrTransactionNotFound
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, "key-notx")

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeTransactionNotFound) {
		t.Error("expected TRANSACTION_NOT_FOUND")
	}
}

func TestRefundHandler_Create_ProviderError(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrProviderFailure
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, "key-prov-err")

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeRefundProviderError) {
		t.Error("expected REFUND_PROVIDER_ERROR")
	}
}

func TestRefundHandler_Create_ProviderTimeout(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	d.svc.mu.Lock()
	d.svc.createErr = service.ErrProviderTimeout
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, "key-prov-to")

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("want 504, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeRefundProviderTimeout) {
		t.Error("expected REFUND_PROVIDER_TIMEOUT")
	}
}

// ─── GET /api/v1/refunds/:id ──────────────────────────────────────────────────

func TestRefundHandler_Get_Success(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	resp := sampleRefundResp(txID)

	d.svc.mu.Lock()
	d.svc.refunds[resp.RefundID] = resp
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "GET", "/api/v1/refunds/"+resp.RefundID.String(), nil, "")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "data", "status") != "SUCCEEDED" {
		t.Error("expected SUCCEEDED")
	}
}

func TestRefundHandler_Get_NotFound(t *testing.T) {
	d := newRefundRouter(t)
	w := doRefundRequest(d.router, "GET", "/api/v1/refunds/"+uuid.New().String(), nil, "")

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeRefundNotFound) {
		t.Error("expected REFUND_NOT_FOUND")
	}
}

func TestRefundHandler_Get_InvalidUUID(t *testing.T) {
	d := newRefundRouter(t)
	w := doRefundRequest(d.router, "GET", "/api/v1/refunds/not-a-uuid", nil, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRefundHandler_Get_CrossMerchant_404(t *testing.T) {
	// Merchant A creates a refund.
	dA := newRefundRouter(t)
	txID := uuid.New()
	resp := sampleRefundResp(txID)
	dA.svc.mu.Lock()
	dA.svc.refunds[resp.RefundID] = resp
	dA.svc.mu.Unlock()

	// Merchant B uses a different service stub that always returns NotFound.
	dB := newRefundRouter(t)
	// dB has empty refunds map → returns ErrRefundNotFound for any ID.

	w := doRefundRequest(dB.router, "GET", "/api/v1/refunds/"+resp.RefundID.String(), nil, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-merchant: want 404, got %d", w.Code)
	}
}

// ─── GET /api/v1/payments/:id/refunds ────────────────────────────────────────

func TestRefundHandler_List_Success(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	refundID1 := uuid.New()
	refundID2 := uuid.New()

	d.svc.mu.Lock()
	d.svc.listResult = &service.ListRefundsResult{
		Refunds: []model.RefundResponse{
			{RefundID: refundID1, TransactionID: txID, Amount: 20_000, Currency: "IDR",
				Status: model.RefundStatusSucceeded, Provider: "MOCK",
				RequestedAt: now, CreatedAt: now, UpdatedAt: now},
			{RefundID: refundID2, TransactionID: txID, Amount: 30_000, Currency: "IDR",
				Status: model.RefundStatusPending, Provider: "MOCK",
				RequestedAt: now, CreatedAt: now, UpdatedAt: now},
		},
		Total: 2, TotalPages: 1, Page: 1, Limit: 20,
	}
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "GET", "/api/v1/payments/"+txID.String()+"/refunds", nil, "")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	data, _ := body["data"].([]any)
	if len(data) != 2 {
		t.Errorf("want 2 refunds, got %d", len(data))
	}
	meta := body["meta"].(map[string]any)
	if int(meta["total"].(float64)) != 2 {
		t.Errorf("meta.total: want 2, got %v", meta["total"])
	}
}

func TestRefundHandler_List_Empty(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()

	w := doRefundRequest(d.router, "GET", "/api/v1/payments/"+txID.String()+"/refunds", nil, "")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := parseBody(t, w)
	data, _ := body["data"].([]any)
	if len(data) != 0 {
		t.Errorf("want empty array, got %d items", len(data))
	}
}

func TestRefundHandler_List_TransactionNotFound(t *testing.T) {
	d := newRefundRouter(t)
	d.svc.mu.Lock()
	d.svc.listErr = repository.ErrTransactionNotFound
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "GET", "/api/v1/payments/"+uuid.New().String()+"/refunds", nil, "")

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestRefundHandler_List_InvalidUUID(t *testing.T) {
	d := newRefundRouter(t)
	w := doRefundRequest(d.router, "GET", "/api/v1/payments/not-a-uuid/refunds", nil, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRefundHandler_List_PaginationMeta(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()

	d.svc.mu.Lock()
	d.svc.listResult = &service.ListRefundsResult{
		Refunds: []model.RefundResponse{
			{RefundID: uuid.New(), TransactionID: txID, Amount: 10_000, Currency: "IDR",
				Status: model.RefundStatusSucceeded, Provider: "MOCK",
				RequestedAt: now, CreatedAt: now, UpdatedAt: now},
		},
		Total: 15, TotalPages: 2, Page: 1, Limit: 10,
	}
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "GET", "/api/v1/payments/"+txID.String()+"/refunds?page=1&limit=10", nil, "")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	meta := parseBody(t, w)["meta"].(map[string]any)
	if int(meta["total"].(float64)) != 15 {
		t.Errorf("meta.total: want 15, got %v", meta["total"])
	}
	if int(meta["total_pages"].(float64)) != 2 {
		t.Errorf("meta.total_pages: want 2, got %v", meta["total_pages"])
	}
}

// ─── Refund response fields ───────────────────────────────────────────────────

func TestRefundHandler_Response_FinancialFields(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	refundID := uuid.New()
	providerRefID := "MOCK-REF-abc123"

	d.svc.mu.Lock()
	d.svc.createResp = &model.RefundResponse{
		RefundID: refundID, TransactionID: txID,
		Amount: 30_000, Currency: "IDR",
		Status: model.RefundStatusSucceeded, Provider: "MOCK",
		ProviderRefundID:  &providerRefID,
		TransactionAmount: 100_000,
		RefundedAmount:    30_000,
		ReservedAmount:    0,
		RefundableAmount:  70_000,
		RequestedAt:       now, CreatedAt: now, UpdatedAt: now,
	}
	d.svc.mu.Unlock()

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 30000, "currency": "IDR"}, "key-fields")

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	data := body["data"].(map[string]any)

	checks := map[string]any{
		"amount":             float64(30_000),
		"transaction_amount": float64(100_000),
		"refunded_amount":    float64(30_000),
		"refundable_amount":  float64(70_000),
		"currency":           "IDR",
		"provider":           "MOCK",
		"provider_refund_id": "MOCK-REF-abc123",
	}
	for field, want := range checks {
		got := data[field]
		if got != want {
			t.Errorf("field %s: want %v, got %v", field, want, got)
		}
	}
	// Secret fields must not appear.
	for _, secret := range []string{"failure_code", "idempotency_key_id"} {
		if _, exists := data[secret]; exists {
			t.Errorf("secret field %q must not appear in response", secret)
		}
	}
}

// ─── Idempotency-Key header size validation ───────────────────────────────────

func TestRefundHandler_Create_IdempotencyKeyTooLong(t *testing.T) {
	d := newRefundRouter(t)
	txID := uuid.New()
	longKey := string(make([]byte, 256)) // 256 chars — over limit

	w := doRefundRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/refunds",
		map[string]any{"amount": 10000, "currency": "IDR"}, longKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for oversized key, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidRequest) {
		t.Error("expected INVALID_REQUEST")
	}
}

// ─── Unused import guard ──────────────────────────────────────────────────────

var (
	_ = errors.New
	_ = pgx.ErrNoRows // used in memRefundRepo
)
