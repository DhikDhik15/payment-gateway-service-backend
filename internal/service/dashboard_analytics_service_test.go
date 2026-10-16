package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

type fakeAnalyticsRepo struct {
	revenue      int64
	paid         int64
	orderRevenue int64
	paidOrders   int64
	unitsSold    int64
	interval     string
	trend        []model.DashboardRevenueBucket
	top          []model.DashboardProductSales
	byRevenue    []model.DashboardProductSales
	err          error

	lastMerchantID uuid.UUID
	lastFrom       time.Time
	lastTo         time.Time
}

func (f *fakeAnalyticsRepo) PaymentSummary(_ context.Context, merchantID uuid.UUID, from, to time.Time) (int64, int64, string, error) {
	f.lastMerchantID = merchantID
	f.lastFrom = from
	f.lastTo = to
	if f.err != nil {
		return 0, 0, "", f.err
	}
	return f.revenue, f.paid, "IDR", nil
}

func (f *fakeAnalyticsRepo) OrderSummary(_ context.Context, _ uuid.UUID, _, _ time.Time) (int64, int64, int64, error) {
	if f.err != nil {
		return 0, 0, 0, f.err
	}
	return f.orderRevenue, f.paidOrders, f.unitsSold, nil
}

func (f *fakeAnalyticsRepo) RevenueTrend(_ context.Context, _ uuid.UUID, _, _ time.Time) (string, []model.DashboardRevenueBucket, error) {
	if f.err != nil {
		return "", nil, f.err
	}
	interval := f.interval
	if interval == "" {
		interval = "day"
	}
	trend := f.trend
	if trend == nil {
		trend = []model.DashboardRevenueBucket{}
	}
	return interval, trend, nil
}

func (f *fakeAnalyticsRepo) TopProductsByUnits(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]model.DashboardProductSales, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.top == nil {
		return []model.DashboardProductSales{}, nil
	}
	return f.top, nil
}

func (f *fakeAnalyticsRepo) RevenueByProduct(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]model.DashboardProductSales, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.byRevenue == nil {
		return []model.DashboardProductSales{}, nil
	}
	return f.byRevenue, nil
}

func analyticsTestRange() (from, to time.Time) {
	from = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	return from, to
}

func TestDashboardAnalytics_PropagatesRangeAndMerchant(t *testing.T) {
	repo := &fakeAnalyticsRepo{}
	svc := service.NewDashboardAnalyticsService(repo)

	merchantID := uuid.New()
	from, to := analyticsTestRange()
	res, err := svc.GetAnalytics(context.Background(), merchantID, &from, &to)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if !repo.lastFrom.Equal(from) || !repo.lastTo.Equal(to) {
		t.Fatalf("repo got [%v, %v), want [%v, %v)", repo.lastFrom, repo.lastTo, from, to)
	}
	if repo.lastMerchantID != merchantID {
		t.Fatalf("repo got merchant %v, want %v", repo.lastMerchantID, merchantID)
	}
	if !res.Period.From.Equal(from) || !res.Period.To.Equal(to) {
		t.Fatalf("period echo mismatch: %+v", res.Period)
	}
}

func TestDashboardAnalytics_SummaryMath(t *testing.T) {
	repo := &fakeAnalyticsRepo{
		revenue: 100000, paid: 4,
		orderRevenue: 90000, paidOrders: 3, unitsSold: 7,
	}
	svc := service.NewDashboardAnalyticsService(repo)

	from, to := analyticsTestRange()
	res, err := svc.GetAnalytics(context.Background(), uuid.New(), &from, &to)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if res.Summary.TotalRevenue != 100000 {
		t.Fatalf("total revenue = %d, want 100000", res.Summary.TotalRevenue)
	}
	if res.Summary.SuccessfulPayments != 4 {
		t.Fatalf("successful payments = %d, want 4", res.Summary.SuccessfulPayments)
	}
	if res.Summary.UnitsSold != 7 {
		t.Fatalf("units sold = %d, want 7", res.Summary.UnitsSold)
	}
	if res.Summary.AverageOrderValue == nil || *res.Summary.AverageOrderValue != 30000 {
		t.Fatalf("AOV = %v, want 30000 (90000/3)", res.Summary.AverageOrderValue)
	}
}

func TestDashboardAnalytics_ZeroOrdersYieldsNullAOV(t *testing.T) {
	repo := &fakeAnalyticsRepo{revenue: 5000, paid: 1}
	svc := service.NewDashboardAnalyticsService(repo)

	from, to := analyticsTestRange()
	res, err := svc.GetAnalytics(context.Background(), uuid.New(), &from, &to)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if res.Summary.AverageOrderValue != nil {
		t.Fatalf("AOV must be null with zero paid orders, got %d", *res.Summary.AverageOrderValue)
	}
	if res.Summary.UnitsSold != 0 || res.Summary.TotalRevenue != 5000 {
		t.Fatalf("unexpected summary: %+v", res.Summary)
	}
}

func TestDashboardAnalytics_InvalidPeriodPropagates(t *testing.T) {
	repo := &fakeAnalyticsRepo{}
	svc := service.NewDashboardAnalyticsService(repo)

	from := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err := svc.GetAnalytics(context.Background(), uuid.New(), &from, &to); !errors.Is(err, service.ErrInvalidOverviewPeriod) {
		t.Fatalf("want ErrInvalidOverviewPeriod, got %v", err)
	}
}

func TestDashboardAnalytics_RepoErrorAborts(t *testing.T) {
	repo := &fakeAnalyticsRepo{err: errors.New("db down")}
	svc := service.NewDashboardAnalyticsService(repo)

	from, to := analyticsTestRange()
	if _, err := svc.GetAnalytics(context.Background(), uuid.New(), &from, &to); err == nil {
		t.Fatalf("want error, got nil")
	}
}
