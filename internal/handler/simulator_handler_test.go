package handler_test

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
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── Phase 8D.1 simulator containment ─────────────────────────────────────────
//
// The simulator is authenticated (X-API-Key in production wiring), allowlisted
// to SIMULATOR_MERCHANT_ID, and every read/write is scoped to the
// AUTHENTICATED merchant from context — never to a client-supplied
// merchant_id. These tests encode the cross-tenant isolation requirements.

// stubSimulatorPaymentSvc is a tenant-scoped PaymentService stub: GetPayment
// honours the merchant_id predicate exactly like the PostgreSQL repository
// (foreign transactions are indistinguishable from missing ones).
type stubSimulatorPaymentSvc struct {
	mu                sync.Mutex
	payments          map[uuid.UUID]stubSimPayment // transactionID → payment
	createMerchantIDs []uuid.UUID                  // merchant IDs seen by CreatePaymentWithIdempotency
	lastReq           model.CreatePaymentRequest
	createResp        *model.CreatePaymentResponse
	createErr         error
}

type stubSimPayment struct {
	merchantID uuid.UUID
	resp       model.PaymentResponse
}

func newStubSimulatorPaymentSvc() *stubSimulatorPaymentSvc {
	return &stubSimulatorPaymentSvc{payments: make(map[uuid.UUID]stubSimPayment)}
}

// addPayment seeds a payment owned by merchantID.
func (s *stubSimulatorPaymentSvc) addPayment(txID, merchantID uuid.UUID, status model.TransactionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[txID] = stubSimPayment{
		merchantID: merchantID,
		resp: model.PaymentResponse{
			TransactionID:   txID,
			MerchantOrderID: "ORDER-" + txID.String()[:8],
			Amount:          1000,
			Currency:        "IDR",
			PaymentMethod:   "QRIS",
			Status:          status,
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		},
	}
}

// statusOf reports the stubbed status (for "must remain unchanged" assertions).
func (s *stubSimulatorPaymentSvc) statusOf(txID uuid.UUID) model.TransactionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payments[txID].resp.Status
}

func (s *stubSimulatorPaymentSvc) CreatePayment(context.Context, uuid.UUID, model.CreatePaymentRequest) (*model.CreatePaymentResponse, error) {
	panic("not used in simulator tests")
}

func (s *stubSimulatorPaymentSvc) CreatePaymentWithIdempotency(_ context.Context, merchantID uuid.UUID, req model.CreatePaymentRequest, _ string) (*model.CreatePaymentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createMerchantIDs = append(s.createMerchantIDs, merchantID)
	s.lastReq = req
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.createResp != nil {
		return s.createResp, nil
	}
	return &model.CreatePaymentResponse{
		TransactionID:   uuid.New(),
		MerchantOrderID: req.MerchantOrderID,
		Amount:          req.Amount,
		Currency:        req.Currency,
		PaymentMethod:   req.PaymentMethod,
		Provider:        "MOCK",
		Status:          model.TransactionStatusPending,
		CreatedAt:       time.Now().UTC(),
	}, nil
}

// GetPayment enforces the tenant predicate: wrong merchant → ErrTransactionNotFound.
func (s *stubSimulatorPaymentSvc) GetPayment(_ context.Context, merchantID, transactionID uuid.UUID) (*model.PaymentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[transactionID]
	if !ok || p.merchantID != merchantID {
		return nil, repository.ErrTransactionNotFound
	}
	cp := p.resp
	return &cp, nil
}

func (s *stubSimulatorPaymentSvc) CancelPayment(context.Context, uuid.UUID, uuid.UUID) (*model.PaymentResponse, error) {
	panic("not used in simulator tests")
}

func (s *stubSimulatorPaymentSvc) ListPayments(context.Context, uuid.UUID, model.TransactionListFilter) (*model.ListPaymentsResult, error) {
	panic("not used in simulator tests")
}

var _ service.PaymentService = (*stubSimulatorPaymentSvc)(nil)

// stubSimulatorWebhookSvc records ProcessWebhook calls (never logs payloads).
type stubSimulatorWebhookSvc struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *stubSimulatorWebhookSvc) ProcessWebhook(_ context.Context, _ string, _ []byte, _ string) (*service.WebhookProcessResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &service.WebhookProcessResult{}, nil
}

func (s *stubSimulatorWebhookSvc) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

var _ service.WebhookService = (*stubSimulatorWebhookSvc)(nil)

// ─── router factory ───────────────────────────────────────────────────────────

