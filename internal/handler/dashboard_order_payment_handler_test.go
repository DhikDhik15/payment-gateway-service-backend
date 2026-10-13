package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type stubOrderPaymentService struct {
	result      *model.CreatePaymentResponse
	err         error
	calls       int
	merchantID  uuid.UUID
	orderID     uuid.UUID
	request     model.CreateOrderPaymentRequest
	idempotency string

	history      []*model.PaymentResponse
	historyErr   error
	historyCalls int
}

func (s *stubOrderPaymentService) CreatePaymentForOrder(_ context.Context, merchantID, orderID uuid.UUID, request model.CreateOrderPaymentRequest, key string) (*model.CreatePaymentResponse, error) {
	s.calls++
	s.merchantID, s.orderID, s.request, s.idempotency = merchantID, orderID, request, key
	return s.result, s.err
}

func (s *stubOrderPaymentService) ListPaymentsForOrder(_ context.Context, merchantID, orderID uuid.UUID) ([]*model.PaymentResponse, error) {
	s.historyCalls++
	s.merchantID, s.orderID = merchantID, orderID
	return s.history, s.historyErr
}

func newOrderPaymentRouter(svc service.OrderPaymentService, user *model.MerchantUser) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDashboardOrderPaymentHandler(svc)
	g := r.Group("/api/v1/dashboard", func(c *gin.Context) {
		if user != nil {
			c.Set(model.ContextKeyDashboardUser, user)
		}
		c.Next()
	})
	g.POST("/orders/:order_id/payments", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.CreatePayment)
	g.GET("/orders/:order_id/payments", h.ListPayments)
	r.POST("/raw/orders/:order_id/payments", h.CreatePayment)
	return r
}

func orderPaymentDo(r *gin.Engine, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/orders/"+uuid.NewString()+"/payments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDashboardOrderPaymentUsesJWTMerchantAndOnlyMethod(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleOwner)
	orderID := uuid.New()
	svc := &stubOrderPaymentService{result: &model.CreatePaymentResponse{TransactionID: uuid.New(), OrderID: &orderID, Status: model.TransactionStatusPending, Amount: 24690, Currency: "IDR", PaymentMethod: "QRIS"}}
	r := newOrderPaymentRouter(svc, user)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/orders/"+orderID.String()+"/payments", strings.NewReader(`{"payment_method":"QRIS"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "stable-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if svc.calls != 1 || svc.merchantID != user.MerchantID || svc.orderID != orderID || svc.request.PaymentMethod != "QRIS" || svc.idempotency != "stable-key" {
		t.Fatalf("service input was not scoped to JWT/order: %+v", svc)
	}
	if strings.Contains(w.Body.String(), "merchant_id") {
		t.Fatalf("response unexpectedly exposes merchant identity: %s", w.Body.String())
	}
}

func TestDashboardOrderPaymentRejectsClientPricingFields(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleOwner)
	svc := &stubOrderPaymentService{result: &model.CreatePaymentResponse{TransactionID: uuid.New()}}
	r := newOrderPaymentRouter(svc, user)
	for _, body := range []string{
		`{"payment_method":"QRIS","amount":1}`,
		`{"payment_method":"QRIS","currency":"USD"}`,
		`{"payment_method":"QRIS","merchant_id":"` + uuid.NewString() + `"}`,
		`{"payment_method":"QRIS","total_amount":1}`,
		`{"payment_method":"QRIS","subtotal":1}`,
		`{"payment_method":"QRIS","unit_price":1}`,
		`{"payment_method":"QRIS","merchant_order_id":"client-order"}`,
	} {
		w := orderPaymentDo(r, body, "key")
		if w.Code != http.StatusBadRequest || svc.calls != 0 {
			t.Fatalf("body=%s status=%d calls=%d", body, w.Code, svc.calls)
		}
	}
}

