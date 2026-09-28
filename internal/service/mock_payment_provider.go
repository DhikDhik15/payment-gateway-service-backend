package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
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
	store          repository.MockPaymentRepository
	frontendURL    string
	createCalls    atomic.Uint64
}

// NewMockPaymentProvider returns a MockPaymentProvider with sensible defaults
// (always succeeds, 30-minute expiry).
func NewMockPaymentProvider() *MockPaymentProvider {
	// Unit-test-only constructor. Server wiring always uses the configured,
	// persisted constructor below.
	return &MockPaymentProvider{frontendURL: "https://mock-provider.test"}
}

func NewPersistedMockPaymentProvider(store repository.MockPaymentRepository, frontendURL string) *MockPaymentProvider {
	return &MockPaymentProvider{store: store, frontendURL: strings.TrimRight(frontendURL, "/")}
}

// Name satisfies PaymentProvider — returns "MOCK".
func (m *MockPaymentProvider) Name() string {
	return mockProviderName
}

// CreatePayment simulates payment creation.
// Returns a deterministic-enough response for unit tests.
func (m *MockPaymentProvider) CreatePayment(ctx context.Context, req ProviderCreateRequest) (*ProviderPaymentResponse, error) {
	m.createCalls.Add(1)
	if m.ShouldTimeout {
		return nil, ErrProviderTimeout
	}
	if m.ShouldFailCreate {
		return nil, ErrProviderFailure
	}

	mockTxID := generateMockID()
	expiry := m.expiredAt()
	if m.store != nil {
		if err := m.store.Create(ctx, &model.MockPayment{PublicID: mockTxID, ProviderTransactionID: mockTxID, GatewayTransactionID: req.TransactionID, MerchantOrderID: req.MerchantOrderID, Amount: req.Amount, Currency: req.Currency, PaymentMethod: req.PaymentMethod, Status: model.TransactionStatusPending, ExpiredAt: expiry, CreatedAt: time.Now().UTC()}); err != nil {
			return nil, fmt.Errorf("persist mock provider transaction: %w", err)
		}
	}
	frontendURL := m.frontendURL
	if frontendURL == "" {
		frontendURL = "https://mock-provider.test"
	}

	return &ProviderPaymentResponse{
		Provider:              mockProviderName,
		ProviderTransactionID: mockTxID,
		Status:                "PENDING",
		PaymentURL:            fmt.Sprintf("%s/pay/%s", frontendURL, mockTxID),
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

// generateMockID returns a 128-bit random public capability in the format
// "MOCK-TXN-{hex32}". It is safe to expose only in the limited development
// provider flow; it is never accepted as dashboard or merchant API auth.
func generateMockID() string {
	b := make([]byte, 16)
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