// newSimulatorRouter mounts all four simulator routes. authenticatedMerchant
// simulates middleware.Auth: nil means "no authenticated merchant in context"
// (only reachable when a route is mistakenly registered without Auth).
func newSimulatorRouter(h *handler.SimulatorHandler, authenticatedMerchant *uuid.UUID) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if authenticatedMerchant != nil {
			c.Set(model.ContextKeyMerchant, &model.Merchant{
				ID:     *authenticatedMerchant,
				Status: model.MerchantStatusActive,
			})
		}
		c.Next()
	})
	r.POST("/api/v1/simulator/payments", h.CreatePayment)
	r.GET("/api/v1/simulator/payments/:payment_id", h.GetPayment)
	r.POST("/api/v1/simulator/payments/:payment_id/success", h.SimulateSuccess)
	r.POST("/api/v1/simulator/payments/:payment_id/fail", h.SimulateFailure)
	return r
}

func simRequest(r *gin.Engine, method, url string, body map[string]any, idem string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	r.ServeHTTP(w, req)
	return w
}

func simErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse response %q: %v", w.Body.String(), err)
	}
	return body.Error.Code
}

var simPaymentID = "9dbaf7dc-7b19-482a-a925-fb8dc51df70c"

// ─── disabled behaviour (regression: PAYMENT_SIMULATOR_ENABLED=false) ────────

func TestSimulatorHandler_Disabled(t *testing.T) {
	// No services are wired: the enabled check must abort before any work.
	h := handler.NewSimulatorHandler(nil, nil, "secret", false, uuid.Nil)
	r := newSimulatorRouter(h, nil)

	tests := []struct {
		method string
		url    string
	}{
		{"POST", "/api/v1/simulator/payments"},
		{"GET", "/api/v1/simulator/payments/" + simPaymentID},
		{"POST", "/api/v1/simulator/payments/" + simPaymentID + "/success"},
		{"POST", "/api/v1/simulator/payments/" + simPaymentID + "/fail"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.url, func(t *testing.T) {
			w := simRequest(r, tc.method, tc.url, map[string]any{
				"merchant_order_id": "ORDER-SIM-001",
				"amount":            1000,
				"currency":          "IDR",
				"payment_method":    "QRIS",
			}, "idem-disabled")

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden when disabled, got %d", w.Code)
			}
			if simErrCode(t, w) != "FORBIDDEN" {
				t.Errorf("expected FORBIDDEN code, got %s", simErrCode(t, w))
			}
		})
	}
}

func TestSimulatorHandler_Disabled_EvenForConfiguredMerchant(t *testing.T) {
	// Even the allowlisted merchant is rejected when the simulator is off —
	// the disabled check runs before authentication-context checks.
	configured := uuid.New()
	paySvc := newStubSimulatorPaymentSvc()
	h := handler.NewSimulatorHandler(paySvc, &stubSimulatorWebhookSvc{}, "secret", false, configured)
	r := newSimulatorRouter(h, &configured)

	w := simRequest(r, "POST", "/api/v1/simulator/payments", map[string]any{
		"merchant_order_id": "ORDER-SIM-OFF",
		"amount":            1000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "idem-off")

	if w.Code != http.StatusForbidden {
		t.Fatalf("configured merchant with disabled simulator: got %d, want 403", w.Code)
	}
	if len(paySvc.createMerchantIDs) != 0 {
		t.Error("payment service must not be called when the simulator is disabled")
	}
}

// ─── authentication / allowlist ───────────────────────────────────────────────

func TestSimulatorHandler_NoAuthenticatedMerchantContext(t *testing.T) {
	// A route mistakenly registered without middleware.Auth fails closed with
	// 500 (wiring bug) — never proceeds anonymously.
	configured := uuid.New()
	paySvc := newStubSimulatorPaymentSvc()
	h := handler.NewSimulatorHandler(paySvc, &stubSimulatorWebhookSvc{}, "secret", true, configured)
	r := newSimulatorRouter(h, nil)

	w := simRequest(r, "POST", "/api/v1/simulator/payments", map[string]any{
		"merchant_order_id": "ORDER-SIM-NOAUTH",
		"amount":            1000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "idem-noauth")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("missing authenticated merchant: got %d, want 500", w.Code)
	}
	if len(paySvc.createMerchantIDs) != 0 {
		t.Error("payment service must not be called without an authenticated merchant")
	}
}

func TestSimulatorHandler_ForeignMerchantRejected(t *testing.T) {
	// A DIFFERENT (valid, active) merchant's key must not drive the simulator:
	// allowlist rejection on every route, service never called.
	configured := uuid.New()
	foreign := uuid.New()
	paySvc := newStubSimulatorPaymentSvc()
	webhookSvc := &stubSimulatorWebhookSvc{}
	h := handler.NewSimulatorHandler(paySvc, webhookSvc, "secret", true, configured)
	r := newSimulatorRouter(h, &foreign)

	txID := uuid.New()
	paySvc.addPayment(txID, configured, model.TransactionStatusPending)

	tests := []struct {
		method string
		url    string
	}{
		{"POST", "/api/v1/simulator/payments"},
		{"GET", "/api/v1/simulator/payments/" + txID.String()},
		{"POST", "/api/v1/simulator/payments/" + txID.String() + "/success"},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.url, func(t *testing.T) {
			w := simRequest(r, tc.method, tc.url, map[string]any{
				"merchant_order_id": "ORDER-SIM-FOREIGN",
				"amount":            1000,
				"currency":          "IDR",
				"payment_method":    "QRIS",
			}, "idem-foreign-"+tc.method)
			if w.Code != http.StatusForbidden {
				t.Errorf("foreign merchant: got %d, want 403", w.Code)
			}
		})
	}

	if len(paySvc.createMerchantIDs) != 0 {
		t.Error("payment service must not be called for a non-allowlisted merchant")
	}
	if webhookSvc.callCount() != 0 {
		t.Error("webhook service must not be called for a non-allowlisted merchant")
	}
	// The configured merchant's transaction is untouched.
	if got := paySvc.statusOf(txID); got != model.TransactionStatusPending {
		t.Errorf("transaction status changed to %s — foreign merchant must not mutate it", got)
	}
}

