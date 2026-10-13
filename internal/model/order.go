package model

import (
	"time"

	"github.com/google/uuid"
)

// OrderStatusCreated is the only order state in Phase 8A; payment has its own lifecycle.
const OrderStatusCreated = "CREATED"

// Order is an immutable price snapshot of a merchant's product selection.
type Order struct {
	ID             uuid.UUID   `json:"id"`
	MerchantID     uuid.UUID   `json:"merchant_id"`
	Status         string      `json:"status"`
	Currency       string      `json:"currency"`
	Subtotal       int64       `json:"subtotal"`
	TotalAmount    int64       `json:"total_amount"`
	Items          []OrderItem `json:"items"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	IdempotencyKey string      `json:"-"`
	RequestHash    string      `json:"-"`
}

type OrderItem struct {
	ID          uuid.UUID  `json:"id"`
	OrderID     uuid.UUID  `json:"order_id"`
	MerchantID  uuid.UUID  `json:"merchant_id"`
	ProductID   *uuid.UUID `json:"product_id"` // nullable after product deletion
	ProductName string     `json:"product_name"`
	Quantity    int64      `json:"quantity"`
	UnitPrice   int64      `json:"unit_price"`
	Subtotal    int64      `json:"subtotal"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateOrderRequest intentionally contains no merchant, price, name or currency fields.
type CreateOrderRequest struct {
	Items []CreateOrderItemRequest `json:"items"`
}

type CreateOrderItemRequest struct {
	ProductID uuid.UUID `json:"product_id"`
	Quantity  int64     `json:"quantity"`
}

type OrderListResult struct {
	Orders     []Order
	Page       int
	Limit      int
	Total      int64
	TotalPages int
}
