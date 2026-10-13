package model

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// TransactionStatus represents every stage in the payment lifecycle.
type TransactionStatus string

const (
	TransactionStatusCreated   TransactionStatus = "CREATED"
	TransactionStatusPending   TransactionStatus = "PENDING"
	TransactionStatusPaid      TransactionStatus = "PAID"
	TransactionStatusFailed    TransactionStatus = "FAILED"
	TransactionStatusExpired   TransactionStatus = "EXPIRED"
	TransactionStatusCancelled TransactionStatus = "CANCELLED"
)

// allowedTransitions defines the valid next states for each current state.
// Any transition not listed here is forbidden.
var allowedTransitions = map[TransactionStatus][]TransactionStatus{
	TransactionStatusCreated: {
		TransactionStatusPending,
		TransactionStatusFailed,
		TransactionStatusCancelled,
	},
	TransactionStatusPending: {
		TransactionStatusPaid,
		TransactionStatusFailed,
		TransactionStatusExpired,
		TransactionStatusCancelled,
	},
	// Terminal states — no outbound transitions.
	TransactionStatusPaid:      {},
	TransactionStatusFailed:    {},
	TransactionStatusExpired:   {},
	TransactionStatusCancelled: {},
}

// CanTransitionTo returns true when transitioning from the current status to
// next is permitted by the state machine.
func (s TransactionStatus) CanTransitionTo(next TransactionStatus) bool {
	allowed, ok := allowedTransitions[s]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == next {
			return true
		}
	}
	return false
}

// IsTerminal returns true when no further state transitions are possible.
func (s TransactionStatus) IsTerminal() bool {
	return len(allowedTransitions[s]) == 0
}

// IsCancellable returns true when a transaction in this state can be cancelled.
func (s TransactionStatus) IsCancellable() bool {
	return s == TransactionStatusCreated || s == TransactionStatusPending
}

// Transaction is the core domain entity representing a single payment request.
// It maps 1-to-1 to the `transactions` database table.
type Transaction struct {
	ID                    uuid.UUID         `db:"id"`
	MerchantID            uuid.UUID         `db:"merchant_id"`
	OrderID               *uuid.UUID        `db:"order_id" json:"order_id,omitempty"`
	MerchantOrderID       string            `db:"merchant_order_id"`
	Amount                int64             `db:"amount"`
	Currency              string            `db:"currency"`
	PaymentMethod         string            `db:"payment_method"`
	Provider              *string           `db:"provider"`
	ProviderTransactionID *string           `db:"provider_transaction_id"`
	PaymentURL            *string           `db:"payment_url"`
	Status                TransactionStatus `db:"status"`
	ExpiredAt             *time.Time        `db:"expired_at"`
	PaidAt                *time.Time        `db:"paid_at"`
	RefundedAmount        int64             `db:"refunded_amount"`
	ReservedRefundAmount  int64             `db:"reserved_refund_amount"`
	CreatedAt             time.Time         `db:"created_at"`
	UpdatedAt             time.Time         `db:"updated_at"`
}

// ─── Request / Response DTOs ─────────────────────────────────────────────────

// SupportedCurrencies is the set of currencies accepted in Phase 1.
var SupportedCurrencies = map[string]bool{
	"IDR": true,
}

// SupportedPaymentMethods is the set of payment methods accepted in Phase 1.
var SupportedPaymentMethods = map[string]bool{
	"QRIS": true,
}

// ─── Listing ─────────────────────────────────────────────────────────────────

// TransactionListFilter carries the validated, merchant-scoped query parameters
// for the listing endpoint.  All optional fields are nil when not supplied.
//
// Date range semantics: half-open interval [CreatedFrom, CreatedTo).
//   - created_at >= CreatedFrom  (inclusive)
//   - created_at <  CreatedTo    (exclusive)
type TransactionListFilter struct {
	// Pagination — always populated (defaults applied in service layer).
	Page  int
	Limit int

	// Optional filters.
	Status          *TransactionStatus
	MerchantOrderID *string
	PaymentMethod   *string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time

	// Search is an optional free-text match against merchant_order_id,
	// provider_transaction_id, or exact transaction UUID (parameterised).
	// Used by dashboard listing; merchant integration list may leave it nil.
	Search *string
}

// ListPaymentsResult is the service-layer return value for listing.
type ListPaymentsResult struct {
	Transactions []PaymentResponse
	Total        int64
	TotalPages   int
	Page         int
	Limit        int
}

