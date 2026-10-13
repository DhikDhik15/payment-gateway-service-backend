package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── stub product service ────────────────────────────────────────────────────

type stubProductService struct {
	createResult *model.Product
	createErr    error
	createCalls  int

	listResult model.ListProductsResult
	listErr    error
	listCalls  int
	lastFilter model.ListProductsFilter
	lastPage   int
	lastLimit  int

	getResult *model.Product
	getErr    error
}

func (s *stubProductService) Create(_ context.Context, _ uuid.UUID, _ model.CreateProductRequest) (*model.Product, error) {
	s.createCalls++
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResult, nil
}

func (s *stubProductService) List(_ context.Context, _ uuid.UUID, filter model.ListProductsFilter, page, limit int) (model.ListProductsResult, error) {
	s.listCalls++
	s.lastFilter, s.lastPage, s.lastLimit = filter, page, limit
	if s.listErr != nil {
		return model.ListProductsResult{}, s.listErr
	}
	return s.listResult, nil
}

func (s *stubProductService) Get(_ context.Context, _, _ uuid.UUID) (*model.Product, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getResult, nil
}

func (s *stubProductService) Update(_ context.Context, _, _ uuid.UUID, _ model.UpdateProductRequest) (*model.Product, error) {
	return s.getResult, s.getErr
}

func (s *stubProductService) Delete(_ context.Context, _, _ uuid.UUID) error {
	return s.getErr
}

func newProductRouter(svc service.ProductService, user *model.MerchantUser) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDashboardProductHandler(svc)
	g := r.Group("/api/v1/dashboard", func(c *gin.Context) {
		if user != nil {
			c.Set(model.ContextKeyDashboardUser, user)
		}
		c.Next()
	})
	g.POST("/products", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.CreateProduct)
	g.GET("/products", h.ListProducts)
	g.GET("/products/:product_id", h.GetProduct)
	g.PATCH("/products/:product_id", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.UpdateProduct)
	g.DELETE("/products/:product_id", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.DeleteProduct)
	return r
}

func productDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type productEnvelope struct {
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

func decodeProductEnvelope(t *testing.T, w *httptest.ResponseRecorder) productEnvelope {
	t.Helper()
	var env productEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return env
}

func TestDashboardProductListPagination(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	svc := &stubProductService{listResult: model.ListProductsResult{
		Products:   []model.Product{{ID: uuid.New(), Name: "Coffee", Price: 25000, Currency: "IDR", Active: true}},
		Page:       2,
		Limit:      5,
		Total:      6,
		TotalPages: 2,
	}}
	r := newProductRouter(svc, owner)

	w := productDo(r, http.MethodGet, "/api/v1/dashboard/products?page=2&limit=5&active=true&search=cof", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if svc.listCalls != 1 || svc.lastPage != 2 || svc.lastLimit != 5 {
		t.Fatalf("pagination not forwarded: calls=%d page=%d limit=%d", svc.listCalls, svc.lastPage, svc.lastLimit)
	}
	if svc.lastFilter.Active == nil || !*svc.lastFilter.Active || svc.lastFilter.Search != "cof" {
		t.Fatalf("filters not forwarded: %+v", svc.lastFilter)
	}
	env := decodeProductEnvelope(t, w)
	if !env.Success || env.Meta.Page == nil || *env.Meta.Page != 2 || env.Meta.Total == nil || *env.Meta.Total != 6 || env.Meta.TotalPages == nil || *env.Meta.TotalPages != 2 {
		t.Fatalf("paginated envelope wrong: %s", w.Body.String())
	}
}

func TestDashboardProductListInvalidPagination(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	r := newProductRouter(&stubProductService{}, owner)
	for _, path := range []string{
		"/api/v1/dashboard/products?page=0",
		"/api/v1/dashboard/products?limit=0",
		"/api/v1/dashboard/products?limit=101",
		"/api/v1/dashboard/products?page=abc",
	} {
		w := productDo(r, http.MethodGet, path, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
		env := decodeProductEnvelope(t, w)
		if env.Error.Code != "VALIDATION_ERROR" {
			t.Fatalf("%s: code=%q", path, env.Error.Code)
		}
	}
}

func TestDashboardProductListInvalidFilters(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	r := newProductRouter(&stubProductService{}, owner)
	w := productDo(r, http.MethodGet, "/api/v1/dashboard/products?category_id=not-a-uuid", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	w = productDo(r, http.MethodGet, "/api/v1/dashboard/products?active=yes", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardProductValidationMapping(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	for _, tc := range []struct {
		name      string
		method    string
		path      string
		body      string
		err       error
		field     string
		wantCode  int
		wantError string
	}{
		{"create blank name", http.MethodPost, "/api/v1/dashboard/products", `{"name":"","price":1,"currency":"IDR"}`, service.ErrProductInvalidName, "name", 400, "VALIDATION_ERROR"},
		{"create negative price", http.MethodPost, "/api/v1/dashboard/products", `{"name":"x","price":-1,"currency":"IDR"}`, service.ErrProductInvalidPrice, "price", 400, "VALIDATION_ERROR"},
		{"create usd", http.MethodPost, "/api/v1/dashboard/products", `{"name":"x","price":1,"currency":"USD"}`, service.ErrProductInvalidCurrency, "currency", 400, "VALIDATION_ERROR"},
		{"create http image", http.MethodPost, "/api/v1/dashboard/products", `{"name":"x","price":1,"currency":"IDR"}`, service.ErrProductInvalidImage, "image_url", 400, "VALIDATION_ERROR"},
		{"update blank name", http.MethodPatch, "/api/v1/dashboard/products/" + uuid.NewString(), `{"name":"  "}`, service.ErrProductInvalidName, "name", 400, "VALIDATION_ERROR"},
		{"update negative price", http.MethodPatch, "/api/v1/dashboard/products/" + uuid.NewString(), `{"price":-1}`, service.ErrProductInvalidPrice, "price", 400, "VALIDATION_ERROR"},
		{"sku conflict", http.MethodPost, "/api/v1/dashboard/products", `{"name":"x","price":1,"currency":"IDR"}`, service.ErrProductSKUUsed, "", 409, "DUPLICATE_MERCHANT_CODE"},
		{"missing product", http.MethodGet, "/api/v1/dashboard/products/" + uuid.NewString(), "", service.ErrProductNotFound, "", 404, "RESOURCE_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubProductService{getErr: tc.err, createErr: tc.err}
			r := newProductRouter(svc, owner)
			w := productDo(r, tc.method, tc.path, tc.body)
			if w.Code != tc.wantCode {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.wantCode, w.Body.String())
			}
			env := decodeProductEnvelope(t, w)
			if env.Error.Code != tc.wantError {
				t.Fatalf("code=%q want=%q body=%s", env.Error.Code, tc.wantError, w.Body.String())
			}
			if tc.field != "" {
				if _, ok := env.Error.Details[tc.field]; !ok {
					t.Fatalf("details missing field %q: %s", tc.field, w.Body.String())
				}
			}
		})
	}
}

func TestDashboardProductUnauthenticatedGuard(t *testing.T) {
	svc := &stubProductService{}
	r := newProductRouter(svc, nil)
	w := productDo(r, http.MethodGet, "/api/v1/dashboard/products", "")
	if w.Code != http.StatusUnauthorized || svc.listCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.listCalls, w.Body.String())
	}
}
