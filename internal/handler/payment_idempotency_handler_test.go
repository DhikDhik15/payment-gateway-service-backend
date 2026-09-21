package handler_test

// ─── Handler-level idempotency tests ─────────────────────────────────────────
//
// These tests exercise the full HTTP → handler → service → idempotency-repo
// path without a real database. They complement the service-level idempotency
// tests in internal/service/payment_idempotency_test.go.
//
// Coverage:
//   - Missing Idempotency-Key header → 400
//   - Empty Idempotency-Key header → 400
//   - Idempotency-Key exceeds 255 chars → 400
//   - First request → 201, creates transaction, calls provider once
//   - Same key + same payload replay → 201, same transaction_id, provider not called again
//   - Same key + different payload → 409 IDEMPOTENCY_KEY_REUSED, provider not called
//   - Different merchants, same key → independent transactions, provider called per merchant
//   - PROCESSING state → 409 IDEMPOTENCY_REQUEST_IN_PROGRESS, provider not called
//   - COMPLETED replay (pre-seeded) → returns stored response, provider not called
//   - FAILED state replay → returns stored error, provider not called after first failure
//   - Expired key → treated as new, provider called again
//   - Concurrent same-key requests → exactly one provider call

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
)

// ─── in-memory idempotency repo (mirrors service test version) ───────────────

type memIdempotencyRepo struct {
	mu      sync.Mutex
	records map[string]*model.IdempotencyKey
}

func newMemIdempotencyRepo() *memIdempotencyRepo {
	return &memIdempotencyRepo{records: make(map[string]*model.IdempotencyKey)}
}

func (r *memIdempotencyRepo) compositeKey(merchantID uuid.UUID, key string) string {
	return merchantID.String() + ":" + key
}

func (r *memIdempotencyRepo) GetByMerchantAndKey(_ context.Context, merchantID uuid.UUID, key string) (*model.IdempotencyKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[r.compositeKey(merchantID, key)]
	if !ok {
		return nil, repository.ErrIdempotencyNotFound
	}
	cp := *rec
	return &cp, nil
}

func (r *memIdempotencyRepo) Create(_ context.Context, rec *model.IdempotencyKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.compositeKey(rec.MerchantID, rec.Key)
	if _, exists := r.records[k]; exists {
		return repository.ErrIdempotencyConflict
	}
	cp := *rec
	r.records[k] = &cp
	return nil
}

func (r *memIdempotencyRepo) Reserve(_ context.Context, rec *model.IdempotencyKey) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.compositeKey(rec.MerchantID, rec.Key)
	if existing, ok := r.records[k]; ok && existing.ExpiresAt.After(time.Now().UTC()) {
		// Active record exists — caller is not the owner.
		return false, nil
	}
	cp := *rec
	r.records[k] = &cp
	return true, nil
}

