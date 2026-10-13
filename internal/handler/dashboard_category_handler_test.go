package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"context"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── stub category service ───────────────────────────────────────────────────

type stubCategoryService struct {
	createResult *model.Category
	createErr    error
}

func (s *stubCategoryService) Create(_ context.Context, _ uuid.UUID, _ model.CreateCategoryRequest) (*model.Category, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResult, nil
}

func (s *stubCategoryService) List(_ context.Context, _ uuid.UUID) ([]model.Category, error) {
	return nil, nil
}

func (s *stubCategoryService) Get(_ context.Context, _, _ uuid.UUID) (*model.Category, error) {
	return nil, nil
}

func (s *stubCategoryService) Update(_ context.Context, _, _ uuid.UUID, req model.UpdateCategoryRequest) (*model.Category, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResult, nil
}

func (s *stubCategoryService) Delete(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func newCategoryRouter(svc service.CategoryService, user *model.MerchantUser) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDashboardCategoryHandler(svc)
	g := r.Group("/api/v1/dashboard", func(c *gin.Context) {
		if user != nil {
			c.Set(model.ContextKeyDashboardUser, user)
		}
		c.Next()
	})
	g.POST("/categories", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.CreateCategory)
	g.PATCH("/categories/:category_id", middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin), h.UpdateCategory)
	return r
}

func TestDashboardCategoryValidationMapping(t *testing.T) {
	owner := orderTestUser(model.DashboardUserRoleOwner)
	for _, tc := range []struct {
		name      string
		method    string
		err       error
		field     string
		wantCode  int
		wantError string
	}{
		{"create blank name", http.MethodPost, service.ErrCategoryInvalidName, "name", 400, "VALIDATION_ERROR"},
		{"update blank name", http.MethodPatch, service.ErrCategoryInvalidName, "name", 400, "VALIDATION_ERROR"},
		{"name conflict preserved", http.MethodPost, service.ErrCategoryNameTaken, "", 409, "DUPLICATE_MERCHANT_CODE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubCategoryService{createErr: tc.err}
			r := newCategoryRouter(svc, owner)
			path := "/api/v1/dashboard/categories"
			body := `{"name":"x"}`
			if tc.method == http.MethodPatch {
				path += "/" + uuid.NewString()
			}
			req := httptest.NewRequest(tc.method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
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
