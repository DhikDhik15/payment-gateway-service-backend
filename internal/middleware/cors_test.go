package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

func TestCORS_PreflightCarriesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS("http://localhost:5173"))
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("preflight response did not carry X-Request-ID")
	}
}

func TestCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("Allowed origin - GET request", func(t *testing.T) {
		r := gin.New()
		r.Use(middleware.CORS("http://localhost:5173,http://localhost:3000"))
		r.GET("/test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
		}

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
			t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, "http://localhost:5173")
		}

		if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("Access-Control-Allow-Credentials: got %q, want %q", got, "true")
		}

		if exposed := w.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exposed, "X-Request-ID") || !strings.Contains(exposed, "Retry-After") {
			t.Errorf("Access-Control-Expose-Headers = %q, want request/retry headers", exposed)
		}

		if got := w.Header().Get("Vary"); got != "Origin" {
			t.Errorf("Vary: got %q, want %q", got, "Origin")
		}
	})

	t.Run("Allowed origin - OPTIONS preflight", func(t *testing.T) {
		r := gin.New()
		r.Use(middleware.CORS("http://localhost:5173"))
		r.POST("/api/v1/simulator/payments", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		req, _ := http.NewRequest(http.MethodOptions, "/api/v1/simulator/payments", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,idempotency-key")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("status: got %d, want %d", w.Code, http.StatusNoContent)
		}

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
			t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, "http://localhost:5173")
		}

		headers := w.Header().Get("Access-Control-Allow-Headers")
		requiredHeaders := []string{"Content-Type", "Authorization", "X-Request-ID", "X-Refresh-Token", "X-Admin-Key", "X-API-Key", "Idempotency-Key"}
		for _, rh := range requiredHeaders {
			if !strings.Contains(strings.ToLower(headers), strings.ToLower(rh)) {
				t.Errorf("Access-Control-Allow-Headers: %q does not contain required header %q", headers, rh)
			}
		}

		methods := w.Header().Get("Access-Control-Allow-Methods")
		if !strings.Contains(methods, "POST") || !strings.Contains(methods, "OPTIONS") {
			t.Errorf("Access-Control-Allow-Methods: got %q, want it to contain POST and OPTIONS", methods)
		}
	})

	t.Run("Disallowed origin", func(t *testing.T) {
		r := gin.New()
		r.Use(middleware.CORS("http://localhost:5173"))
		r.GET("/test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Origin", "http://malicious.com")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
		}

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Access-Control-Allow-Origin should be empty for disallowed origin, got %q", got)
		}
	})

	t.Run("Empty configuration (CORS disabled)", func(t *testing.T) {
		r := gin.New()
		r.Use(middleware.CORS(""))
		r.GET("/test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Access-Control-Allow-Origin should be empty when disabled, got %q", got)
		}
	})
}
