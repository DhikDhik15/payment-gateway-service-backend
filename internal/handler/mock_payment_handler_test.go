package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
)

type mockPublicStore struct{ p *model.MockPayment }

func (s *mockPublicStore) Create(context.Context, *model.MockPayment) error { return nil }
func (s *mockPublicStore) FindByPublicID(_ context.Context, id string) (*model.MockPayment, error) {
	if s.p == nil || id != s.p.PublicID {
		return nil, repository.ErrMockPaymentNotFound
	}
	return s.p, nil
}
func (s *mockPublicStore) FindByGatewayTransactionID(_ context.Context, id string) (*model.MockPayment, error) {
	if s.p == nil || id != s.p.GatewayTransactionID {
		return nil, repository.ErrMockPaymentNotFound
	}
	return s.p, nil
}
func (s *mockPublicStore) Transition(_ context.Context, id string, to model.TransactionStatus) (*model.MockPayment, error) {
	if s.p == nil || id != s.p.PublicID {
		return nil, repository.ErrMockPaymentNotFound
	}
	if !s.p.ExpiredAt.After(time.Now()) {
		return nil, repository.ErrMockPaymentExpired
	}
	if s.p.Status == to {
		return s.p, nil
	}
	if s.p.Status != model.TransactionStatusPending {
		return nil, repository.ErrMockPaymentInvalidState
	}
	s.p.Status = to
	now := time.Now()
	s.p.TerminalAt = &now
	return s.p, nil
}

type mockPublicWebhook struct{ calls int }

func (w *mockPublicWebhook) ProcessWebhook(context.Context, string, []byte, string) (*service.WebhookProcessResult, error) {
	w.calls++
	return &service.WebhookProcessResult{Status: model.WebhookEventStatusProcessed}, nil
}
func TestMockPaymentPublicReadAndTransition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &model.MockPayment{PublicID: "MOCK-TXN-token", ProviderTransactionID: "MOCK-TXN-token", MerchantOrderID: "order-public", Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS", Status: model.TransactionStatusPending, ExpiredAt: time.Now().Add(time.Hour)}
	store := &mockPublicStore{p: p}
	wh := &mockPublicWebhook{}
	h := NewMockPaymentHandler(store, wh, "secret")
	r := gin.New()
	r.GET("/pay/:identifier", h.Get)
	r.POST("/api/v1/mock-payments/:identifier/success", h.Success)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pay/MOCK-TXN-token", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d", w.Code)
	}
	if body := w.Body.String(); !(contains(body, "qris_payload") && !contains(body, "gateway_transaction_id")) {
		t.Fatalf("unsafe/incomplete public response: %s", body)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/mock-payments/MOCK-TXN-token/success", nil))
	if w.Code != http.StatusOK || p.Status != model.TransactionStatusPaid || wh.calls != 1 {
		t.Fatalf("success code=%d status=%s calls=%d", w.Code, p.Status, wh.calls)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/mock-payments/MOCK-TXN-token/success", nil))
	if w.Code != http.StatusOK || wh.calls != 1 {
		t.Fatalf("duplicate success code=%d calls=%d", w.Code, wh.calls)
	}
}
func TestMockPaymentPublicUnknownIdentifier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &mockPublicStore{p: &model.MockPayment{
		PublicID:              "MOCK-TXN-known",
		ProviderTransactionID: "MOCK-TXN-known",
		Status:                model.TransactionStatusPending,
		ExpiredAt:             time.Now().Add(time.Hour),
	}}
	h := NewMockPaymentHandler(store, &mockPublicWebhook{}, "secret")
	r := gin.New()
	r.GET("/api/v1/mock-payments/:identifier", h.Get)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mock-payments/MOCK-TXN-unknown", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown identifier status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if body := w.Body.String(); !contains(body, `"code":"TRANSACTION_NOT_FOUND"`) {
		t.Fatalf("unknown identifier body = %s, want TRANSACTION_NOT_FOUND", body)
	}
}

func TestMockPaymentPublicResponseSchema(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expiresAt := time.Now().UTC().Add(45 * time.Minute).Truncate(time.Second)
	p := &model.MockPayment{
		PublicID:              "MOCK-TXN-schema",
		ProviderTransactionID: "MOCK-TXN-schema",
		GatewayTransactionID:  "gateway-internal-id",
		MerchantOrderID:       "ORDER-SCHEMA",
		Amount:                75000,
		Currency:              "IDR",
		PaymentMethod:         "QRIS",
		Status:                model.TransactionStatusPending,
		ExpiredAt:             expiresAt,
	}
	h := NewMockPaymentHandler(&mockPublicStore{p: p}, &mockPublicWebhook{}, "secret")
	r := gin.New()
	r.GET("/api/v1/mock-payments/:identifier", h.Get)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mock-payments/"+p.PublicID, nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, want := range []string{
		`"reference":"ORDER-SCHEMA"`,
		`"amount":75000`,
		`"currency":"IDR"`,
		`"payment_method":"QRIS"`,
		`"status":"PENDING"`,
		`"provider_transaction_id":"MOCK-TXN-schema"`,
		`"qris_payload":"PAYGATE-MOCK-QRIS:MOCK-TXN-schema"`,
		`"expired_at":"` + expiresAt.Format(time.RFC3339) + `"`,
	} {
		if !contains(body, want) {
			t.Fatalf("public response missing %s\nbody: %s", want, body)
		}
	}
	if contains(body, "gateway-internal-id") || contains(body, "gateway_transaction_id") {
		t.Fatalf("public response leaked gateway internals: %s", body)
	}
}

func TestMockPaymentPublicNonQRISOmitsPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &model.MockPayment{
		PublicID:              "MOCK-TXN-nonqris",
		ProviderTransactionID: "MOCK-TXN-nonqris",
		MerchantOrderID:       "ORDER-NONQRIS",
		Amount:                25000,
		Currency:              "IDR",
		PaymentMethod:         "BANK_TRANSFER",
		Status:                model.TransactionStatusPaid,
		ExpiredAt:             time.Now().UTC().Add(time.Hour),
	}
	h := NewMockPaymentHandler(&mockPublicStore{p: p}, &mockPublicWebhook{}, "secret")
	r := gin.New()
	r.GET("/api/v1/mock-payments/:identifier", h.Get)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mock-payments/"+p.PublicID, nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	if contains(body, "qris_payload") {
		t.Fatalf("non-QRIS response must omit qris_payload: %s", body)
	}
	if !contains(body, `"status":"PAID"`) {
		t.Fatalf("terminal status not returned: %s", body)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
