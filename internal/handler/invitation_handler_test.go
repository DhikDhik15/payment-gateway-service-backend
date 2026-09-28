package handler_test

// invitation_handler_test.go — Phase 8B HTTP-layer tests for self-service
// team invitations: creation authorization (OWNER-only), merchant lifecycle
// gating, duplicate/email conflicts, the one-time token disclosure, and the
// unauthenticated get/accept token endpoints.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// ─── Mock InvitationService ───────────────────────────────────────────────────

type mockInvitationService struct {
	createResp *model.CreateInvitationResponse
	createErr  error
	getResp    *model.InvitationMetadataResponse
	getErr     error
	acceptResp *model.DashboardUserResponse
	acceptErr  error

	// Call recording for authorization / isolation assertions.
	createCalled       bool
	lastCallerRole     model.DashboardUserRole
	lastCallerMerchant uuid.UUID
	lastCreateReq      model.CreateInvitationRequest
	getCalled          bool
	lastGetToken       string
	acceptCalled       bool
	lastAcceptToken    string
	lastAcceptReq      model.AcceptInvitationRequest
}

func (m *mockInvitationService) CreateInvitation(
	_ context.Context,
	callerRole model.DashboardUserRole,
	callerMerchantID uuid.UUID,
	req model.CreateInvitationRequest,
) (*model.CreateInvitationResponse, error) {
	m.createCalled = true
	m.lastCallerRole = callerRole
	m.lastCallerMerchant = callerMerchantID
	m.lastCreateReq = req
	return m.createResp, m.createErr
}

func (m *mockInvitationService) GetInvitationByToken(
	_ context.Context,
	token string,
) (*model.InvitationMetadataResponse, error) {
	m.getCalled = true
	m.lastGetToken = token
	return m.getResp, m.getErr
}

func (m *mockInvitationService) AcceptInvitation(
	_ context.Context,
	token string,
	req model.AcceptInvitationRequest,
) (*model.DashboardUserResponse, error) {
	m.acceptCalled = true
	m.lastAcceptToken = token
	m.lastAcceptReq = req
	return m.acceptResp, m.acceptErr
}

// ─── Router factory ───────────────────────────────────────────────────────────

func newInvitationRouter(
	authSvc service.AuthService,
	userRepo repository.MerchantUserRepository,
	merchantRepo repository.MerchantRepository,
	invSvc service.InvitationService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())

	ih := handler.NewInvitationHandler(invSvc)

	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, userRepo, merchantRepo))
	{
		dash.POST("/users/invite",
			middleware.RequireRole(model.DashboardUserRoleOwner),
			ih.Create,
		)
	}

	inv := r.Group("/api/v1/invitations")
	{
		inv.GET("/:token", ih.GetByToken)
		inv.POST("/:token/accept", ih.Accept)
	}
	return r
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// invitationData unwraps a success envelope's `data` payload into dst.
func invitationData(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v — body: %s", err, w.Body.String())
	}
	if !env.Success {
		t.Fatalf("expected success envelope, got: %s", w.Body.String())
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("unmarshal data: %v — body: %s", err, w.Body.String())
	}
}

func newCreateResp(merchantID uuid.UUID) *model.CreateInvitationResponse {
	now := time.Now().UTC()
	return &model.CreateInvitationResponse{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Email:      "invitee@example.com",
		Role:       model.DashboardUserRoleViewer,
		Status:     model.InvitationStatusPending,
		ExpiresAt:  now.Add(48 * time.Hour),
		CreatedAt:  now,
		Token:      strings.Repeat("a", 64),
	}
}

// ─── Create: authorization ───────────────────────────────────────────────────