func (r *memIdempotencyRepo) UpdateProcessing(_ context.Context, id uuid.UUID) error {
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

func (r *memIdempotencyRepo) AttachRefund(_ context.Context, id, refundID uuid.UUID) error {
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

func (r *memIdempotencyRepo) Complete(_ context.Context, id uuid.UUID, status int, body []byte, txID *uuid.UUID, refundID *uuid.UUID) error {
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

func (r *memIdempotencyRepo) Fail(_ context.Context, id uuid.UUID, status int, body []byte, txID *uuid.UUID, refundID *uuid.UUID) error {
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

// compile-time check
var _ repository.IdempotencyKeyRepository = (*memIdempotencyRepo)(nil)

// ─── test router factory ─────────────────────────────────────────────────────

type idempotencyTestDeps struct {
	router      *gin.Engine
	merchant    *model.Merchant
	txRepo      *memTxRepo // reuse from payment_handler_test.go
	attemptRepo *memAttemptRepo
	provider    *service.MockPaymentProvider
	idempRepo   *memIdempotencyRepo
}

func newIdempotencyTestRouter(t *testing.T, providerOpts ...func(*service.MockPaymentProvider)) *idempotencyTestDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prov := service.NewMockPaymentProvider()
	for _, opt := range providerOpts {
		opt(prov)
	}

	txRepo := newMemTxRepo()
	attemptRepo := newMemAttemptRepo()
	idempRepo := newMemIdempotencyRepo()

	svc := service.NewPaymentServiceWithIdempotency(
		txRepo, attemptRepo, prov, idempRepo, time.Hour,
	)
	h := handler.NewPaymentHandler(svc)

	merchant := &model.Merchant{
		ID:     uuid.New(),
		Name:   "Idempotency Merchant",
		Code:   "IDEMP001",
		APIKey: "pk_idemp",
		Status: model.MerchantStatusActive,
	}

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(func(c *gin.Context) {
		c.Set(model.ContextKeyMerchant, merchant)
		c.Next()
	})
	r.POST("/api/v1/payments", h.Create)

	return &idempotencyTestDeps{
		router:      r,
		merchant:    merchant,
		txRepo:      txRepo,
		attemptRepo: attemptRepo,
		provider:    prov,
		idempRepo:   idempRepo,
	}
}

// doIdempotentRequest sends POST /api/v1/payments with the given idempotency key.
func doIdempotentRequest(r *gin.Engine, body any, idempKey string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments", &buf)
	req.Header.Set("Content-Type", "application/json")
	if idempKey != "" {
		req.Header.Set("Idempotency-Key", idempKey)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Idempotency-Key header validation ───────────────────────────────────────

// TestIdempotency_MissingKey verifies that omitting Idempotency-Key returns 400.
func TestIdempotency_MissingKey(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	w := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-MISS",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "") // no header

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing Idempotency-Key, got %d\nbody: %s", w.Code, w.Body)
	}
	code := getStr(t, parseBody(t, w), "error", "code")
	if code != string(response.CodeInvalidRequest) {
		t.Errorf("expected INVALID_REQUEST, got %s", code)
	}
	// Provider must not be called.
	if d.provider.CreatePaymentCallCount() != 0 {
		t.Errorf("provider must not be called when key is missing, got %d calls",
			d.provider.CreatePaymentCallCount())
	}
}

// TestIdempotency_EmptyKey verifies that an empty Idempotency-Key returns 400.
func TestIdempotency_EmptyKey(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	// Send the header explicitly with an empty value.
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"merchant_order_id": "ORDER-IH-EMPTY",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "   ") // whitespace-only → trimmed to empty
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty Idempotency-Key, got %d\nbody: %s", w.Code, w.Body)
	}
	if d.provider.CreatePaymentCallCount() != 0 {
		t.Error("provider must not be called for empty key")
	}
}

// TestIdempotency_KeyTooLong verifies that a key exceeding 255 characters returns 400.
func TestIdempotency_KeyTooLong(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	longKey := strings.Repeat("x", 256)
	w := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-LONG",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, longKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for key >255 chars, got %d\nbody: %s", w.Code, w.Body)
	}
	if d.provider.CreatePaymentCallCount() != 0 {
		t.Error("provider must not be called for oversized key")
	}
}

// TestIdempotency_KeyAtMaxLength verifies that exactly 255-char key is accepted.
func TestIdempotency_KeyAtMaxLength(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	maxKey := strings.Repeat("a", 255)
	w := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-MAX255",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, maxKey)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for 255-char key, got %d\nbody: %s", w.Code, w.Body)
	}
}

// ─── First request ────────────────────────────────────────────────────────────

