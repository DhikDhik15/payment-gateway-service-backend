package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// ─── Stub MerchantService (for auth-backed router) ────────────────────────────

// stubMerchantSvc implements service.MerchantService for handler auth tests.
type stubMerchantSvc struct {
	merchant      *model.Merchant
	internalError bool
}

func (s *stubMerchantSvc) CreateMerchant(_ context.Context, _ model.CreateMerchantRequest) (*model.CreateMerchantResponse, error) {
	panic("not used in handler auth tests")
}
func (s *stubMerchantSvc) GetMerchant(_ context.Context, _ uuid.UUID) (*model.GetMerchantResponse, error) {
	panic("not used in handler auth tests")
}
func (s *stubMerchantSvc) GetMerchantByAPIKey(_ context.Context, apiKey string) (*model.Merchant, error) {
	if s.internalError {
		return nil, errors.New("database error")
	}
	if s.merchant != nil && s.merchant.APIKey == apiKey {
		return s.merchant, nil
	}
	return nil, repository.ErrMerchantNotFound
}

func (s *stubMerchantSvc) UpdateMerchantStatus(_ context.Context, _ uuid.UUID, _ model.MerchantStatus) (*model.GetMerchantResponse, error) {
	return nil, nil
}

// compile-time interface check
var _ service.MerchantService = (*stubMerchantSvc)(nil)

// ─── In-memory repos (duplicated from service tests; keeps handler tests self-contained) ──

type memTxRepo struct {
	txs map[uuid.UUID]*model.Transaction
}

func newMemTxRepo() *memTxRepo { return &memTxRepo{txs: make(map[uuid.UUID]*model.Transaction)} }

