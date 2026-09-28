package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── stubs for the dashboard-auth matrix ──────────────────────────────────────

// stubDashboardAuthService controls VerifyAccessToken; unused methods panic.
type stubDashboardAuthService struct {
	claims *service.JWTClaims
	verify error
}

func (s *stubDashboardAuthService) Login(_ context.Context, _ model.LoginRequest) (*model.LoginResponse, string, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardAuthService) Logout(_ context.Context, _ string) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardAuthService) LogoutSession(_ context.Context, _ uuid.UUID, _ uuid.UUID) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardAuthService) Me(_ context.Context, _ uuid.UUID) (*model.DashboardUserResponse, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardAuthService) RefreshAccessToken(_ context.Context, _ string) (*model.LoginResponse, string, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardAuthService) VerifyAccessToken(_ string) (*service.JWTClaims, error) {
	return s.claims, s.verify
}

var _ service.AuthService = (*stubDashboardAuthService)(nil)

// stubDashboardUserRepo serves GetByID; the rest of the interface panics.
type stubDashboardUserRepo struct {
	user *model.MerchantUser
	err  error
}

func (s *stubDashboardUserRepo) Create(_ context.Context, _ *model.MerchantUser) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) CreateInTx(_ context.Context, _ pgx.Tx, _ *model.MerchantUser) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*model.MerchantUser, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}
func (s *stubDashboardUserRepo) GetByEmail(_ context.Context, _ string) (*model.MerchantUser, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) ListByMerchant(_ context.Context, _ uuid.UUID) ([]*model.MerchantUser, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ model.DashboardUserStatus) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) UpdateRole(_ context.Context, _ uuid.UUID, _ model.DashboardUserRole) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) CountActiveOwners(_ context.Context, _ uuid.UUID) (int, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) UpdatePasswordHash(_ context.Context, _ uuid.UUID, _ string) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardUserRepo) UpdateLastLoginAt(_ context.Context, _ uuid.UUID, _ time.Time) error {
	panic("not used in dashboard auth tests")
}

var _ repository.MerchantUserRepository = (*stubDashboardUserRepo)(nil)

// stubDashboardMerchantRepo serves GetByID; the rest of the interface panics.
type stubDashboardMerchantRepo struct {
	merchant *model.Merchant
	err      error
}

func (s *stubDashboardMerchantRepo) Create(_ context.Context, _ *model.Merchant) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardMerchantRepo) CreateInTx(_ context.Context, _ pgx.Tx, _ *model.Merchant) error {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardMerchantRepo) GetByID(_ context.Context, _ uuid.UUID) (*model.Merchant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.merchant, nil
}
func (s *stubDashboardMerchantRepo) GetByAPIKey(_ context.Context, _ string) (*model.Merchant, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardMerchantRepo) ExistsByCode(_ context.Context, _ string) (bool, error) {
	panic("not used in dashboard auth tests")
}
func (s *stubDashboardMerchantRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ model.MerchantStatus) error {
	panic("not used in dashboard auth tests")
}

var _ repository.MerchantRepository = (*stubDashboardMerchantRepo)(nil)

// ─── router factory ───────────────────────────────────────────────────────────

type dashboardAuthFixture struct {
	authSvc      *stubDashboardAuthService
	userRepo     *stubDashboardUserRepo
	merchantRepo *stubDashboardMerchantRepo
	user         *model.MerchantUser
	merchant     *model.Merchant
	router       *gin.Engine
}

// newDashboardAuthFixture wires a protected /me route with active defaults.
func newDashboardAuthFixture(t *testing.T) *dashboardAuthFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	merchantID := uuid.New()
	merchant := &model.Merchant{
		ID:     merchantID,
		Name:   "Test Merchant",
		Code:   "TEST-MERCHANT",
		Status: model.MerchantStatusActive,
	}
	user := &model.MerchantUser{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Email:      "owner@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}

	f := &dashboardAuthFixture{
		authSvc:      &stubDashboardAuthService{claims: &service.JWTClaims{Subject: user.ID.String(), MerchantID: merchantID.String()}},
		userRepo:     &stubDashboardUserRepo{user: user},
		merchantRepo: &stubDashboardMerchantRepo{merchant: merchant},
		user:         user,
		merchant:     merchant,
	}

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.RequireDashboardAuth(f.authSvc, f.userRepo, f.merchantRepo))
	r.GET("/me", func(c *gin.Context) {
		u := middleware.DashboardUserFromContext(c)
		if u == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no user in context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user_id": u.ID.String()})
	})
	f.router = r
	return f
}

func dashboardDo(r *gin.Engine, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Phase 8D.1 auth regression matrix: dashboard JWT ─────────────────────────

func TestDashboardAuth_MissingToken(t *testing.T) {
	f := newDashboardAuthFixture(t)
	w := dashboardDo(f.router, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d, want 401", w.Code)
	}
}

func TestDashboardAuth_InvalidToken(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.authSvc.verify = errors.New("signature is invalid")

	w := dashboardDo(f.router, "Bearer not-a-real-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token: got %d, want 401", w.Code)
	}
}

func TestDashboardAuth_ExpiredToken(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.authSvc.verify = service.ErrJWTExpiredPublic

	w := dashboardDo(f.router, "Bearer expired-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: got %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Access token has expired") {
		t.Errorf("expected expiry-specific message, got body %s", w.Body.String())
	}
}

func TestDashboardAuth_DisabledUser(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.user.Status = model.DashboardUserStatusDisabled

	w := dashboardDo(f.router, "Bearer valid-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user: got %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "USER_DISABLED") {
		t.Errorf("expected USER_DISABLED code, got body %s", w.Body.String())
	}
}

func TestDashboardAuth_DeletedUser(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.userRepo.err = repository.ErrMerchantUserNotFound

	w := dashboardDo(f.router, "Bearer valid-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user: got %d, want 401", w.Code)
	}
}

func TestDashboardAuth_InactiveMerchant(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.merchant.Status = model.MerchantStatusInactive

	w := dashboardDo(f.router, "Bearer valid-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("inactive merchant: got %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "MERCHANT_INACTIVE") {
		t.Errorf("expected MERCHANT_INACTIVE code, got body %s", w.Body.String())
	}
}

func TestDashboardAuth_SuspendedMerchant(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.merchant.Status = model.MerchantStatusSuspended

	w := dashboardDo(f.router, "Bearer valid-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("suspended merchant: got %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "MERCHANT_INACTIVE") {
		t.Errorf("expected MERCHANT_INACTIVE code, got body %s", w.Body.String())
	}
}

func TestDashboardAuth_DeletedMerchant(t *testing.T) {
	f := newDashboardAuthFixture(t)
	f.merchantRepo.err = repository.ErrMerchantNotFound

	w := dashboardDo(f.router, "Bearer valid-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted merchant: got %d, want 401", w.Code)
	}
}

func TestDashboardAuth_ValidTokenActiveMerchant(t *testing.T) {
	f := newDashboardAuthFixture(t)

	w := dashboardDo(f.router, "Bearer valid-jwt")
	if w.Code != http.StatusOK {
		t.Fatalf("valid token: got %d body=%s, want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), f.user.ID.String()) {
		t.Errorf("expected user id in body, got %s", w.Body.String())
	}
}