// ─── create ───────────────────────────────────────────────────────────────────

func TestSimulatorHandler_CreatePayment_MissingIdempotency(t *testing.T) {
	configured := uuid.New()
	h := handler.NewSimulatorHandler(newStubSimulatorPaymentSvc(), &stubSimulatorWebhookSvc{}, "secret", true, configured)
	r := newSimulatorRouter(h, &configured)

	w := simRequest(r, "POST", "/api/v1/simulator/payments", map[string]any{
		"merchant_order_id": "ORDER-SIM-001",
		"amount":            100000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}, "")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["success"] != false {
		t.Errorf("expected success to be false, got %v", body["success"])
	}
}

func TestSimulatorHandler_CreatePayment_UsesAuthenticatedMerchant(t *testing.T) {
	configured := uuid.MustParse("755ef533-1b2e-4dfd-a0d2-7ab36efbd389")
	other := uuid.MustParse("ab2c414f-9f14-4369-adf2-c74ea5911d6c")
	paySvc := newStubSimulatorPaymentSvc()
	h := handler.NewSimulatorHandler(paySvc, &stubSimulatorWebhookSvc{}, "secret", true, configured)
	r := newSimulatorRouter(h, &configured)

	w := simRequest(r, "POST", "/api/v1/simulator/payments", map[string]any{
		"merchant_order_id": "ORDER-SIM-CFG-001",
		"amount":            1000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
		// client-supplied merchant_id must be ignored (not in DTO; must not switch tenant)
		"merchant_id": other.String(),
	}, "idem-cfg-1")

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if len(paySvc.createMerchantIDs) != 1 {
		t.Fatalf("expected exactly 1 create call, got %d", len(paySvc.createMerchantIDs))
	}
	if got := paySvc.createMerchantIDs[0]; got != configured {
		t.Errorf("create targeted merchant %s, want authenticated/configured %s", got, configured)
	}
	if paySvc.createMerchantIDs[0] == other {
		t.Error("must not use client-supplied merchant_id")
	}
}

// ─── tenant-scoped reads / transitions ───────────────────────────────────────

func TestSimulatorHandler_GetPayment_TenantIsolation(t *testing.T) {
	configured := uuid.New() // merchant A (allowlisted simulator merchant)
	merchantB := uuid.New()  // merchant B
	paySvc := newStubSimulatorPaymentSvc()
	h := handler.NewSimulatorHandler(paySvc, &stubSimulatorWebhookSvc{}, "secret", true, configured)

	txA := uuid.New()
	txB := uuid.New()
	paySvc.addPayment(txA, configured, model.TransactionStatusPending)
	paySvc.addPayment(txB, merchantB, model.TransactionStatusPending)

	r := newSimulatorRouter(h, &configured)

	// Merchant A reads its own payment → 200.
	w := simRequest(r, "GET", "/api/v1/simulator/payments/"+txA.String(), nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("own payment: got %d body=%s, want 200", w.Code, w.Body.String())
	}

	// Merchant A reads merchant B's payment → 404 (no existence leak).
	w = simRequest(r, "GET", "/api/v1/simulator/payments/"+txB.String(), nil, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign payment: got %d, want 404", w.Code)
	}
	if got := simErrCode(t, w); got != "TRANSACTION_NOT_FOUND" {
		t.Errorf("foreign payment code: got %s, want TRANSACTION_NOT_FOUND", got)
	}
}

