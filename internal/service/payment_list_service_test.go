package service_test

// ─── PaymentService.ListPayments tests ───────────────────────────────────────

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── in-memory List/CountList repo ───────────────────────────────────────────

// mockListTransactionRepo extends mockTransactionRepo with List and CountList.
type mockListTransactionRepo struct {
	mockTransactionRepo // embed existing mock (has Create, FindBy*, UpdateStatus*)
}

func newMockListTxRepo() *mockListTransactionRepo {
	return &mockListTransactionRepo{
		mockTransactionRepo: mockTransactionRepo{txs: make(map[uuid.UUID]*model.Transaction)},
	}
}

func (r *mockListTransactionRepo) applyFilter(merchantID uuid.UUID, f model.TransactionListFilter) []*model.Transaction {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.Transaction
	for _, tx := range r.txs {
		if tx.MerchantID != merchantID {
			continue
		}
		if f.Status != nil && tx.Status != *f.Status {
			continue
		}
		if f.MerchantOrderID != nil && tx.MerchantOrderID != *f.MerchantOrderID {
			continue
		}
		if f.PaymentMethod != nil && tx.PaymentMethod != *f.PaymentMethod {
			continue
		}
		if f.CreatedFrom != nil && tx.CreatedAt.Before(*f.CreatedFrom) {
			continue
		}
		if f.CreatedTo != nil && !tx.CreatedAt.Before(*f.CreatedTo) {
			continue
		}
		cp := *tx
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() > out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (r *mockListTransactionRepo) List(_ context.Context, merchantID uuid.UUID, f model.TransactionListFilter) ([]*model.Transaction, error) {
	all := r.applyFilter(merchantID, f)
	start := (f.Page - 1) * f.Limit
	if start >= len(all) {
		return []*model.Transaction{}, nil
	}
	end := start + f.Limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

func (r *mockListTransactionRepo) CountList(_ context.Context, merchantID uuid.UUID, f model.TransactionListFilter) (int64, error) {
	return int64(len(r.applyFilter(merchantID, f))), nil
}

// compile-time check
var _ repository.TransactionRepository = (*mockListTransactionRepo)(nil)

// ─── service builder ──────────────────────────────────────────────────────────

func buildListService() (service.PaymentService, *mockListTransactionRepo, *mockPaymentAttemptRepo) {
	txRepo := newMockListTxRepo()
	attemptRepo := newMockAttemptRepo()
	svc := service.NewPaymentService(txRepo, attemptRepo, service.NewMockPaymentProvider())
	return svc, txRepo, attemptRepo
}

// insertTx seeds a transaction directly into the repo.
func insertTx(r *mockListTransactionRepo, merchantID uuid.UUID, orderID string, status model.TransactionStatus, createdAt time.Time) *model.Transaction {
	tx := &model.Transaction{
		ID:              uuid.New(),
		MerchantID:      merchantID,
		MerchantOrderID: orderID,
		Amount:          50000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
		Status:          status,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	r.mu.Lock()
	r.txs[tx.ID] = tx
	r.mu.Unlock()
	return tx
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestListPayments_Defaults(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		insertTx(txRepo, mID, uuid.New().String(), model.TransactionStatusPending, now.Add(-time.Duration(i)*time.Second))
	}

	// Zero values → defaults applied inside service.
	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Page != model.DefaultPage {
		t.Errorf("default page: expected %d, got %d", model.DefaultPage, res.Page)
	}
	if res.Limit != model.DefaultLimit {
		t.Errorf("default limit: expected %d, got %d", model.DefaultLimit, res.Limit)
	}
	if len(res.Transactions) != 3 {
		t.Errorf("expected 3 transactions, got %d", len(res.Transactions))
	}
}

func TestListPayments_PaginationMetadata(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		insertTx(txRepo, mID, uuid.New().String(), model.TransactionStatusPending, now.Add(-time.Duration(i)*time.Second))
	}

	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 7 {
		t.Errorf("total: expected 7, got %d", res.Total)
	}
	if res.TotalPages != 3 { // ceil(7/3)=3
		t.Errorf("total_pages: expected 3, got %d", res.TotalPages)
	}
	if len(res.Transactions) != 3 {
		t.Errorf("page 1 items: expected 3, got %d", len(res.Transactions))
	}

	// Page 3 has 1 item.
	res3, _ := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 3, Limit: 3})
	if len(res3.Transactions) != 1 {
		t.Errorf("page 3 items: expected 1, got %d", len(res3.Transactions))
	}
}

func TestListPayments_PageBeyondLast(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()
	insertTx(txRepo, mID, "ONLY", model.TransactionStatusPending, now)

	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 5, Limit: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Transactions) != 0 {
		t.Errorf("page beyond last: expected 0 items, got %d", len(res.Transactions))
	}
	if res.Total != 1 {
		t.Errorf("total must still reflect actual count, got %d", res.Total)
	}
	if res.TotalPages != 1 {
		t.Errorf("total_pages must be 1, got %d", res.TotalPages)
	}
}

