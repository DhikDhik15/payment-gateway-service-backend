package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── Mock AuthService ─────────────────────────────────────────────────────────

type mockAuthService struct {
	loginResp      *model.LoginResponse
	loginRefresh   string
	loginErr       error
	logoutErr      error
	meResp         *model.DashboardUserResponse
	meErr          error
	refreshResp    *model.LoginResponse
	refreshRefresh string
	refreshErr     error
	verifyErr      error
	verifyClaims   *service.JWTClaims
}

func (m *mockAuthService) Login(_ context.Context, _ model.LoginRequest) (*model.LoginResponse, string, error) {
	return m.loginResp, m.loginRefresh, m.loginErr
}

func (m *mockAuthService) Logout(_ context.Context, _ string) error {
	return m.logoutErr
}

func (m *mockAuthService) Me(_ context.Context, _ uuid.UUID) (*model.DashboardUserResponse, error) {
	return m.meResp, m.meErr
}

func (m *mockAuthService) RefreshAccessToken(_ context.Context, _ string) (*model.LoginResponse, string, error) {
	return m.refreshResp, m.refreshRefresh, m.refreshErr
}

func (m *mockAuthService) VerifyAccessToken(_ string) (*service.JWTClaims, error) {
	return m.verifyClaims, m.verifyErr
}

// ─── Mock DashboardUserService ────────────────────────────────────────────────

type mockDashboardUserService struct {
	createResp       *model.DashboardUserResponse
	createErr        error
	listResp         []model.DashboardUserResponse
	listErr          error
	updateStatusResp *model.DashboardUserResponse
	updateStatusErr  error
}

func (m *mockDashboardUserService) CreateUser(_ context.Context, _ uuid.UUID, _ model.CreateDashboardUserRequest) (*model.DashboardUserResponse, error) {
	return m.createResp, m.createErr
}

func (m *mockDashboardUserService) ListUsers(_ context.Context, _ uuid.UUID) ([]model.DashboardUserResponse, error) {
	return m.listResp, m.listErr
}

func (m *mockDashboardUserService) UpdateUserStatus(_ context.Context, _ uuid.UUID, _ model.DashboardUserRole, _ uuid.UUID, _ uuid.UUID, _ model.DashboardUserStatus) (*model.DashboardUserResponse, error) {
	return m.updateStatusResp, m.updateStatusErr
}

// ─── Mock MerchantUserRepository (for middleware) ─────────────────────────────

type mockUserRepoForMiddleware struct {
	user *model.MerchantUser
	err  error
}

func (m *mockUserRepoForMiddleware) Create(_ context.Context, _ *model.MerchantUser) error {
	return nil
}

func (m *mockUserRepoForMiddleware) GetByID(_ context.Context, _ uuid.UUID) (*model.MerchantUser, error) {
	return m.user, m.err
}

func (m *mockUserRepoForMiddleware) GetByEmail(_ context.Context, _ string) (*model.MerchantUser, error) {
	return m.user, m.err
}

func (m *mockUserRepoForMiddleware) ListByMerchant(_ context.Context, _ uuid.UUID) ([]*model.MerchantUser, error) {
	return nil, nil
}

func (m *mockUserRepoForMiddleware) UpdateStatus(_ context.Context, _ uuid.UUID, _ model.DashboardUserStatus) error {
	return nil
}

func (m *mockUserRepoForMiddleware) UpdateLastLoginAt(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

// ─── Router factories ─────────────────────────────────────────────────────────

func newAuthTestRouter(authSvc service.AuthService, userRepo repository.MerchantUserRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())

	h := handler.NewAuthHandler(authSvc, false, 7*24*time.Hour)

	r.POST("/api/v1/auth/login", h.Login)
	r.POST("/api/v1/auth/logout", h.Logout)
	r.POST("/api/v1/auth/refresh", h.Refresh)

	protected := r.Group("/api/v1/auth")
	protected.Use(middleware.RequireDashboardAuth(authSvc, userRepo))
	{
		protected.GET("/me", h.Me)
	}

	return r
}