// TestIdempotency_FirstRequest verifies the happy path:
// idempotency record is COMPLETED, transaction created, provider called once.
func TestIdempotency_FirstRequest(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	body := map[string]any{
		"merchant_order_id": "ORDER-IH-FIRST",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}
	w := doIdempotentRequest(d.router, body, "IH-KEY-FIRST")

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	resp := parseBody(t, w)
	txID := getStr(t, resp, "data", "transaction_id")
	if txID == "" {
		t.Fatal("expected non-empty transaction_id")
	}
	if getStr(t, resp, "data", "status") != "PENDING" {
		t.Errorf("expected PENDING, got %s", getStr(t, resp, "data", "status"))
	}

	// Provider called exactly once.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("expected 1 provider call, got %d", d.provider.CreatePaymentCallCount())
	}

	// Idempotency record is COMPLETED.
	rec, err := d.idempRepo.GetByMerchantAndKey(context.Background(), d.merchant.ID, "IH-KEY-FIRST")
	if err != nil {
		t.Fatalf("idempotency record not found: %v", err)
	}
	if rec.Status != model.IdempotencyStatusCompleted {
		t.Errorf("expected COMPLETED status, got %s", rec.Status)
	}
	if rec.TransactionID == nil {
		t.Error("expected transaction_id set on idempotency record")
	}
	if rec.ResponseBody == nil {
		t.Error("expected response_body set on idempotency record")
	}
}

// ─── Replay (same key + same payload) ────────────────────────────────────────

// TestIdempotency_SameKeySamePayload_Replay verifies that repeating an identical
// request returns the same transaction_id without calling the provider again.
func TestIdempotency_SameKeySamePayload_Replay(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	body := map[string]any{
		"merchant_order_id": "ORDER-IH-REPLAY",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}

	// First request.
	w1 := doIdempotentRequest(d.router, body, "IH-KEY-REPLAY")
	if w1.Code != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d\nbody: %s", w1.Code, w1.Body)
	}
	txID1 := getStr(t, parseBody(t, w1), "data", "transaction_id")

	// Replay — identical key + payload.
	w2 := doIdempotentRequest(d.router, body, "IH-KEY-REPLAY")
	if w2.Code != http.StatusCreated {
		t.Fatalf("replay: expected 201, got %d\nbody: %s", w2.Code, w2.Body)
	}
	txID2 := getStr(t, parseBody(t, w2), "data", "transaction_id")

	// Same transaction.
	if txID1 != txID2 {
		t.Errorf("replay must return same transaction_id: first=%s replay=%s", txID1, txID2)
	}

	// Provider called exactly once across both requests.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("expected 1 provider call total, got %d", d.provider.CreatePaymentCallCount())
	}

	// Only one transaction in the repo.
	count := 0
	for _, tx := range d.txRepo.txs {
		if tx.MerchantID == d.merchant.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 transaction, found %d", count)
	}
}

// TestIdempotency_ReplayPreservesRequestID verifies that a replayed response
// returns the *current* request's request_id in meta, not the original one.
func TestIdempotency_ReplayPreservesRequestID(t *testing.T) {
	d := newIdempotencyTestRouter(t)
	body := map[string]any{
		"merchant_order_id": "ORDER-IH-REQID",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}

	// First request with a specific request ID.
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req1, _ := http.NewRequest(http.MethodPost, "/api/v1/payments", &buf)
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Idempotency-Key", "IH-KEY-REQID")
	req1.Header.Set("X-Request-ID", "first-request-id")
	w1 := httptest.NewRecorder()
	d.router.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first request failed: %d %s", w1.Code, w1.Body)
	}

	// Replay with a *different* request ID.
	buf.Reset()
	_ = json.NewEncoder(&buf).Encode(body)
	req2, _ := http.NewRequest(http.MethodPost, "/api/v1/payments", &buf)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", "IH-KEY-REQID")
	req2.Header.Set("X-Request-ID", "replay-request-id")
	w2 := httptest.NewRecorder()
	d.router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("replay failed: %d %s", w2.Code, w2.Body)
	}

	// The replayed meta.request_id must be the *current* request's ID.
	metaRequestID := getStr(t, parseBody(t, w2), "meta", "request_id")
	if metaRequestID != "replay-request-id" {
		t.Errorf("replay meta.request_id: got %q, want %q", metaRequestID, "replay-request-id")
	}
}

// ─── Same key + different payload → IDEMPOTENCY_KEY_REUSED ───────────────────

