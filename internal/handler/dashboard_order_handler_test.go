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
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── stub order service ──────────────────────────────────────────────────────

type stubOrderService struct {
	createOrder  *model.Order
	createReplay bool
	createErr    error
	createCalls  int
	lastMerchant uuid.UUID
	lastKey      string
	lastReq      model.CreateOrderRequest

	getOrder  *model.Order
	getErr    error
	getCalls  int
	lastOrder uuid.UUID

	listResult model.OrderListResult
	listErr    error
	listCalls  int
	lastPage   int
	lastLimit  int
}

func (s *stubOrderService) CreateOrder(_ context.Context, merchantID uuid.UUID, key string, req model.CreateOrderRequest) (*model.Order, bool, error) {
	s.createCalls++
	s.lastMerchant, s.lastKey, s.lastReq = merchantID, key, req
	if s.createErr != nil {
		return nil, false, s.createErr
	}
	return s.createOrder, s.createReplay, nil
}

func (s *stubOrderService) GetOrder(_ context.Context, merchantID, orderID uuid.UUID) (*model.Order, error) {
	s.getCalls++
	s.lastMerchant, s.lastOrder = merchantID, orderID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getOrder, nil
}

func (s *stubOrderService) ListOrders(_ context.Context, merchantID uuid.UUID, page, limit int) (model.OrderListResult, error) {
	s.listCalls++
	s.lastMerchant, s.lastPage, s.lastLimit = merchantID, page, limit
	if s.listErr != nil {
		return model.OrderListResult{}, s.listErr
	}
	r := s.listResult
	r.Page, r.Limit = page, limit
	return r, nil
}

// ─── router helpers ──────────────────────────────────────────────────────────

// newOrderRouter wires the order handler exactly as cmd/server/main.go does,
// substituting the JWT middleware with direct injection of the dashboard user.
// A nil user simulates a request that reached the handler unauthenticated.
func newOrderRouter(svc service.OrderService, user *model.MerchantUser) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDashboardOrderHandler(svc)
	g := r.Group("/api/v1/dashboard", func(c *gin.Context) {
		if user != nil {
			c.Set(model.ContextKeyDashboardUser, user)
		}
		c.Next()
	})
	g.POST("/orders", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.CreateOrder)
	g.GET("/orders", h.ListOrders)
	g.GET("/orders/:order_id", h.GetOrder)
	// Unwired variant to exercise the handler's own nil-caller guard.
	r.POST("/raw/orders", h.CreateOrder)
	r.GET("/raw/orders", h.ListOrders)
	r.GET("/raw/orders/:order_id", h.GetOrder)
	return r
}

func orderTestUser(role model.DashboardUserRole) *model.MerchantUser {
	return &model.MerchantUser{
		ID: uuid.New(), MerchantID: uuid.New(), Email: "u@test.com",
		Role: role, Status: model.DashboardUserStatusActive,
	}
}

func orderDo(r *gin.Engine, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type orderEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Details map[string]string `json:"details"`
	} `json:"error"`
	Meta struct {
		Page       *int   `json:"page"`
		Limit      *int   `json:"limit"`
		Total      *int64 `json:"total"`
		TotalPages *int   `json:"total_pages"`
	} `json:"meta"`
}

func decodeOrderEnvelope(t *testing.T, w *httptest.ResponseRecorder) orderEnvelope {
	t.Helper()
	var env orderEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return env
}

func sampleOrder(merchant uuid.UUID) *model.Order {
	pid := uuid.New()
	id := uuid.New()
	now := time.Now().UTC()
	return &model.Order{
		ID: id, MerchantID: merchant, Status: model.OrderStatusCreated, Currency: "IDR",
		Subtotal: 5000, TotalAmount: 5000, CreatedAt: now, UpdatedAt: now,
		IdempotencyKey: "secret-key", RequestHash: "deadbeef",
		Items: []model.OrderItem{{ID: uuid.New(), OrderID: id, MerchantID: merchant, ProductID: &pid,
			ProductName: "Coffee", Quantity: 2, UnitPrice: 2500, Subtotal: 5000, CreatedAt: now}},
	}
}

const orderPath = "/api/v1/dashboard/orders"

func validOrderBody() string {
	return fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":2}]}`, uuid.NewString())
}

// ─── CreateOrder ─────────────────────────────────────────────────────────────

