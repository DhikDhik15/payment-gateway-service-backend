package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/service"
)

func TestMidtransProviderCreateAndCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "server-key" || pass != "" {
			t.Fatal("missing Basic authentication")
		}
		switch r.URL.Path {
		case "/snap/v1/transactions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"snap-token","redirect_url":"https://pay.example/token"}`))
		case "/v2/order-1/cancel":
			_, _ = w.Write([]byte(`{"status_code":"200"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	p, err := service.NewMidtransProvider(server.URL, "server-key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.CreatePayment(context.Background(), service.ProviderCreateRequest{TransactionID: "order-1", Amount: 50000, Currency: "IDR"})
	if err != nil || got.ProviderTransactionID != "order-1" || got.PaymentURL == "" {
		t.Fatalf("unexpected create: %#v, %v", got, err)
	}
	if err := p.CancelPayment(context.Background(), "order-1"); err != nil {
		t.Fatal(err)
	}
}

func TestMidtransProviderMapsFailureAndTimeout(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer fail.Close()
	p, _ := service.NewMidtransProvider(fail.URL, "key", time.Second, fail.Client())
	_, err := p.CreatePayment(context.Background(), service.ProviderCreateRequest{TransactionID: "o", Amount: 1})
	if !errors.Is(err, service.ErrProviderFailure) {
		t.Fatalf("got %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { time.Sleep(50 * time.Millisecond) }))
	defer slow.Close()
	p, _ = service.NewMidtransProvider(slow.URL, "key", time.Millisecond, nil)
	_, err = p.CreatePayment(context.Background(), service.ProviderCreateRequest{TransactionID: "o", Amount: 1})
	if !errors.Is(err, service.ErrProviderTimeout) {
		t.Fatalf("got %v", err)
	}
}
