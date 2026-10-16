package handler_test

// Handler contract tests for the public forgot-password endpoints: generic
// responses, status-code mapping, and no token/credential leakage.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
)

type stubPasswordResetService struct {
	requestErr error
	confirmErr error
}

func (s *stubPasswordResetService) RequestPasswordReset(_ context.Context, _ string) error {
	return s.requestErr
}

func (s *stubPasswordResetService) ConfirmPasswordReset(_ context.Context, _, _ string) error {
	return s.confirmErr
}

func newPasswordResetRouter(svc service.PasswordResetService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewPasswordResetHandler(svc)
	auth := r.Group("/api/v1/auth")
	auth.POST("/password-reset/request", h.RequestReset)
	auth.POST("/password-reset/confirm", h.ConfirmReset)
	return r
}

func doJSON(t *testing.T, r *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPasswordResetRequest_GenericResponse(t *testing.T) {
	r := newPasswordResetRouter(&stubPasswordResetService{})
	w := doJSON(t, r, "/api/v1/auth/password-reset/request", map[string]string{"email": "owner@example.com"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "If an account exists") {
		t.Errorf("response must be generic, got %q", w.Body.String())
	}
}

func TestPasswordResetRequest_MissingEmailIsValidationError(t *testing.T) {
	r := newPasswordResetRouter(&stubPasswordResetService{})
	w := doJSON(t, r, "/api/v1/auth/password-reset/request", map[string]string{})
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 4xx (body %q)", w.Code, w.Body.String())
	}
}

func TestPasswordResetConfirm_Mapping(t *testing.T) {
	cases := []struct {
		name   string
		svcErr error
		status int
		code   string
	}{
		{"invalid", service.ErrPasswordResetInvalid, http.StatusBadRequest, "RESET_TOKEN_INVALID"},
		{"expired", service.ErrPasswordResetExpired, http.StatusGone, "RESET_TOKEN_EXPIRED"},
		{"consumed", service.ErrPasswordResetConsumed, http.StatusConflict, "RESET_TOKEN_CONSUMED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPasswordResetRouter(&stubPasswordResetService{confirmErr: tc.svcErr})
			w := doJSON(t, r, "/api/v1/auth/password-reset/confirm",
				map[string]string{"token": "opaque-token-value", "password": "new-strong-pass"})
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tc.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.code) {
				t.Errorf("body must carry %s, got %q", tc.code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "opaque-token-value") {
				t.Errorf("token must never be echoed, got %q", w.Body.String())
			}
		})
	}
}

func TestPasswordResetConfirm_Success(t *testing.T) {
	r := newPasswordResetRouter(&stubPasswordResetService{})
	w := doJSON(t, r, "/api/v1/auth/password-reset/confirm",
		map[string]string{"token": "opaque-token-value", "password": "new-strong-pass"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Sign in") {
		t.Errorf("success must point back to login, got %q", w.Body.String())
	}
}
