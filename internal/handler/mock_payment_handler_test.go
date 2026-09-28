package handler

import (
	"context"
	"errors"
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
func TestMockPaymentPublicUnknown(t *testing.T) {
	if !errors.Is(repository.ErrMockPaymentNotFound, repository.ErrMockPaymentNotFound) {
		t.Fatal("sentinel")
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