// TestIdempotency_SameKeyDifferentPayload_Conflict verifies that changing a
// business field with the same key returns 409 IDEMPOTENCY_KEY_REUSED.
func TestIdempotency_SameKeyDifferentPayload_Conflict(t *testing.T) {
	d := newIdempotencyTestRouter(t)

	// Establish the key with amount=50000.
	w1 := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-MISMATCH",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-MISMATCH")
	if w1.Code != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d\nbody: %s", w1.Code, w1.Body)
	}

	// Replay with a different amount.
	w2 := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-MISMATCH",
		"amount":            100000, // changed
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-MISMATCH")

	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for key reuse, got %d\nbody: %s", w2.Code, w2.Body)
	}
	code := getStr(t, parseBody(t, w2), "error", "code")
	if code != string(response.CodeIdempotencyKeyReused) {
		t.Errorf("expected IDEMPOTENCY_KEY_REUSED, got %s", code)
	}

	// Provider must have been called only for the original request.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("expected 1 provider call, got %d", d.provider.CreatePaymentCallCount())
	}

	// Only one transaction must exist.
	count := 0
	for _, tx := range d.txRepo.txs {
		if tx.MerchantID == d.merchant.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 transaction after key reuse, found %d", count)
	}
}

// TestIdempotency_DifferentMerchantKeysSameKey verifies that the same
// Idempotency-Key is independent per merchant.
func TestIdempotency_DifferentMerchantKeysSameKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Merchant A.
	idempRepoA := newMemIdempotencyRepo()
	txRepoA := newMemTxRepo()
	attemptRepoA := newMemAttemptRepo()
	provA := service.NewMockPaymentProvider()
	merchantA := &model.Merchant{ID: uuid.New(), Name: "Merch A", Code: "MERCHA", APIKey: "pk_a", Status: model.MerchantStatusActive}
	svcA := service.NewPaymentServiceWithIdempotency(txRepoA, attemptRepoA, provA, idempRepoA, time.Hour)
	hA := handler.NewPaymentHandler(svcA)
	rA := gin.New()
	rA.Use(middleware.RequestID())
	rA.Use(func(c *gin.Context) { c.Set(model.ContextKeyMerchant, merchantA); c.Next() })
	rA.POST("/api/v1/payments", hA.Create)

	// Merchant B — separate repos so orders don't collide.
	idempRepoB := newMemIdempotencyRepo()
	txRepoB := newMemTxRepo()
	attemptRepoB := newMemAttemptRepo()
	provB := service.NewMockPaymentProvider()
	merchantB := &model.Merchant{ID: uuid.New(), Name: "Merch B", Code: "MERCHB", APIKey: "pk_b", Status: model.MerchantStatusActive}
	svcB := service.NewPaymentServiceWithIdempotency(txRepoB, attemptRepoB, provB, idempRepoB, time.Hour)
	hB := handler.NewPaymentHandler(svcB)
	rB := gin.New()
	rB.Use(middleware.RequestID())
	rB.Use(func(c *gin.Context) { c.Set(model.ContextKeyMerchant, merchantB); c.Next() })
	rB.POST("/api/v1/payments", hB.Create)

	body := map[string]any{
		"merchant_order_id": "ORDER-ISO",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}

	wA := doIdempotentRequest(rA, body, "SHARED-KEY")
	wB := doIdempotentRequest(rB, body, "SHARED-KEY")

	if wA.Code != http.StatusCreated {
		t.Fatalf("merchant A: expected 201, got %d\nbody: %s", wA.Code, wA.Body)
	}
	if wB.Code != http.StatusCreated {
		t.Fatalf("merchant B: expected 201, got %d\nbody: %s", wB.Code, wB.Body)
	}

	txIDA := getStr(t, parseBody(t, wA), "data", "transaction_id")
	txIDB := getStr(t, parseBody(t, wB), "data", "transaction_id")
	if txIDA == txIDB {
		t.Errorf("different merchants must produce different transactions, both got %s", txIDA)
	}

	// Each provider called exactly once.
	if provA.CreatePaymentCallCount() != 1 {
		t.Errorf("merchant A provider calls: expected 1, got %d", provA.CreatePaymentCallCount())
	}
	if provB.CreatePaymentCallCount() != 1 {
		t.Errorf("merchant B provider calls: expected 1, got %d", provB.CreatePaymentCallCount())
	}
}

