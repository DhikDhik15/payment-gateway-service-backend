package service_test

// ─── Payment detail order-item breakdown tests ──────────────────────────────
//
// GetPaymentDetail enriches the merchant-scoped payment with the linked
// order's immutable item snapshots (name, unit price, quantity, stored
// subtotal) for the dashboard product breakdown. These tests use the existing
// in-memory doubles plus a narrow OrderItemsLookup fake — no database
// required. Values asserted here are historical snapshots; the fake has no
// product catalog, so catalog prices can never leak in.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// itemsTxRepo adds OrderItemsLookup support to the existing list fake.
type itemsTxRepo struct {
	*mockListTransactionRepo
	items       map[uuid.UUID][]model.OrderItem
	err         error
	gotMerchant uuid.UUID
	gotOrderID  uuid.UUID
	calls       int
}

var _ repository.OrderItemsLookup = (*itemsTxRepo)(nil)

func (r *itemsTxRepo) GetOrderItems(_ context.Context, merchantID, orderID uuid.UUID) ([]model.OrderItem, error) {
	r.calls++
	r.gotMerchant = merchantID
	r.gotOrderID = orderID
	if r.err != nil {
		return nil, r.err
	}
	return r.items[orderID], nil
}

func detailService(repo *itemsTxRepo) service.PaymentService {
	return service.NewPaymentService(repo, newMockAttemptRepo(), service.NewMockPaymentProvider())
}

func detailItem(productID, name string, unitPrice, quantity, subtotal int64) model.OrderItem {
	var pid *uuid.UUID
	if productID != "" {
		parsed := uuid.MustParse(productID)
		pid = &parsed
	}
	return model.OrderItem{
		ID:          uuid.New(),
		MerchantID:  uuid.Nil,
		ProductID:   pid,
		ProductName: name,
		Quantity:    quantity,
		UnitPrice:   unitPrice,
		Subtotal:    subtotal,
		CreatedAt:   time.Now().UTC(),
	}
}

func TestGetPaymentDetail_SingleItem(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	orderID := uuid.New()
	repo := &itemsTxRepo{
		mockListTransactionRepo: newMockListTxRepo(),
		items: map[uuid.UUID][]model.OrderItem{
			orderID: {
				detailItem("5fcaf6af-ea8f-4328-866e-168d07e7c9a0", "Kopi Arabika", 35000, 2, 70000),
			},
		},
	}
	svc := detailService(repo)
	linked := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-order-1", model.TransactionStatusPending, time.Now().UTC())
	linked.OrderID = &orderID

	got, err := svc.GetPaymentDetail(ctx, merchantID, linked.ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if got.TransactionID != linked.ID {
		t.Error("transaction ID mismatch")
	}
	if got.Amount != linked.Amount || got.Status != linked.Status {
		t.Error("existing payment fields must remain intact")
	}
	if len(got.OrderItems) != 1 {
		t.Fatalf("expected 1 order item, got %d", len(got.OrderItems))
	}
	item := got.OrderItems[0]
	if item.ProductName != "Kopi Arabika" || item.UnitPrice != 35000 || item.Quantity != 2 || item.Subtotal != 70000 {
		t.Errorf("snapshot values must pass through verbatim, got %+v", item)
	}
	if repo.calls != 1 {
		t.Errorf("expected exactly one items lookup, got %d", repo.calls)
	}
	if repo.gotMerchant != merchantID || repo.gotOrderID != orderID {
		t.Errorf("lookup must be scoped to the payment merchant and linked order, got %s / %s", repo.gotMerchant, repo.gotOrderID)
	}
}

func TestGetPaymentDetail_MultipleItemsPreserveOrder(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	orderID := uuid.New()
	repo := &itemsTxRepo{
		mockListTransactionRepo: newMockListTxRepo(),
		items: map[uuid.UUID][]model.OrderItem{
			orderID: {
				detailItem("5fcaf6af-ea8f-4328-866e-168d07e7c9a0", "Kopi Arabika", 35000, 2, 70000),
				detailItem("7a1b2c3d-4e5f-6071-8273-94a5b6c7d8e9", "Teh Hijau", 20000, 1, 20000),
			},
		},
	}
	svc := detailService(repo)
	linked := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-order-2", model.TransactionStatusPaid, time.Now().UTC())
	linked.OrderID = &orderID

	got, err := svc.GetPaymentDetail(ctx, merchantID, linked.ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if len(got.OrderItems) != 2 {
		t.Fatalf("expected 2 order items, got %d", len(got.OrderItems))
	}
	if got.OrderItems[0].ProductName != "Kopi Arabika" || got.OrderItems[1].ProductName != "Teh Hijau" {
		t.Errorf("canonical item order must be preserved, got %+v", got.OrderItems)
	}
	var sum int64
	for _, item := range got.OrderItems {
		sum += item.Subtotal
	}
	if sum != 90000 {
		t.Errorf("product subtotal must sum stored line subtotals, got %d", sum)
	}
}

func TestGetPaymentDetail_NoLinkedOrderOmitsItems(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	repo := &itemsTxRepo{mockListTransactionRepo: newMockListTxRepo()}
	svc := detailService(repo)
	legacy := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-legacy", model.TransactionStatusPending, time.Now().UTC())

	got, err := svc.GetPaymentDetail(ctx, merchantID, legacy.ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if len(got.OrderItems) != 0 {
		t.Errorf("payments without a linked order must omit items, got %+v", got.OrderItems)
	}
	if repo.calls != 0 {
		t.Errorf("no items lookup expected without a linked order, got %d", repo.calls)
	}
}

func TestGetPaymentDetail_LookupErrorOmitsItems(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	orderID := uuid.New()
	repo := &itemsTxRepo{
		mockListTransactionRepo: newMockListTxRepo(),
		err:                     errors.New("db unavailable"),
	}
	svc := detailService(repo)
	linked := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-order-3", model.TransactionStatusPending, time.Now().UTC())
	linked.OrderID = &orderID

	got, err := svc.GetPaymentDetail(ctx, merchantID, linked.ID)
	if err != nil {
		t.Fatalf("items lookup failure must not fail the detail read, got %v", err)
	}
	if len(got.OrderItems) != 0 {
		t.Errorf("unresolvable items must be omitted, got %+v", got.OrderItems)
	}
	if got.TransactionID != linked.ID {
		t.Error("existing payment fields must remain intact on lookup failure")
	}
}

func TestGetPaymentDetail_WrongMerchant(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	otherMerchant := uuid.New()
	orderID := uuid.New()
	repo := &itemsTxRepo{
		mockListTransactionRepo: newMockListTxRepo(),
		items: map[uuid.UUID][]model.OrderItem{
			orderID: {detailItem("", "Kopi Arabika", 35000, 1, 35000)},
		},
	}
	svc := detailService(repo)
	linked := insertTx(repo.mockListTransactionRepo, merchantID, "merchant-order-4", model.TransactionStatusPending, time.Now().UTC())
	linked.OrderID = &orderID

	_, err := svc.GetPaymentDetail(ctx, otherMerchant, linked.ID)
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		t.Errorf("expected ErrTransactionNotFound for cross-merchant access, got %v", err)
	}
	if repo.calls != 0 {
		t.Errorf("no items lookup expected after failed scoping, got %d", repo.calls)
	}
}
