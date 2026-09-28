package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/service"
)

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