// ─── Pagination constants ────────────────────────────────────────────────────

const (
	DefaultPage  = 1
	DefaultLimit = 20
	MaxLimit     = 100
)

// ─── CreatePaymentRequest ─────────────────────────────────────────────────────

// CreatePaymentRequest is the validated input for the create-payment endpoint.
type CreatePaymentRequest struct {
	MerchantOrderID string     `json:"merchant_order_id" binding:"required,min=1,max=100"`
	Amount          int64      `json:"amount"            binding:"required,min=1"`
	Currency        string     `json:"currency"          binding:"required,len=3"`
	PaymentMethod   string     `json:"payment_method"    binding:"required"`
	OrderID         *uuid.UUID `json:"-"`
}

// CreateOrderPaymentRequest is the only client input for Order-backed payment
// initiation. Amount, currency, merchant, and merchant_order_id are derived.
type CreateOrderPaymentRequest struct {
	PaymentMethod string `json:"payment_method" binding:"required"`
}

// CreatePaymentResponse is returned on successful payment creation (HTTP 201).
// It includes provider-specific fields that are only known at creation time.
type CreatePaymentResponse struct {
	TransactionID         uuid.UUID         `json:"transaction_id"`
	OrderID               *uuid.UUID        `json:"order_id,omitempty"`
	MerchantOrderID       string            `json:"merchant_order_id"`
	Amount                int64             `json:"amount"`
	Currency              string            `json:"currency"`
	PaymentMethod         string            `json:"payment_method"`
	Provider              string            `json:"provider"`
	ProviderTransactionID string            `json:"provider_transaction_id"`
	Status                TransactionStatus `json:"status"`
	PaymentURL            string            `json:"payment_url,omitempty"`
	ExpiredAt             *time.Time        `json:"expired_at,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
}

// PaymentResponse is returned when retrieving an existing payment (GET / cancel).
type PaymentResponse struct {
	TransactionID         uuid.UUID         `json:"transaction_id"`
	OrderID               *uuid.UUID        `json:"order_id,omitempty"`
	MerchantOrderID       string            `json:"merchant_order_id"`
	Amount                int64             `json:"amount"`
	Currency              string            `json:"currency"`
	PaymentMethod         string            `json:"payment_method"`
	Provider              *string           `json:"provider,omitempty"`
	ProviderTransactionID *string           `json:"provider_transaction_id,omitempty"`
	PaymentURL            *string           `json:"payment_url,omitempty"`
	Status                TransactionStatus `json:"status"`
	ExpiredAt             *time.Time        `json:"expired_at,omitempty"`
	PaidAt                *time.Time        `json:"paid_at,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	// OrderSummary is an optional, additive snapshot of the linked order's
	// product makeup, built from immutable order_items rows. Omitted for
	// transactions without a linked order (or when the summary cannot be
	// resolved); clients must fall back to merchant_order_id.
	OrderSummary *OrderSummary `json:"order_summary,omitempty"`
}

// OrderSummary describes the product makeup of a transaction's linked order
// for display purposes (e.g. Recent Transactions titles).
//
// ProductNames carries the first entries in stable order-item order
// (product_id NULLS LAST, id); at most orderSummaryNameLimit names are kept
// to bound list payloads. ProductCount is the number of distinct product
// lines with a usable name. Names come from the immutable order_items
// snapshot taken at purchase time — never from the live product catalog.
type OrderSummary struct {
	ProductNames []string `json:"product_names"`
	ProductCount int      `json:"product_count"`
}

// orderSummaryNameLimit bounds the embedded names per transaction.
const orderSummaryNameLimit = 2

// SummarizeOrderItems builds an [OrderSummary] from an order's items.
// Blank product names are skipped; nil is returned when no usable name
// exists (including an empty order) so callers can omit the field and let
// clients use their merchant-order fallback.
func SummarizeOrderItems(items []OrderItem) *OrderSummary {
	names := make([]string, 0, orderSummaryNameLimit)
	count := 0
	for _, item := range items {
		name := strings.TrimSpace(item.ProductName)
		if name == "" {
			continue
		}
		count++
		if len(names) < orderSummaryNameLimit {
			names = append(names, name)
		}
	}
	if count == 0 {
		return nil
	}
	return &OrderSummary{ProductNames: names, ProductCount: count}
}
