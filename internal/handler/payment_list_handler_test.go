package handler_test

// ─── Handler-level listing tests — GET /api/v1/payments ──────────────────────
//
// Coverage:
//   - Missing API key → 401
//   - Default pagination (no params)
//   - Custom page + limit
//   - Limit > MaxLimit → 400 VALIDATION_ERROR
//   - Invalid page → 400 VALIDATION_ERROR
//   - Status filter
//   - Invalid status → 400 VALIDATION_ERROR
//   - merchant_order_id filter
//   - payment_method filter
//   - Invalid payment_method → 400 INVALID_PAYMENT_METHOD
//   - created_from filter
//   - created_to filter
//   - created_from >= created_to → 400 VALIDATION_ERROR
//   - Empty result → 200 with data=[]
//   - Pagination metadata in meta
//   - Merchant isolation (Merchant A cannot see Merchant B's transactions)
//   - Deterministic ordering (newest first)

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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

// ─── in-memory list repo for handler tests ────────────────────────────────────

// listAwareTxRepo wraps memTxRepo and adds List / CountList.
type listAwareTxRepo struct {
	*memTxRepo
}

func newListAwareTxRepo() *listAwareTxRepo {
	return &listAwareTxRepo{newMemTxRepo()}
}

func (r *listAwareTxRepo) applyFilter(merchantID uuid.UUID, f model.TransactionListFilter) []*model.Transaction {
	var out []*model.Transaction
	for _, tx := range r.txs {
		if tx.MerchantID != merchantID {
			continue
		}
		if f.Status != nil && tx.Status != *f.Status {
			continue
		}
		if f.MerchantOrderID != nil && tx.MerchantOrderID != *f.MerchantOrderID {
			continue
		}
		if f.PaymentMethod != nil && tx.PaymentMethod != *f.PaymentMethod {
			continue
		}
		if f.CreatedFrom != nil && tx.CreatedAt.Before(*f.CreatedFrom) {
			continue
		}
		if f.CreatedTo != nil && !tx.CreatedAt.Before(*f.CreatedTo) {
			continue
		}
		cp := *tx
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() > out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (r *listAwareTxRepo) List(_ context.Context, merchantID uuid.UUID, f model.TransactionListFilter) ([]*model.Transaction, error) {
	all := r.applyFilter(merchantID, f)
	start := (f.Page - 1) * f.Limit
	if start >= len(all) {
		return []*model.Transaction{}, nil
	}
	end := start + f.Limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

func (r *listAwareTxRepo) CountList(_ context.Context, merchantID uuid.UUID, f model.TransactionListFilter) (int64, error) {
	return int64(len(r.applyFilter(merchantID, f))), nil
}

// compile-time check
var _ repository.TransactionRepository = (*listAwareTxRepo)(nil)

// ─── router factory ───────────────────────────────────────────────────────────

type listTestDeps struct {
	router   *gin.Engine
	merchant *model.Merchant
	txRepo   *listAwareTxRepo
}

func newListTestRouter(t *testing.T) *listTestDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	txRepo := newListAwareTxRepo()
	attemptRepo := newMemAttemptRepo()
	prov := service.NewMockPaymentProvider()
	svc := service.NewPaymentService(txRepo, attemptRepo, prov)
	h := handler.NewPaymentHandler(svc)

	merchant := &model.Merchant{
		ID: uuid.New(), Name: "List Merchant", Code: "LIST001",
		APIKey: "pk_list", Status: model.MerchantStatusActive,
	}

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(func(c *gin.Context) {
		c.Set(model.ContextKeyMerchant, merchant)
		c.Next()
	})
	r.GET("/api/v1/payments", h.List)
	r.POST("/api/v1/payments", h.Create)

	return &listTestDeps{router: r, merchant: merchant, txRepo: txRepo}
}

// insertListTx seeds a transaction directly into the in-memory repo.
func insertListTx(r *listAwareTxRepo, merchantID uuid.UUID, orderID string, status model.TransactionStatus, createdAt time.Time) *model.Transaction {
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: merchantID, MerchantOrderID: orderID,
		Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: status, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	r.txs[tx.ID] = tx
	return tx
}

// doListRequest sends GET /api/v1/payments with optional query string.
func doListRequest(r *gin.Engine, query string) *httptest.ResponseRecorder {
	url := "/api/v1/payments"
	if query != "" {
		url += "?" + query
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestListPaymentHandler_MissingAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prov := service.NewMockPaymentProvider()
	txRepo := newListAwareTxRepo()
	svc := service.NewPaymentService(txRepo, newMemAttemptRepo(), prov)
	h := handler.NewPaymentHandler(svc)

	merchant := &model.Merchant{
		ID: uuid.New(), Name: "Auth Merchant", Code: "AUTHM",
		APIKey: "pk_auth", Status: model.MerchantStatusActive,
	}
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.Auth(&stubMerchantSvc{merchant: merchant}, noopAPIKeySvc()))
	r.GET("/api/v1/payments", h.List)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/payments", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidAPIKey) {
		t.Error("expected INVALID_API_KEY")
	}
}

func TestListPaymentHandler_DefaultPagination(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		insertListTx(d.txRepo, d.merchant.ID, uuid.New().String(), model.TransactionStatusPending,
			now.Add(-time.Duration(i)*time.Second))
	}

	w := doListRequest(d.router, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got %T", body["data"])
	}
	if len(data) != 3 {
		t.Errorf("expected 3 items, got %d", len(data))
	}
	meta := body["meta"].(map[string]any)
	if int(meta["page"].(float64)) != 1 {
		t.Errorf("meta.page: expected 1, got %v", meta["page"])
	}
	if int(meta["limit"].(float64)) != model.DefaultLimit {
		t.Errorf("meta.limit: expected %d, got %v", model.DefaultLimit, meta["limit"])
	}
}