func (r *memTxRepo) Create(_ context.Context, tx *model.Transaction) error {
	r.txs[tx.ID] = tx
	return nil
}
func (r *memTxRepo) FindByID(_ context.Context, id uuid.UUID) (*model.Transaction, error) {
	tx, ok := r.txs[id]
	if !ok {
		return nil, repository.ErrTransactionNotFound
	}
	return tx, nil
}
func (r *memTxRepo) FindByMerchantAndID(_ context.Context, mid, id uuid.UUID) (*model.Transaction, error) {
	tx, ok := r.txs[id]
	if !ok || tx.MerchantID != mid {
		return nil, repository.ErrTransactionNotFound
	}
	return tx, nil
}
func (r *memTxRepo) FindByMerchantOrderID(_ context.Context, mid uuid.UUID, oid string) (*model.Transaction, error) {
	for _, tx := range r.txs {
		if tx.MerchantID == mid && tx.MerchantOrderID == oid {
			return tx, nil
		}
	}
	return nil, repository.ErrTransactionNotFound
}
func (r *memTxRepo) UpdateStatus(_ context.Context, id uuid.UUID, from, to model.TransactionStatus) error {
	tx, ok := r.txs[id]
	if !ok || tx.Status != from {
		return repository.ErrTransactionNotFound
	}
	tx.Status = to
	tx.UpdatedAt = time.Now().UTC()
	return nil
}
func (r *memTxRepo) UpdateStatusWithProvider(_ context.Context, id uuid.UUID, from, to model.TransactionStatus, p, providerTransactionID, paymentURL string, expiredAt *time.Time) error {
	tx, ok := r.txs[id]
	if !ok || tx.Status != from {
		return repository.ErrTransactionNotFound
	}
	tx.Status = to
	tx.Provider = &p
	tx.ProviderTransactionID = &providerTransactionID
	tx.PaymentURL = &paymentURL
	if expiredAt != nil {
		tx.ExpiredAt = expiredAt
	}
	tx.UpdatedAt = time.Now().UTC()
	return nil
}
func (r *memTxRepo) FindByProviderTransactionID(_ context.Context, provider, providerTxID string) (*model.Transaction, error) {
	for _, tx := range r.txs {
		if tx.Provider != nil && *tx.Provider == provider &&
			tx.ProviderTransactionID != nil && *tx.ProviderTransactionID == providerTxID {
			return tx, nil
		}
	}
	return nil, repository.ErrTransactionNotFound
}
func (r *memTxRepo) FindExpiredPendingTransactions(_ context.Context, limit int) ([]*model.Transaction, error) {
	now := time.Now().UTC()
	var out []*model.Transaction
	for _, tx := range r.txs {
		if tx.Status == model.TransactionStatusPending &&
			tx.ExpiredAt != nil && tx.ExpiredAt.Before(now) {
			out = append(out, tx)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
func (r *memTxRepo) UpdateStatusWithPaidAt(_ context.Context, id uuid.UUID, from model.TransactionStatus) error {
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

// List and CountList are no-op stubs. Tests that exercise listing use
// listAwareTxRepo (defined in payment_list_handler_test.go) instead.
func (r *memTxRepo) List(_ context.Context, _ uuid.UUID, _ model.TransactionListFilter) ([]*model.Transaction, error) {
	return nil, nil
}
func (r *memTxRepo) CountList(_ context.Context, _ uuid.UUID, _ model.TransactionListFilter) (int64, error) {
	return 0, nil
}

type memAttemptRepo struct{ attempts []model.PaymentAttempt }

func newMemAttemptRepo() *memAttemptRepo { return &memAttemptRepo{} }
func (r *memAttemptRepo) Create(_ context.Context, a *model.PaymentAttempt) error {
	r.attempts = append(r.attempts, *a)
	return nil
}
func (r *memAttemptRepo) FindByTransactionID(_ context.Context, txID uuid.UUID) ([]model.PaymentAttempt, error) {
	var out []model.PaymentAttempt
	for _, a := range r.attempts {
		if a.TransactionID == txID {
			out = append(out, a)
		}
	}
	return out, nil
}
func (r *memAttemptRepo) CountByTransactionID(_ context.Context, txID uuid.UUID) (int, error) {
	n := 0
	for _, a := range r.attempts {
		if a.TransactionID == txID {
			n++
		}
	}
	return n, nil
}

// ─── Router factory ───────────────────────────────────────────────────────────

type testDeps struct {
	router      *gin.Engine
	merchant    *model.Merchant
	txRepo      *memTxRepo
	attemptRepo *memAttemptRepo
	provider    *service.MockPaymentProvider
}

func newTestRouter(t *testing.T, providerOpts ...func(*service.MockPaymentProvider)) *testDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prov := service.NewMockPaymentProvider()
	for _, opt := range providerOpts {
		opt(prov)
	}

	txRepo := newMemTxRepo()
	attemptRepo := newMemAttemptRepo()
	svc := service.NewPaymentService(txRepo, attemptRepo, prov)
	h := handler.NewPaymentHandler(svc)

	merchant := &model.Merchant{
		ID:     uuid.New(),
		Name:   "Test Merchant",
		Code:   "TEST001",
		APIKey: "pk_test",
		Status: model.MerchantStatusActive,
	}

	r := gin.New()
	r.Use(middleware.RequestID())

	// Inject merchant into context — simulates Auth middleware passing.
	r.Use(func(c *gin.Context) {
		c.Set(model.ContextKeyMerchant, merchant)
		c.Next()
	})

	r.POST("/api/v1/payments", h.Create)
	r.GET("/api/v1/payments/:id", h.GetByID)
	r.POST("/api/v1/payments/:id/cancel", h.Cancel)

	return &testDeps{
		router:      r,
		merchant:    merchant,
		txRepo:      txRepo,
		attemptRepo: attemptRepo,
		provider:    prov,
	}
}

// newTestRouterWithAuth builds a router that uses REAL Auth middleware backed by
// the given merchant service stub. No merchant is pre-injected into the context —
// authentication must succeed via the X-API-Key header.
func newTestRouterWithAuth(t *testing.T, merchantSvc service.MerchantService, providerOpts ...func(*service.MockPaymentProvider)) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prov := service.NewMockPaymentProvider()
	for _, opt := range providerOpts {
		opt(prov)
	}

	txRepo := newMemTxRepo()
	attemptRepo := newMemAttemptRepo()
	svc := service.NewPaymentService(txRepo, attemptRepo, prov)
	h := handler.NewPaymentHandler(svc)

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.Auth(merchantSvc, noopAPIKeySvc(), true))

	r.POST("/api/v1/payments", h.Create)
	r.GET("/api/v1/payments/:id", h.GetByID)
	r.POST("/api/v1/payments/:id/cancel", h.Cancel)

	return r
}

// noopAPIKeySvc returns a MerchantAPIKeyService that always rejects new-style
// keys, forcing Auth to use the legacy merchants.api_key path in tests.
func noopAPIKeySvc() service.MerchantAPIKeyService {
	return &stubAPIKeySvc{authErr: service.ErrAPIKeyNotFound}
}

// stubAPIKeySvc is a minimal MerchantAPIKeyService for Auth-backed handler tests.
type stubAPIKeySvc struct {
	merchant *model.Merchant
	authErr  error
}

func (s *stubAPIKeySvc) CreateKey(_ context.Context, _ uuid.UUID, _ model.CreateMerchantAPIKeyRequest) (*model.CreateMerchantAPIKeyResponse, error) {
	panic("not used")
}
func (s *stubAPIKeySvc) ListKeys(_ context.Context, _ uuid.UUID) ([]model.MerchantAPIKeyResponse, error) {
	panic("not used")
}
func (s *stubAPIKeySvc) RevokeKey(_ context.Context, _, _ uuid.UUID) error { panic("not used") }
func (s *stubAPIKeySvc) RotateKey(_ context.Context, _, _ uuid.UUID) (*model.RotateMerchantAPIKeyResponse, error) {
	panic("not used")
}
func (s *stubAPIKeySvc) AuthenticateByAPIKey(_ context.Context, _ string) (*model.Merchant, error) {
	if s.authErr != nil {
		return nil, s.authErr
	}
	return s.merchant, nil
}

var _ service.MerchantAPIKeyService = (*stubAPIKeySvc)(nil)

// ─── request helpers ──────────────────────────────────────────────────────────

func doRequest(r *gin.Engine, method, url string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func parseBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("parse body: %v\nbody: %s", err, w.Body.String())
	}
	return m
}

