package service

import (
	"context"
	"errors"
	"time"
)

// ─── Provider error sentinels ─────────────────────────────────────────────────

// ErrProviderFailure is returned when the payment provider rejects the request.
var ErrProviderFailure = errors.New("payment provider failure")

// ErrProviderTimeout is returned when the provider does not respond in time.
var ErrProviderTimeout = errors.New("payment provider timeout")

// ─── Provider request / response types ───────────────────────────────────────

// ProviderCreateRequest carries the data sent to a payment provider when
// initiating a new payment.
type ProviderCreateRequest struct {
	// TransactionID is the gateway's own UUID for this transaction.
	// Sent to the provider so it can reference us in callbacks.
	TransactionID string

	// MerchantOrderID is the merchant's own order identifier.
	MerchantOrderID string

	// Amount in the smallest currency unit (e.g. IDR cents → integer).
	Amount int64

	// Currency is the ISO 4217 currency code (e.g. "IDR").
	Currency string

	// PaymentMethod is the requested payment instrument (e.g. "QRIS").
	PaymentMethod string
}

// ProviderPaymentResponse is what the provider returns after creating or
// querying a payment.
type ProviderPaymentResponse struct {
	// Provider is the name of the provider that handled the request (e.g. "MOCK").
	Provider string

	// ProviderTransactionID is the provider's own reference for this payment.
	ProviderTransactionID string

	// Status is the provider-reported payment status.
	// The service layer maps this to a TransactionStatus.
	Status string

	// PaymentURL is the URL the customer should be redirected to (if applicable).
	PaymentURL string

	// ExpiredAt is when the payment link/QR code expires.
	ExpiredAt *time.Time
}

// ─── Interface ────────────────────────────────────────────────────────────────

// PaymentProvider is the abstraction over external payment processors.
//
// Adding a new provider (QRIS, VA, e-wallet) only requires:
//  1. Implementing this interface.
//  2. Registering the implementation in main.go.
//
// PaymentService depends on this interface — never on a concrete type.
type PaymentProvider interface {
	// Name returns the provider identifier stored in the database (e.g. "MOCK").
	Name() string

	// CreatePayment initiates a new payment with the provider.
	// Returns ErrProviderFailure or ErrProviderTimeout on error.
	CreatePayment(ctx context.Context, req ProviderCreateRequest) (*ProviderPaymentResponse, error)

	// GetPayment queries the current status of a payment from the provider.
	GetPayment(ctx context.Context, providerTransactionID string) (*ProviderPaymentResponse, error)

	// CancelPayment requests the provider to cancel an in-flight payment.
	// Returns ErrProviderFailure if the provider refuses, ErrProviderTimeout on timeout.
	CancelPayment(ctx context.Context, providerTransactionID string) error
}