func TestListPaymentHandler_CustomPagination(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		insertListTx(d.txRepo, d.merchant.ID, uuid.New().String(), model.TransactionStatusPending,
			now.Add(-time.Duration(i)*time.Second))
	}

	w := doListRequest(d.router, "page=2&limit=2")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	data := body["data"].([]any)
	if len(data) != 2 {
		t.Errorf("page 2 limit 2: expected 2, got %d", len(data))
	}
	meta := body["meta"].(map[string]any)
	if int(meta["total"].(float64)) != 5 {
		t.Errorf("total: expected 5, got %v", meta["total"])
	}
	if int(meta["total_pages"].(float64)) != 3 {
		t.Errorf("total_pages: expected 3, got %v", meta["total_pages"])
	}
}

func TestListPaymentHandler_LimitOverMax(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, fmt.Sprintf("limit=%d", model.MaxLimit+1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit over max, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != "VALIDATION_ERROR" {
		t.Error("expected VALIDATION_ERROR")
	}
}

func TestListPaymentHandler_InvalidPage(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, "page=0")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for page=0, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != "VALIDATION_ERROR" {
		t.Error("expected VALIDATION_ERROR")
	}
}

func TestListPaymentHandler_InvalidPageNonNumeric(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, "page=abc")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for page=abc, got %d", w.Code)
	}
}

func TestListPaymentHandler_StatusFilter(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	insertListTx(d.txRepo, d.merchant.ID, "PAID-X", model.TransactionStatusPaid, now)
	insertListTx(d.txRepo, d.merchant.ID, "PENDING-X", model.TransactionStatusPending, now.Add(-time.Second))

	w := doListRequest(d.router, "status=PAID")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 1 {
		t.Errorf("status=PAID: expected 1, got %d", len(data))
	}
	tx := data[0].(map[string]any)
	if tx["status"] != "PAID" {
		t.Errorf("expected PAID, got %v", tx["status"])
	}
}

func TestListPaymentHandler_InvalidStatus(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, "status=BOGUS")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != "VALIDATION_ERROR" {
		t.Error("expected VALIDATION_ERROR")
	}
}

func TestListPaymentHandler_MerchantOrderIDFilter(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	insertListTx(d.txRepo, d.merchant.ID, "TARGET-001", model.TransactionStatusPending, now)
	insertListTx(d.txRepo, d.merchant.ID, "OTHER-999", model.TransactionStatusPending, now.Add(-time.Second))

	w := doListRequest(d.router, "merchant_order_id=TARGET-001")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 1 {
		t.Errorf("merchant_order_id filter: expected 1, got %d", len(data))
	}
	tx := data[0].(map[string]any)
	if tx["merchant_order_id"] != "TARGET-001" {
		t.Errorf("expected TARGET-001, got %v", tx["merchant_order_id"])
	}
}

func TestListPaymentHandler_PaymentMethodFilter(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	insertListTx(d.txRepo, d.merchant.ID, "QRIS-1", model.TransactionStatusPending, now)

	w := doListRequest(d.router, "payment_method=QRIS")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 1 {
		t.Errorf("payment_method=QRIS: expected 1, got %d", len(data))
	}
}

func TestListPaymentHandler_InvalidPaymentMethod(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, "payment_method=UNKNOWN")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid payment method, got %d", w.Code)
	}
}