func TestDashboardCreateOrder_Created(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleOwner)
	svc := &stubOrderService{createOrder: sampleOrder(user.MerchantID)}
	r := newOrderRouter(svc, user)

	pid := uuid.New()
	body := fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":3}]}`, pid)
	w := orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": "abc-123"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if svc.lastMerchant != user.MerchantID {
		t.Fatalf("merchant from JWT not propagated")
	}
	if svc.lastKey != "abc-123" {
		t.Fatalf("key = %q", svc.lastKey)
	}
	if len(svc.lastReq.Items) != 1 || svc.lastReq.Items[0].ProductID != pid || svc.lastReq.Items[0].Quantity != 3 {
		t.Fatalf("req = %+v", svc.lastReq)
	}
	env := decodeOrderEnvelope(t, w)
	if !env.Success {
		t.Fatalf("success=false")
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["total_amount"].(float64) != 5000 || data["status"] != model.OrderStatusCreated {
		t.Fatalf("data = %v", data)
	}
	for _, secret := range []string{"idempotency_key", "request_hash", "IdempotencyKey", "RequestHash"} {
		if _, ok := data[secret]; ok {
			t.Fatalf("response leaks %s", secret)
		}
	}
	if strings.Contains(w.Body.String(), "secret-key") || strings.Contains(w.Body.String(), "deadbeef") {
		t.Fatalf("response leaks idempotency internals: %s", w.Body.String())
	}
}

func TestDashboardCreateOrder_ReplayReturns200(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleAdmin)
	svc := &stubOrderService{createOrder: sampleOrder(user.MerchantID), createReplay: true}
	r := newOrderRouter(svc, user)
	w := orderDo(r, http.MethodPost, orderPath, validOrderBody(), map[string]string{"Idempotency-Key": "k"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for replay", w.Code)
	}
}

func TestDashboardCreateOrder_MerchantFromBodyIgnoredAndRejected(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleOwner)
	svc := &stubOrderService{createOrder: sampleOrder(user.MerchantID)}
	r := newOrderRouter(svc, user)
	other := uuid.NewString()
	body := fmt.Sprintf(`{"merchant_id":%q,"items":[{"product_id":%q,"quantity":1}]}`, other, uuid.NewString())
	w := orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": "k"})
	if w.Code != http.StatusBadRequest || svc.createCalls != 0 {
		t.Fatalf("status=%d calls=%d", w.Code, svc.createCalls)
	}
}