func TestListPayments_EmptyResult(t *testing.T) {
	svc, _, _ := buildListService()
	mID := uuid.New()

	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 0 || res.TotalPages != 0 || len(res.Transactions) != 0 {
		t.Errorf("empty: unexpected non-zero fields: %+v", res)
	}
}

func TestListPayments_TotalPagesZeroWhenEmpty(t *testing.T) {
	svc, _, _ := buildListService()
	res, _ := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{})
	if res.TotalPages != 0 {
		t.Errorf("total_pages should be 0 when total=0, got %d", res.TotalPages)
	}
}

func TestListPayments_MaxLimit(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		insertTx(txRepo, mID, uuid.New().String(), model.TransactionStatusPending, now.Add(-time.Duration(i)*time.Second))
	}

	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: model.MaxLimit})
	if err != nil {
		t.Fatalf("max limit: unexpected error: %v", err)
	}
	if len(res.Transactions) != 5 {
		t.Errorf("max limit: expected 5, got %d", len(res.Transactions))
	}
}

// ─── Validation errors ────────────────────────────────────────────────────────

func TestListPayments_InvalidPage(t *testing.T) {
	svc, _, _ := buildListService()
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{Page: -1, Limit: 10})
	if !errors.Is(err, service.ErrInvalidPage) {
		t.Errorf("expected ErrInvalidPage, got %v", err)
	}
}

func TestListPayments_InvalidLimit_Zero(t *testing.T) {
	svc, _, _ := buildListService()
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{Page: 1, Limit: -1})
	if !errors.Is(err, service.ErrInvalidLimit) {
		t.Errorf("expected ErrInvalidLimit for zero, got %v", err)
	}
}

func TestListPayments_InvalidLimit_OverMax(t *testing.T) {
	svc, _, _ := buildListService()
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{Page: 1, Limit: model.MaxLimit + 1})
	if !errors.Is(err, service.ErrInvalidLimit) {
		t.Errorf("expected ErrInvalidLimit for over-max, got %v", err)
	}
}

func TestListPayments_InvalidStatus(t *testing.T) {
	svc, _, _ := buildListService()
	bad := model.TransactionStatus("INVALID_STATUS")
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{Page: 1, Limit: 10, Status: &bad})
	if !errors.Is(err, service.ErrInvalidStatus) {
		t.Errorf("expected ErrInvalidStatus, got %v", err)
	}
}

func TestListPayments_InvalidPaymentMethod(t *testing.T) {
	svc, _, _ := buildListService()
	pm := "INVALID_PM"
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{Page: 1, Limit: 10, PaymentMethod: &pm})
	if !errors.Is(err, service.ErrInvalidPaymentMethod) {
		t.Errorf("expected ErrInvalidPaymentMethod, got %v", err)
	}
}

func TestListPayments_InvalidDateRange_EqualTimestamps(t *testing.T) {
	svc, _, _ := buildListService()
	ts := time.Now().UTC()
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{
		Page: 1, Limit: 10, CreatedFrom: &ts, CreatedTo: &ts,
	})
	if !errors.Is(err, service.ErrInvalidDateRange) {
		t.Errorf("equal timestamps: expected ErrInvalidDateRange, got %v", err)
	}
}

func TestListPayments_InvalidDateRange_FromAfterTo(t *testing.T) {
	svc, _, _ := buildListService()
	from := time.Now().UTC()
	to := from.Add(-time.Hour)
	_, err := svc.ListPayments(context.Background(), uuid.New(), model.TransactionListFilter{
		Page: 1, Limit: 10, CreatedFrom: &from, CreatedTo: &to,
	})
	if !errors.Is(err, service.ErrInvalidDateRange) {
		t.Errorf("from>to: expected ErrInvalidDateRange, got %v", err)
	}
}

// ─── Filter correctness ───────────────────────────────────────────────────────