func TestListPaymentHandler_CreatedFromFilter(t *testing.T) {
	d := newListTestRouter(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	insertListTx(d.txRepo, d.merchant.ID, "BEFORE", model.TransactionStatusPending, base.Add(-time.Hour))
	insertListTx(d.txRepo, d.merchant.ID, "AFTER", model.TransactionStatusPending, base.Add(time.Hour))

	w := doListRequest(d.router, "created_from=2026-06-01T00:00:00Z")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 1 {
		t.Errorf("created_from: expected 1, got %d", len(data))
	}
	tx := data[0].(map[string]any)
	if tx["merchant_order_id"] != "AFTER" {
		t.Errorf("expected AFTER, got %v", tx["merchant_order_id"])
	}
}

func TestListPaymentHandler_CreatedToFilter(t *testing.T) {
	d := newListTestRouter(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	insertListTx(d.txRepo, d.merchant.ID, "BEFORE", model.TransactionStatusPending, base.Add(-time.Hour))
	insertListTx(d.txRepo, d.merchant.ID, "AFTER", model.TransactionStatusPending, base.Add(time.Hour))

	w := doListRequest(d.router, "created_to=2026-06-01T00:00:00Z")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 1 {
		t.Errorf("created_to: expected 1 (BEFORE), got %d", len(data))
	}
}

func TestListPaymentHandler_InvalidCreatedFrom(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, "created_from=not-a-date")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid created_from, got %d", w.Code)
	}
}

func TestListPaymentHandler_InvalidDateRange_FromGteqTo(t *testing.T) {
	d := newListTestRouter(t)
	w := doListRequest(d.router, "created_from=2026-06-01T00:00:00Z&created_to=2026-05-01T00:00:00Z")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for created_from >= created_to, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != "VALIDATION_ERROR" {
		t.Error("expected VALIDATION_ERROR")
	}
}

func TestListPaymentHandler_EmptyResult(t *testing.T) {
	d := newListTestRouter(t)

	w := doListRequest(d.router, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty result, got %d", w.Code)
	}
	body := parseBody(t, w)
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("expected array, got %T: %v", body["data"], body["data"])
	}
	if len(data) != 0 {
		t.Errorf("expected empty array, got %d items", len(data))
	}
	meta := body["meta"].(map[string]any)
	if int(meta["total"].(float64)) != 0 {
		t.Errorf("total should be 0, got %v", meta["total"])
	}
	if int(meta["total_pages"].(float64)) != 0 {
		t.Errorf("total_pages should be 0, got %v", meta["total_pages"])
	}
}

func TestListPaymentHandler_PageBeyondLast(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	insertListTx(d.txRepo, d.merchant.ID, "ONLY", model.TransactionStatusPending, now)

	w := doListRequest(d.router, "page=99&limit=20")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for page beyond last, got %d", w.Code)
	}
	body := parseBody(t, w)
	data := body["data"].([]any)
	if len(data) != 0 {
		t.Errorf("page beyond last: expected 0 items, got %d", len(data))
	}
	meta := body["meta"].(map[string]any)
	if int(meta["total"].(float64)) != 1 {
		t.Errorf("total should still be 1, got %v", meta["total"])
	}
}

// TestListPaymentHandler_MerchantIsolation is the security test:
// Merchant A must never see Merchant B's transactions.
func TestListPaymentHandler_MerchantIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	txRepo := newListAwareTxRepo()
	attemptRepo := newMemAttemptRepo()
	prov := service.NewMockPaymentProvider()

	mA := &model.Merchant{
		ID: uuid.New(), Name: "Merchant A", Code: "MERCHA",
		APIKey: "pk_a", Status: model.MerchantStatusActive,
	}
	mB := &model.Merchant{
		ID: uuid.New(), Name: "Merchant B", Code: "MERCHB",
		APIKey: "pk_b", Status: model.MerchantStatusActive,
	}

	// Seed transactions for both merchants.
	now := time.Now().UTC()
	insertListTx(txRepo, mA.ID, "A-ONLY-1", model.TransactionStatusPending, now)
	insertListTx(txRepo, mA.ID, "A-ONLY-2", model.TransactionStatusPaid, now.Add(-time.Second))
	insertListTx(txRepo, mB.ID, "B-ONLY-1", model.TransactionStatusPending, now)

	// Build two routers, one per merchant context.
	svc := service.NewPaymentService(txRepo, attemptRepo, prov)
	h := handler.NewPaymentHandler(svc)

	routerFor := func(m *model.Merchant) *gin.Engine {
		r := gin.New()
		r.Use(middleware.RequestID())
		r.Use(func(c *gin.Context) { c.Set(model.ContextKeyMerchant, m); c.Next() })
		r.GET("/api/v1/payments", h.List)
		return r
	}

	rA := routerFor(mA)
	rB := routerFor(mB)

	// Merchant A listing.
	wA := doListRequest(rA, "")
	if wA.Code != http.StatusOK {
		t.Fatalf("merchant A: expected 200, got %d", wA.Code)
	}
	dataA := parseBody(t, wA)["data"].([]any)
	if len(dataA) != 2 {
		t.Errorf("merchant A: expected 2 transactions, got %d", len(dataA))
	}
	for _, item := range dataA {
		tx := item.(map[string]any)
		oid := tx["merchant_order_id"].(string)
		if oid == "B-ONLY-1" {
			t.Error("SECURITY: Merchant A listing contains Merchant B's transaction B-ONLY-1")
		}
	}

	// Merchant B listing.
	wB := doListRequest(rB, "")
	if wB.Code != http.StatusOK {
		t.Fatalf("merchant B: expected 200, got %d", wB.Code)
	}
	dataB := parseBody(t, wB)["data"].([]any)
	if len(dataB) != 1 {
		t.Errorf("merchant B: expected 1 transaction, got %d", len(dataB))
	}
	tx := dataB[0].(map[string]any)
	if tx["merchant_order_id"] != "B-ONLY-1" {
		t.Errorf("merchant B: expected B-ONLY-1, got %v", tx["merchant_order_id"])
	}
}