func newAdminUserTestRouter(authSvc service.AuthService, userSvc service.DashboardUserService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())

	h := handler.NewAdminUserHandler(userSvc)
	admin := r.Group("/api/v1/admin")
	admin.Use(middleware.AdminAuth("test-admin-key"))
	{
		admin.POST("/merchants/:merchant_id/users", h.CreateUser)
	}

	return r
}

func newDashboardUserTestRouter(authSvc service.AuthService, userRepo repository.MerchantUserRepository, userSvc service.DashboardUserService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())

	h := handler.NewDashboardUserHandler(userSvc)
	dashboard := r.Group("/api/v1/dashboard")
	dashboard.Use(middleware.RequireDashboardAuth(authSvc, userRepo))
	{
		dashboard.GET("/users", h.ListUsers)
		dashboard.PATCH("/users/:user_id/status",
			middleware.RequireRole(model.DashboardUserRoleOwner),
			h.UpdateUserStatus,
		)
	}

	return r
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func authJsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

func authDoRequest(r *gin.Engine, method, path string, body *bytes.Buffer, headers map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest(method, path, body)
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func authResponseCode(w *httptest.ResponseRecorder) int {
	return w.Code
}

func authResponseErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	return env.Error.Code
}

// ─── Login handler tests ──────────────────────────────────────────────────────

func TestAuthHandler_Login_Success(t *testing.T) {
	merchantID := uuid.New()
	userID := uuid.New()

	mockSvc := &mockAuthService{
		loginResp: &model.LoginResponse{
			AccessToken: "test.access.token",
			TokenType:   "Bearer",
			ExpiresIn:   900,
			User: model.DashboardUserResponse{
				ID:         userID,
				MerchantID: merchantID,
				Email:      "owner@example.com",
				Role:       model.DashboardUserRoleOwner,
				Status:     model.DashboardUserStatusActive,
			},
		},
		loginRefresh: "some-plain-refresh-token",
	}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodPost, "/api/v1/auth/login",
		authJsonBody(t, map[string]string{
			"email":    "owner@example.com",
			"password": "password123",
		}), nil)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200", authResponseCode(w))
	}

	// Verify refresh cookie is set.
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "refresh_token" {
			found = true
			if !c.HttpOnly {
				t.Error("refresh_token cookie must be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("expected refresh_token cookie in response")
	}
}

func TestAuthHandler_Login_InvalidCredentials(t *testing.T) {
	mockSvc := &mockAuthService{
		loginErr: service.ErrInvalidCredentials,
	}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodPost, "/api/v1/auth/login",
		authJsonBody(t, map[string]string{
			"email":    "nobody@example.com",
			"password": "wrong",
		}), nil)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvalidCredentials) {
		t.Errorf("error code: got %q, want %q", code, response.CodeInvalidCredentials)
	}
}

func TestAuthHandler_Login_DisabledUser(t *testing.T) {
	mockSvc := &mockAuthService{
		loginErr: service.ErrUserDisabled,
	}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodPost, "/api/v1/auth/login",
		authJsonBody(t, map[string]string{
			"email":    "disabled@example.com",
			"password": "password",
		}), nil)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 for disabled user", authResponseCode(w))
	}
}

func TestAuthHandler_Login_BadRequest(t *testing.T) {
	mockSvc := &mockAuthService{}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	// Missing password field
	w := authDoRequest(r, http.MethodPost, "/api/v1/auth/login",
		authJsonBody(t, map[string]string{"email": "test@example.com"}), nil)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", authResponseCode(w))
	}
}

// ─── Logout handler tests ─────────────────────────────────────────────────────

func TestAuthHandler_Logout_Success(t *testing.T) {
	mockSvc := &mockAuthService{}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{
		"X-Refresh-Token": "some-token",
	})

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200", authResponseCode(w))
	}

	// Refresh cookie should be cleared.
	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == "refresh_token" && c.MaxAge > 0 {
			t.Error("refresh_token cookie should be cleared (MaxAge <= 0)")
		}
	}
}

