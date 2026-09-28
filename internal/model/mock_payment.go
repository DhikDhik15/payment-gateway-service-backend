package model

import "time"

// MockPayment is the provider-side projection used by the development mock
// provider. It intentionally stores no merchant credentials or gateway-only
// metadata; the gateway transaction remains the canonical payment record.
type MockPayment struct {
	PublicID              string
	ProviderTransactionID string
	GatewayTransactionID  string
	MerchantOrderID       string
	Amount                int64
	Currency              string
	PaymentMethod         string
	Status                TransactionStatus
	ExpiredAt             time.Time
	CreatedAt             time.Time
	TerminalAt            *time.Time
}