// ─── PROCESSING state ─────────────────────────────────────────────────────────

// TestIdempotency_ProcessingState verifies that an existing PROCESSING record
// returns 409 IDEMPOTENCY_REQUEST_IN_PROGRESS without calling the provider.
func TestIdempotency_ProcessingState(t *testing.T) {
	d := newIdempotencyTestRouter(t)

	// Manually insert a PROCESSING record scoped to the test merchant.
	processingRecord := &model.IdempotencyKey{
		ID:         uuid.New(),
		MerchantID: d.merchant.ID,
		Key:        "IH-KEY-PROC",
		RequestHash: model.PaymentRequestHash(model.CreatePaymentRequest{
			MerchantOrderID: "ORDER-IH-PROC",
			Amount:          50000,
			Currency:        "IDR",
			PaymentMethod:   "QRIS",
		}),
		Status:    model.IdempotencyStatusProcessing,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := d.idempRepo.Create(context.Background(), processingRecord); err != nil {
		t.Fatalf("seed PROCESSING record: %v", err)
	}

	w := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-PROC",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-PROC")

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for PROCESSING state, got %d\nbody: %s", w.Code, w.Body)
	}
	code := getStr(t, parseBody(t, w), "error", "code")
	if code != string(response.CodeIdempotencyInProgress) {
		t.Errorf("expected IDEMPOTENCY_REQUEST_IN_PROGRESS, got %s", code)
	}
	// Provider must not be called.
	if d.provider.CreatePaymentCallCount() != 0 {
		t.Errorf("provider must not be called for PROCESSING state, got %d calls",
			d.provider.CreatePaymentCallCount())
	}
}

// ─── COMPLETED replay (pre-seeded) ───────────────────────────────────────────

// TestIdempotency_CompletedReplay verifies that a pre-seeded COMPLETED record
// is replayed immediately without calling the provider or creating a new transaction.
func TestIdempotency_CompletedReplay(t *testing.T) {
	d := newIdempotencyTestRouter(t)

	storedTxID := uuid.New()
	storedBody, _ := json.Marshal(model.CreatePaymentResponse{
		TransactionID:         storedTxID,
		MerchantOrderID:       "ORDER-IH-COMPLETED",
		Amount:                50000,
		Currency:              "IDR",
		PaymentMethod:         "QRIS",
		Provider:              "MOCK",
		ProviderTransactionID: "MOCK-TXN-seeded",
		Status:                model.TransactionStatusPending,
	})
	status200 := 201
	completedRecord := &model.IdempotencyKey{
		ID:         uuid.New(),
		MerchantID: d.merchant.ID,
		Key:        "IH-KEY-COMPLETED",
		RequestHash: model.PaymentRequestHash(model.CreatePaymentRequest{
			MerchantOrderID: "ORDER-IH-COMPLETED",
			Amount:          50000,
			Currency:        "IDR",
			PaymentMethod:   "QRIS",
		}),
		Status:         model.IdempotencyStatusCompleted,
		ResponseStatus: &status200,
		ResponseBody:   storedBody,
		TransactionID:  &storedTxID,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
	}
	if err := d.idempRepo.Create(context.Background(), completedRecord); err != nil {
		t.Fatalf("seed COMPLETED record: %v", err)
	}

	w := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-COMPLETED",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-COMPLETED")

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for COMPLETED replay, got %d\nbody: %s", w.Code, w.Body)
	}

	resp := parseBody(t, w)
	gotTxID := getStr(t, resp, "data", "transaction_id")
	if gotTxID != storedTxID.String() {
		t.Errorf("replay transaction_id: got %s, want %s", gotTxID, storedTxID)
	}
	if getStr(t, resp, "data", "provider_transaction_id") != "MOCK-TXN-seeded" {
		t.Errorf("replay provider_transaction_id mismatch: %s",
			getStr(t, resp, "data", "provider_transaction_id"))
	}

	// Provider must NOT be called.
	if d.provider.CreatePaymentCallCount() != 0 {
		t.Errorf("provider must not be called for COMPLETED replay, got %d calls",
			d.provider.CreatePaymentCallCount())
	}
}