func TestSimulatorHandler_SimulateSuccess_TenantIsolation(t *testing.T) {
	configured := uuid.New() // merchant A
	merchantB := uuid.New()  // merchant B
	paySvc := newStubSimulatorPaymentSvc()
	webhookSvc := &stubSimulatorWebhookSvc{}
	h := handler.NewSimulatorHandler(paySvc, webhookSvc, "secret", true, configured)

	txA := uuid.New()
	txB := uuid.New()
	paySvc.addPayment(txA, configured, model.TransactionStatusPending)
	paySvc.addPayment(txB, merchantB, model.TransactionStatusPending)

	r := newSimulatorRouter(h, &configured)

	// 1) Merchant A tries to flip merchant B's payment → 404, no side effects.
	w := simRequest(r, "POST", "/api/v1/simulator/payments/"+txB.String()+"/success", nil, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign payment transition: got %d, want 404", w.Code)
	}
	if webhookSvc.callCount() != 0 {
		t.Errorf("webhook service called %d times for a foreign payment — want 0", webhookSvc.callCount())
	}
	if got := paySvc.statusOf(txB); got != model.TransactionStatusPending {
		t.Errorf("merchant B payment changed to %s — must remain %s", got, model.TransactionStatusPending)
	}

	// 2) Merchant A flips its OWN payment → 200, webhook processing ran once.
	w = simRequest(r, "POST", "/api/v1/simulator/payments/"+txA.String()+"/success", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("own payment transition: got %d body=%s, want 200", w.Code, w.Body.String())
	}
	if webhookSvc.callCount() != 1 {
		t.Errorf("webhook service calls: got %d, want 1", webhookSvc.callCount())
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if body.Data.Status != "PAID" {
		t.Errorf("status: got %q, want PAID", body.Data.Status)
	}
}

func TestSimulatorHandler_TransitionErrorDoesNotLeakInternalDetails(t *testing.T) {
	merchantID := uuid.New()
	paymentID := uuid.New()
	paymentSvc := newStubSimulatorPaymentSvc()
	paymentSvc.addPayment(paymentID, merchantID, model.TransactionStatusPending)
	webhookSvc := &stubSimulatorWebhookSvc{err: errors.New("pq: SQLSTATE 42P01 relation secret_table does not exist")}
	h := handler.NewSimulatorHandler(paymentSvc, webhookSvc, "mock-secret", true, merchantID)
	r := newSimulatorRouter(h, &merchantID)

	w := simRequest(r, http.MethodPost, "/api/v1/simulator/payments/"+paymentID.String()+"/success", nil, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	body := w.Body.String()
	for _, leak := range []string{"SQLSTATE", "relation secret_table", "pq:"} {
		if strings.Contains(body, leak) {
			t.Fatalf("simulator response leaked %q: %s", leak, body)
		}
	}
}

func TestSimulatorHandler_SimulateSuccess_TerminalStateRejected(t *testing.T) {
	configured := uuid.New()
	paySvc := newStubSimulatorPaymentSvc()
	webhookSvc := &stubSimulatorWebhookSvc{}
	h := handler.NewSimulatorHandler(paySvc, webhookSvc, "secret", true, configured)

	txDone := uuid.New()
	paySvc.addPayment(txDone, configured, model.TransactionStatusPaid)

	r := newSimulatorRouter(h, &configured)

	w := simRequest(r, "POST", "/api/v1/simulator/payments/"+txDone.String()+"/fail", nil, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("terminal payment: got %d, want 400", w.Code)
	}
	if got := simErrCode(t, w); got != "INVALID_TRANSACTION_STATE" {
		t.Errorf("code: got %s, want INVALID_TRANSACTION_STATE", got)
	}
	if webhookSvc.callCount() != 0 {
		t.Errorf("webhook service must not run for a terminal payment, calls=%d", webhookSvc.callCount())
	}
}