func getStr(t *testing.T, m map[string]any, keys ...string) string {
	t.Helper()
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("getStr: expected map at key %q, got %T", k, cur)
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// ─── POST /api/v1/payments ────────────────────────────────────────────────────

func TestCreatePaymentHandler_Success(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-001",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)
	if getStr(t, body, "data", "status") != "PENDING" {
		t.Errorf("expected status PENDING, got %s", getStr(t, body, "data", "status"))
	}
	if getStr(t, body, "data", "provider") != "MOCK" {
		t.Error("expected provider MOCK")
	}
	if getStr(t, body, "meta", "request_id") == "" {
		t.Error("expected non-empty request_id in meta")
	}
	if getStr(t, body, "data", "provider_transaction_id") == "" {
		t.Error("expected non-empty provider_transaction_id")
	}
}

func TestCreatePaymentHandler_ValidationError_MissingFields(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"amount": 50000,
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	body := parseBody(t, w)
	if getStr(t, body, "error", "code") != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR, got %s", getStr(t, body, "error", "code"))
	}
}

func TestCreatePaymentHandler_InvalidCurrency(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-CUR",
		"amount":            50000,
		"currency":          "EUR",
		"payment_method":    "QRIS",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidCurrency) {
		t.Error("expected INVALID_CURRENCY")
	}
}

func TestCreatePaymentHandler_InvalidPaymentMethod(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-PM",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "GOPAY",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidPaymentMethod) {
		t.Error("expected INVALID_PAYMENT_METHOD")
	}
}

func TestCreatePaymentHandler_DuplicateOrder(t *testing.T) {
	d := newTestRouter(t)
	req := map[string]any{
		"merchant_order_id": "ORDER-H-DUP",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}
	doRequest(d.router, "POST", "/api/v1/payments", req)

	w := doRequest(d.router, "POST", "/api/v1/payments", req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeDuplicateOrder) {
		t.Error("expected DUPLICATE_ORDER")
	}
}

func TestCreatePaymentHandler_ProviderFailure(t *testing.T) {
	d := newTestRouter(t, func(p *service.MockPaymentProvider) {
		p.ShouldFailCreate = true
	})
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-PF",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodePaymentProviderError) {
		t.Error("expected PAYMENT_PROVIDER_ERROR")
	}
}

