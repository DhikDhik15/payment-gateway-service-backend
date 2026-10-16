package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
)

// inMemoryMockPaymentStore implements repository.MockPaymentRepository without
// a database, for provider-level URL tests.
type inMemoryMockPaymentStore struct {
	mu       sync.Mutex
	payments map[string]*model.MockPayment
}

func newInMemoryMockPaymentStore() *inMemoryMockPaymentStore {
	return &inMemoryMockPaymentStore{payments: make(map[string]*model.MockPayment)}
}

func (s *inMemoryMockPaymentStore) Create(_ context.Context, p *model.MockPayment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[p.PublicID] = p
	return nil
}

func (s *inMemoryMockPaymentStore) FindByPublicID(_ context.Context, id string) (*model.MockPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return nil, repository.ErrMockPaymentNotFound
	}
	return p, nil
}

func (s *inMemoryMockPaymentStore) FindByGatewayTransactionID(_ context.Context, id string) (*model.MockPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.payments {
		if p.ProviderTransactionID == id || p.GatewayTransactionID == id {
			return p, nil
		}
	}
	return nil, repository.ErrMockPaymentNotFound
}

func (s *inMemoryMockPaymentStore) Transition(_ context.Context, _ string, _ model.TransactionStatus) (*model.MockPayment, error) {
	return nil, repository.ErrMockPaymentInvalidState
}

func pendingMockPayment(publicID, gatewayTxID string) *model.MockPayment {
	return &model.MockPayment{
		PublicID:              publicID,
		ProviderTransactionID: publicID,
		GatewayTransactionID:  gatewayTxID,
		MerchantOrderID:       "order-1",
		Amount:                50000,
		Currency:              "IDR",
		PaymentMethod:         "QRIS",
		Status:                model.TransactionStatusPending,
		ExpiredAt:             time.Now().UTC().Add(30 * time.Minute),
		CreatedAt:             time.Now().UTC(),
	}
}

func TestPersistedMockPaymentProviderUsesConfiguredFrontendURL(t *testing.T) {
	provider := service.NewPersistedMockPaymentProvider(nil, "http://localhost:5173")

	response, err := provider.CreatePayment(context.Background(), service.ProviderCreateRequest{
		TransactionID:   "transaction-1",
		MerchantOrderID: "order-1",
		Amount:          50000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
	})
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}

	const prefix = "http://localhost:5173/pay/MOCK-TXN-"
	if !strings.HasPrefix(response.PaymentURL, prefix) {
		t.Fatalf("PaymentURL = %q, want prefix %q", response.PaymentURL, prefix)
	}
	if response.ProviderTransactionID == "" || !strings.HasSuffix(response.PaymentURL, response.ProviderTransactionID) {
		t.Fatalf("PaymentURL %q does not preserve provider identifier %q", response.PaymentURL, response.ProviderTransactionID)
	}
}

func TestPersistedMockPaymentProviderGetPaymentPreservesConfiguredFrontendURL(t *testing.T) {
	store := newInMemoryMockPaymentStore()
	p := pendingMockPayment("MOCK-TXN-"+strings.Repeat("a", 32), "gateway-a")
	if err := store.Create(context.Background(), p); err != nil {
		t.Fatalf("store.Create() error = %v", err)
	}

	provider := service.NewPersistedMockPaymentProvider(store, "http://localhost:5173")
	response, err := provider.GetPayment(context.Background(), p.PublicID)
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}

	want := "http://localhost:5173/pay/" + p.PublicID
	if response.PaymentURL != want {
		t.Fatalf("PaymentURL = %q, want %q", response.PaymentURL, want)
	}
	if response.ProviderTransactionID != p.ProviderTransactionID {
		t.Fatalf("ProviderTransactionID = %q, want %q", response.ProviderTransactionID, p.ProviderTransactionID)
	}
	if response.Status != string(model.TransactionStatusPending) {
		t.Fatalf("Status = %q, want %q", response.Status, model.TransactionStatusPending)
	}
}

func TestPersistedMockPaymentProviderGetPaymentNeverReturnsPathOnlyURL(t *testing.T) {
	store := newInMemoryMockPaymentStore()
	p := pendingMockPayment("MOCK-TXN-"+strings.Repeat("b", 32), "gateway-b")
	if err := store.Create(context.Background(), p); err != nil {
		t.Fatalf("store.Create() error = %v", err)
	}

	// An empty frontend URL must not produce a path-only URL such as "/pay/MOCK-TXN-...".
	provider := service.NewPersistedMockPaymentProvider(store, "")
	response, err := provider.GetPayment(context.Background(), p.PublicID)
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if strings.HasPrefix(response.PaymentURL, "/") {
		t.Fatalf("PaymentURL = %q, must be an absolute URL, not path-only", response.PaymentURL)
	}
	if !strings.HasPrefix(response.PaymentURL, "https://mock-provider.test/pay/") {
		t.Fatalf("PaymentURL = %q, want fallback origin prefix", response.PaymentURL)
	}
	if !strings.HasSuffix(response.PaymentURL, "/"+p.PublicID) {
		t.Fatalf("PaymentURL = %q, must preserve the payment identifier", response.PaymentURL)
	}
}
