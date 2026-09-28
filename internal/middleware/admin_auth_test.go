package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newAdminTestRouter mounts one route behind AdminAuth with the given key.
func newAdminTestRouter(t *testing.T, configuredKey string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.AdminAuth(configuredKey))
	r.GET("/ops/settlements", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func adminDo(r *gin.Engine, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ops/settlements", nil)
	if key != "" {
		req.Header.Set("X-Admin-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func adminErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse response %q: %v", w.Body.String(), err)
	}
	if body.Success {
		t.Error("success must be false for an auth failure")
	}
	return body.Error.Code
}

// ─── Phase 8D.1 auth regression matrix: admin key ─────────────────────────────

func TestAdminAuth_MissingKey(t *testing.T) {
	r := newAdminTestRouter(t, "super-secret-admin-key")

	w := adminDo(r, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: got %d, want 401", w.Code)
	}
	if got := adminErrCode(t, w); got != "ADMIN_UNAUTHORIZED" {
		t.Errorf("error.code: got %q, want ADMIN_UNAUTHORIZED", got)
	}
}

func TestAdminAuth_InvalidKey(t *testing.T) {
	r := newAdminTestRouter(t, "super-secret-admin-key")

	w := adminDo(r, "wrong-key")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: got %d, want 401", w.Code)
	}
	if got := adminErrCode(t, w); got != "ADMIN_UNAUTHORIZED" {
		t.Errorf("error.code: got %q, want ADMIN_UNAUTHORIZED", got)
	}
	// The configured secret must never appear in the response.
	if strings.Contains(w.Body.String(), "super-secret-admin-key") {
		t.Error("response must not leak the configured admin key")
	}
}

func TestAdminAuth_KeyLengthMismatchRejectedWithoutPanic(t *testing.T) {
	r := newAdminTestRouter(t, "super-secret-admin-key")

	// Different-length inputs must be rejected cleanly (constant-time compare
	// returns 0 on length mismatch — no panic, no partial accept).
	for _, key := range []string{"x", "super-secret-admin-key-extra-long-value"} {
		w := adminDo(r, key)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("key %q: got %d, want 401", key, w.Code)
		}
	}
}

func TestAdminAuth_ValidKey(t *testing.T) {
	r := newAdminTestRouter(t, "super-secret-admin-key")

	w := adminDo(r, "super-secret-admin-key")
	if w.Code != http.StatusOK {
		t.Fatalf("valid key: got %d body=%s, want 200", w.Code, w.Body.String())
	}
}

func TestAdminAuth_NotConfiguredFailsClosed(t *testing.T) {
	// Empty ADMIN_API_KEY ⇒ every admin route must 503 (never open access).
	r := newAdminTestRouter(t, "")

	w := adminDo(r, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured admin key: got %d, want 503", w.Code)
	}
	if got := adminErrCode(t, w); got != "ADMIN_NOT_CONFIGURED" {
		t.Errorf("error.code: got %q, want ADMIN_NOT_CONFIGURED", got)
	}
	// Even a matching (empty) key must never open an unconfigured admin API —
	// sending no header at all must behave the same.
	if w2 := adminDo(r, ""); w2.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured admin key second probe: got %d, want 503", w2.Code)
	}
}

// TestAdminAuth_UsesConstantTimeComparison guards the DoD item "existing
// constant-time comparison remains intact" — timing behaviour is not
// observable from a black-box test, so assert on the source directly. If this
// fails, AdminAuth was rewritten with a non-constant-time comparison.
func TestAdminAuth_UsesConstantTimeComparison(t *testing.T) {
	src, err := os.ReadFile("admin_auth.go")
	if err != nil {
		t.Fatalf("read admin_auth.go: %v", err)
	}
	if !strings.Contains(string(src), "subtle.ConstantTimeCompare") {
		t.Error("admin_auth.go must keep using crypto/subtle.ConstantTimeCompare")
	}
}