// TestListPaymentHandler_IsolationWithMerchantOrderIDFilter verifies that
// filtering by an order ID belonging to another merchant returns empty results.
func TestListPaymentHandler_IsolationWithMerchantOrderIDFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	txRepo := newListAwareTxRepo()
	mA := &model.Merchant{
		ID: uuid.New(), Name: "M A", Code: "MA",
		APIKey: "pk_ma", Status: model.MerchantStatusActive,
	}
	mB := &model.Merchant{
		ID: uuid.New(), Name: "M B", Code: "MB",
		APIKey: "pk_mb", Status: model.MerchantStatusActive,
	}

	now := time.Now().UTC()
	insertListTx(txRepo, mB.ID, "B-SECRET-ORDER", model.TransactionStatusPending, now)

	svc := service.NewPaymentService(txRepo, newMemAttemptRepo(), service.NewMockPaymentProvider())
	h := handler.NewPaymentHandler(svc)

	rA := gin.New()
	rA.Use(middleware.RequestID())
	rA.Use(func(c *gin.Context) { c.Set(model.ContextKeyMerchant, mA); c.Next() })
	rA.GET("/api/v1/payments", h.List)

	// Merchant A queries for Merchant B's order ID.
	w := doListRequest(rA, "merchant_order_id=B-SECRET-ORDER")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 0 {
		t.Errorf("SECURITY: cross-merchant order ID filter leaked %d transactions", len(data))
	}
}

func TestListPaymentHandler_DeterministicOrdering(t *testing.T) {
	d := newListTestRouter(t)
	base := time.Now().UTC()

	// Insert 3 transactions with strictly decreasing times.
	tx1 := insertListTx(d.txRepo, d.merchant.ID, "NEWEST", model.TransactionStatusPending, base)
	tx2 := insertListTx(d.txRepo, d.merchant.ID, "MIDDLE", model.TransactionStatusPending, base.Add(-time.Second))
	tx3 := insertListTx(d.txRepo, d.merchant.ID, "OLDEST", model.TransactionStatusPending, base.Add(-2*time.Second))
	_ = tx1
	_ = tx2
	_ = tx3

	w := doListRequest(d.router, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 3 {
		t.Fatalf("expected 3, got %d", len(data))
	}
	if data[0].(map[string]any)["merchant_order_id"] != "NEWEST" {
		t.Errorf("expected NEWEST first, got %v", data[0].(map[string]any)["merchant_order_id"])
	}
	if data[2].(map[string]any)["merchant_order_id"] != "OLDEST" {
		t.Errorf("expected OLDEST last, got %v", data[2].(map[string]any)["merchant_order_id"])
	}
}

func TestListPaymentHandler_ResponseContainsSafeFields(t *testing.T) {
	d := newListTestRouter(t)
	now := time.Now().UTC()
	insertListTx(d.txRepo, d.merchant.ID, "SAFE-FIELDS", model.TransactionStatusPending, now)

	w := doListRequest(d.router, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data := parseBody(t, w)["data"].([]any)
	tx := data[0].(map[string]any)

	// These fields must be present.
	requiredFields := []string{
		"transaction_id", "merchant_order_id", "amount", "currency",
		"payment_method", "status", "created_at", "updated_at",
	}
	for _, field := range requiredFields {
		if _, ok := tx[field]; !ok {
			t.Errorf("safe field %q missing from response", field)
		}
	}
}

func TestListPaymentHandler_RequestIDInMeta(t *testing.T) {
	d := newListTestRouter(t)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/payments", nil)
	req.Header.Set("X-Request-ID", "list-reqid-test")
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	meta := parseBody(t, w)["meta"].(map[string]any)
	if meta["request_id"] != "list-reqid-test" {
		t.Errorf("meta.request_id: expected list-reqid-test, got %v", meta["request_id"])
	}
}