// ─── FAILED state replay ──────────────────────────────────────────────────────

// TestIdempotency_FailedStateReplay verifies that a provider failure is stored
// and replayed without a second provider call.
func TestIdempotency_FailedStateReplay(t *testing.T) {
	d := newIdempotencyTestRouter(t, func(p *service.MockPaymentProvider) {
		p.ShouldFailCreate = true
	})

	// First request — provider fails.
	w1 := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-FAIL",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-FAIL")

	if w1.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on provider failure, got %d\nbody: %s", w1.Code, w1.Body)
	}
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Fatalf("expected 1 provider call after failure, got %d", d.provider.CreatePaymentCallCount())
	}

	// Second request — same key + same payload — must replay the stored failure.
	w2 := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-FAIL",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-FAIL")

	if w2.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on FAILED replay, got %d\nbody: %s", w2.Code, w2.Body)
	}
	// Provider must NOT be called a second time.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("expected still 1 provider call after failure replay, got %d",
			d.provider.CreatePaymentCallCount())
	}
}

// TestIdempotency_ProviderTimeoutReplay verifies provider timeout is stored and
// replayed (ambiguous external result — must not trigger a second provider call).
func TestIdempotency_ProviderTimeoutReplay(t *testing.T) {
	d := newIdempotencyTestRouter(t, func(p *service.MockPaymentProvider) {
		p.ShouldTimeout = true
	})

	// First request — provider times out.
	w1 := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-TIMEOUT",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-TIMEOUT")

	if w1.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 on provider timeout, got %d\nbody: %s", w1.Code, w1.Body)
	}

	// Replay — same key + same payload — must return the stored timeout, not call provider again.
	w2 := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-TIMEOUT",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-TIMEOUT")

	if w2.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 on timeout replay, got %d\nbody: %s", w2.Code, w2.Body)
	}
	// Provider called once in total.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("expected 1 provider call total (timeout must not be retried), got %d",
			d.provider.CreatePaymentCallCount())
	}
}

// ─── Expired key ──────────────────────────────────────────────────────────────

// TestIdempotency_ExpiredKeyIsReservable verifies that an expired idempotency
// record is treated as non-existent and a fresh payment is created.
func TestIdempotency_ExpiredKeyIsReservable(t *testing.T) {
	d := newIdempotencyTestRouter(t)

	storedTxID := uuid.New()
	storedBody, _ := json.Marshal(model.CreatePaymentResponse{
		TransactionID:   storedTxID,
		MerchantOrderID: "ORDER-IH-EXP",
		Amount:          50000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
		Provider:        "MOCK",
		Status:          model.TransactionStatusPending,
	})
	status201 := 201
	expiredRecord := &model.IdempotencyKey{
		ID:         uuid.New(),
		MerchantID: d.merchant.ID,
		Key:        "IH-KEY-EXPIRED",
		RequestHash: model.PaymentRequestHash(model.CreatePaymentRequest{
			MerchantOrderID: "ORDER-IH-EXP",
			Amount:          50000,
			Currency:        "IDR",
			PaymentMethod:   "QRIS",
		}),
		Status:         model.IdempotencyStatusCompleted,
		ResponseStatus: &status201,
		ResponseBody:   storedBody,
		TransactionID:  &storedTxID,
		CreatedAt:      time.Now().UTC().Add(-25 * time.Hour),
		UpdatedAt:      time.Now().UTC().Add(-25 * time.Hour),
		ExpiresAt:      time.Now().UTC().Add(-1 * time.Minute), // already expired
	}
	if err := d.idempRepo.Create(context.Background(), expiredRecord); err != nil {
		t.Fatalf("seed expired record: %v", err)
	}

	// Request with a *different* order ID to avoid DUPLICATE_ORDER from txRepo.
	w := doIdempotentRequest(d.router, map[string]any{
		"merchant_order_id": "ORDER-IH-EXP-NEW",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "IH-KEY-EXPIRED")

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for expired key, got %d\nbody: %s", w.Code, w.Body)
	}

	// New transaction — different from the expired one.
	newTxID := getStr(t, parseBody(t, w), "data", "transaction_id")
	if newTxID == storedTxID.String() {
		t.Error("expired key: expected a new transaction, got the old stored one")
	}
	// Provider called once for the new request.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("expected 1 provider call for expired key, got %d", d.provider.CreatePaymentCallCount())
	}
}

