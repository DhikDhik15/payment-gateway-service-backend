package model

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestPaymentRequestHash(t *testing.T) {
	a := CreatePaymentRequest{MerchantOrderID: "ORDER-1", Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS"}
	if PaymentRequestHash(a) != PaymentRequestHash(a) {
		t.Fatal("hash must be deterministic")
	}
	a.Amount++
	if PaymentRequestHash(a) == PaymentRequestHash(CreatePaymentRequest{MerchantOrderID: "ORDER-1", Amount: 50000, Currency: "IDR", PaymentMethod: "QRIS"}) {
		t.Fatal("business change must change hash")
	}
}

// TestOrderPaymentRequestHash pins the Phase 9A idempotency contract: the hash
// is derived from the merchant, the immutable Order, the server-derived amount
// and currency, and the selected payment method — never from client-supplied
// pricing fields.
func TestOrderPaymentRequestHash(t *testing.T) {
	merchantA, merchantB := uuid.New(), uuid.New()
	orderA, orderB := uuid.New(), uuid.New()

	base := func() CreatePaymentRequest {
		return CreatePaymentRequest{Amount: 77000, Currency: "IDR", PaymentMethod: "QRIS"}
	}

	if OrderPaymentRequestHash(merchantA, orderA, base()) != OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("hash must be deterministic")
	}

	// merchant_order_id is intentionally excluded: it is regenerated per
	// attempt and must not turn a replay into a 409.
	withRef := base()
	withRef.MerchantOrderID = "order-other-reference"
	if OrderPaymentRequestHash(merchantA, orderA, withRef) != OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("merchant_order_id must not participate in the hash")
	}

	changed := base()
	changed.Amount = 1
	if OrderPaymentRequestHash(merchantA, orderA, changed) == OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("server-derived amount must change the hash")
	}
	changed = base()
	changed.Currency = "USD"
	if OrderPaymentRequestHash(merchantA, orderA, changed) == OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("server-derived currency must change the hash")
	}
	changed = base()
	changed.PaymentMethod = "VA"
	if OrderPaymentRequestHash(merchantA, orderA, changed) == OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("payment method must change the hash")
	}
	if OrderPaymentRequestHash(merchantB, orderA, base()) == OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("merchant must change the hash")
	}
	if OrderPaymentRequestHash(merchantA, orderB, base()) == OrderPaymentRequestHash(merchantA, orderA, base()) {
		t.Fatal("order must change the hash")
	}
}

// TestCreateOrderPaymentRequestAcceptsOnlyPaymentMethod documents that the
// wire DTO has no pricing surface at all.
func TestCreateOrderPaymentRequestAcceptsOnlyPaymentMethod(t *testing.T) {
	var req CreateOrderPaymentRequest
	if err := json.Unmarshal([]byte(`{"payment_method":"QRIS","amount":1,"currency":"USD"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.PaymentMethod != "QRIS" {
		t.Fatalf("got %q", req.PaymentMethod)
	}
}
