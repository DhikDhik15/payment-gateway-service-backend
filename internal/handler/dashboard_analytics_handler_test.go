package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type stubAnalyticsService struct {
	resp           *model.DashboardAnalyticsResponse
	err            error
	lastMerchantID uuid.UUID
	lastFrom       *time.Time
	lastTo         *time.Time
}

func (s *stubAnalyticsService) GetAnalytics(_ context.Context, merchantID uuid.UUID, from, to *time.Time) (*model.DashboardAnalyticsResponse, error) {
	s.lastMerchantID = merchantID
	s.lastFrom = from
	s.lastTo = to
	if s.err != nil {
		return nil, s.err
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &model.DashboardAnalyticsResponse{
		Period:   model.DashboardPeriod{From: time.Now().UTC(), To: time.Now().UTC()},
		Interval: "day",
	}, nil
}

func newAnalyticsRouter() (*gin.Engine, *model.MerchantUser, *stubAnalyticsService) {
	gin.SetMode(gin.TestMode)

	merchantID := uuid.New()
	userID := uuid.New()
	user := &model.MerchantUser{
		ID:         userID,
		MerchantID: merchantID,
		Email:      "owner@test.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	authSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
		},
	}
	authSvc.verifyClaims.Subject = userID.String()

	analyticsSvc := &stubAnalyticsService{}
	analyticsH := handler.NewDashboardAnalyticsHandler(analyticsSvc)

	r := gin.New()
	r.Use(middleware.RequestID())
	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, &mockUserRepoForMiddleware{user: user}, newActiveMerchantRepo(merchantID)))
	dash.GET("/analytics", analyticsH.GetAnalytics)
	return r, user, analyticsSvc
}

func TestDashboardAnalytics_ValidRangeAndMerchant(t *testing.T) {
	r, user, svc := newAnalyticsRouter()
	w := dashDo(r, http.MethodGet, "/api/v1/dashboard/analytics?from=2026-10-01&to=2026-10-08", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.lastMerchantID != user.MerchantID {
		t.Fatalf("used merchant %v, want %v", svc.lastMerchantID, user.MerchantID)
	}
	if svc.lastFrom == nil || svc.lastTo == nil {
		t.Fatalf("service never received the period")
	}
}

func TestDashboardAnalytics_InvalidRanges400(t *testing.T) {
	for _, path := range []string{
		"/api/v1/dashboard/analytics?from=2026-10-08&to=2026-10-01",
		"/api/v1/dashboard/analytics?from=bogus&to=2026-10-08",
		"/api/v1/dashboard/analytics?from=2026-10-01",
	} {
		r, _, _ := newAnalyticsRouter()
		w := dashDo(r, http.MethodGet, path, nil, "token")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestDashboardAnalytics_NoParamsKeepsDefault(t *testing.T) {
	r, _, svc := newAnalyticsRouter()
	w := dashDo(r, http.MethodGet, "/api/v1/dashboard/analytics", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.lastFrom != nil || svc.lastTo != nil {
		t.Fatalf("no-param request must reach service with nil period")
	}
}

func TestDashboardAnalytics_MissingBearer401(t *testing.T) {
	r, _, _ := newAnalyticsRouter()
	w := dashDo(r, http.MethodGet, "/api/v1/dashboard/analytics", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestDashboardAnalytics_ResponseContract(t *testing.T) {
	r, _, svc := newAnalyticsRouter()
	aov := int64(30000)
	pid := uuid.New()
	svc.resp = &model.DashboardAnalyticsResponse{
		Period:   model.DashboardPeriod{From: time.Now().UTC(), To: time.Now().UTC()},
		Interval: "day",
		Summary: model.DashboardAnalyticsSummary{
			TotalRevenue: 100000, SuccessfulPayments: 4, UnitsSold: 7, AverageOrderValue: &aov,
		},
		RevenueTrend: []model.DashboardRevenueBucket{{Date: "2026-10-01", Revenue: 100000}},
		TopProducts: []model.DashboardProductSales{
			{ProductID: &pid, ProductName: "Kopi", UnitsSold: 7, Revenue: 90000},
		},
		RevenueByProduct: []model.DashboardProductSales{
			{ProductID: &pid, ProductName: "Kopi", UnitsSold: 7, Revenue: 90000},
		},
	}
	w := dashDo(r, http.MethodGet, "/api/v1/dashboard/analytics?from=2026-10-01&to=2026-10-08", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	data := env["data"].(map[string]any)
	summary := data["summary"].(map[string]any)
	for _, key := range []string{"total_revenue", "successful_payments", "units_sold", "average_order_value"} {
		if _, ok := summary[key]; !ok {
			t.Fatalf("summary field %q missing", key)
		}
	}
	if summary["average_order_value"].(float64) != 30000 {
		t.Fatalf("aov = %v, want 30000", summary["average_order_value"])
	}
	if data["interval"].(string) != "day" {
		t.Fatalf("interval = %v, want day", data["interval"])
	}
	if len(data["revenue_trend"].([]any)) != 1 || len(data["top_products"].([]any)) != 1 || len(data["revenue_by_product"].([]any)) != 1 {
		t.Fatalf("trend/products arrays not passed through: %s", w.Body.String())
	}
}
