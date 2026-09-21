package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ErrInvalidOverviewPeriod is returned when from >= to.
var ErrInvalidOverviewPeriod = errors.New("overview period from must be before to")

// DashboardOverviewService builds merchant-scoped dashboard overview aggregates.
type DashboardOverviewService interface {
	GetOverview(ctx context.Context, merchantID uuid.UUID, from, to *time.Time) (*model.DashboardOverviewResponse, error)
}

type dashboardOverviewService struct {
	overviewRepo repository.DashboardOverviewRepository
	paymentSvc   PaymentService
}

// NewDashboardOverviewService constructs a DashboardOverviewService.
func NewDashboardOverviewService(
	overviewRepo repository.DashboardOverviewRepository,
	paymentSvc PaymentService,
) DashboardOverviewService {
	return &dashboardOverviewService{
		overviewRepo: overviewRepo,
		paymentSvc:   paymentSvc,
	}
}

func (s *dashboardOverviewService) GetOverview(
	ctx context.Context,
	merchantID uuid.UUID,
	from, to *time.Time,
) (*model.DashboardOverviewResponse, error) {
	periodFrom, periodTo, err := resolveOverviewPeriod(from, to)
	if err != nil {
		return nil, err
	}

	paymentStats, err := s.overviewRepo.CountPaymentsByStatus(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("overview payments: %w", err)
	}

	revenueAmount, revenueCurrency, err := s.overviewRepo.SumPaidRevenue(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("overview revenue: %w", err)
	}

	refundCount, refundAmount, refundCurrency, err := s.overviewRepo.SumSucceededRefunds(ctx, merchantID, periodFrom, periodTo)
	if err != nil {
		return nil, fmt.Errorf("overview refunds: %w", err)
	}

	// Recent payments: last 10 for this merchant (not limited to period — more useful for ops).
	recent, err := s.paymentSvc.ListPayments(ctx, merchantID, model.TransactionListFilter{
		Page:  1,
		Limit: 10,
	})
	if err != nil {
		return nil, fmt.Errorf("overview recent payments: %w", err)
	}
	recentItems := recent.Transactions
	if recentItems == nil {
		recentItems = []model.PaymentResponse{}
	}

	return &model.DashboardOverviewResponse{
		Period:   model.DashboardPeriod{From: periodFrom, To: periodTo},
		Payments: paymentStats,
		Revenue: model.DashboardRevenueStats{
			Amount:   revenueAmount,
			Currency: revenueCurrency,
		},
		Refunds: model.DashboardRefundStats{
			Count:    refundCount,
			Amount:   refundAmount,
			Currency: refundCurrency,
		},
		RecentPayments: recentItems,
	}, nil
}

// resolveOverviewPeriod defaults to the current UTC calendar month [start, nextMonth).
func resolveOverviewPeriod(from, to *time.Time) (time.Time, time.Time, error) {
	if from == nil && to == nil {
		now := time.Now().UTC()
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0)
		return start, end, nil
	}
	if from == nil || to == nil {
		return time.Time{}, time.Time{}, ErrInvalidOverviewPeriod
	}
	if !from.Before(*to) {
		return time.Time{}, time.Time{}, ErrInvalidOverviewPeriod
	}
	return from.UTC(), to.UTC(), nil
}
