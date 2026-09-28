package handler_test

// dashboard_user_team_handler_test.go — Phase 8A HTTP-layer tests for
// self-service team management: role change, status hardening, password change,
// role authorization middleware, merchant lifecycle gating, and tenant
// isolation at the route level.

import (
	"net/http"
	"strings"
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

// ─── Router factory with explicit merchant repo (for lifecycle tests) ────────

func newPhase8ARouter(
	authSvc service.AuthService,
	userRepo repository.MerchantUserRepository,
	merchantRepo repository.MerchantRepository,
	userSvc service.DashboardUserService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())

	h := handler.NewDashboardUserHandler(userSvc)
	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, userRepo, merchantRepo))
	{
		dash.GET("/users", h.ListUsers)
		dash.PATCH("/users/:user_id/status",
			middleware.RequireRole(model.DashboardUserRoleOwner),
			h.UpdateUserStatus,
		)
		dash.PATCH("/users/:user_id/role",
			middleware.RequireRole(model.DashboardUserRoleOwner),
			h.UpdateUserRole,
		)
		dash.PATCH("/me/password", h.ChangePassword)
	}
	return r
}

// teamAuth builds an AuthService stub whose claims point at user.
func teamAuth(user *model.MerchantUser) *mockAuthService {
	return &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    user.ID.String(),
			MerchantID: user.MerchantID.String(),
			Role:       string(user.Role),
			ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		},
	}
}

// teamCaller builds an ACTIVE dashboard user + its middleware repo.
func teamCaller(role model.DashboardUserRole) (*model.MerchantUser, *mockUserRepoForMiddleware) {
	merchantID := uuid.New()
	user := &model.MerchantUser{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Email:      string(role) + "@merchant.test",
		Role:       role,
		Status:     model.DashboardUserStatusActive,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	return user, &mockUserRepoForMiddleware{user: user}
}

// ─── Role change: authorization (spec §18 #10–#13) ──────────────────────────

func TestDashboardUserHandler_UpdateRole_OwnerAllowed(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	targetID := uuid.New()
	userSvc := &mockDashboardUserService{
		updateRoleResp: &model.DashboardUserResponse{
			ID: targetID, MerchantID: caller.MerchantID,
			Role: model.DashboardUserRoleViewer, Status: model.DashboardUserStatusActive,
		},
	}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleViewer}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
	// Tenant isolation: merchant identity came from the authenticated caller.
	if userSvc.lastRoleCallerMerchant != caller.MerchantID {
		t.Error("service must receive the caller's merchant from context, not the request")
	}
	if userSvc.lastRoleTarget != targetID {
		t.Error("service must receive the path target ID")
	}
}

func TestDashboardUserHandler_UpdateRole_AdminForbidden(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleAdmin)
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleViewer}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for ADMIN — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInsufficientRole) {
		t.Errorf("error code: got %q, want INSUFFICIENT_ROLE", code)
	}
	if userSvc.lastRoleTarget != uuid.Nil {
		t.Error("service must not be reached for ADMIN role change")
	}
}

func TestDashboardUserHandler_UpdateRole_ViewerForbidden(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleViewer)
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleAdmin}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for VIEWER — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInsufficientRole) {
		t.Errorf("error code: got %q, want INSUFFICIENT_ROLE", code)
	}
}

// ─── Role change: last-OWNER + validation + tenant (spec §18 #5, #14) ───────

func TestDashboardUserHandler_UpdateRole_LastOwnerConflict(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{updateRoleErr: service.ErrLastOwnerRequired}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleAdmin}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeLastOwnerRequired) {
		t.Errorf("error code: got %q, want LAST_OWNER_REQUIRED", code)
	}
}

func TestDashboardUserHandler_UpdateRole_CrossMerchantReturns404(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{updateRoleErr: service.ErrCrossmerchantAccess}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleAdmin}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 for cross-tenant target — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeDashboardUserNotFound) {
		t.Errorf("error code: got %q, want DASHBOARD_USER_NOT_FOUND (existence must not be confirmed)", code)
	}
}

func TestDashboardUserHandler_UpdateRole_InvalidRoleValue400(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
		authJsonBody(t, map[string]string{"role": "SUPERADMIN"}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for invalid role — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeValidationError) {
		t.Errorf("error code: got %q, want VALIDATION_ERROR", code)
	}
	if userSvc.lastRoleTarget != uuid.Nil {
		t.Error("service must not be reached for an invalid role")
	}
}

