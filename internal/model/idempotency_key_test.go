package model

import "testing"

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
