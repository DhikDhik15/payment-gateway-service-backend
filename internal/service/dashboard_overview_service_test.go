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

// ─── fakes ───────────────────────────────────────────────────────────────────

type fakeOverviewRepo struct {
	stats          model.DashboardPaymentStats
	statsErr       error
	revenueAmount  int64
	revenueCurr    string
	revenueErr     error
	refundCount    int64
	refundAmount   int64
	refundCurr     string
	refundErr      error
	lastMerchantID uuid.UUID
	lastFrom       time.Time
	lastTo         time.Time
	calls          int
}

func (f *fakeOverviewRepo) CountPaymentsByStatus(_ context.Context, merchantID uuid.UUID, from, to time.Time) (model.DashboardPaymentStats, error) {
	f.lastMerchantID = merchantID
	f.lastFrom = from
	f.lastTo = to
	f.calls++
	if f.statsErr != nil {
		return model.DashboardPaymentStats{}, f.statsErr
	}
	return f.stats, nil
}

func (f *fakeOverviewRepo) SumPaidRevenue(_ context.Context, merchantID uuid.UUID, from, to time.Time) (int64, string, error) {
	if f.revenueErr != nil {
		return 0, "", f.revenueErr
	}
	if f.revenueCurr == "" {
		f.revenueCurr = "IDR"
	}
	return f.revenueAmount, f.revenueCurr, nil
}

func (f *fakeOverviewRepo) SumSucceededRefunds(_ context.Context, merchantID uuid.UUID, from, to time.Time) (int64, int64, string, error) {
	if f.refundErr != nil {
		return 0, 0, "", f.refundErr
	}
	if f.refundCurr == "" {
		f.refundCurr = "IDR"
	}
	return f.refundCount, f.refundAmount, f.refundCurr, nil
}

type fakePaymentService struct {
	listResult   *model.ListPaymentsResult
	listErr      error
	lastMerchant uuid.UUID
	lastFilter   model.TransactionListFilter
}

func (f *fakePaymentService) CreatePayment(context.Context, uuid.UUID, model.CreatePaymentRequest) (*model.CreatePaymentResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePaymentService) CreatePaymentWithIdempotency(context.Context, uuid.UUID, model.CreatePaymentRequest, string) (*model.CreatePaymentResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePaymentService) GetPayment(context.Context, uuid.UUID, uuid.UUID) (*model.PaymentResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePaymentService) GetPaymentDetail(context.Context, uuid.UUID, uuid.UUID) (*model.DashboardPaymentDetailResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePaymentService) CancelPayment(context.Context, uuid.UUID, uuid.UUID) (*model.PaymentResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePaymentService) ListPayments(_ context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (*model.ListPaymentsResult, error) {
	f.lastMerchant = merchantID
	f.lastFilter = filter
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listResult != nil {
		return f.listResult, nil
	}
	return &model.ListPaymentsResult{Transactions: []model.PaymentResponse{}, Page: 1, Limit: 10}, nil
}

// ─── tests ───────────────────────────────────────────────────────────────────

func overviewTestRange() (from, to time.Time) {
	from = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	return from, to
}

func TestDashboardOverview_AppliesSelectedRangeToEveryQuery(t *testing.T) {
	repo := &fakeOverviewRepo{
		stats: model.DashboardPaymentStats{Total: 5, Paid: 2, Failed: 1, Expired: 1, Cancelled: 1},
	}
	payments := &fakePaymentService{}
	svc := service.NewDashboardOverviewService(repo, payments)

	merchantID := uuid.New()
	from, to := overviewTestRange()
	res, err := svc.GetOverview(context.Background(), merchantID, &from, &to)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}

	// Aggregates receive the exact selected half-open window.
	if !repo.lastFrom.Equal(from) || !repo.lastTo.Equal(to) {
		t.Fatalf("aggregates got [%v, %v), want [%v, %v)", repo.lastFrom, repo.lastTo, from, to)
	}
	if repo.lastMerchantID != merchantID {
		t.Fatalf("aggregates got merchant %v, want %v", repo.lastMerchantID, merchantID)
	}

	// Recent payments use the SAME range on created_at (half-open).
	if payments.lastFilter.CreatedFrom == nil || payments.lastFilter.CreatedTo == nil {
		t.Fatalf("recent payments missing period filter: %+v", payments.lastFilter)
	}
	if !payments.lastFilter.CreatedFrom.Equal(from) || !payments.lastFilter.CreatedTo.Equal(to) {
		t.Fatalf("recent got [%v, %v), want [%v, %v)",
			payments.lastFilter.CreatedFrom, payments.lastFilter.CreatedTo, from, to)
	}
	if payments.lastMerchant != merchantID {
		t.Fatalf("recent got merchant %v, want %v", payments.lastMerchant, merchantID)
	}
	if payments.lastFilter.Page != 1 || payments.lastFilter.Limit != 10 {
		t.Fatalf("recent pagination changed: %+v", payments.lastFilter)
	}

	// Echoed period matches the selection.
	if !res.Period.From.Equal(from) || !res.Period.To.Equal(to) {
		t.Fatalf("period echo [%v, %v), want [%v, %v)", res.Period.From, res.Period.To, from, to)
	}
}

