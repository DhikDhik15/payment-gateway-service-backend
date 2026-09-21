package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
)

// MockRefundProvider simulates refund API calls for tests and local dev.
type MockRefundProvider struct {
	ShouldFail       bool
	ShouldTimeout    bool
	ReturnProcessing bool

	createCalls atomic.Uint64
}

func NewMockRefundProvider() *MockRefundProvider {
	return &MockRefundProvider{}
}

func (m *MockRefundProvider) ProviderName() string {
	return mockProviderName
}

func (m *MockRefundProvider) CreateRefund(_ context.Context, _ ProviderRefundCreateRequest) (*ProviderRefundResponse, error) {
	m.createCalls.Add(1)
	if m.ShouldTimeout {
		return nil, ErrProviderTimeout
	}
	if m.ShouldFail {
		return nil, ErrProviderFailure
	}
	refID := "MOCK-REF-" + randomHex(4)
	status := "SUCCEEDED"
	if m.ReturnProcessing {
		status = "PROCESSING"
	}
	return &ProviderRefundResponse{
		ProviderRefundID: refID,
		Status:           status,
	}, nil
}

func (m *MockRefundProvider) CreateRefundCallCount() uint64 {
	return m.createCalls.Load()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b)
}
