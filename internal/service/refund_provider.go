package service

import (
	"context"
)

// ProviderRefundCreateRequest is sent to RefundProvider.CreateRefund.
type ProviderRefundCreateRequest struct {
	GatewayRefundID       string
	GatewayTransactionID  string
	ProviderTransactionID string
	Amount                int64
	Currency              string
	Reason                string
}

// ProviderRefundResponse is returned by the provider after a refund request.
type ProviderRefundResponse struct {
	ProviderRefundID string
	// Status: PROCESSING, SUCCEEDED, or FAILED (provider-normalised).
	Status string
}

// RefundProvider abstracts provider-specific refund operations.
type RefundProvider interface {
	ProviderName() string
	CreateRefund(ctx context.Context, req ProviderRefundCreateRequest) (*ProviderRefundResponse, error)
}