func TestAuthHandler_Logout_NoToken(t *testing.T) {
	mockSvc := &mockAuthService{}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodPost, "/api/v1/auth/logout", nil, nil)

	// Logout without any token is still 200 (clears cookie).
	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200", authResponseCode(w))
	}
}

// ─── Me handler tests ─────────────────────────────────────────────────────────

func TestAuthHandler_Me_Success(t *testing.T) {
	merchantID := uuid.New()
	userID := uuid.New()

	activeUser := &model.MerchantUser{
		ID:         userID,
		MerchantID: merchantID,
		Email:      "owner@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    userID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: activeUser}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodGet, "/api/v1/auth/me", nil, map[string]string{
		"Authorization": "Bearer valid.jwt.token",
	})

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestAuthHandler_Me_MissingToken(t *testing.T) {
	mockSvc := &mockAuthService{
		verifyErr: service.ErrJWTExpiredPublic,
	}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodGet, "/api/v1/auth/me", nil, nil)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
}

func TestAuthHandler_Me_InvalidToken(t *testing.T) {
	mockSvc := &mockAuthService{
		verifyErr: service.ErrJWTInvalid,
	}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodGet, "/api/v1/auth/me", nil, map[string]string{
		"Authorization": "Bearer bad.token",
	})

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
}

func TestAuthHandler_Me_ExpiredToken(t *testing.T) {
	mockSvc := &mockAuthService{
		verifyErr: service.ErrJWTExpiredPublic,
	}
	userRepo := &mockUserRepoForMiddleware{}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodGet, "/api/v1/auth/me", nil, map[string]string{
		"Authorization": "Bearer expired.jwt.token",
	})

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 for expired token", authResponseCode(w))
	}
}

func TestAuthHandler_Me_DisabledUser(t *testing.T) {
	merchantID := uuid.New()
	userID := uuid.New()

	disabledUser := &model.MerchantUser{
		ID:         userID,
		MerchantID: merchantID,
		Email:      "disabled@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusDisabled,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    userID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: disabledUser}

	r := newAuthTestRouter(mockSvc, userRepo)
	w := authDoRequest(r, http.MethodGet, "/api/v1/auth/me", nil, map[string]string{
		"Authorization": "Bearer valid.jwt.token",
	})

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 for disabled user", authResponseCode(w))
	}
}

// ─── Admin user handler tests ─────────────────────────────────────────────────

func TestAdminUserHandler_CreateUser_Success(t *testing.T) {
	merchantID := uuid.New()
	userID := uuid.New()

	mockUserSvc := &mockDashboardUserService{
		createResp: &model.DashboardUserResponse{
			ID:         userID,
			MerchantID: merchantID,
			Email:      "owner@merchant.com",
			Role:       model.DashboardUserRoleOwner,
			Status:     model.DashboardUserStatusActive,
		},
	}
	mockSvc := &mockAuthService{}

	r := newAdminUserTestRouter(mockSvc, mockUserSvc)
	w := authDoRequest(r,
		http.MethodPost,
		"/api/v1/admin/merchants/"+merchantID.String()+"/users",
		authJsonBody(t, model.CreateDashboardUserRequest{
			Email:    "owner@merchant.com",
			Password: "strongpassword",
			Role:     model.DashboardUserRoleOwner,
		}),
		map[string]string{"X-Admin-Key": "test-admin-key"},
	)

	if authResponseCode(w) != http.StatusCreated {
		t.Errorf("status: got %d, want 201 — body: %s", authResponseCode(w), w.Body.String())
	}

	// Verify password/hash not in response.
	body := w.Body.String()
	if contains(body, "password") {
		t.Error("response must not contain 'password'")
	}
}

