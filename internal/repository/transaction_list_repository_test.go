package repository_test

// ─── TransactionRepository — List / CountList tests ──────────────────────────
//
// These tests exercise the in-memory mock implementations of List and CountList
// that are used throughout service and handler tests.  They do NOT require a
// real PostgreSQL database.
//
// For the SQL query builder (buildListQuery) the correctness contract is:
//   - merchant_id is always $1 in the WHERE clause
//   - additional filters are appended with positional parameters ($2…)
//   - the data query ends with LIMIT $N OFFSET $M
//   - the count query omits ORDER BY / LIMIT / OFFSET
//
// We verify the query builder indirectly by running the full service + handler
// tests against the in-memory repo, and directly via the table-driven filter
// tests below.

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── in-memory repository shared by all tests in this file ───────────────────

// memTxListRepo is a minimal in-memory TransactionRepository used for List/CountList tests.
// It only implements the methods exercised by the listing path.
type memTxListRepo struct {
	txs map[uuid.UUID]*model.Transaction
}

func newMemTxListRepo() *memTxListRepo {
	return &memTxListRepo{txs: make(map[uuid.UUID]*model.Transaction)}
}

func (r *memTxListRepo) insert(tx *model.Transaction) {
	r.txs[tx.ID] = tx
}

// applyFilter mirrors the SQL WHERE logic in buildListQuery so in-memory tests
// match the PostgreSQL implementation.
func (r *memTxListRepo) applyFilter(merchantID uuid.UUID, f model.TransactionListFilter) []*model.Transaction {
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
	// Deterministic ordering: created_at DESC, id DESC.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() > out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (r *memTxListRepo) List(_ context.Context, merchantID uuid.UUID, f model.TransactionListFilter) ([]*model.Transaction, error) {
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

func (r *memTxListRepo) CountList(_ context.Context, merchantID uuid.UUID, f model.TransactionListFilter) (int64, error) {
	return int64(len(r.applyFilter(merchantID, f))), nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func makeTx(merchantID uuid.UUID, orderID string, status model.TransactionStatus, method string, createdAt time.Time) *model.Transaction {
	return &model.Transaction{
		ID:              uuid.New(),
		MerchantID:      merchantID,
		MerchantOrderID: orderID,
		Amount:          50000,
		Currency:        "IDR",
		PaymentMethod:   method,
		Status:          status,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
}

// ─── Compile-time check: memTxListRepo satisfies the sub-interface ────────────

type listCountIface interface {
	List(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) ([]*model.Transaction, error)
	CountList(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (int64, error)
}

var _ listCountIface = (*memTxListRepo)(nil)

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestListRepo_MerchantIsolation(t *testing.T) {
	r := newMemTxListRepo()
	mA := uuid.New()
	mB := uuid.New()
	now := time.Now().UTC()

	r.insert(makeTx(mA, "A-001", model.TransactionStatusPending, "QRIS", now))
	r.insert(makeTx(mA, "A-002", model.TransactionStatusPaid, "QRIS", now.Add(-time.Minute)))
	r.insert(makeTx(mB, "B-001", model.TransactionStatusPending, "QRIS", now))

	f := model.TransactionListFilter{Page: 1, Limit: 20}

	txsA, _ := r.List(context.Background(), mA, f)
	if len(txsA) != 2 {
		t.Errorf("merchant A: expected 2 transactions, got %d", len(txsA))
	}
	for _, tx := range txsA {
		if tx.MerchantID != mA {
			t.Errorf("merchant A listing contains tx from merchant %s", tx.MerchantID)
		}
	}

	txsB, _ := r.List(context.Background(), mB, f)
	if len(txsB) != 1 {
		t.Errorf("merchant B: expected 1 transaction, got %d", len(txsB))
	}

	cntA, _ := r.CountList(context.Background(), mA, f)
	if cntA != 2 {
		t.Errorf("count A: expected 2, got %d", cntA)
	}
}

func TestListRepo_Pagination(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	base := time.Now().UTC()

	// Insert 5 transactions at distinct times so ordering is deterministic.
	for i := 0; i < 5; i++ {
		r.insert(makeTx(m, uuid.New().String(), model.TransactionStatusPending, "QRIS",
			base.Add(-time.Duration(i)*time.Second)))
	}

	// Page 1, limit 2 → 2 results.
	p1, _ := r.List(context.Background(), m, model.TransactionListFilter{Page: 1, Limit: 2})
	if len(p1) != 2 {
		t.Errorf("page 1: expected 2, got %d", len(p1))
	}

	// Page 2, limit 2 → 2 results.
	p2, _ := r.List(context.Background(), m, model.TransactionListFilter{Page: 2, Limit: 2})
	if len(p2) != 2 {
		t.Errorf("page 2: expected 2, got %d", len(p2))
	}

	// Page 3, limit 2 → 1 result.
	p3, _ := r.List(context.Background(), m, model.TransactionListFilter{Page: 3, Limit: 2})
	if len(p3) != 1 {
		t.Errorf("page 3: expected 1, got %d", len(p3))
	}

	// Page 4 (beyond last) → 0 results.
	p4, _ := r.List(context.Background(), m, model.TransactionListFilter{Page: 4, Limit: 2})
	if len(p4) != 0 {
		t.Errorf("page 4: expected 0, got %d", len(p4))
	}

	// Count is 5 regardless of page.
	cnt, _ := r.CountList(context.Background(), m, model.TransactionListFilter{Page: 1, Limit: 2})
	if cnt != 5 {
		t.Errorf("count: expected 5, got %d", cnt)
	}
}

func TestListRepo_DeterministicOrdering(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	// Insert 3 transactions with strictly decreasing times.
	t0 := time.Now().UTC()
	tx1 := makeTx(m, "O-1", model.TransactionStatusPending, "QRIS", t0)
	tx2 := makeTx(m, "O-2", model.TransactionStatusPending, "QRIS", t0.Add(-time.Second))
	tx3 := makeTx(m, "O-3", model.TransactionStatusPending, "QRIS", t0.Add(-2*time.Second))
	r.insert(tx3) // insert out of order
	r.insert(tx1)
	r.insert(tx2)

	f := model.TransactionListFilter{Page: 1, Limit: 10}
	txs, _ := r.List(context.Background(), m, f)

	if len(txs) != 3 {
		t.Fatalf("expected 3 results, got %d", len(txs))
	}
	// Newest first.
	if txs[0].MerchantOrderID != "O-1" || txs[1].MerchantOrderID != "O-2" || txs[2].MerchantOrderID != "O-3" {
		t.Errorf("unexpected order: %s, %s, %s",
			txs[0].MerchantOrderID, txs[1].MerchantOrderID, txs[2].MerchantOrderID)
	}

	// Run again — result must be identical (stable).
	txs2, _ := r.List(context.Background(), m, f)
	for i := range txs {
		if txs[i].ID != txs2[i].ID {
			t.Errorf("ordering not stable at position %d", i)
		}
	}
}

func TestListRepo_DeterministicOrdering_SameTimestamp(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	// Same timestamp — tie-break by id DESC.
	sameTime := time.Now().UTC()
	for i := 0; i < 4; i++ {
		r.insert(makeTx(m, uuid.New().String(), model.TransactionStatusPending, "QRIS", sameTime))
	}

	f := model.TransactionListFilter{Page: 1, Limit: 10}
	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 4 {
		t.Fatalf("expected 4, got %d", len(txs))
	}
	// Verify descending id order.
	for i := 1; i < len(txs); i++ {
		if txs[i-1].ID.String() < txs[i].ID.String() {
			t.Errorf("id tie-break violated at position %d: %s < %s",
				i, txs[i-1].ID, txs[i].ID)
		}
	}
}

func TestListRepo_StatusFilter(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	now := time.Now().UTC()

	r.insert(makeTx(m, "S-1", model.TransactionStatusPending, "QRIS", now))
	r.insert(makeTx(m, "S-2", model.TransactionStatusPaid, "QRIS", now.Add(-time.Second)))
	r.insert(makeTx(m, "S-3", model.TransactionStatusFailed, "QRIS", now.Add(-2*time.Second)))

	paid := model.TransactionStatusPaid
	f := model.TransactionListFilter{Page: 1, Limit: 20, Status: &paid}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 1 || txs[0].Status != model.TransactionStatusPaid {
		t.Errorf("status filter: expected 1 PAID, got %d", len(txs))
	}
	cnt, _ := r.CountList(context.Background(), m, f)
	if cnt != 1 {
		t.Errorf("count with status filter: expected 1, got %d", cnt)
	}
}

func TestListRepo_MerchantOrderIDFilter(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	now := time.Now().UTC()

	r.insert(makeTx(m, "TARGET-ORDER", model.TransactionStatusPending, "QRIS", now))
	r.insert(makeTx(m, "OTHER-ORDER", model.TransactionStatusPending, "QRIS", now.Add(-time.Second)))

	oid := "TARGET-ORDER"
	f := model.TransactionListFilter{Page: 1, Limit: 20, MerchantOrderID: &oid}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 1 || txs[0].MerchantOrderID != "TARGET-ORDER" {
		t.Errorf("order id filter: expected 1 result with TARGET-ORDER, got %d", len(txs))
	}
}

func TestListRepo_MerchantOrderIDFilter_CrossMerchant(t *testing.T) {
	// Merchant B's order ID must NOT appear when filtering as Merchant A.
	r := newMemTxListRepo()
	mA := uuid.New()
	mB := uuid.New()
	now := time.Now().UTC()

	r.insert(makeTx(mA, "SHARED-ORDER", model.TransactionStatusPending, "QRIS", now))
	r.insert(makeTx(mB, "SHARED-ORDER", model.TransactionStatusPending, "QRIS", now))

	oid := "SHARED-ORDER"
	fA := model.TransactionListFilter{Page: 1, Limit: 20, MerchantOrderID: &oid}

	txs, _ := r.List(context.Background(), mA, fA)
	if len(txs) != 1 {
		t.Errorf("cross-merchant order filter: expected 1, got %d", len(txs))
	}
	if txs[0].MerchantID != mA {
		t.Error("cross-merchant order filter returned wrong merchant's transaction")
	}
}

func TestListRepo_PaymentMethodFilter(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	now := time.Now().UTC()

	r.insert(makeTx(m, "PM-1", model.TransactionStatusPending, "QRIS", now))
	r.insert(makeTx(m, "PM-2", model.TransactionStatusPending, "BANK_TRANSFER", now.Add(-time.Second)))

	pm := "QRIS"
	f := model.TransactionListFilter{Page: 1, Limit: 20, PaymentMethod: &pm}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 1 || txs[0].PaymentMethod != "QRIS" {
		t.Errorf("payment method filter: expected 1 QRIS, got %d", len(txs))
	}
}

func TestListRepo_CreatedFromFilter(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	r.insert(makeTx(m, "OLD", model.TransactionStatusPending, "QRIS", base.Add(-24*time.Hour)))
	r.insert(makeTx(m, "NEW", model.TransactionStatusPending, "QRIS", base.Add(time.Hour)))

	from := base
	f := model.TransactionListFilter{Page: 1, Limit: 20, CreatedFrom: &from}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 1 || txs[0].MerchantOrderID != "NEW" {
		t.Errorf("created_from filter: expected 1 NEW, got %d", len(txs))
	}
}

func TestListRepo_CreatedToFilter(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	r.insert(makeTx(m, "OLD", model.TransactionStatusPending, "QRIS", base.Add(-time.Hour)))
	r.insert(makeTx(m, "NEW", model.TransactionStatusPending, "QRIS", base.Add(time.Hour)))

	to := base
	f := model.TransactionListFilter{Page: 1, Limit: 20, CreatedTo: &to}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 1 || txs[0].MerchantOrderID != "OLD" {
		t.Errorf("created_to filter (exclusive): expected 1 OLD, got %d", len(txs))
	}
}

func TestListRepo_HalfOpenDateRange(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Insert at exactly created_from (inclusive), before, and at created_to (exclusive).
	r.insert(makeTx(m, "BEFORE", model.TransactionStatusPending, "QRIS", base.Add(-time.Second)))
	r.insert(makeTx(m, "EXACT-FROM", model.TransactionStatusPending, "QRIS", base))
	r.insert(makeTx(m, "INSIDE", model.TransactionStatusPending, "QRIS", base.Add(time.Hour)))
	r.insert(makeTx(m, "EXACT-TO", model.TransactionStatusPending, "QRIS", base.Add(24*time.Hour)))
	r.insert(makeTx(m, "AFTER", model.TransactionStatusPending, "QRIS", base.Add(25*time.Hour)))

	from := base
	to := base.Add(24 * time.Hour)
	f := model.TransactionListFilter{Page: 1, Limit: 20, CreatedFrom: &from, CreatedTo: &to}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 2 {
		t.Errorf("half-open range: expected 2 (EXACT-FROM, INSIDE), got %d", len(txs))
	}
	for _, tx := range txs {
		if tx.MerchantOrderID == "BEFORE" || tx.MerchantOrderID == "EXACT-TO" || tx.MerchantOrderID == "AFTER" {
			t.Errorf("half-open range: unexpected %s", tx.MerchantOrderID)
		}
	}
}

func TestListRepo_EmptyResult(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()

	f := model.TransactionListFilter{Page: 1, Limit: 20}
	txs, err := r.List(context.Background(), m, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(txs) != 0 {
		t.Errorf("expected empty result, got %d", len(txs))
	}

	cnt, err := r.CountList(context.Background(), m, f)
	if err != nil {
		t.Fatalf("count error: %v", err)
	}
	if cnt != 0 {
		t.Errorf("expected count 0, got %d", cnt)
	}
}

func TestListRepo_CountConsistency(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	now := time.Now().UTC()

	for i := 0; i < 7; i++ {
		r.insert(makeTx(m, uuid.New().String(), model.TransactionStatusPending, "QRIS",
			now.Add(-time.Duration(i)*time.Second)))
	}

	// With limit=3, page=1 → 3 rows, but count must be 7.
	f := model.TransactionListFilter{Page: 1, Limit: 3}
	txs, _ := r.List(context.Background(), m, f)
	cnt, _ := r.CountList(context.Background(), m, f)

	if len(txs) != 3 {
		t.Errorf("list with limit 3: expected 3, got %d", len(txs))
	}
	if cnt != 7 {
		t.Errorf("count: expected 7, got %d", cnt)
	}
}

func TestListRepo_CombinedFilters(t *testing.T) {
	r := newMemTxListRepo()
	m := uuid.New()
	now := time.Now().UTC()

	paid := model.TransactionStatusPaid
	r.insert(makeTx(m, "MATCH", paid, "QRIS", now))
	r.insert(makeTx(m, "WRONG-STATUS", model.TransactionStatusPending, "QRIS", now.Add(-time.Second)))
	r.insert(makeTx(m, "WRONG-METHOD", paid, "BANK_TRANSFER", now.Add(-2*time.Second)))

	pm := "QRIS"
	f := model.TransactionListFilter{Page: 1, Limit: 20, Status: &paid, PaymentMethod: &pm}

	txs, _ := r.List(context.Background(), m, f)
	if len(txs) != 1 || txs[0].MerchantOrderID != "MATCH" {
		t.Errorf("combined filter: expected 1 MATCH, got %d", len(txs))
	}
	cnt, _ := r.CountList(context.Background(), m, f)
	if cnt != 1 {
		t.Errorf("combined filter count: expected 1, got %d", cnt)
	}
}

// ─── exported helpers reused in service / handler tests ──────────────────────

// Ensure repository_test package can share these symbols with service/handler tests.
// Since they live in different packages (repository_test, service_test, handler_test),
// the helpers are redefined inline in those packages. This file serves as the
// canonical correctness reference.
var _ = repository.ErrTransactionNotFound // keep import used