func TestInvitationHandler_Create_OwnerAllowed(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{createResp: newCreateResp(caller.MerchantID)}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusCreated {
		t.Errorf("status: got %d, want 201 — body: %s", authResponseCode(w), w.Body.String())
	}

	// The plaintext token is disclosed exactly once, in the create response.
	var resp model.CreateInvitationResponse
	invitationData(t, w, &resp)
	if resp.Token == "" {
		t.Error("create response must include the one-time token")
	}
	if resp.Status != model.InvitationStatusPending {
		t.Errorf("status: got %v, want PENDING", resp.Status)
	}

	// Authorization context: caller identity comes from the authenticated
	// session, and no merchant_id exists in the request body at all.
	if !invSvc.createCalled {
		t.Fatal("service must be called")
	}
	if invSvc.lastCallerRole != model.DashboardUserRoleOwner {
		t.Errorf("caller role: got %v, want OWNER", invSvc.lastCallerRole)
	}
	if invSvc.lastCallerMerchant != caller.MerchantID {
		t.Error("service must receive the caller's merchant from context, not the request")
	}
	if invSvc.lastCreateReq.Email != "invitee@example.com" {
		t.Errorf("email: got %q", invSvc.lastCreateReq.Email)
	}
	if invSvc.lastCreateReq.Role != model.DashboardUserRoleViewer {
		t.Errorf("role: got %v, want VIEWER", invSvc.lastCreateReq.Role)
	}
}

func TestInvitationHandler_Create_AdminForbidden(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleAdmin)
	invSvc := &mockInvitationService{}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for ADMIN — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInsufficientRole) {
		t.Errorf("error code: got %q, want INSUFFICIENT_ROLE", code)
	}
	if invSvc.createCalled {
		t.Error("service must not be reached for ADMIN")
	}
}

func TestInvitationHandler_Create_ViewerForbidden(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleViewer)
	invSvc := &mockInvitationService{}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for VIEWER — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInsufficientRole) {
		t.Errorf("error code: got %q, want INSUFFICIENT_ROLE", code)
	}
	if invSvc.createCalled {
		t.Error("service must not be reached for VIEWER")
	}
}

func TestInvitationHandler_Create_Unauthenticated(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		nil, // no Authorization header
	)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 — body: %s", authResponseCode(w), w.Body.String())
	}
	if invSvc.createCalled {
		t.Error("service must not be reached without authentication")
	}
}

// ─── Create: merchant lifecycle ──────────────────────────────────────────────

func TestInvitationHandler_Create_SuspendedMerchantRejectedByMiddleware(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{}

	suspended := &mockMerchantRepoForMiddleware{
		resp: &model.Merchant{
			ID:     caller.MerchantID,
			Status: model.MerchantStatusSuspended,
		},
	}
	r := newInvitationRouter(teamAuth(caller), userRepo, suspended, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeMerchantInactive) {
		t.Errorf("error code: got %q, want MERCHANT_INACTIVE", code)
	}
	if invSvc.createCalled {
		t.Error("service must not be reached for a SUSPENDED merchant")
	}
}

func TestInvitationHandler_Create_ServiceLevelMerchantInactive(t *testing.T) {
	// Middleware passes (merchant ACTIVE at auth time) but the service-level
	// defense-in-depth check rejects — mapped to 403 MERCHANT_INACTIVE.
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{createErr: service.ErrMerchantNotActive}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeMerchantInactive) {
		t.Errorf("error code: got %q, want MERCHANT_INACTIVE", code)
	}
}

// ─── Create: conflicts + validation ──────────────────────────────────────────

func TestInvitationHandler_Create_DuplicatePendingConflict(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{createErr: service.ErrInvitationAlreadyPending}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvitationAlreadyPending) {
		t.Errorf("error code: got %q, want INVITATION_ALREADY_PENDING", code)
	}
}

func TestInvitationHandler_Create_EmailAlreadyExistsConflict(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{createErr: service.ErrEmailAlreadyExists}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "taken@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeEmailAlreadyExists) {
		t.Errorf("error code: got %q, want EMAIL_ALREADY_EXISTS", code)
	}
}

func TestInvitationHandler_Create_InvalidRoleBindingRejected(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: "SUPERADMIN",
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeValidationError) {
		t.Errorf("error code: got %q, want VALIDATION_ERROR", code)
	}
	if invSvc.createCalled {
		t.Error("service must not be reached for a role binding violation")
	}
}