func TestDashboardUserHandler_UpdateRole_BodyMerchantIDIgnored(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	targetID := uuid.New()
	foreignMerchantID := uuid.New()
	userSvc := &mockDashboardUserService{
		updateRoleResp: &model.DashboardUserResponse{ID: targetID, Role: model.DashboardUserRoleViewer},
	}

	// A client-supplied merchant_id must NEVER override the authenticated
	// merchant context — binding ignores unknown fields and the handler only
	// passes caller.MerchantID to the service.
	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/role",
		authJsonBody(t, map[string]any{
			"role":        "VIEWER",
			"merchant_id": foreignMerchantID.String(),
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
	if userSvc.lastRoleCallerMerchant == foreignMerchantID {
		t.Fatal("merchant_id from the request body must be ignored")
	}
	if userSvc.lastRoleCallerMerchant != caller.MerchantID {
		t.Error("service must receive the authenticated caller's merchant")
	}
}

func TestDashboardUserHandler_UpdateRole_Unauthenticated401(t *testing.T) {
	authSvc := &mockAuthService{verifyErr: service.ErrJWTInvalid}
	userRepo := &mockUserRepoForMiddleware{}
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(authSvc, userRepo, newActiveMerchantRepo(uuid.New()), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleAdmin}),
		nil,
	)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", authResponseCode(w))
	}
}

// ─── Status change: last-OWNER conflict (spec §18 #6) ───────────────────────

func TestDashboardUserHandler_UpdateStatus_LastOwnerConflict(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{updateStatusErr: service.ErrLastOwnerRequired}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+uuid.New().String()+"/status",
		authJsonBody(t, model.UpdateUserStatusRequest{Status: model.DashboardUserStatusDisabled}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeLastOwnerRequired) {
		t.Errorf("error code: got %q, want LAST_OWNER_REQUIRED", code)
	}
}

// ─── Password change (spec §20) ─────────────────────────────────────────────