func TestDashboardOrderPaymentRoleAndOrderIsolation(t *testing.T) {
	viewer := orderTestUser(model.DashboardUserRoleViewer)
	svc := &stubOrderPaymentService{result: &model.CreatePaymentResponse{TransactionID: uuid.New()}}
	r := newOrderPaymentRouter(svc, viewer)
	if w := orderPaymentDo(r, `{"payment_method":"QRIS"}`, "key"); w.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", w.Code, w.Body.String())
	}

	owner := orderTestUser(model.DashboardUserRoleOwner)
	svc.err = repository.ErrOrderNotFound
	r = newOrderPaymentRouter(svc, owner)
	orderID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/orders/"+orderID.String()+"/payments", strings.NewReader(`{"payment_method":"QRIS"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign/missing Order status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Error responseError `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != string(response.CodeResourceNotFound) {
		t.Fatalf("unexpected not-found envelope: %s (%v)", w.Body.String(), err)
	}
}

type responseError struct {
	Code string `json:"code"`
}

func TestDashboardOrderPaymentMapsConflictAndRequiresKey(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleAdmin)
	svc := &stubOrderPaymentService{err: service.ErrOrderPaymentConflict}
	r := newOrderPaymentRouter(svc, owner)
	if w := orderPaymentDo(r, `{"payment_method":"QRIS"}`, "key"); w.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", w.Code, w.Body.String())
	}
	if w := orderPaymentDo(r, `{"payment_method":"QRIS"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", w.Code, w.Body.String())
	}

	svc.err = errors.New("unexpected")
	if w := orderPaymentDo(r, `{"payment_method":"QRIS"}`, "key-2"); w.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected service error status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestDashboardOrderPaymentErrorMappings covers the service error -> HTTP status
// and error-code mappings that the happy-path and generic conflict tests miss:
// idempotency key reuse (409), unsupported payment method (400) and unsupported
// order currency (400).
func TestDashboardOrderPaymentErrorMappings(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"key reused with different payload", service.ErrIdempotencyKeyReused, http.StatusConflict, string(response.CodeIdempotencyKeyReused)},
		{"wrapped key reuse", fmt.Errorf("tx: %w", service.ErrIdempotencyKeyReused), http.StatusConflict, string(response.CodeIdempotencyKeyReused)},
		{"key already in progress", service.ErrIdempotencyInProgress, http.StatusConflict, string(response.CodeIdempotencyInProgress)},
		{"unsupported payment method", service.ErrInvalidPaymentMethod, http.StatusBadRequest, string(response.CodeInvalidPaymentMethod)},
		{"unsupported order currency", service.ErrInvalidCurrency, http.StatusBadRequest, string(response.CodeInvalidCurrency)},
		{"provider timeout", service.ErrProviderTimeout, http.StatusGatewayTimeout, ""},
		{"provider failure", service.ErrProviderFailure, http.StatusBadGateway, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubOrderPaymentService{err: tc.err}
			r := newOrderPaymentRouter(svc, owner)
			w := orderPaymentDo(r, `{"payment_method":"QRIS"}`, "key")
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantCode == "" {
				return
			}
			var envelope struct {
				Error responseError `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != tc.wantCode {
				t.Fatalf("code=%q want=%q body=%s (%v)", envelope.Error.Code, tc.wantCode, w.Body.String(), err)
			}
		})
	}
}

func TestDashboardOrderPaymentUnauthenticatedGuard(t *testing.T) {
	svc := &stubOrderPaymentService{}
	r := newOrderPaymentRouter(svc, nil)
	w := orderPaymentDo(r, `{"payment_method":"QRIS"}`, "key")
	if w.Code != http.StatusUnauthorized || svc.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.calls, w.Body.String())
	}
}

func orderPaymentHistoryDo(r *gin.Engine, orderID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/orders/"+orderID+"/payments", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func orderPaymentHistoryItem(txID, orderID uuid.UUID, status model.TransactionStatus) *model.PaymentResponse {
	return &model.PaymentResponse{
		TransactionID:   txID,
		OrderID:         &orderID,
		MerchantOrderID: "order-" + orderID.String() + "-attempt",
		Amount:          30000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
		Status:          status,
	}
}

