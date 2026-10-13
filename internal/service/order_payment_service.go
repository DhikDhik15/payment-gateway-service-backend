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

var (
	ErrOrderPaymentNotAvailable = errors.New("order is not available for payment")
	ErrOrderPaymentConflict     = errors.New("order already has an active or paid payment")
)

// orderAttemptReconcileGrace is how long a CREATED Order payment attempt is
// treated as possibly still in flight at the provider. Only past this window
// does a retry attempt a status lookup: an in-flight create must never be
// hijacked by a concurrent initiation, and a create is never retried blindly.
// It must exceed the configured provider timeout (15s by default).
const orderAttemptReconcileGrace = 30 * time.Second

type OrderPaymentService interface {
	CreatePaymentForOrder(ctx context.Context, merchantID, orderID uuid.UUID, req model.CreateOrderPaymentRequest, key string) (*model.CreatePaymentResponse, error)
	// ListPaymentsForOrder returns the payment attempts for an Order, newest
	// first. It is strictly read-only: it never creates, mutates,
	// reconciles, simulates, or cancels anything.
	ListPaymentsForOrder(ctx context.Context, merchantID, orderID uuid.UUID) ([]*model.PaymentResponse, error)
}

type orderPaymentService struct {
	payments *paymentService
	orders   repository.OrderRepository
}

func NewOrderPaymentService(payments PaymentService, orders repository.OrderRepository) OrderPaymentService {
	implementation, ok := payments.(*paymentService)
	if !ok {
		panic("OrderPaymentService requires the configured PostgreSQL PaymentService")
	}
	return &orderPaymentService{payments: implementation, orders: orders}
}

func (s *orderPaymentService) CreatePaymentForOrder(
	ctx context.Context,
	merchantID, orderID uuid.UUID,
	req model.CreateOrderPaymentRequest,
	key string,
) (*model.CreatePaymentResponse, error) {
	order, err := s.orders.GetOrderByID(ctx, merchantID, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != model.OrderStatusCreated || order.TotalAmount <= 0 {
		return nil, ErrOrderPaymentNotAvailable
	}
	if !model.SupportedCurrencies[order.Currency] {
		return nil, ErrInvalidCurrency
	}
	if !model.SupportedPaymentMethods[req.PaymentMethod] {
		return nil, ErrInvalidPaymentMethod
	}

	paymentRequest := model.CreatePaymentRequest{
		MerchantOrderID: fmt.Sprintf("order-%s-%s", order.ID, uuid.NewString()),
		Amount:          order.TotalAmount,
		Currency:        order.Currency,
		PaymentMethod:   req.PaymentMethod,
		OrderID:         &order.ID,
	}
	hash := model.OrderPaymentRequestHash(merchantID, orderID, paymentRequest)
	return s.payments.createWithIdempotency(ctx, merchantID, key, hash, func() (*model.CreatePaymentResponse, error) {
		return s.createAttempt(ctx, merchantID, order, paymentRequest)
	})
}

// ListPaymentsForOrder returns the payment attempts recorded for an Order,
// newest first (created_at DESC, id DESC, matching the repository ordering).
//
// The Order lookup is merchant-scoped, so a foreign or missing Order surfaces
// as repository.ErrOrderNotFound and the handler maps it to 404 without
// leaking existence. This is a pure read: no attempt is created, transitioned,
// reconciled, or simulated as a side effect.
func (s *orderPaymentService) ListPaymentsForOrder(
	ctx context.Context,
	merchantID, orderID uuid.UUID,
) ([]*model.PaymentResponse, error) {
	order, err := s.orders.GetOrderByID(ctx, merchantID, orderID)
	if err != nil {
		return nil, err
	}
	lookup, ok := s.payments.txRepo.(repository.OrderPaymentLookup)
	if !ok {
		return nil, errors.New("transaction repository does not support Order payment lookup")
	}
	attempts, err := lookup.FindByMerchantAndOrderID(ctx, merchantID, orderID)
	if err != nil {
		return nil, err
	}
	// The order (with its immutable item snapshots) is already loaded above
	// for the merchant-scope check, so summaries cost no extra query.
	summary := model.SummarizeOrderItems(order.Items)
	out := make([]*model.PaymentResponse, 0, len(attempts))
	for _, tx := range attempts {
		resp := txToPaymentResponse(tx)
		resp.OrderSummary = summary
		out = append(out, resp)
	}
	return out, nil
}

func (s *orderPaymentService) createAttempt(
	ctx context.Context,
	merchantID uuid.UUID,
	order *model.Order,
	request model.CreatePaymentRequest,
) (*model.CreatePaymentResponse, error) {
	lookup, ok := s.payments.txRepo.(repository.OrderPaymentLookup)
	if !ok {
		return nil, errors.New("transaction repository does not support Order payment lookup")
	}
	attempts, err := lookup.FindByMerchantAndOrderID(ctx, merchantID, order.ID)
	if err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		switch attempt.Status {
		case model.TransactionStatusPaid:
			return nil, ErrOrderPaymentConflict
		case model.TransactionStatusCreated:
			if time.Since(attempt.CreatedAt) < orderAttemptReconcileGrace {
				// Fresh CREATED row: either a create is still in flight at the
				// provider or the outcome is still unresolved. Never start a
				// second attempt and never race the first one with a lookup.
				return nil, ErrOrderPaymentConflict
			}
			if err := s.payments.reconcileOrderAttempt(ctx, attempt); err != nil {
				return nil, err
			}
			if attempt.Status == model.TransactionStatusFailed ||
				attempt.Status == model.TransactionStatusCancelled ||
				attempt.Status == model.TransactionStatusExpired {
				continue
			}
			return nil, ErrOrderPaymentConflict
		case model.TransactionStatusPending:
			return nil, ErrOrderPaymentConflict
		}
	}

	return s.payments.createPayment(ctx, merchantID, request)
}