func TestCreatePaymentHandler_ProviderTimeout(t *testing.T) {
	d := newTestRouter(t, func(p *service.MockPaymentProvider) {
		p.ShouldTimeout = true
	})
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-TO",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodePaymentProviderTimeout) {
		t.Error("expected PAYMENT_PROVIDER_TIMEOUT")
	}
}

// ─── GET /api/v1/payments/:id ─────────────────────────────────────────────────

func TestGetPaymentHandler_Success(t *testing.T) {
	d := newTestRouter(t)

	created := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-GET",
		"amount":            75000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	txID := getStr(t, parseBody(t, created), "data", "transaction_id")

	w := doRequest(d.router, "GET", "/api/v1/payments/"+txID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	if getStr(t, body, "data", "transaction_id") != txID {
		t.Error("transaction_id mismatch")
	}
}

func TestGetPaymentHandler_NotFound(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "GET", "/api/v1/payments/"+uuid.New().String(), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeTransactionNotFound) {
		t.Error("expected TRANSACTION_NOT_FOUND")
	}
}

func TestGetPaymentHandler_InvalidUUID(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "GET", "/api/v1/payments/not-a-uuid", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetPaymentHandler_CrossMerchantIsolation(t *testing.T) {
	// Merchant A creates a payment.
	dA := newTestRouter(t)
	created := doRequest(dA.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-ISO",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	txID := getStr(t, parseBody(t, created), "data", "transaction_id")

	// Merchant B (different router/merchant context) tries to GET it.
	gin.SetMode(gin.TestMode)
	dB_merchant := &model.Merchant{
		ID:   uuid.New(), // different ID
		Name: "Merchant B", Code: "TESTB",
		APIKey: "pk_b", Status: model.MerchantStatusActive,
	}
	// Use dA's repos but inject merchant B into context.
	svc := service.NewPaymentService(dA.txRepo, dA.attemptRepo, service.NewMockPaymentProvider())
	hB := handler.NewPaymentHandler(svc)
	rB := gin.New()
	rB.Use(middleware.RequestID())
	rB.Use(func(c *gin.Context) {
		c.Set(model.ContextKeyMerchant, dB_merchant)
		c.Next()
	})
	rB.GET("/api/v1/payments/:id", hB.GetByID)

	req, _ := http.NewRequest("GET", "/api/v1/payments/"+txID, nil)
	w := httptest.NewRecorder()
	rB.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-merchant: expected 404, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeTransactionNotFound) {
		t.Error("expected TRANSACTION_NOT_FOUND (not 403)")
	}
}

// ─── POST /api/v1/payments/:id/cancel ────────────────────────────────────────

func TestCancelPaymentHandler_Success(t *testing.T) {
	d := newTestRouter(t)

	created := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-CAN",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	txID := getStr(t, parseBody(t, created), "data", "transaction_id")

	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID+"/cancel", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "data", "status") != "CANCELLED" {
		t.Error("expected status CANCELLED")
	}
}

func TestCancelPaymentHandler_AlreadyCancelled(t *testing.T) {
	d := newTestRouter(t)

	created := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-CAN2",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	txID := getStr(t, parseBody(t, created), "data", "transaction_id")

	// First cancel succeeds.
	doRequest(d.router, "POST", "/api/v1/payments/"+txID+"/cancel", nil)

	// Second cancel must fail.
	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID+"/cancel", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidTransactionState) {
		t.Error("expected INVALID_TRANSACTION_STATE")
	}
}