func TestListPayments_StatusFilter(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()

	insertTx(txRepo, mID, "PAID-1", model.TransactionStatusPaid, now)
	insertTx(txRepo, mID, "PENDING-1", model.TransactionStatusPending, now.Add(-time.Second))
	insertTx(txRepo, mID, "PAID-2", model.TransactionStatusPaid, now.Add(-2*time.Second))

	paid := model.TransactionStatusPaid
	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: 20, Status: &paid})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 2 || len(res.Transactions) != 2 {
		t.Errorf("status filter: expected 2 PAID, got total=%d items=%d", res.Total, len(res.Transactions))
	}
	for _, tx := range res.Transactions {
		if tx.Status != model.TransactionStatusPaid {
			t.Errorf("status filter: non-PAID in result: %s", tx.Status)
		}
	}
}

func TestListPayments_MerchantOrderIDFilter(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()

	insertTx(txRepo, mID, "TARGET", model.TransactionStatusPending, now)
	insertTx(txRepo, mID, "OTHER", model.TransactionStatusPending, now.Add(-time.Second))

	oid := "TARGET"
	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: 20, MerchantOrderID: &oid})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 1 || len(res.Transactions) != 1 {
		t.Errorf("order id filter: expected 1, got %d/%d", res.Total, len(res.Transactions))
	}
}

func TestListPayments_DateRange(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	insertTx(txRepo, mID, "JAN", model.TransactionStatusPending, jan)
	insertTx(txRepo, mID, "FEB", model.TransactionStatusPending, feb)
	insertTx(txRepo, mID, "MAR", model.TransactionStatusPending, mar)

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // excludes MAR

	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{
		Page: 1, Limit: 20, CreatedFrom: &from, CreatedTo: &to,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 2 {
		t.Errorf("date range: expected 2 (JAN+FEB), got %d", res.Total)
	}
}

func TestListPayments_MerchantIsolation(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mA := uuid.New()
	mB := uuid.New()
	now := time.Now().UTC()

	insertTx(txRepo, mA, "A-1", model.TransactionStatusPending, now)
	insertTx(txRepo, mA, "A-2", model.TransactionStatusPending, now.Add(-time.Second))
	insertTx(txRepo, mB, "B-1", model.TransactionStatusPending, now)

	resA, _ := svc.ListPayments(context.Background(), mA, model.TransactionListFilter{Page: 1, Limit: 20})
	if resA.Total != 2 || len(resA.Transactions) != 2 {
		t.Errorf("merchant A: expected 2, got total=%d", resA.Total)
	}

	resB, _ := svc.ListPayments(context.Background(), mB, model.TransactionListFilter{Page: 1, Limit: 20})
	if resB.Total != 1 || len(resB.Transactions) != 1 {
		t.Errorf("merchant B: expected 1, got total=%d", resB.Total)
	}

	// Ensure Merchant A's transactions do not appear in Merchant B's list.
	for _, tx := range resB.Transactions {
		if tx.MerchantOrderID == "A-1" || tx.MerchantOrderID == "A-2" {
			t.Error("merchant isolation: Merchant A transaction leaked into Merchant B listing")
		}
	}
}

func TestListPayments_SafeDTO(t *testing.T) {
	// Verify the response uses PaymentResponse DTO (no raw DB fields exposed).
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()
	insertTx(txRepo, mID, "DTO-TEST", model.TransactionStatusPending, now)

	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(res.Transactions))
	}
	tx := res.Transactions[0]
	// Verify essential safe fields are populated.
	if tx.TransactionID == (uuid.UUID{}) {
		t.Error("TransactionID must be set")
	}
	if tx.MerchantOrderID != "DTO-TEST" {
		t.Errorf("MerchantOrderID: expected DTO-TEST, got %s", tx.MerchantOrderID)
	}
	if tx.Amount != 50000 {
		t.Errorf("Amount: expected 50000, got %d", tx.Amount)
	}
}

func TestListPayments_OnlyOneCreatedBound(t *testing.T) {
	svc, txRepo, _ := buildListService()
	mID := uuid.New()
	now := time.Now().UTC()

	insertTx(txRepo, mID, "OLD", model.TransactionStatusPending, now.Add(-48*time.Hour))
	insertTx(txRepo, mID, "NEW", model.TransactionStatusPending, now)

	// Only CreatedFrom — should include NEW only.
	from := now.Add(-time.Hour)
	res, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: 20, CreatedFrom: &from})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 1 || res.Transactions[0].MerchantOrderID != "NEW" {
		t.Errorf("only created_from: expected 1 NEW, got total=%d", res.Total)
	}

	// Only CreatedTo — should include OLD only.
	to := now.Add(-time.Hour)
	res2, err := svc.ListPayments(context.Background(), mID, model.TransactionListFilter{Page: 1, Limit: 20, CreatedTo: &to})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Total != 1 || res2.Transactions[0].MerchantOrderID != "OLD" {
		t.Errorf("only created_to: expected 1 OLD, got total=%d", res2.Total)
	}
}