func TestDashboardOrderPaymentHistoryHappyPath(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	orderID := uuid.New()
	newest := orderPaymentHistoryItem(uuid.New(), orderID, model.TransactionStatusPending)
	oldest := orderPaymentHistoryItem(uuid.New(), orderID, model.TransactionStatusFailed)
	svc := &stubOrderPaymentService{history: []*model.PaymentResponse{newest, oldest}}
	r := newOrderPaymentRouter(svc, owner)

	w := orderPaymentHistoryDo(r, orderID.String())
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if svc.historyCalls != 1 || svc.merchantID != owner.MerchantID || svc.orderID != orderID {
		t.Fatalf("service input was not scoped to JWT/order: calls=%d merchant=%v order=%v", svc.historyCalls, svc.merchantID, svc.orderID)
	}
	if svc.calls != 0 {
		t.Fatalf("history read must not create payments, create calls=%d", svc.calls)
	}
	var envelope struct {
		Success bool                    `json:"success"`
		Data    []model.PaymentResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || !envelope.Success {
		t.Fatalf("unexpected envelope: %s (%v)", w.Body.String(), err)
	}
	if len(envelope.Data) != 2 || envelope.Data[0].TransactionID != newest.TransactionID || envelope.Data[1].TransactionID != oldest.TransactionID {
		t.Fatalf("history ordering/content wrong: %s", w.Body.String())
	}
	for _, item := range envelope.Data {
		if item.OrderID == nil || *item.OrderID != orderID {
			t.Fatalf("history item missing order link: %+v", item)
		}
		if item.MerchantOrderID == "" || item.Amount != 30000 || item.Currency != "IDR" || item.PaymentMethod != "QRIS" {
			t.Fatalf("history item missing payment fields: %+v", item)
		}
	}
}

func TestDashboardOrderPaymentHistoryEmpty(t *testing.T) {
	viewer := orderTestUser(model.DashboardUserRoleViewer)
	svc := &stubOrderPaymentService{history: []*model.PaymentResponse{}}
	r := newOrderPaymentRouter(svc, viewer)
	w := orderPaymentHistoryDo(r, uuid.NewString())
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Success bool                     `json:"success"`
		Data    []*model.PaymentResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || !envelope.Success {
		t.Fatalf("unexpected envelope: %s (%v)", w.Body.String(), err)
	}
	if envelope.Data == nil || len(envelope.Data) != 0 {
		t.Fatalf("empty history must be [], got %s", w.Body.String())
	}
}

func TestDashboardOrderPaymentHistoryNotFound(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	svc := &stubOrderPaymentService{historyErr: repository.ErrOrderNotFound}
	r := newOrderPaymentRouter(svc, owner)
	w := orderPaymentHistoryDo(r, uuid.NewString())
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign/missing Order status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Error responseError `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != string(response.CodeResourceNotFound) {
		t.Fatalf("unexpected not-found envelope: %s (%v)", w.Body.String(), err)
	}
}

func TestDashboardOrderPaymentHistoryGuards(t *testing.T) {
	svc := &stubOrderPaymentService{}
	r := newOrderPaymentRouter(svc, nil)
	if w := orderPaymentHistoryDo(r, uuid.NewString()); w.Code != http.StatusUnauthorized || svc.historyCalls != 0 {
		t.Fatalf("unauthenticated status=%d calls=%d body=%s", w.Code, svc.historyCalls, w.Body.String())
	}

	owner := orderTestUser(model.DashboardUserRoleOwner)
	r = newOrderPaymentRouter(&stubOrderPaymentService{}, owner)
	if w := orderPaymentHistoryDo(r, "not-a-uuid"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid order id status=%d body=%s", w.Code, w.Body.String())
	}

	svcErr := &stubOrderPaymentService{historyErr: errors.New("db down")}
	r = newOrderPaymentRouter(svcErr, owner)
	if w := orderPaymentHistoryDo(r, uuid.NewString()); w.Code != http.StatusInternalServerError {
		t.Fatalf("service error status=%d body=%s", w.Code, w.Body.String())
	}
}
