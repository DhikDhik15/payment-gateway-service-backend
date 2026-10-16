package service

import (
	"context"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// DashboardAnalyticsService builds merchant-scoped analytics aggregates.
type DashboardAnalyticsService interface {
	GetAnalytics(ctx context.Context, merchantID uuid.UUID, from, to *time.Time) (*model.DashboardAnalyticsResponse, error)
}

type dashboardAnalyticsService struct {
	analyticsRepo repository.DashboardAnalyticsRepository
}

// NewDashboardAnalyticsService constructs a DashboardAnalyticsService.
func NewDashboardAnalyticsService(
	analyticsRepo repository.DashboardAnalyticsRepository,
) DashboardAnalyticsService {
	return &dashboardAnalyticsService{
		analyticsRepo: analyticsRepo,
	}
}

func (s *dashboardAnalyticsService) GetAnalytics(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to *time.Time,
) (*model.DashboardAnalyticsResponse, error) {
	// Same period contract as the overview: default current UTC month,
	// otherwise the validated half-open window.
	periodFrom, periodTo, err := resolveOverviewPeriod(from, to)
	if err != nil {
		return nil, err
	}

	revenue, paid, currency, err := s.analyticsRepo.PaymentSummary(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("analytics payment summary: %w", err)
	}

	orderRevenue, paidOrders, unitsSold, err := s.analyticsRepo.OrderSummary(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("analytics order summary: %w", err)
	}

	var averageOrderValue *int64
	if paidOrders > 0 {
		aov := orderRevenue / paidOrders
		averageOrderValue = &aov
	}

	interval, trend, err := s.analyticsRepo.RevenueTrend(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("analytics revenue trend: %w", err)
	}

	topProducts, err := s.analyticsRepo.TopProductsByUnits(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("analytics top products: %w", err)
	}

	revenueByProduct, err := s.analyticsRepo.RevenueByProduct(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("analytics revenue by product: %w", err)
	}

	return &model.DashboardAnalyticsResponse{
		Period:   model.DashboardPeriod{From: periodFrom, To: periodTo},
		Interval: interval,
		Currency: currency,
		Summary: model.DashboardAnalyticsSummary{
			TotalRevenue:       revenue,
			SuccessfulPayments: paid,
			UnitsSold:          unitsSold,
			AverageOrderValue:  averageOrderValue,
		},
		RevenueTrend:     trend,
		TopProducts:      topProducts,
		RevenueByProduct: revenueByProduct,
	}, nil
}
