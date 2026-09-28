package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

func TestHealthLiveReturnsProcessStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewHealthHandler(nil)
	r.GET("/health", h.Live)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHealthReadyWithoutDatabaseIs503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	h := handler.NewHealthHandler(nil)
	r.GET("/health/ready", h.Ready)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if body := w.Body.String(); body == "" {
		t.Fatal("readiness failure response was empty")
	}
}