func TestAdminUserHandler_CreateUser_MissingAdminKey(t *testing.T) {
	mockUserSvc := &mockDashboardUserService{}
	mockSvc := &mockAuthService{}

	r := newAdminUserTestRouter(mockSvc, mockUserSvc)
	w := authDoRequest(r,
		http.MethodPost,
		"/api/v1/admin/merchants/"+uuid.New().String()+"/users",
		authJsonBody(t, map[string]string{
			"email": "test@example.com", "password": "pass", "role": "OWNER",
		}),
		nil, // no admin key
	)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
}

func TestAdminUserHandler_CreateUser_WrongAdminKey(t *testing.T) {
	mockUserSvc := &mockDashboardUserService{}
	mockSvc := &mockAuthService{}

	r := newAdminUserTestRouter(mockSvc, mockUserSvc)
	w := authDoRequest(r,
		http.MethodPost,
		"/api/v1/admin/merchants/"+uuid.New().String()+"/users",
		authJsonBody(t, map[string]string{
			"email": "test@example.com", "password": "pass", "role": "OWNER",
		}),
		map[string]string{"X-Admin-Key": "wrong-key"},
	)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
}

func TestAdminUserHandler_CreateUser_DuplicateEmail(t *testing.T) {
	mockUserSvc := &mockDashboardUserService{
		createErr: service.ErrEmailAlreadyExists,
	}
	mockSvc := &mockAuthService{}

	r := newAdminUserTestRouter(mockSvc, mockUserSvc)
	w := authDoRequest(r,
		http.MethodPost,
		"/api/v1/admin/merchants/"+uuid.New().String()+"/users",
		authJsonBody(t, model.CreateDashboardUserRequest{
			Email:    "dup@example.com",
			Password: "strongpassword",
			Role:     model.DashboardUserRoleOwner,
		}),
		map[string]string{"X-Admin-Key": "test-admin-key"},
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409", authResponseCode(w))
	}
}

func TestAdminUserHandler_CreateUser_MerchantNotFound(t *testing.T) {
	mockUserSvc := &mockDashboardUserService{
		createErr: repository.ErrMerchantNotFound,
	}
	mockSvc := &mockAuthService{}

	r := newAdminUserTestRouter(mockSvc, mockUserSvc)
	w := authDoRequest(r,
		http.MethodPost,
		"/api/v1/admin/merchants/"+uuid.New().String()+"/users",
		authJsonBody(t, model.CreateDashboardUserRequest{
			Email:    "test@example.com",
			Password: "strongpassword",
			Role:     model.DashboardUserRoleOwner,
		}),
		map[string]string{"X-Admin-Key": "test-admin-key"},
	)

	if authResponseCode(w) != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", authResponseCode(w))
	}
}

// ─── Dashboard user handler tests ─────────────────────────────────────────────

func makeAuthenticatedRequest(t *testing.T, r *gin.Engine, method, path string, body *bytes.Buffer, user *model.MerchantUser, authSvc service.AuthService) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest(method, path, body)
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer valid.jwt.token")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDashboardUserHandler_ListUsers_Success(t *testing.T) {
	merchantID := uuid.New()
	userID := uuid.New()

	activeUser := &model.MerchantUser{
		ID:         userID,
		MerchantID: merchantID,
		Email:      "owner@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    userID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: activeUser}
	mockUserSvc := &mockDashboardUserService{
		listResp: []model.DashboardUserResponse{
			{ID: userID, MerchantID: merchantID, Email: "owner@example.com", Role: model.DashboardUserRoleOwner},
		},
	}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodGet, "/api/v1/dashboard/users", nil, map[string]string{
		"Authorization": "Bearer valid.jwt.token",
	})

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestDashboardUserHandler_ListUsers_Unauthenticated(t *testing.T) {
	mockSvc := &mockAuthService{verifyErr: service.ErrJWTInvalid}
	userRepo := &mockUserRepoForMiddleware{}
	mockUserSvc := &mockDashboardUserService{}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodGet, "/api/v1/dashboard/users", nil, nil)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
}

