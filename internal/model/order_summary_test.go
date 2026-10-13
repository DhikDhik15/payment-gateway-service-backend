package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func orderItem(name string) OrderItem {
	return OrderItem{
		ID:          uuid.New(),
		OrderID:     uuid.New(),
		ProductName: name,
		Quantity:    1,
		UnitPrice:   10000,
		Subtotal:    10000,
	}
}

func TestSummarizeOrderItems(t *testing.T) {
	t.Run("nil for empty order", func(t *testing.T) {
		if got := SummarizeOrderItems(nil); got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("single product keeps its snapshot name", func(t *testing.T) {
		got := SummarizeOrderItems([]OrderItem{orderItem("Mechanical Keyboard")})
		if got == nil {
			t.Fatal("expected summary, got nil")
		}
		if len(got.ProductNames) != 1 || got.ProductNames[0] != "Mechanical Keyboard" {
			t.Fatalf("unexpected names: %+v", got.ProductNames)
		}
		if got.ProductCount != 1 {
			t.Fatalf("unexpected count: %d", got.ProductCount)
		}
	})

	t.Run("two products keep both names", func(t *testing.T) {
		got := SummarizeOrderItems([]OrderItem{
			orderItem("Mechanical Keyboard"),
			orderItem("Mouse"),
		})
		if got == nil {
			t.Fatal("expected summary, got nil")
		}
		if len(got.ProductNames) != 2 || got.ProductNames[1] != "Mouse" {
			t.Fatalf("unexpected names: %+v", got.ProductNames)
		}
		if got.ProductCount != 2 {
			t.Fatalf("unexpected count: %d", got.ProductCount)
		}
	})

	t.Run("more than two products caps names but keeps the count", func(t *testing.T) {
		got := SummarizeOrderItems([]OrderItem{
			orderItem("Mechanical Keyboard"),
			orderItem("Mouse"),
			orderItem("Monitor"),
		})
		if got == nil {
			t.Fatal("expected summary, got nil")
		}
		if len(got.ProductNames) != 2 {
			t.Fatalf("names must be capped at two, got %+v", got.ProductNames)
		}
		if got.ProductCount != 3 {
			t.Fatalf("count must cover every line, got %d", got.ProductCount)
		}
	})

	t.Run("blank names are skipped without fabrication", func(t *testing.T) {
		got := SummarizeOrderItems([]OrderItem{
			orderItem("   "),
			orderItem("Mouse"),
		})
		if got == nil {
			t.Fatal("expected summary, got nil")
		}
		if len(got.ProductNames) != 1 || got.ProductNames[0] != "Mouse" {
			t.Fatalf("unexpected names: %+v", got.ProductNames)
		}
		if got.ProductCount != 1 {
			t.Fatalf("blank lines must not be counted, got %d", got.ProductCount)
		}
	})

	t.Run("nil when every name is blank", func(t *testing.T) {
		got := SummarizeOrderItems([]OrderItem{orderItem("  "), orderItem("")})
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("names preserve caller order", func(t *testing.T) {
		// Callers pass items in canonical item order; the summary must not
		// reorder them.
		got := SummarizeOrderItems([]OrderItem{
			orderItem("Zebra Cable"),
			orderItem("Apple Adapter"),
		})
		if got == nil || got.ProductNames[0] != "Zebra Cable" {
			t.Fatalf("order must be preserved: %+v", got)
		}
	})
}

func TestPaymentResponseOrderSummaryJSON(t *testing.T) {
	t.Run("omitted when nil (backward compatible)", func(t *testing.T) {
		body, err := json.Marshal(PaymentResponse{TransactionID: uuid.New()})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "order_summary") {
			t.Fatalf("nil summary must be omitted: %s", body)
		}
	})

	t.Run("present when populated", func(t *testing.T) {
		resp := PaymentResponse{
			TransactionID: uuid.New(),
			OrderSummary:  &OrderSummary{ProductNames: []string{"Mechanical Keyboard"}, ProductCount: 1},
		}
		body, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		var decoded PaymentResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.OrderSummary == nil || decoded.OrderSummary.ProductNames[0] != "Mechanical Keyboard" {
			t.Fatalf("round trip failed: %s", body)
		}
	})
}
