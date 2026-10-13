package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrInvalidOrder        = errors.New("invalid order items or quantity")
	ErrOrderProductMissing = errors.New("product not found or inactive")
	ErrOrderCurrency       = errors.New("mixed order currencies")
	ErrOrderOverflow       = errors.New("order total exceeds bigint range")
	ErrOrderKey            = errors.New("invalid order idempotency key")
	ErrOrderKeyConflict    = errors.New("order idempotency key reused for different request")
)

const (
	MaxOrderItems    = 100
	MaxOrderQuantity = 1000
)

type OrderService interface {
	CreateOrder(ctx context.Context, merchantID uuid.UUID, key string, req model.CreateOrderRequest) (*model.Order, bool, error)
	GetOrder(ctx context.Context, merchantID, orderID uuid.UUID) (*model.Order, error)
	ListOrders(ctx context.Context, merchantID uuid.UUID, page, limit int) (model.OrderListResult, error)
}

type orderService struct{ repo repository.OrderRepository }

func NewOrderService(repo repository.OrderRepository) OrderService { return &orderService{repo: repo} }

func (s *orderService) CreateOrder(ctx context.Context, merchantID uuid.UUID, key string, req model.CreateOrderRequest) (*model.Order, bool, error) {
	if key == "" || len(key) > 255 || strings.TrimSpace(key) != key {
		return nil, false, ErrOrderKey
	}
	for _, char := range key {
		if char < 33 || char > 126 {
			return nil, false, ErrOrderKey
		}
	}
	if len(req.Items) == 0 || len(req.Items) > MaxOrderItems {
		return nil, false, ErrInvalidOrder
	}
	// Duplicate product IDs are rejected rather than normalized; this keeps
	// each item a single unambiguous snapshot. Sort for equivalent-request
	// hashing AND a stable product lock order (avoids cross-request deadlocks).
	items := append([]model.CreateOrderItemRequest(nil), req.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ProductID.String() < items[j].ProductID.String() })
	for i, item := range items {
		if item.ProductID == uuid.Nil || item.Quantity < 1 || item.Quantity > MaxOrderQuantity ||
			(i > 0 && item.ProductID == items[i-1].ProductID) {
			return nil, false, ErrInvalidOrder
		}
	}
	canonical, err := json.Marshal(model.CreateOrderRequest{Items: items})
	if err != nil {
		return nil, false, err
	}
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	var order *model.Order
	var replay bool
	err = s.repo.InTransaction(ctx, func(tx repository.OrderTransaction) error {
		if err := tx.LockIdempotencyKey(ctx, merchantID, key); err != nil {
			return err
		}
		existing, err := tx.FindByIdempotencyKey(ctx, merchantID, key)
		if err == nil {
			if existing.RequestHash != hash {
				return ErrOrderKeyConflict
			}
			order, replay = existing, true
			return nil
		}
		if !errors.Is(err, repository.ErrOrderNotFound) {
			return err
		}
		order = &model.Order{
			ID: uuid.New(), MerchantID: merchantID, Status: model.OrderStatusCreated,
			IdempotencyKey: key, RequestHash: hash, Items: make([]model.OrderItem, 0, len(items)),
		}
		for _, item := range items {
			product, err := tx.GetProductForOrder(ctx, merchantID, item.ProductID)
			if errors.Is(err, repository.ErrProductNotFound) {
				return ErrOrderProductMissing
			}
			if err != nil {
				return err
			}
			if !product.Active {
				return ErrOrderProductMissing
			}
			if order.Currency == "" {
				order.Currency = product.Currency
			} else if product.Currency != order.Currency {
				return ErrOrderCurrency
			}
			if product.Price < 0 || product.Price > math.MaxInt64/item.Quantity {
				return ErrOrderOverflow
			}
			lineTotal := product.Price * item.Quantity
			if lineTotal > math.MaxInt64-order.Subtotal {
				return ErrOrderOverflow
			}
			order.Subtotal += lineTotal
			productID := product.ID
			order.Items = append(order.Items, model.OrderItem{
				ID: uuid.New(), OrderID: order.ID, MerchantID: merchantID, ProductID: &productID,
				ProductName: product.Name, Quantity: item.Quantity, UnitPrice: product.Price, Subtotal: lineTotal,
			})
		}
		order.TotalAmount = order.Subtotal // no taxes, discounts, fees or FX in Phase 8A
		return tx.CreateOrder(ctx, order)
	})
	if err != nil {
		return nil, false, err
	}
	return order, replay, nil
}

func (s *orderService) GetOrder(ctx context.Context, merchantID, orderID uuid.UUID) (*model.Order, error) {
	return s.repo.GetOrderByID(ctx, merchantID, orderID)
}

func (s *orderService) ListOrders(ctx context.Context, merchantID uuid.UUID, page, limit int) (model.OrderListResult, error) {
	if page < 1 || limit < 1 || limit > model.MaxLimit || page > math.MaxInt/limit {
		return model.OrderListResult{}, ErrInvalidOrder
	}
	return s.repo.ListOrders(ctx, merchantID, page, limit)
}
