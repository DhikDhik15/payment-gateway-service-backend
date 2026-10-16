package handler_test

// Handler contract tests for development-only account recovery: gating,
// authorization, validation mapping, and no secret leakage.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const devRecoveryTestKey = "0123456789abcdef0123456789abcdef"

type stubDevRecoveryService struct {
	enabled    bool
	recoverErr error
	respEmail  string
	gotKey     string
}

func (s *stubDevRecoveryService) Enabled() bool { return s.enabled }

func (s *stubDevRecoveryService) Recover(_ context.Context, providedKey, email, _, _ string) (*model.DashboardUserResponse, error) {
	s.gotKey = providedKey
	if s.recoverErr != nil {
		return nil, s.recoverErr
	}
	return &model.DashboardUserResponse{
		ID:         uuid.New(),
		MerchantID: uuid.New(),
		Email:      s.respEmail,
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}, nil
}

func newDevRecoveryRouter(svc service.DevRecoveryService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDevRecoveryHandler(svc)
	auth := r.Group("/api/v1/auth")
	auth.GET("/dev-recovery/status", h.Status)
	auth.POST("/dev-recovery/recover", h.Recover)
	return r
}

func doDevRecoveryJSON(t *testing.T, r *gin.Engine, path string, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(handler.DevRecoveryKeyHeader, key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDevRecoveryStatus_Disabled(t *testing.T) {
	r := newDevRecoveryRouter(&stubDevRecoveryService{enabled: false})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/dev-recovery/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("status must report enabled=false, got %q", w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "key") && strings.Contains(w.Body.String(), devRecoveryTestKey) {
		t.Errorf("status must never carry secrets, got %q", w.Body.String())
	}
}

func TestDevRecoveryStatus_Enabled(t *testing.T) {
	r := newDevRecoveryRouter(&stubDevRecoveryService{enabled: true})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/dev-recovery/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Errorf("status must report enabled=true, got %q", w.Body.String())
	}
}

func TestDevRecovery_RecoverDisabledIs404(t *testing.T) {
	r := newDevRecoveryRouter(&stubDevRecoveryService{enabled: false})
	w := doDevRecoveryJSON(t, r, "/api/v1/auth/dev-recovery/recover", devRecoveryTestKey,
		map[string]string{"email": "owner@example.com", "new_password": "new-strong-pass-1", "confirm_password": "new-strong-pass-1"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled recover status = %d, want 404 (body %q)", w.Code, w.Body.String())
	}
}

func TestDevRecovery_RecoverMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name   string
		svcErr error
		status int
		code   string
	}{
		{"disabled", service.ErrDevRecoveryDisabled, http.StatusNotFound, "RESOURCE_NOT_FOUND"},
		{"unauthorized", service.ErrDevRecoveryUnauthorized, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"notfound", service.ErrDevRecoveryNotFound, http.StatusNotFound, "DASHBOARD_USER_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newDevRecoveryRouter(&stubDevRecoveryService{enabled: true, recoverErr: tc.svcErr})
			key := devRecoveryTestKey
			if tc.name == "unauthorized" {
				key = "wrong-key"
			}
			w := doDevRecoveryJSON(t, r, "/api/v1/auth/dev-recovery/recover", key,
				map[string]string{"email": "owner@example.com", "new_password": "new-strong-pass-1", "confirm_password": "new-strong-pass-1"})
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tc.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.code) {
				t.Errorf("body must carry %s, got %q", tc.code, w.Body.String())
			}
		})
	}
}

func TestDevRecovery_RecoverValidation(t *testing.T) {
	r := newDevRecoveryRouter(&stubDevRecoveryService{enabled: true})
	// Missing confirmation → binding validation error.
	w := doDevRecoveryJSON(t, r, "/api/v1/auth/dev-recovery/recover", devRecoveryTestKey,
		map[string]string{"email": "owner@example.com", "new_password": "new-strong-pass-1"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("validation status = %d, want 400 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Errorf("body must carry VALIDATION_ERROR, got %q", w.Body.String())
	}
}

func TestDevRecovery_RecoverSuccessCarriesNoSecrets(t *testing.T) {
	stub := &stubDevRecoveryService{enabled: true, respEmail: "owner@example.com"}
	r := newDevRecoveryRouter(stub)
	w := doDevRecoveryJSON(t, r, "/api/v1/auth/dev-recovery/recover", devRecoveryTestKey,
		map[string]string{"email": "owner@example.com", "new_password": "new-strong-pass-1", "confirm_password": "new-strong-pass-1"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Sign in") {
		t.Errorf("success must point back to login, got %q", w.Body.String())
	}
	body := strings.ToLower(w.Body.String())
	for _, secret := range []string{"password_hash", "passwordhash", "refresh", "token_hash", devRecoveryTestKey, "new-strong-pass-1"} {
		if strings.Contains(body, strings.ToLower(secret)) {
			t.Errorf("response must never contain %q, got %q", secret, w.Body.String())
		}
	}
	if stub.gotKey != devRecoveryTestKey {
		t.Errorf("service must receive the header key, got %q", stub.gotKey)
	}
}

func TestDevRecovery_KeyComesFromHeaderNotBody(t *testing.T) {
	stub := &stubDevRecoveryService{enabled: true, respEmail: "owner@example.com"}
	r := newDevRecoveryRouter(stub)
	// Key smuggled in the JSON body (not header) must not authenticate: the
	// stub records whatever the handler passed from the header (empty here).
	w := doDevRecoveryJSON(t, r, "/api/v1/auth/dev-recovery/recover", "",
		map[string]any{"email": "owner@example.com", "new_password": "new-strong-pass-1", "confirm_password": "new-strong-pass-1", "key": devRecoveryTestKey})
	if w.Code != http.StatusOK {
		t.Fatalf("stub always succeeds; status = %d (body %q)", w.Code, w.Body.String())
	}
	if stub.gotKey != "" {
		t.Errorf("handler must pass only the header value, got %q", stub.gotKey)
	}
}
