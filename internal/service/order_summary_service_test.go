package service_test

// ─── OrderSummary enrichment tests ───────────────────────────────────────────
//
// PaymentResponse.OrderSummary is built from immutable order_items snapshots:
// batch-loaded for list/detail reads, taken from the already-loaded order for
// order-scoped reads. These tests use the existing in-memory doubles plus a
// narrow OrderSummaryLookup fake — no database required.

import (
	"context"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// lookupTxRepo adds OrderSummaryLookup support to the existing list fake.
type lookupTxRepo struct {
	*mockListTransactionRepo
	summaries map[uuid.UUID]*model.OrderSummary
	calls     int
}

var _ repository.OrderSummaryLookup = (*lookupTxRepo)(nil)

func (r *lookupTxRepo) GetOrderSummaries(_ context.Context, _ uuid.UUID, orderIDs []uuid.UUID) (map[uuid.UUID]*model.OrderSummary, error) {
	r.calls++
	out := make(map[uuid.UUID]*model.OrderSummary)
	for _, id := range orderIDs {
		if s, ok := r.summaries[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func summaryService(repo *lookupTxRepo) service.PaymentService {
	return service.NewPaymentService(repo, newMockAttemptRepo(), service.NewMockPaymentProvider())
}

func TestListPaymentsAttachesOrderSummaries(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	orderID := uuid.New()
	repo := &lookupTxRepo{
		mockListTransactionRepo: newMockListTxRepo(),
		summaries: map[uuid.UUID]*model.OrderSummary{
			orderID: {ProductNames: []string{"Mechanical Keyboard", "Mouse"}, ProductCount: 3},
		},
	}
	svc := summaryService(repo)
	now := time.Now().UTC()
	linked := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-"+orderID.String(), model.TransactionStatusPending, now)
	linked.OrderID = &orderID
	insertTx(repo.mockListTransactionRepo, merchantID, "merchant-legacy", model.TransactionStatusPending, now.Add(-time.Minute))

	res, err := svc.ListPayments(ctx, merchantID, model.TransactionListFilter{Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(res.Transactions))
	}
	// Newest first: the order-linked transaction carries the summary…
	first := res.Transactions[0]
	if first.OrderSummary == nil {
		t.Fatal("expected order summary on the order-linked transaction")
	}
	if first.OrderSummary.ProductCount != 3 || first.OrderSummary.ProductNames[0] != "Mechanical Keyboard" {
		t.Fatalf("unexpected summary: %+v", first.OrderSummary)
	}
	// …while the legacy transaction keeps the field omitted.
	if res.Transactions[1].OrderSummary != nil {
		t.Fatalf("legacy transaction must omit the summary: %+v", res.Transactions[1].OrderSummary)
	}
	// One batched lookup for the whole page — never per-transaction.
	if repo.calls != 1 {
		t.Fatalf("expected a single batched summary lookup, got %d", repo.calls)
	}
}

func TestListPaymentsOmitsSummariesWithoutLookup(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	orderID := uuid.New()
	// Plain mock without OrderSummaryLookup: enrichment degrades gracefully.
	repo := newMockListTxRepo()
	svc := service.NewPaymentService(repo, newMockAttemptRepo(), service.NewMockPaymentProvider())
	linked := insertTx(repo, merchantID, "merchant-"+orderID.String(), model.TransactionStatusPending, time.Now().UTC())
	linked.OrderID = &orderID

	res, err := svc.ListPayments(ctx, merchantID, model.TransactionListFilter{Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(res.Transactions))
	}
	if res.Transactions[0].OrderSummary != nil {
		t.Fatalf("summary must be omitted without lookup support: %+v", res.Transactions[0].OrderSummary)
	}
}

func TestGetPaymentAttachesOrderSummary(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	orderID := uuid.New()
	repo := &lookupTxRepo{
		mockListTransactionRepo: newMockListTxRepo(),
		summaries: map[uuid.UUID]*model.OrderSummary{
			orderID: {ProductNames: []string{"Mechanical Keyboard"}, ProductCount: 1},
		},
	}
	svc := summaryService(repo)
	tx := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-"+orderID.String(), model.TransactionStatusPaid, time.Now().UTC())
	tx.OrderID = &orderID

	resp, err := svc.GetPayment(ctx, merchantID, tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.OrderSummary == nil || resp.OrderSummary.ProductNames[0] != "Mechanical Keyboard" {
		t.Fatalf("expected attached summary: %+v", resp.OrderSummary)
	}
}

func TestOrderPaymentsListUsesSnapshotNames(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	h := newOrderPaymentHarness(t, merchantID)
	orderID := h.addOrder(merchantID, "IDR", 40000)

	// Immutable snapshots as OrderService persisted them at purchase time.
	h.orders.mu.Lock()
	h.orders.orders[orderID].Items = []model.OrderItem{
		{ID: uuid.New(), OrderID: orderID, MerchantID: merchantID, ProductName: "Mechanical Keyboard", Quantity: 1, UnitPrice: 30000, Subtotal: 30000},
		{ID: uuid.New(), OrderID: orderID, MerchantID: merchantID, ProductName: "Mouse", Quantity: 1, UnitPrice: 10000, Subtotal: 10000},
	}
	h.orders.mu.Unlock()
	h.seedAttempt(orderID, merchantID, model.TransactionStatusPending, time.Minute)

	responses, err := h.svc.ListPaymentsForOrder(ctx, merchantID, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(responses))
	}
	summary := responses[0].OrderSummary
	if summary == nil {
		t.Fatal("expected order summary from the loaded order (no extra query)")
	}
	if len(summary.ProductNames) != 2 || summary.ProductNames[0] != "Mechanical Keyboard" || summary.ProductCount != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestOrderSummarySurvivesCatalogRename(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	h := newOrderPaymentHarness(t, merchantID)

	// The catalog name at purchase time…
	productID := h.orders.addProduct(merchantID, "Mechanical Keyboard", 30000, "IDR", true)
	orderID := h.addOrder(merchantID, "IDR", 30000)
	h.orders.mu.Lock()
	h.orders.orders[orderID].Items = []model.OrderItem{
		{ID: uuid.New(), OrderID: orderID, MerchantID: merchantID, ProductID: &productID, ProductName: "Mechanical Keyboard", Quantity: 1, UnitPrice: 30000, Subtotal: 30000},
	}
	// …is renamed afterwards. The transaction title must keep the snapshot.
	h.orders.products[productID] = model.Product{
		ID:         productID,
		MerchantID: merchantID,
		Name:       "Mechanical Keyboard v2",
		Price:      35000,
		Currency:   "IDR",
		Active:     true,
	}
	h.orders.mu.Unlock()
	h.seedAttempt(orderID, merchantID, model.TransactionStatusPending, time.Minute)

	responses, err := h.svc.ListPaymentsForOrder(ctx, merchantID, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 || responses[0].OrderSummary == nil {
		t.Fatalf("expected summary: %+v", responses)
	}
	if responses[0].OrderSummary.ProductNames[0] != "Mechanical Keyboard" {
		t.Fatalf("summary must use the immutable snapshot, got %+v", responses[0].OrderSummary)
	}
}

func TestOrderPaymentsListOmitsSummaryWithoutItems(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	h := newOrderPaymentHarness(t, merchantID)
	orderID := h.addOrder(merchantID, "IDR", 20000)
	// No items seeded: legacy/empty orders keep the field omitted.
	h.seedAttempt(orderID, merchantID, model.TransactionStatusPending, time.Minute)

	responses, err := h.svc.ListPaymentsForOrder(ctx, merchantID, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(responses))
	}
	if responses[0].OrderSummary != nil {
		t.Fatalf("orders without items must omit the summary: %+v", responses[0].OrderSummary)
	}
}