func TestCancelPaymentHandler_NotFound(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments/"+uuid.New().String()+"/cancel", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestCancelPaymentHandler_InvalidUUID(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments/not-a-uuid/cancel", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCancelPaymentHandler_TerminalState_PAID(t *testing.T) {
	d := newTestRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	d.txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: d.merchant.ID, MerchantOrderID: "ORDER-H-PAID",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusPaid, CreatedAt: now, UpdatedAt: now,
	}
	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/cancel", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestCancelPaymentHandler_ProviderFailure(t *testing.T) {
	// Create payment with working provider.
	dCreate := newTestRouter(t)
	created := doRequest(dCreate.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-PCF",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	txID := getStr(t, parseBody(t, created), "data", "transaction_id")

	// Cancel with a provider that fails.
	failProv := &service.MockPaymentProvider{ShouldFailCancel: true}
	svcFail := service.NewPaymentService(dCreate.txRepo, dCreate.attemptRepo, failProv)
	hFail := handler.NewPaymentHandler(svcFail)
	rFail := gin.New()
	rFail.Use(middleware.RequestID())
	rFail.Use(func(c *gin.Context) {
		c.Set(model.ContextKeyMerchant, dCreate.merchant)
		c.Next()
	})
	rFail.POST("/api/v1/payments/:id/cancel", hFail.Cancel)

	req, _ := http.NewRequest("POST", "/api/v1/payments/"+txID+"/cancel", nil)
	w := httptest.NewRecorder()
	rFail.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodePaymentProviderError) {
		t.Error("expected PAYMENT_PROVIDER_ERROR")
	}
}

// ─── Auth middleware integration tests ───────────────────────────────────────

// TestCreatePaymentHandler_MissingAPIKey verifies that POST /payments without
// an X-API-Key header is rejected with 401 INVALID_API_KEY.
func TestCreatePaymentHandler_MissingAPIKey(t *testing.T) {
	r := newTestRouterWithAuth(t, &stubMerchantSvc{})

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"merchant_order_id": "ORDER-AUTH-1",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	req, _ := http.NewRequest("POST", "/api/v1/payments", &buf)
	req.Header.Set("Content-Type", "application/json")
	// No X-API-Key header.

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidAPIKey) {
		t.Errorf("expected INVALID_API_KEY, got %s", getStr(t, parseBody(t, w), "error", "code"))
	}
}

// TestCreatePaymentHandler_InvalidAPIKey verifies that POST /payments with an
// unknown API key is rejected with 401 INVALID_API_KEY.
func TestCreatePaymentHandler_InvalidAPIKey(t *testing.T) {
	r := newTestRouterWithAuth(t, &stubMerchantSvc{}) // no merchant registered

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"merchant_order_id": "ORDER-AUTH-2",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	req, _ := http.NewRequest("POST", "/api/v1/payments", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "pk_doesnotexist")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidAPIKey) {
		t.Errorf("expected INVALID_API_KEY, got %s", getStr(t, parseBody(t, w), "error", "code"))
	}
}

// TestCreatePaymentHandler_InactiveMerchant verifies that POST /payments with
// an API key belonging to an inactive merchant is rejected with 401
// MERCHANT_INACTIVE.
func TestCreatePaymentHandler_InactiveMerchant(t *testing.T) {
	inactiveMerchant := &model.Merchant{
		ID:     uuid.New(),
		Name:   "Inactive Corp",
		Code:   "INACT01",
		APIKey: "pk_inactive",
		Status: model.MerchantStatusInactive,
		// Phase 8D.3: the state check must PASS so the lifecycle check runs —
		// this asserts MERCHANT_INACTIVE is still reached for an auth-eligible
		// but non-ACTIVE merchant (no lifecycle regression).
		LegacyCredentialState: model.LegacyCredentialStateLegacy,
	}
	r := newTestRouterWithAuth(t, &stubMerchantSvc{merchant: inactiveMerchant})

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"merchant_order_id": "ORDER-AUTH-3",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	req, _ := http.NewRequest("POST", "/api/v1/payments", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "pk_inactive")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeMerchantInactive) {
		t.Errorf("expected MERCHANT_INACTIVE, got %s", getStr(t, parseBody(t, w), "error", "code"))
	}
}

// ─── X-Request-ID propagation test ───────────────────────────────────────────