func TestDashboardCreateOrder_RejectsMalformedBodies(t *testing.T) {
	pid := uuid.NewString()
	cases := map[string]string{
		"unknown top-level total":    fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}],"total_amount":1}`, pid),
		"unknown top-level currency": fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}],"currency":"USD"}`, pid),
		"unknown top-level status":   fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}],"status":"PAID"}`, pid),
		"unknown nested price":       fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1,"unit_price":1}]}`, pid),
		"unknown nested name":        fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1,"product_name":"x"}]}`, pid),
		"unknown nested merchant":    fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1,"merchant_id":%q}]}`, pid, uuid.NewString()),
		"second JSON document":       fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}]}{"items":[]}`, pid),
		"trailing garbage":           fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}]} x`, pid),
		"malformed JSON":             `{"items":[`,
		"empty body":                 ``,
		"JSON null":                  `null null`,
		"array root":                 `[]`,
		"string quantity":            fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":"1"}]}`, pid),
		"float quantity":             fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1.5}]}`, pid),
		"quantity overflows int64":   fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":9223372036854775808}]}`, pid),
		"invalid uuid":               `{"items":[{"product_id":"not-a-uuid","quantity":1}]}`,
		"items not array":            `{"items":{}}`,
		"case-variant unknown field": fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}],"Total_Amount":1}`, pid),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			user := orderTestUser(model.DashboardUserRoleOwner)
			svc := &stubOrderService{createOrder: sampleOrder(user.MerchantID)}
			r := newOrderRouter(svc, user)
			w := orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": "k"})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			if svc.createCalls != 0 {
				t.Fatalf("service called for malformed body")
			}
			env := decodeOrderEnvelope(t, w)
			if env.Success || env.Error.Code != "VALIDATION_ERROR" {
				t.Fatalf("envelope = %+v", env)
			}
		})
	}
}

func TestDashboardCreateOrder_TrailingWhitespaceAllowed(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleOwner)
	svc := &stubOrderService{createOrder: sampleOrder(user.MerchantID)}
	r := newOrderRouter(svc, user)
	w := orderDo(r, http.MethodPost, orderPath, validOrderBody()+"\n  \t\n", map[string]string{"Idempotency-Key": "k"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardCreateOrder_ServiceErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"bad key", service.ErrOrderKey, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"invalid items", service.ErrInvalidOrder, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"missing product", service.ErrOrderProductMissing, http.StatusBadRequest, "INVALID_REQUEST"},
		{"currency", service.ErrOrderCurrency, http.StatusBadRequest, "INVALID_CURRENCY"},
		{"overflow", service.ErrOrderOverflow, http.StatusBadRequest, "INVALID_AMOUNT"},
		{"key conflict", service.ErrOrderKeyConflict, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED"},
		{"wrapped conflict", fmt.Errorf("tx: %w", service.ErrOrderKeyConflict), http.StatusConflict, "IDEMPOTENCY_KEY_REUSED"},
		{"infra", errors.New("pq: connection refused at 10.0.0.5"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := orderTestUser(model.DashboardUserRoleOwner)
			svc := &stubOrderService{createErr: tc.err}
			r := newOrderRouter(svc, user)
			w := orderDo(r, http.MethodPost, orderPath, validOrderBody(), map[string]string{"Idempotency-Key": "k"})
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			env := decodeOrderEnvelope(t, w)
			if env.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", env.Error.Code, tc.code)
			}
			if strings.Contains(w.Body.String(), "10.0.0.5") {
				t.Fatalf("internal error leaked: %s", w.Body.String())
			}
		})
	}
}

func TestDashboardCreateOrder_RoleAndAuth(t *testing.T) {
	t.Run("viewer forbidden", func(t *testing.T) {
		svc := &stubOrderService{}
		r := newOrderRouter(svc, orderTestUser(model.DashboardUserRoleViewer))
		w := orderDo(r, http.MethodPost, orderPath, validOrderBody(), map[string]string{"Idempotency-Key": "k"})
		if w.Code != http.StatusForbidden || svc.createCalls != 0 {
			t.Fatalf("status=%d calls=%d", w.Code, svc.createCalls)
		}
	})
	t.Run("unauthenticated via route", func(t *testing.T) {
		svc := &stubOrderService{}
		r := newOrderRouter(svc, nil)
		w := orderDo(r, http.MethodPost, orderPath, validOrderBody(), map[string]string{"Idempotency-Key": "k"})
		if w.Code != http.StatusUnauthorized || svc.createCalls != 0 {
			t.Fatalf("status=%d calls=%d", w.Code, svc.createCalls)
		}
	})
	for _, path := range []string{"/raw/orders", "/raw/orders/" + uuid.NewString()} {
		method := http.MethodGet
		t.Run("handler guard GET "+path, func(t *testing.T) {
			svc := &stubOrderService{}
			r := newOrderRouter(svc, nil)
			w := orderDo(r, method, path, "", nil)
			if w.Code != http.StatusUnauthorized || svc.getCalls+svc.listCalls != 0 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
	t.Run("handler guard POST", func(t *testing.T) {
		svc := &stubOrderService{}
		r := newOrderRouter(svc, nil)
		w := orderDo(r, http.MethodPost, "/raw/orders", validOrderBody(), map[string]string{"Idempotency-Key": "k"})
		if w.Code != http.StatusUnauthorized || svc.createCalls != 0 {
			t.Fatalf("status=%d", w.Code)
		}
	})
}

// fakeHandlerOrderRepo is a minimal real-repository double so the handler can
// be exercised end-to-end with the real OrderService (header validation,
// pricing and replay status codes).
type fakeHandlerOrderRepo struct {
	products map[uuid.UUID]model.Product
	orders   map[string]*model.Order
	txCalls  int
}

type fakeHandlerOrderTx struct {
	r      *fakeHandlerOrderRepo
	staged *model.Order
}

func (r *fakeHandlerOrderRepo) InTransaction(_ context.Context, fn func(repository.OrderTransaction) error) error {
	r.txCalls++
	tx := &fakeHandlerOrderTx{r: r}
	if err := fn(tx); err != nil {
		return err
	}
	if tx.staged != nil {
		r.orders[tx.staged.MerchantID.String()+":"+tx.staged.IdempotencyKey] = tx.staged
	}
	return nil
}
func (t *fakeHandlerOrderTx) LockIdempotencyKey(context.Context, uuid.UUID, string) error { return nil }
func (t *fakeHandlerOrderTx) FindByIdempotencyKey(_ context.Context, m uuid.UUID, k string) (*model.Order, error) {
	if o, ok := t.r.orders[m.String()+":"+k]; ok {
		return o, nil
	}
	return nil, repository.ErrOrderNotFound
}
func (t *fakeHandlerOrderTx) GetProductForOrder(_ context.Context, m, id uuid.UUID) (*model.Product, error) {
	p, ok := t.r.products[id]
	if !ok || p.MerchantID != m {
		return nil, repository.ErrProductNotFound
	}
	return &p, nil
}
func (t *fakeHandlerOrderTx) CreateOrder(_ context.Context, o *model.Order) error {
	t.staged = o
	return nil
}
func (r *fakeHandlerOrderRepo) GetOrderByID(context.Context, uuid.UUID, uuid.UUID) (*model.Order, error) {
	return nil, repository.ErrOrderNotFound
}
func (r *fakeHandlerOrderRepo) ListOrders(context.Context, uuid.UUID, int, int) (model.OrderListResult, error) {
	return model.OrderListResult{}, nil
}

func TestDashboardCreateOrder_EndToEndWithRealService(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleOwner)
	pid, foreign := uuid.New(), uuid.New()
	repo := &fakeHandlerOrderRepo{
		products: map[uuid.UUID]model.Product{
			pid:     {ID: pid, MerchantID: user.MerchantID, Name: "Tea", Price: 1200, Currency: "IDR", Active: true},
			foreign: {ID: foreign, MerchantID: uuid.New(), Name: "Other", Price: 1, Currency: "IDR", Active: true},
		},
		orders: map[string]*model.Order{},
	}
	r := newOrderRouter(service.NewOrderService(repo), user)
	body := fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":3}]}`, pid)

	t.Run("missing header", func(t *testing.T) {
		w := orderDo(r, http.MethodPost, orderPath, body, nil)
		env := decodeOrderEnvelope(t, w)
		if w.Code != http.StatusBadRequest || env.Error.Code != "VALIDATION_ERROR" || env.Error.Details["idempotency_key"] == "" {
			t.Fatalf("status=%d env=%+v", w.Code, env)
		}
		if repo.txCalls != 0 {
			t.Fatalf("transaction opened without key")
		}
	})
	t.Run("whitespace header", func(t *testing.T) {
		w := orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": "a b"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", w.Code)
		}
	})
	t.Run("too long header", func(t *testing.T) {
		w := orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": strings.Repeat("a", 256)})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", w.Code)
		}
	})
	t.Run("invalid items via service", func(t *testing.T) {
		w := orderDo(r, http.MethodPost, orderPath, `{"items":[]}`, map[string]string{"Idempotency-Key": "empty"})
		env := decodeOrderEnvelope(t, w)
		if w.Code != http.StatusBadRequest || env.Error.Details["items"] == "" {
			t.Fatalf("status=%d env=%+v", w.Code, env)
		}
	})
	t.Run("foreign product is 400 not found", func(t *testing.T) {
		b := fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":1}]}`, foreign)
		w := orderDo(r, http.MethodPost, orderPath, b, map[string]string{"Idempotency-Key": "foreign"})
		env := decodeOrderEnvelope(t, w)
		if w.Code != http.StatusBadRequest || env.Error.Code != "INVALID_REQUEST" {
			t.Fatalf("status=%d env=%+v", w.Code, env)
		}
	})

	var firstID string
	t.Run("create then replay then conflict", func(t *testing.T) {
		w := orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": "e2e"})
		if w.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
		}
		var o model.Order
		if err := json.Unmarshal(decodeOrderEnvelope(t, w).Data, &o); err != nil {
			t.Fatal(err)
		}
		if o.TotalAmount != 3600 || o.MerchantID != user.MerchantID || len(o.Items) != 1 || o.Items[0].UnitPrice != 1200 {
			t.Fatalf("order = %+v", o)
		}
		firstID = o.ID.String()

		w = orderDo(r, http.MethodPost, orderPath, body, map[string]string{"Idempotency-Key": "e2e"})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), firstID) {
			t.Fatalf("replay status=%d body=%s", w.Code, w.Body.String())
		}

		diff := fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":4}]}`, pid)
		w = orderDo(r, http.MethodPost, orderPath, diff, map[string]string{"Idempotency-Key": "e2e"})
		if w.Code != http.StatusConflict || decodeOrderEnvelope(t, w).Error.Code != "IDEMPOTENCY_KEY_REUSED" {
			t.Fatalf("conflict status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

// ─── GetOrder ────────────────────────────────────────────────────────────────

func TestDashboardGetOrder(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleViewer)

	t.Run("success scoped to caller", func(t *testing.T) {
		o := sampleOrder(user.MerchantID)
		svc := &stubOrderService{getOrder: o}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"/"+o.ID.String(), "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if svc.lastMerchant != user.MerchantID || svc.lastOrder != o.ID {
			t.Fatalf("merchant/order not propagated")
		}
		if strings.Contains(w.Body.String(), "request_hash") || strings.Contains(w.Body.String(), "idempotency_key") {
			t.Fatalf("internals leaked")
		}
	})
	t.Run("invalid id", func(t *testing.T) {
		svc := &stubOrderService{}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"/not-a-uuid", "", nil)
		if w.Code != http.StatusBadRequest || svc.getCalls != 0 {
			t.Fatalf("status=%d calls=%d", w.Code, svc.getCalls)
		}
	})
	t.Run("not found (incl. other merchant)", func(t *testing.T) {
		svc := &stubOrderService{getErr: repository.ErrOrderNotFound}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"/"+uuid.NewString(), "", nil)
		if w.Code != http.StatusNotFound || decodeOrderEnvelope(t, w).Error.Code != "RESOURCE_NOT_FOUND" {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("internal error", func(t *testing.T) {
		svc := &stubOrderService{getErr: errors.New("db secret detail")}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"/"+uuid.NewString(), "", nil)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

// ─── ListOrders ──────────────────────────────────────────────────────────────

func TestDashboardListOrders(t *testing.T) {
	user := orderTestUser(model.DashboardUserRoleViewer)

	t.Run("defaults and pagination meta", func(t *testing.T) {
		svc := &stubOrderService{listResult: model.OrderListResult{
			Orders: []model.Order{*sampleOrder(user.MerchantID)}, Total: 41, TotalPages: 3,
		}}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if svc.lastMerchant != user.MerchantID || svc.lastPage != 1 || svc.lastLimit != 20 {
			t.Fatalf("merchant=%s page=%d limit=%d", svc.lastMerchant, svc.lastPage, svc.lastLimit)
		}
		env := decodeOrderEnvelope(t, w)
		if env.Meta.Page == nil || *env.Meta.Page != 1 || *env.Meta.Limit != 20 || *env.Meta.Total != 41 || *env.Meta.TotalPages != 3 {
			t.Fatalf("meta = %+v", env.Meta)
		}
		var orders []map[string]any
		if err := json.Unmarshal(env.Data, &orders); err != nil || len(orders) != 1 {
			t.Fatalf("data=%s err=%v", env.Data, err)
		}
		if _, ok := orders[0]["request_hash"]; ok {
			t.Fatalf("internals leaked")
		}
	})
	t.Run("empty list is array", func(t *testing.T) {
		svc := &stubOrderService{listResult: model.OrderListResult{Orders: []model.Order{}}}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"?page=3&limit=100", "", nil)
		if w.Code != http.StatusOK || svc.lastPage != 3 || svc.lastLimit != 100 {
			t.Fatalf("status=%d page=%d limit=%d", w.Code, svc.lastPage, svc.lastLimit)
		}
		if string(decodeOrderEnvelope(t, w).Data) != "[]" {
			t.Fatalf("data = %s", decodeOrderEnvelope(t, w).Data)
		}
	})
	for _, q := range []string{"page=0", "page=-1", "page=abc", "limit=0", "limit=101", "limit=-3", "limit=1.5", "page=99999999999999999999"} {
		t.Run("invalid "+q, func(t *testing.T) {
			svc := &stubOrderService{}
			w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"?"+q, "", nil)
			if w.Code != http.StatusBadRequest || svc.listCalls != 0 {
				t.Fatalf("status=%d calls=%d", w.Code, svc.listCalls)
			}
			if decodeOrderEnvelope(t, w).Error.Code != "VALIDATION_ERROR" {
				t.Fatalf("body=%s", w.Body.String())
			}
		})
	}
	t.Run("page too large from service", func(t *testing.T) {
		svc := &stubOrderService{listErr: service.ErrInvalidOrder}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath+"?page=1000000", "", nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("internal error", func(t *testing.T) {
		svc := &stubOrderService{listErr: errors.New("boom")}
		w := orderDo(newOrderRouter(svc, user), http.MethodGet, orderPath, "", nil)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d", w.Code)
		}
	})
}