func TestDashboardUserHandler_ChangePassword_Success(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{
		changePassResp: &model.DashboardUserResponse{
			ID: caller.ID, MerchantID: caller.MerchantID, Email: caller.Email,
			Role: caller.Role, Status: model.DashboardUserStatusActive,
		},
	}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/me/password",
		authJsonBody(t, model.ChangePasswordRequest{
			CurrentPassword: "current-password-1",
			NewPassword:     "brand-new-password",
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
	// Password material must never appear in any response body.
	body := strings.ToLower(w.Body.String())
	if strings.Contains(body, "password") || strings.Contains(body, "argon2") {
		t.Errorf("response must not contain password material: %s", w.Body.String())
	}
}

func TestDashboardUserHandler_ChangePassword_WrongCurrentPassword400(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{changePassErr: service.ErrInvalidCurrentPassword}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/me/password",
		authJsonBody(t, model.ChangePasswordRequest{
			CurrentPassword: "wrong-password",
			NewPassword:     "brand-new-password",
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvalidCurrentPassword) {
		t.Errorf("error code: got %q, want INVALID_CURRENT_PASSWORD", code)
	}
}

func TestDashboardUserHandler_ChangePassword_EmptyNewPassword400(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/me/password",
		authJsonBody(t, map[string]string{
			"current_password": "current-password-1",
			"new_password":     "",
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("empty new password: status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
	if userSvc.lastPassCaller != uuid.Nil {
		t.Error("service must not be reached for an empty new password")
	}
}

func TestDashboardUserHandler_ChangePassword_ShortNewPassword400(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/me/password",
		authJsonBody(t, map[string]string{
			"current_password": "current-password-1",
			"new_password":     "1234567", // 7 chars — below policy minimum
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("short new password: status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
	if userSvc.lastPassCaller != uuid.Nil {
		t.Error("service must not be reached for a policy-violating new password")
	}
}

func TestDashboardUserHandler_ChangePassword_IdentityFromSessionOnly(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	otherUserID := uuid.New()
	userSvc := &mockDashboardUserService{
		changePassResp: &model.DashboardUserResponse{ID: caller.ID},
	}

	// A user_id smuggled into the body must be ignored — the handler always
	// passes the authenticated caller's ID, so a user can never change
	// another user's password.
	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/me/password",
		authJsonBody(t, map[string]any{
			"current_password": "current-password-1",
			"new_password":     "brand-new-password",
			"user_id":          otherUserID.String(),
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
	if userSvc.lastPassCaller != caller.ID {
		t.Errorf("service must receive the session caller ID %s, got %s", caller.ID, userSvc.lastPassCaller)
	}
	if userSvc.lastPassCaller == otherUserID {
		t.Fatal("user_id from the request body must never be used")
	}
}

func TestDashboardUserHandler_ChangePassword_DisabledUserBlockedByMiddleware(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	caller.Status = model.DashboardUserStatusDisabled // still carries a "valid" JWT
	userSvc := &mockDashboardUserService{}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)
	w := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/me/password",
		authJsonBody(t, model.ChangePasswordRequest{
			CurrentPassword: "current-password-1",
			NewPassword:     "brand-new-password",
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("disabled user: status: got %d, want 401 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeUserDisabled) {
		t.Errorf("error code: got %q, want USER_DISABLED", code)
	}
	if userSvc.lastPassCaller != uuid.Nil {
		t.Error("service must not be reached for a disabled user")
	}
}

// ─── Role freshness: no stale authorization after a role change (spec §19 #4)

func TestDashboardUserHandler_RoleChangeTakesEffectImmediately(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	targetID := uuid.New()
	userSvc := &mockDashboardUserService{
		updateRoleResp: &model.DashboardUserResponse{ID: targetID, Role: model.DashboardUserRoleViewer},
	}

	r := newPhase8ARouter(teamAuth(caller), userRepo, newActiveMerchantRepo(caller.MerchantID), userSvc)

	// Request 1: OWNER → role change allowed.
	w1 := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleViewer}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)
	if authResponseCode(w1) != http.StatusOK {
		t.Fatalf("request 1: got %d, want 200 — body: %s", authResponseCode(w1), w1.Body.String())
	}

	// Simulate the database role change landing while the caller still holds
	// the same JWT (its `role` claim still says OWNER). The middleware reloads
	// the user from the DB on every request, so the NEW role applies at once.
	caller.Role = model.DashboardUserRoleAdmin

	w2 := authDoRequest(r, http.MethodPatch,
		"/api/v1/dashboard/users/"+targetID.String()+"/role",
		authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleViewer}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)
	if authResponseCode(w2) != http.StatusForbidden {
		t.Errorf("request 2 after demotion: got %d, want 403 (fresh DB role must apply) — body: %s",
			authResponseCode(w2), w2.Body.String())
	}
	if code := authResponseErrorCode(t, w2); code != string(response.CodeInsufficientRole) {
		t.Errorf("error code: got %q, want INSUFFICIENT_ROLE", code)
	}
}

// ─── Merchant lifecycle gating (spec §15, §19 #6) ───────────────────────────

func TestDashboardUserHandler_MerchantNotActiveBlocksTeamAccess(t *testing.T) {
	for _, status := range []model.MerchantStatus{
		model.MerchantStatusSuspended,
		model.MerchantStatusInactive,
	} {
		t.Run(string(status), func(t *testing.T) {
			caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
			userSvc := &mockDashboardUserService{
				updateRoleResp: &model.DashboardUserResponse{Role: model.DashboardUserRoleAdmin},
			}
			merchantRepo := &mockMerchantRepoForMiddleware{
				resp: &model.Merchant{ID: caller.MerchantID, Status: status},
			}

			r := newPhase8ARouter(teamAuth(caller), userRepo, merchantRepo, userSvc)

			// Role change is blocked.
			w := authDoRequest(r, http.MethodPatch,
				"/api/v1/dashboard/users/"+uuid.New().String()+"/role",
				authJsonBody(t, model.UpdateUserRoleRequest{Role: model.DashboardUserRoleAdmin}),
				map[string]string{"Authorization": "Bearer valid.jwt.token"},
			)
			if authResponseCode(w) != http.StatusUnauthorized {
				t.Errorf("role change on %s merchant: got %d, want 401 — body: %s", status, authResponseCode(w), w.Body.String())
			}
			if code := authResponseErrorCode(t, w); code != string(response.CodeMerchantInactive) {
				t.Errorf("error code: got %q, want MERCHANT_INACTIVE", code)
			}

			// Password change is blocked too.
			w = authDoRequest(r, http.MethodPatch,
				"/api/v1/dashboard/me/password",
				authJsonBody(t, model.ChangePasswordRequest{
					CurrentPassword: "current-password-1",
					NewPassword:     "brand-new-password",
				}),
				map[string]string{"Authorization": "Bearer valid.jwt.token"},
			)
			if authResponseCode(w) != http.StatusUnauthorized {
				t.Errorf("password change on %s merchant: got %d, want 401", status, authResponseCode(w))
			}

			// Team listing is blocked as well.
			w = authDoRequest(r, http.MethodGet, "/api/v1/dashboard/users", nil,
				map[string]string{"Authorization": "Bearer valid.jwt.token"})
			if authResponseCode(w) != http.StatusUnauthorized {
				t.Errorf("team listing on %s merchant: got %d, want 401", status, authResponseCode(w))
			}

			if userSvc.lastRoleTarget != uuid.Nil || userSvc.lastPassCaller != uuid.Nil {
				t.Error("services must not be reached while the merchant is not ACTIVE")
			}
		})
	}
}