func TestInvitationHandler_Create_MissingRoleBindingRejected(t *testing.T) {
	// The role is always explicit — an omitted role must NOT be defaulted.
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{Email: "invitee@example.com"}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for a missing role — body: %s", authResponseCode(w), w.Body.String())
	}
	if invSvc.createCalled {
		t.Error("service must not be reached when the role is omitted")
	}
}

func TestInvitationHandler_Create_InvalidEmailFromService(t *testing.T) {
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{createErr: service.ErrInvalidEmail}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "not-an-email", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
}

func TestInvitationHandler_Create_InsufficientRoleFromService(t *testing.T) {
	// Route RequireRole(OWNER) plus the service-level check — defense in depth.
	caller, userRepo := teamCaller(model.DashboardUserRoleOwner)
	invSvc := &mockInvitationService{createErr: service.ErrInsufficientRole}

	r := newInvitationRouter(teamAuth(caller), userRepo,
		newActiveMerchantRepo(caller.MerchantID), invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/dashboard/users/invite",
		authJsonBody(t, model.CreateInvitationRequest{
			Email: "invitee@example.com", Role: model.DashboardUserRoleViewer,
		}),
		map[string]string{"Authorization": "Bearer valid.jwt.token"},
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInsufficientRole) {
		t.Errorf("error code: got %q, want INSUFFICIENT_ROLE", code)
	}
}

// ─── Get metadata (unauthenticated) ──────────────────────────────────────────

func TestInvitationHandler_GetByToken_Valid(t *testing.T) {
	invSvc := &mockInvitationService{
		getResp: &model.InvitationMetadataResponse{
			Email:        "invitee@example.com",
			Role:         model.DashboardUserRoleAdmin,
			MerchantName: "Test Merchant",
			Status:       model.InvitationStatusPending,
			ExpiresAt:    time.Now().UTC().Add(48 * time.Hour),
		},
	}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	token := strings.Repeat("b", 64)
	w := authDoRequest(r, http.MethodGet, "/api/v1/invitations/"+token, nil, nil)

	if authResponseCode(w) != http.StatusOK {
		t.Errorf("status: got %d, want 200 — body: %s", authResponseCode(w), w.Body.String())
	}
	var meta model.InvitationMetadataResponse
	invitationData(t, w, &meta)
	if meta.Email != "invitee@example.com" {
		t.Errorf("email: got %q", meta.Email)
	}
	if meta.MerchantName != "Test Merchant" {
		t.Errorf("merchant name: got %q", meta.MerchantName)
	}
	if !invSvc.getCalled || invSvc.lastGetToken != token {
		t.Error("service must receive the token from the URL path")
	}
	// The metadata payload must not contain a token hash field.
	if strings.Contains(w.Body.String(), "token_hash") {
		t.Error("metadata must never expose token_hash")
	}
}

func TestInvitationHandler_GetByToken_NotFound(t *testing.T) {
	invSvc := &mockInvitationService{getErr: service.ErrInvitationNotFound}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodGet,
		"/api/v1/invitations/"+strings.Repeat("f", 64), nil, nil)

	if authResponseCode(w) != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvitationNotFound) {
		t.Errorf("error code: got %q, want INVITATION_NOT_FOUND", code)
	}
}

// ─── Accept (unauthenticated) ────────────────────────────────────────────────

func TestInvitationHandler_Accept_Success(t *testing.T) {
	merchantID := uuid.New()
	invSvc := &mockInvitationService{
		acceptResp: &model.DashboardUserResponse{
			ID:         uuid.New(),
			MerchantID: merchantID,
			Email:      "invitee@example.com",
			Role:       model.DashboardUserRoleAdmin,
			Status:     model.DashboardUserStatusActive,
		},
	}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	token := strings.Repeat("c", 64)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+token+"/accept",
		authJsonBody(t, model.AcceptInvitationRequest{Password: "acceptedpassword1"}),
		nil,
	)

	if authResponseCode(w) != http.StatusCreated {
		t.Errorf("status: got %d, want 201 — body: %s", authResponseCode(w), w.Body.String())
	}
	var user model.DashboardUserResponse
	invitationData(t, w, &user)
	if user.Email != "invitee@example.com" {
		t.Errorf("email: got %q", user.Email)
	}
	if user.Status != model.DashboardUserStatusActive {
		t.Errorf("status: got %v, want ACTIVE", user.Status)
	}
	if !invSvc.acceptCalled || invSvc.lastAcceptToken != token {
		t.Error("service must receive the token from the URL path")
	}
	if invSvc.lastAcceptReq.Password != "acceptedpassword1" {
		t.Error("service must receive the submitted password")
	}
	// The response envelope must never contain a password field.
	if strings.Contains(w.Body.String(), "password") {
		t.Error("accept response must never echo the password")
	}
}