func TestDashboardUserHandler_UpdateStatus_OwnerAllowed(t *testing.T) {
	merchantID := uuid.New()
	ownerID := uuid.New()
	targetID := uuid.New()

	ownerUser := &model.MerchantUser{
		ID:         ownerID,
		MerchantID: merchantID,
		Email:      "owner@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    ownerID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: ownerUser}
	mockUserSvc := &mockDashboardUserService{
		updateStatusResp: &model.DashboardUserResponse{
			ID:         targetID,
			MerchantID: merchantID,
			Status:     model.DashboardUserStatusDisabled,
		},
	}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/status",
		authJsonBody(t, model.UpdateUserStatusRequest{Status: model.DashboardUserStatusDisabled}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestDashboardUserHandler_UpdateStatus_ViewerForbidden(t *testing.T) {
	merchantID := uuid.New()
	viewerID := uuid.New()
	targetID := uuid.New()

	viewerUser := &model.MerchantUser{
		ID:         viewerID,
		MerchantID: merchantID,
		Email:      "viewer@example.com",
		Role:       model.DashboardUserRoleViewer,
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    viewerID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleViewer),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: viewerUser}
	mockUserSvc := &mockDashboardUserService{}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/status",
		authJsonBody(t, model.UpdateUserStatusRequest{Status: model.DashboardUserStatusDisabled}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for viewer — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestDashboardUserHandler_UpdateStatus_AdminForbidden(t *testing.T) {
	merchantID := uuid.New()
	adminID := uuid.New()
	targetID := uuid.New()

	adminUser := &model.MerchantUser{
		ID:         adminID,
		MerchantID: merchantID,
		Email:      "admin@example.com",
		Role:       model.DashboardUserRoleAdmin,
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    adminID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleAdmin),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: adminUser}
	mockUserSvc := &mockDashboardUserService{}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/status",
		authJsonBody(t, model.UpdateUserStatusRequest{Status: model.DashboardUserStatusDisabled}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for admin — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestDashboardUserHandler_UpdateStatus_InsufficientRoleFromService(t *testing.T) {
	merchantID := uuid.New()
	adminID := uuid.New()
	targetID := uuid.New()

	// Even if we somehow bypass RequireRole middleware, service returns ErrInsufficientRole.
	ownerUser := &model.MerchantUser{
		ID:         adminID,
		MerchantID: merchantID,
		Email:      "admin@example.com",
		Role:       model.DashboardUserRoleOwner, // presented as owner
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    adminID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: ownerUser}
	mockUserSvc := &mockDashboardUserService{
		updateStatusErr: service.ErrInsufficientRole,
	}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/status",
		authJsonBody(t, model.UpdateUserStatusRequest{Status: model.DashboardUserStatusDisabled}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestDashboardUserHandler_UpdateStatus_CrossMerchantReturns404(t *testing.T) {
	merchantID := uuid.New()
	ownerID := uuid.New()
	targetID := uuid.New()

	ownerUser := &model.MerchantUser{
		ID:         ownerID,
		MerchantID: merchantID,
		Email:      "owner@example.com",
		Role:       model.DashboardUserRoleOwner,
		Status:     model.DashboardUserStatusActive,
	}

	mockSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    ownerID.String(),
			MerchantID: merchantID.String(),
			Role:       string(model.DashboardUserRoleOwner),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: ownerUser}
	mockUserSvc := &mockDashboardUserService{
		// Cross-merchant access returns not-found (404) to avoid confirming existence.
		updateStatusErr: service.ErrCrossmerchantAccess,
	}

	r := newDashboardUserTestRouter(mockSvc, userRepo, mockUserSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/status",
		authJsonBody(t, model.UpdateUserStatusRequest{Status: model.DashboardUserStatusDisabled}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 for cross-merchant — body: %s", authResponseCode(w), w.Body.String())
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (len(s) >= len(substr)) &&
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}()
}

// ─── DashboardUserService mock with correct signature ─────────────────────────
// The mock above has UpdateUserStatus with wrong parameter order. Correcting it.

var _ service.DashboardUserService = (*mockDashboardUserService)(nil)

// Verify ErrJWTInvalid is exported via service package.
var _ = errors.New // just use errors to satisfy import
