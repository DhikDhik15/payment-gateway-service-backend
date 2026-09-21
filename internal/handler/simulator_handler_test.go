package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/gin-gonic/gin"
)

func TestSimulatorHandler_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewSimulatorHandler(nil, nil, nil, "secret", false)

	r := gin.New()
	r.POST("/api/v1/simulator/payments", h.CreatePayment)
	r.GET("/api/v1/simulator/payments/:payment_id", h.GetPayment)
	r.POST("/api/v1/simulator/payments/:payment_id/success", h.SimulateSuccess)
	r.POST("/api/v1/simulator/payments/:payment_id/fail", h.SimulateFailure)

	tests := []struct {
		method string
		url    string
	}{
		{"POST", "/api/v1/simulator/payments"},
		{"GET", "/api/v1/simulator/payments/9dbaf7dc-7b19-482a-a925-fb8dc51df70c"},
		{"POST", "/api/v1/simulator/payments/9dbaf7dc-7b19-482a-a925-fb8dc51df70c/success"},
		{"POST", "/api/v1/simulator/payments/9dbaf7dc-7b19-482a-a925-fb8dc51df70c/fail"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.url, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tc.method, tc.url, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden when disabled, got %d", w.Code)
			}

			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			if body["success"] != false {
				t.Errorf("expected success to be false, got %v", body["success"])
			}
		})
	}
}

func TestSimulatorHandler_CreatePayment_MissingIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewSimulatorHandler(nil, nil, nil, "secret", true)

	r := gin.New()
	r.POST("/api/v1/simulator/payments", h.CreatePayment)

	payload := map[string]any{
		"merchant_order_id": "ORDER-SIM-001",
		"amount":            100000,
		"currency":          "IDR",
		"payment_method":    "QRIS",
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/simulator/payments", &buf)
	// Do not set Idempotency-Key header
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)

	if body["success"] != false {
		t.Errorf("expected success to be false, got %v", body["success"])
	}
}