// TestRequestIDPropagation verifies that:
//   - a client-supplied X-Request-ID header is echoed back in the response header
//   - the same value appears in meta.request_id of the JSON envelope
func TestRequestIDPropagation(t *testing.T) {
	const clientRequestID = "req-test-123"

	d := newTestRouter(t)

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"merchant_order_id": "ORDER-REQID",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	req, _ := http.NewRequest("POST", "/api/v1/payments", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "request-id-test-key")
	req.Header.Set("X-Request-ID", clientRequestID)

	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	// Verify response header.
	gotHeader := w.Header().Get("X-Request-ID")
	if gotHeader != clientRequestID {
		t.Errorf("X-Request-ID response header: got %q, want %q", gotHeader, clientRequestID)
	}

	// Verify meta.request_id in the JSON body.
	body := parseBody(t, w)
	gotMeta := getStr(t, body, "meta", "request_id")
	if gotMeta != clientRequestID {
		t.Errorf("meta.request_id: got %q, want %q", gotMeta, clientRequestID)
	}
}

// ─── Additional Phase 2 required tests ───────────────────────────────────────

// TestCreatePaymentHandler_ZeroAmount verifies that amount=0 is rejected 400.
func TestCreatePaymentHandler_ZeroAmount(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-Z",
		"amount":            0,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for amount=0, got %d\nbody: %s", w.Code, w.Body)
	}
	code := getStr(t, parseBody(t, w), "error", "code")
	if code != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR, got %s", code)
	}
}

// TestCreatePaymentHandler_NegativeAmount verifies that a negative amount is rejected 400.
// (Gin binding uses min=1 on int64, so negative values also fail.)
func TestCreatePaymentHandler_NegativeAmount(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-NEG",
		"amount":            -500,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative amount, got %d\nbody: %s", w.Code, w.Body)
	}
	code := getStr(t, parseBody(t, w), "error", "code")
	if code != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR, got %s", code)
	}
}

// TestCreatePaymentHandler_MissingMerchantOrderID verifies that missing
// merchant_order_id produces a VALIDATION_ERROR.
func TestCreatePaymentHandler_MissingMerchantOrderID(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"amount":         50000,
		"currency":       "IDR",
		"payment_method": "QRIS",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != "VALIDATION_ERROR" {
		t.Error("expected VALIDATION_ERROR for missing merchant_order_id")
	}
}

// TestGetPaymentHandler_MissingAPIKey verifies that GET /payments/:id without
// X-API-Key is rejected with 401.
func TestGetPaymentHandler_MissingAPIKey(t *testing.T) {
	r := newTestRouterWithAuth(t, &stubMerchantSvc{})

	req, _ := http.NewRequest("GET", "/api/v1/payments/"+uuid.New().String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidAPIKey) {
		t.Error("expected INVALID_API_KEY")
	}
}

// TestCancelPaymentHandler_MissingAPIKey verifies that POST /payments/:id/cancel
// without X-API-Key is rejected with 401.
func TestCancelPaymentHandler_MissingAPIKey(t *testing.T) {
	r := newTestRouterWithAuth(t, &stubMerchantSvc{})

	req, _ := http.NewRequest("POST", "/api/v1/payments/"+uuid.New().String()+"/cancel", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidAPIKey) {
		t.Error("expected INVALID_API_KEY")
	}
}

// TestCancelPaymentHandler_TerminalState_FAILED verifies that cancelling a
// FAILED transaction returns 409 INVALID_TRANSACTION_STATE.
func TestCancelPaymentHandler_TerminalState_FAILED(t *testing.T) {
	d := newTestRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	d.txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: d.merchant.ID, MerchantOrderID: "ORDER-H-FAILED",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusFailed, CreatedAt: now, UpdatedAt: now,
	}
	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/cancel", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidTransactionState) {
		t.Error("expected INVALID_TRANSACTION_STATE")
	}
}

// TestCancelPaymentHandler_TerminalState_EXPIRED verifies that cancelling an
// EXPIRED transaction returns 409 INVALID_TRANSACTION_STATE.
func TestCancelPaymentHandler_TerminalState_EXPIRED(t *testing.T) {
	d := newTestRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	d.txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: d.merchant.ID, MerchantOrderID: "ORDER-H-EXPIRED",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusExpired, CreatedAt: now, UpdatedAt: now,
	}
	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/cancel", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidTransactionState) {
		t.Error("expected INVALID_TRANSACTION_STATE")
	}
}