func TestInvitationHandler_Accept_ReplayConflict(t *testing.T) {
	invSvc := &mockInvitationService{acceptErr: service.ErrInvitationAlreadyAccepted}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+strings.Repeat("d", 64)+"/accept",
		authJsonBody(t, model.AcceptInvitationRequest{Password: "acceptedpassword1"}),
		nil,
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvitationAlreadyAccepted) {
		t.Errorf("error code: got %q, want INVITATION_ALREADY_ACCEPTED", code)
	}
}

func TestInvitationHandler_Accept_NotFound(t *testing.T) {
	invSvc := &mockInvitationService{acceptErr: service.ErrInvitationNotFound}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+strings.Repeat("e", 64)+"/accept",
		authJsonBody(t, model.AcceptInvitationRequest{Password: "acceptedpassword1"}),
		nil,
	)

	if authResponseCode(w) != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvitationNotFound) {
		t.Errorf("error code: got %q, want INVITATION_NOT_FOUND", code)
	}
}

func TestInvitationHandler_Accept_MerchantSuspendedRejected(t *testing.T) {
	// The merchant was ACTIVE at invite time but suspended before acceptance.
	invSvc := &mockInvitationService{acceptErr: service.ErrMerchantNotActive}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+strings.Repeat("1", 64)+"/accept",
		authJsonBody(t, model.AcceptInvitationRequest{Password: "acceptedpassword1"}),
		nil,
	)

	if authResponseCode(w) != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeMerchantInactive) {
		t.Errorf("error code: got %q, want MERCHANT_INACTIVE", code)
	}
}

func TestInvitationHandler_Accept_EmailAlreadyExistsConflict(t *testing.T) {
	invSvc := &mockInvitationService{acceptErr: service.ErrEmailAlreadyExists}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+strings.Repeat("2", 64)+"/accept",
		authJsonBody(t, model.AcceptInvitationRequest{Password: "acceptedpassword1"}),
		nil,
	)

	if authResponseCode(w) != http.StatusConflict {
		t.Errorf("status: got %d, want 409 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeEmailAlreadyExists) {
		t.Errorf("error code: got %q, want EMAIL_ALREADY_EXISTS", code)
	}
}

func TestInvitationHandler_Accept_WeakPasswordBindingRejected(t *testing.T) {
	invSvc := &mockInvitationService{}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+strings.Repeat("3", 64)+"/accept",
		authJsonBody(t, model.AcceptInvitationRequest{Password: "short"}),
		nil,
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeValidationError) {
		t.Errorf("error code: got %q, want VALIDATION_ERROR", code)
	}
	if invSvc.acceptCalled {
		t.Error("service must not be reached for a password policy violation")
	}
}

func TestInvitationHandler_Accept_InvalidPasswordFromService(t *testing.T) {
	invSvc := &mockInvitationService{acceptErr: service.ErrInvalidPassword}

	r := newInvitationRouter(nil, nil, nil, invSvc)
	w := authDoRequest(r, http.MethodPost,
		"/api/v1/invitations/"+strings.Repeat("4", 64)+"/accept",
		// Binding-valid length — the service-level policy check is defense in
		// depth behind the binding, so mock its rejection here.
		authJsonBody(t, model.AcceptInvitationRequest{Password: "validlengthpw"}),
		nil,
	)

	if authResponseCode(w) != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 — body: %s", authResponseCode(w), w.Body.String())
	}
	if code := authResponseErrorCode(t, w); code != string(response.CodeInvalidPassword) {
		t.Errorf("error code: got %q, want INVALID_PASSWORD", code)
	}
}
