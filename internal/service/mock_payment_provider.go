package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

const mockProviderName = "MOCK"

// MockPaymentProvider simulates a payment provider for local development and
// unit testing. It never calls any external service.
//
// Configurable failure modes allow tests to exercise error paths without
// modifying production code:
//
//	provider := &MockPaymentProvider{ShouldFailCreate: true}
//	provider := &MockPaymentProvider{ShouldFailCancel: true}
type MockPaymentProvider struct {
	// ShouldFailCreate makes CreatePayment return ErrProviderFailure.
	ShouldFailCreate bool

	// ShouldFailCancel makes CancelPayment return ErrProviderFailure.
	ShouldFailCancel bool

	// ShouldTimeout makes all methods return ErrProviderTimeout.
	ShouldTimeout bool

	// ExpiryDuration controls how far ahead ExpiredAt is set.
	// Defaults to 30 minutes when zero.
	ExpiryDuration time.Duration
	createCalls    atomic.Uint64
}

// NewMockPaymentProvider returns a MockPaymentProvider with sensible defaults
// (always succeeds, 30-minute expiry).
func NewMockPaymentProvider() *MockPaymentProvider {
	return &MockPaymentProvider{}
}

// Name satisfies PaymentProvider — returns "MOCK".
func (m *MockPaymentProvider) Name() string {
	return mockProviderName
}

// CreatePayment simulates payment creation.
// Returns a deterministic-enough response for unit tests.
func (m *MockPaymentProvider) CreatePayment(_ context.Context, req ProviderCreateRequest) (*ProviderPaymentResponse, error) {
	m.createCalls.Add(1)
	if m.ShouldTimeout {
		return nil, ErrProviderTimeout
	}
	if m.ShouldFailCreate {
		return nil, ErrProviderFailure
	}

	mockTxID := generateMockID()
	expiry := m.expiredAt()

	return &ProviderPaymentResponse{
		Provider:              mockProviderName,
		ProviderTransactionID: mockTxID,
		Status:                "PENDING",
		PaymentURL:            fmt.Sprintf("https://mock-payment.local/pay/%s", mockTxID),
		ExpiredAt:             &expiry,
	}, nil
}

func (m *MockPaymentProvider) CreatePaymentCallCount() uint64 {
	return m.createCalls.Load()
}

// GetPayment simulates querying a payment's current status.
func (m *MockPaymentProvider) GetPayment(_ context.Context, providerTransactionID string) (*ProviderPaymentResponse, error) {
	if m.ShouldTimeout {
		return nil, ErrProviderTimeout
	}
	if m.ShouldFailCreate {
		return nil, ErrProviderFailure
	}

	expiry := m.expiredAt()
	return &ProviderPaymentResponse{
		Provider:              mockProviderName,
		ProviderTransactionID: providerTransactionID,
		Status:                "PENDING",
		ExpiredAt:             &expiry,
	}, nil
}

// CancelPayment simulates cancellation at the provider.
func (m *MockPaymentProvider) CancelPayment(_ context.Context, _ string) error {
	if m.ShouldTimeout {
		return ErrProviderTimeout
	}
	if m.ShouldFailCancel {
		return ErrProviderFailure
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// generateMockID returns a random hex string in the format "MOCK-TXN-{hex8}".
func generateMockID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback — should never happen.
		return "MOCK-TXN-fallback"
	}
	return "MOCK-TXN-" + hex.EncodeToString(b)
}

// expiredAt returns the payment expiry time.
func (m *MockPaymentProvider) expiredAt() time.Time {
	d := m.ExpiryDuration
	if d == 0 {
		d = 30 * time.Minute
	}
	return time.Now().UTC().Add(d)
}