// ─── Concurrent requests ──────────────────────────────────────────────────────

// TestIdempotency_ConcurrentSameKey verifies that two concurrent requests with
// the same key result in exactly one provider call and one transaction.
//
// One goroutine will win the reservation (Reserve returns true) and complete
// the payment. The other will either receive IDEMPOTENCY_REQUEST_IN_PROGRESS
// (if it reads the PROCESSING record before completion) or the completed
// response (if it reads after completion). Both outcomes are acceptable.
// What is NOT acceptable: two transactions or two provider calls.
func TestIdempotency_ConcurrentSameKey(t *testing.T) {
	d := newIdempotencyTestRouter(t)

	body := map[string]any{
		"merchant_order_id": "ORDER-IH-CONC",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}

	type result struct {
		code int
		body map[string]any
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := doIdempotentRequest(d.router, body, "IH-KEY-CONC")
			results <- result{code: w.Code, body: parseBody(t, w)}
		}()
	}
	wg.Wait()
	close(results)

	var codes []int
	for r := range results {
		codes = append(codes, r.code)
		// Both responses must be either 201 (success/replay) or 409 (in-progress).
		if r.code != http.StatusCreated && r.code != http.StatusConflict {
			t.Errorf("concurrent request: unexpected status %d", r.code)
		}
		if r.code == http.StatusConflict {
			code := getStr(t, r.body, "error", "code")
			if code != string(response.CodeIdempotencyInProgress) && code != string(response.CodeIdempotencyKeyReused) {
				t.Errorf("concurrent 409 should be IDEMPOTENCY_REQUEST_IN_PROGRESS, got %s", code)
			}
		}
	}
	_ = codes

	// Provider must have been called exactly once.
	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("concurrent requests: expected exactly 1 provider call, got %d",
			d.provider.CreatePaymentCallCount())
	}

	// Exactly one transaction must exist.
	txCount := 0
	for _, tx := range d.txRepo.txs {
		if tx.MerchantID == d.merchant.ID {
			txCount++
		}
	}
	if txCount != 1 {
		t.Errorf("concurrent requests: expected 1 transaction, found %d", txCount)
	}
}

// TestIdempotency_ConcurrentHighContention stress-tests with 10 concurrent
// goroutines all using the same key. Exactly one provider call is required.
func TestIdempotency_ConcurrentHighContention(t *testing.T) {
	d := newIdempotencyTestRouter(t)

	body := map[string]any{
		"merchant_order_id": "ORDER-IH-HC",
		"amount":            50000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := doIdempotentRequest(d.router, body, "IH-KEY-HC")
			if w.Code != http.StatusCreated && w.Code != http.StatusConflict {
				errCh <- errors.New("unexpected status: " + w.Body.String())
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	if d.provider.CreatePaymentCallCount() != 1 {
		t.Errorf("high contention: expected 1 provider call, got %d",
			d.provider.CreatePaymentCallCount())
	}

	txCount := 0
	for _, tx := range d.txRepo.txs {
		if tx.MerchantID == d.merchant.ID {
			txCount++
		}
	}
	if txCount != 1 {
		t.Errorf("high contention: expected 1 transaction, found %d", txCount)
	}
}