// TestCancelPaymentHandler_TerminalState_CANCELLED verifies that cancelling an
// already-CANCELLED transaction returns 409 INVALID_TRANSACTION_STATE.
func TestCancelPaymentHandler_TerminalState_CANCELLED(t *testing.T) {
	d := newTestRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	d.txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: d.merchant.ID, MerchantOrderID: "ORDER-H-CANC2",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusCancelled, CreatedAt: now, UpdatedAt: now,
	}
	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/cancel", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidTransactionState) {
		t.Error("expected INVALID_TRANSACTION_STATE")
	}
}

// TestCreatePaymentHandler_PaymentURLAndProviderTxIDInResponse verifies that
// the create payment response includes payment_url and provider_transaction_id.
func TestCreatePaymentHandler_PaymentURLAndProviderTxIDInResponse(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-URL",
		"amount":            100000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)
	paymentURL := getStr(t, body, "data", "payment_url")
	if paymentURL == "" {
		t.Error("expected non-empty payment_url in create response")
	}
	providerTxID := getStr(t, body, "data", "provider_transaction_id")
	if providerTxID == "" {
		t.Error("expected non-empty provider_transaction_id in create response")
	}
}

// TestGetPaymentHandler_ReturnsPaymentURL verifies that GET /payments/:id
// returns payment_url and provider_transaction_id after creation.
func TestGetPaymentHandler_ReturnsPaymentURL(t *testing.T) {
	d := newTestRouter(t)
	createResp := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-GETURL",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createResp.Code)
	}
	createBody := parseBody(t, createResp)
	txID := getStr(t, createBody, "data", "transaction_id")
	expectedPaymentURL := getStr(t, createBody, "data", "payment_url")
	expectedProviderTxID := getStr(t, createBody, "data", "provider_transaction_id")

	// GET the payment.
	w := doRequest(d.router, "GET", "/api/v1/payments/"+txID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)
	gotPaymentURL := getStr(t, body, "data", "payment_url")
	if gotPaymentURL != expectedPaymentURL {
		t.Errorf("payment_url mismatch: got %q, want %q", gotPaymentURL, expectedPaymentURL)
	}
	gotProviderTxID := getStr(t, body, "data", "provider_transaction_id")
	if gotProviderTxID != expectedProviderTxID {
		t.Errorf("provider_transaction_id mismatch: got %q, want %q", gotProviderTxID, expectedProviderTxID)
	}
}

// TestCancelPaymentHandler_FromCREATEDState verifies that a CREATED transaction
// can be cancelled without calling the provider (no PENDING means no provider call).
func TestCancelPaymentHandler_FromCREATEDState(t *testing.T) {
	// We cannot create a CREATED transaction through the API directly (it always
	// moves to PENDING). Insert one directly into the in-memory repo.
	d := newTestRouter(t)
	txID := uuid.New()
	now := time.Now().UTC()
	d.txRepo.txs[txID] = &model.Transaction{
		ID: txID, MerchantID: d.merchant.ID, MerchantOrderID: "ORDER-H-CRTD",
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusCreated, CreatedAt: now, UpdatedAt: now,
	}
	w := doRequest(d.router, "POST", "/api/v1/payments/"+txID.String()+"/cancel", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "data", "status") != "CANCELLED" {
		t.Error("expected CANCELLED status in response")
	}
}

// TestMockProviderIDFormat verifies the Mock provider uses MOCK-TXN-{hex} format.
func TestMockProviderIDFormat(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-FMT",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	providerTxID := getStr(t, parseBody(t, w), "data", "provider_transaction_id")
	if len(providerTxID) < 9 || providerTxID[:9] != "MOCK-TXN-" {
		t.Errorf("expected provider_transaction_id starting with MOCK-TXN-, got %q", providerTxID)
	}
}

// TestMockProviderPaymentURLHTTPS verifies the Mock provider uses https for payment URLs.
func TestMockProviderPaymentURLHTTPS(t *testing.T) {
	d := newTestRouter(t)
	w := doRequest(d.router, "POST", "/api/v1/payments", map[string]any{
		"merchant_order_id": "ORDER-H-HTTPS",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	paymentURL := getStr(t, parseBody(t, w), "data", "payment_url")
	if len(paymentURL) < 8 || paymentURL[:8] != "https://" {
		t.Errorf("expected payment_url starting with https://, got %q", paymentURL)
	}
}