func TestDashboardOverview_RecentPaymentsHonorPeriodLimit(t *testing.T) {
	inPeriod := []model.PaymentResponse{
		{TransactionID: uuid.New(), MerchantOrderID: "IN-1"},
		{TransactionID: uuid.New(), MerchantOrderID: "IN-2"},
	}
	repo := &fakeOverviewRepo{}
	payments := &fakePaymentService{
		listResult: &model.ListPaymentsResult{Transactions: inPeriod, Total: 2, Page: 1, Limit: 10},
	}
	svc := service.NewDashboardOverviewService(repo, payments)

	from, to := overviewTestRange()
	res, err := svc.GetOverview(context.Background(), uuid.New(), &from, &to)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if len(res.RecentPayments) != 2 || res.RecentPayments[0].MerchantOrderID != "IN-1" {
		t.Fatalf("recent payments not passed through: %+v", res.RecentPayments)
	}
}

func TestDashboardOverview_NilRecentBecomesEmptyArray(t *testing.T) {
	repo := &fakeOverviewRepo{}
	payments := &fakePaymentService{
		listResult: &model.ListPaymentsResult{Transactions: nil},
	}
	svc := service.NewDashboardOverviewService(repo, payments)

	from, to := overviewTestRange()
	res, err := svc.GetOverview(context.Background(), uuid.New(), &from, &to)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if res.RecentPayments == nil {
		t.Fatalf("recent payments must be [] not null")
	}
}

func TestDashboardOverview_InvalidPeriodPropagates(t *testing.T) {
	repo := &fakeOverviewRepo{}
	payments := &fakePaymentService{}
	svc := service.NewDashboardOverviewService(repo, payments)

	from := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err := svc.GetOverview(context.Background(), uuid.New(), &from, &to); !errors.Is(err, service.ErrInvalidOverviewPeriod) {
		t.Fatalf("want ErrInvalidOverviewPeriod, got %v", err)
	}
	if repo.calls != 0 {
		t.Fatalf("no repository query must run for an invalid period, got %d calls", repo.calls)
	}
}

func TestDashboardOverview_DefaultPeriodIsCurrentUTCMonth(t *testing.T) {
	repo := &fakeOverviewRepo{}
	payments := &fakePaymentService{}
	svc := service.NewDashboardOverviewService(repo, payments)

	res, err := svc.GetOverview(context.Background(), uuid.New(), nil, nil)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	now := time.Now().UTC()
	wantFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !res.Period.From.Equal(wantFrom) {
		t.Fatalf("default from %v, want %v", res.Period.From, wantFrom)
	}
	if !res.Period.To.Equal(wantFrom.AddDate(0, 1, 0)) {
		t.Fatalf("default to %v, want month after from", res.Period.To)
	}
	if res.Period.From.Location() != time.UTC || res.Period.To.Location() != time.UTC {
		t.Fatalf("default period must be UTC: %v .. %v", res.Period.From, res.Period.To)
	}
}

func TestDashboardOverview_RepoErrorAbortsBeforeRecent(t *testing.T) {
	repo := &fakeOverviewRepo{statsErr: errors.New("db down")}
	payments := &fakePaymentService{}
	svc := service.NewDashboardOverviewService(repo, payments)

	from, to := overviewTestRange()
	if _, err := svc.GetOverview(context.Background(), uuid.New(), &from, &to); err == nil {
		t.Fatalf("want error, got nil")
	}
	if payments.lastFilter != (model.TransactionListFilter{}) {
		t.Fatalf("recent must not be queried after aggregate failure")
	}
}
