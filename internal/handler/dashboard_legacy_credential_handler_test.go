package handler_test

// dashboard_legacy_credential_handler_test.go — Phase 8D.3 tests for the two
// dashboard legacy-credential endpoints and the complete HTTP lifecycle:
//
//	legacy key works → migrate (one-time secret) → legacy key STILL works
//	→ disable → legacy key rejected forever.
//
// Covered: authorization (OWNER/ADMIN allowed, VIEWER 403, anonymous 401),
// merchant lifecycle (non-ACTIVE blocked before the handler), tenant isolation
// (merchant always from the JWT, never from the body), the four stable 409/404
// mappings, idempotent disable, single-winner concurrency, and credential
// leakage in responses.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// ─── stub LegacyCredentialService ────────────────────────────────────────────

type stubLegacyCredentialService struct {
	mu           sync.Mutex
	migrateCalls []uuid.UUID
	disableCalls []uuid.UUID

	migrateResp *model.MigrateLegacyCredentialResponse
	migrateErr  error
	disableResp *model.LegacyCredentialStatusResponse
	disableErr  error
}

func (s *stubLegacyCredentialService) Migrate(_ context.Context, merchantID uuid.UUID) (*model.MigrateLegacyCredentialResponse, error) {
	s.mu.Lock()
	s.migrateCalls = append(s.migrateCalls, merchantID)
	s.mu.Unlock()
	if s.migrateErr != nil {
		return nil, s.migrateErr
	}
	return s.migrateResp, nil
}

func (s *stubLegacyCredentialService) Disable(_ context.Context, merchantID uuid.UUID) (*model.LegacyCredentialStatusResponse, error) {
	s.mu.Lock()
	s.disableCalls = append(s.disableCalls, merchantID)
	s.mu.Unlock()
	if s.disableErr != nil {
		return nil, s.disableErr
	}
	return s.disableResp, nil
}

func (s *stubLegacyCredentialService) migrated() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.migrateCalls...)
}

func (s *stubLegacyCredentialService) disabled() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.disableCalls...)
}

var _ service.LegacyCredentialService = (*stubLegacyCredentialService)(nil)

// ─── router factory (mirrors cmd/server/main.go wiring) ──────────────────────

type legacyCredRouter struct {
	router     *gin.Engine
	svc        service.LegacyCredentialService
	merchantID uuid.UUID
	userID     uuid.UUID
	token      string
}

func newLegacyCredRouter(role model.DashboardUserRole, merchantStatus model.MerchantStatus, svc service.LegacyCredentialService) *legacyCredRouter {
	gin.SetMode(gin.TestMode)

	merchantID := uuid.New()
	userID := uuid.New()
	user := &model.MerchantUser{
		ID:         userID,
		MerchantID: merchantID,
		Email:      "caller@test.com",
		Role:       role,
		Status:     model.DashboardUserStatusActive,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}

	authSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    userID.String(),
			MerchantID: merchantID.String(),
			Role:       string(role),
		},
	}
	userRepo := &mockUserRepoForMiddleware{user: user}
	merchantRepo := &mockMerchantRepoForMiddleware{
		resp: &model.Merchant{ID: merchantID, Status: merchantStatus},
	}

	h := handler.NewDashboardLegacyCredentialHandler(svc)

	r := gin.New()
	r.Use(middleware.RequestID())
	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, userRepo, merchantRepo))
	{
		legacy := dash.Group("/legacy-credential")
		legacy.POST("/migrate",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			h.MigrateLegacyCredential,
		)
		legacy.POST("/disable",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			h.DisableLegacyCredential,
		)
	}

	return &legacyCredRouter{
		router: r, svc: svc,
		merchantID: merchantID, userID: userID, token: "test-token",
	}
}

func (d *legacyCredRouter) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, path, nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)
	return w
}

func defaultMigrateResp(merchantID uuid.UUID) *model.MigrateLegacyCredentialResponse {
	now := time.Now().UTC()
	return &model.MigrateLegacyCredentialResponse{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		Name:                  "Migrated API Key",
		KeyID:                 "pk_" + strings.Repeat("a", 40),
		Secret:                "sk_" + strings.Repeat("b", 64),
		Status:                model.MerchantAPIKeyStatusActive,
		CreatedAt:             now,
		LegacyCredentialState: model.LegacyCredentialStateMigrated,
		LegacyDisabled:        false,
	}
}

const legacyCredMigratePath = "/api/v1/dashboard/legacy-credential/migrate"
const legacyCredDisablePath = "/api/v1/dashboard/legacy-credential/disable"

// ─── migrate: authorization ──────────────────────────────────────────────────

func TestLegacyCredential_Migrate_Owner_201(t *testing.T) {
	svc := &stubLegacyCredentialService{}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)
	svc.migrateResp = defaultMigrateResp(d.merchantID)

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)
	if body["success"] != true {
		t.Error("expected success:true")
	}
	if got := getStr(t, body, "data", "merchant_id"); got != d.merchantID.String() {
		t.Errorf("merchant_id = %s, want caller's %s", got, d.merchantID)
	}
	if got := getStr(t, body, "data", "legacy_credential_state"); got != string(model.LegacyCredentialStateMigrated) {
		t.Errorf("legacy_credential_state = %q, want MIGRATED", got)
	}
	if data, _ := body["data"].(map[string]any); data["legacy_disabled"] != false {
		t.Errorf("legacy_disabled = %v, want false (legacy key still works)", data["legacy_disabled"])
	}
	// The one-time secret must be present exactly in this response.
	if getStr(t, body, "data", "secret") == "" {
		t.Error("one-time secret missing from the migrate response")
	}

	ids := svc.migrated()
	if len(ids) != 1 || ids[0] != d.merchantID {
		t.Fatalf("service must be called once with the caller's merchant, got %v", ids)
	}
}

func TestLegacyCredential_Migrate_Admin_201(t *testing.T) {
	svc := &stubLegacyCredentialService{}
	d := newLegacyCredRouter(model.DashboardUserRoleAdmin, model.MerchantStatusActive, svc)
	svc.migrateResp = defaultMigrateResp(d.merchantID)

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("ADMIN: expected 201, got %d\nbody: %s", w.Code, w.Body)
	}
}

// TestLegacyCredential_Migrate_Viewer_403 is the authorization test: a
// read-only role must never be able to mint credentials.
func TestLegacyCredential_Migrate_Viewer_403(t *testing.T) {
	svc := &stubLegacyCredentialService{
		migrateResp: defaultMigrateResp(uuid.Nil), // would be returned if authorized
	}
	d := newLegacyCredRouter(model.DashboardUserRoleViewer, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("VIEWER: expected 403, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeInsufficientRole) {
		t.Errorf("expected INSUFFICIENT_ROLE, got %s", code)
	}
	if len(svc.migrated()) != 0 {
		t.Error("service must not be invoked for an unauthorized role")
	}
	// Nothing may leak in the 403 body.
	assertNoCredentialMaterial(t, w.Body.String())
}

func TestLegacyCredential_Migrate_NoBearer_401(t *testing.T) {
	svc := &stubLegacyCredentialService{migrateResp: defaultMigrateResp(uuid.Nil)}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	req := httptest.NewRequest(http.MethodPost, legacyCredMigratePath, nil)
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if len(svc.migrated()) != 0 {
		t.Error("service must not be invoked without a token")
	}
}

// ─── migrate: merchant lifecycle ─────────────────────────────────────────────

// TestLegacyCredential_Migrate_SuspendedMerchant_401 verifies the Phase 7
// lifecycle gate still runs ahead of the handler: a non-ACTIVE tenant cannot
// migrate (or disable) its credential.
func TestLegacyCredential_Migrate_SuspendedMerchant_401(t *testing.T) {
	for _, status := range []model.MerchantStatus{model.MerchantStatusInactive, model.MerchantStatusSuspended} {
		t.Run(string(status), func(t *testing.T) {
			svc := &stubLegacyCredentialService{migrateResp: defaultMigrateResp(uuid.Nil)}
			d := newLegacyCredRouter(model.DashboardUserRoleOwner, status, svc)

			w := d.post(t, legacyCredMigratePath, "")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status %s: expected 401, got %d\nbody: %s", status, w.Code, w.Body)
			}
			if len(svc.migrated()) != 0 {
				t.Errorf("status %s: service must not be invoked for a non-ACTIVE merchant", status)
			}
			assertNoCredentialMaterial(t, w.Body.String())
		})
	}
}

// ─── migrate: stable error mappings ─────────────────────────────────────────

func TestLegacyCredential_Migrate_AlreadyMigrated_409(t *testing.T) {
	svc := &stubLegacyCredentialService{migrateErr: service.ErrLegacyCredentialAlreadyMigrated}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeLegacyCredentialAlreadyMigrated) {
		t.Errorf("expected LEGACY_CREDENTIAL_ALREADY_MIGRATED, got %s", code)
	}
	assertNoCredentialMaterial(t, w.Body.String())
}

func TestLegacyCredential_Migrate_NotFound_404(t *testing.T) {
	svc := &stubLegacyCredentialService{migrateErr: repository.ErrMerchantNotFound}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeMerchantNotFound) {
		t.Errorf("expected MERCHANT_NOT_FOUND, got %s", code)
	}
}

func TestLegacyCredential_Migrate_InternalError_500_NoLeak(t *testing.T) {
	// A non-sentinel error must surface as a generic 500 that names nothing.
	svc := &stubLegacyCredentialService{
		migrateErr: errors.New("argon2id salt fetch failed for sk_supersecretvalue"),
	}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeInternalError) {
		t.Errorf("expected INTERNAL_ERROR, got %s", code)
	}
	assertNoCredentialMaterial(t, w.Body.String())
	if strings.Contains(w.Body.String(), "salt") {
		t.Errorf("internal error detail leaked: %s", w.Body)
	}
}

// ─── migrate: tenant isolation ───────────────────────────────────────────────

// TestLegacyCredential_Migrate_IgnoresBodyMerchantID proves the merchant is
// always taken from the JWT: a caller cannot target another tenant by putting
// its id in the request body.
func TestLegacyCredential_Migrate_IgnoresBodyMerchantID(t *testing.T) {
	svc := &stubLegacyCredentialService{}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)
	svc.migrateResp = defaultMigrateResp(d.merchantID)

	attackerChosen := uuid.New().String()
	w := d.post(t, legacyCredMigratePath, `{"merchant_id":"`+attackerChosen+`","id":"`+attackerChosen+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	ids := svc.migrated()
	if len(ids) != 1 {
		t.Fatalf("expected 1 call, got %d", len(ids))
	}
	if ids[0] == uuid.MustParse(attackerChosen) {
		t.Fatal("tenant isolation violation: body merchant_id was honoured")
	}
	if ids[0] != d.merchantID {
		t.Fatalf("service must receive the caller's merchant %s, got %s", d.merchantID, ids[0])
	}
	if got := getStr(t, parseBody(t, w), "data", "merchant_id"); got != d.merchantID.String() {
		t.Errorf("response merchant_id = %s, want %s", got, d.merchantID)
	}
}

// ─── migrate: leakage ────────────────────────────────────────────────────────

// TestLegacyCredential_Migrate_NoLegacyMaterialInResponse asserts the response
// discloses ONLY the new Phase 5C secret — never the legacy credential that is
// being migrated away from, and never any hash.
func TestLegacyCredential_Migrate_NoLegacyMaterialInResponse(t *testing.T) {
	svc := &stubLegacyCredentialService{}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	resp := defaultMigrateResp(d.merchantID)
	resp.KeyID = "pk_" + strings.Repeat("c", 40)
	resp.Secret = "sk_" + strings.Repeat("d", 64)
	svc.migrateResp = resp

	w := d.post(t, legacyCredMigratePath, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	raw := w.Body.String()

	// The one-time secret and its public key id ARE present.
	if !strings.Contains(raw, resp.Secret) {
		t.Error("the one-time secret must be returned")
	}
	// Fields that must never appear anywhere.
	for _, forbidden := range []string{"api_secret", "secret_hash", "argon2", "password", "api_key"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("response must not contain %q: %s", forbidden, raw)
		}
	}
	// The secret must appear exactly once (no accidental duplication).
	if strings.Count(raw, resp.Secret) != 1 {
		t.Errorf("secret must appear exactly once, got %d", strings.Count(raw, resp.Secret))
	}
}

// ─── disable ─────────────────────────────────────────────────────────────────

func TestLegacyCredential_Disable_Owner_200(t *testing.T) {
	disabledAt := time.Now().UTC()
	svc := &stubLegacyCredentialService{
		disableResp: &model.LegacyCredentialStatusResponse{
			LegacyCredentialState:      model.LegacyCredentialStateLegacyDisabled,
			LegacyCredentialDisabledAt: &disabledAt,
			AlreadyDisabled:            false,
		},
	}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredDisablePath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	if got := getStr(t, body, "data", "legacy_credential_state"); got != string(model.LegacyCredentialStateLegacyDisabled) {
		t.Errorf("state = %q, want LEGACY_DISABLED", got)
	}
	if data, _ := body["data"].(map[string]any); data["already_disabled"] != false {
		t.Errorf("already_disabled = %v, want false on first call", data["already_disabled"])
	}
	ids := svc.disabled()
	if len(ids) != 1 || ids[0] != d.merchantID {
		t.Fatalf("service must be called once with the caller's merchant, got %v", ids)
	}
}

func TestLegacyCredential_Disable_IdempotentPassesThrough(t *testing.T) {
	disabledAt := time.Now().UTC().Add(-time.Hour)
	svc := &stubLegacyCredentialService{
		disableResp: &model.LegacyCredentialStatusResponse{
			LegacyCredentialState:      model.LegacyCredentialStateLegacyDisabled,
			LegacyCredentialDisabledAt: &disabledAt,
			AlreadyDisabled:            true,
		},
	}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	for i := 0; i < 3; i++ {
		w := d.post(t, legacyCredDisablePath, "")
		if w.Code != http.StatusOK {
			t.Fatalf("repeat %d: expected 200, got %d", i, w.Code)
		}
		data, _ := parseBody(t, w)["data"].(map[string]any)
		if data["already_disabled"] != true {
			t.Errorf("repeat %d: already_disabled = %v, want true", i, data["already_disabled"])
		}
	}
	if len(svc.disabled()) != 3 {
		t.Errorf("expected 3 idempotent calls, got %d", len(svc.disabled()))
	}
}

func TestLegacyCredential_Disable_MigrationRequired_409(t *testing.T) {
	svc := &stubLegacyCredentialService{disableErr: service.ErrLegacyCredentialMigrationRequired}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredDisablePath, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeLegacyCredentialMigrationRequired) {
		t.Errorf("expected LEGACY_CREDENTIAL_MIGRATION_REQUIRED, got %s", code)
	}
	assertNoCredentialMaterial(t, w.Body.String())
}

func TestLegacyCredential_Disable_NotFound_404(t *testing.T) {
	svc := &stubLegacyCredentialService{disableErr: repository.ErrMerchantNotFound}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredDisablePath, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d\nbody: %s", w.Code, w.Body)
	}
}

func TestLegacyCredential_Disable_Viewer_403(t *testing.T) {
	svc := &stubLegacyCredentialService{
		disableResp: &model.LegacyCredentialStatusResponse{
			LegacyCredentialState: model.LegacyCredentialStateLegacyDisabled,
		},
	}
	d := newLegacyCredRouter(model.DashboardUserRoleViewer, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredDisablePath, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("VIEWER: expected 403, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeInsufficientRole) {
		t.Errorf("expected INSUFFICIENT_ROLE, got %s", code)
	}
	if len(svc.disabled()) != 0 {
		t.Error("service must not be invoked for VIEWER")
	}
}

func TestLegacyCredential_Disable_SuspendedMerchant_401(t *testing.T) {
	svc := &stubLegacyCredentialService{
		disableResp: &model.LegacyCredentialStatusResponse{
			LegacyCredentialState: model.LegacyCredentialStateLegacyDisabled,
		},
	}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusSuspended, svc)

	w := d.post(t, legacyCredDisablePath, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if len(svc.disabled()) != 0 {
		t.Error("service must not be invoked for a SUSPENDED merchant")
	}
}

// TestLegacyCredential_Disable_NoCredentialMaterial asserts the disable
// response is a pure state report: no key id, no secret, no hash.
func TestLegacyCredential_Disable_NoCredentialMaterial(t *testing.T) {
	disabledAt := time.Now().UTC()
	svc := &stubLegacyCredentialService{
		disableResp: &model.LegacyCredentialStatusResponse{
			LegacyCredentialState:      model.LegacyCredentialStateLegacyDisabled,
			LegacyCredentialDisabledAt: &disabledAt,
			AlreadyDisabled:            false,
		},
	}
	d := newLegacyCredRouter(model.DashboardUserRoleOwner, model.MerchantStatusActive, svc)

	w := d.post(t, legacyCredDisablePath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	assertNoCredentialMaterial(t, w.Body.String())
	// The timestamp IS part of the contract.
	if !strings.Contains(w.Body.String(), "legacy_credential_disabled_at") {
		t.Error("legacy_credential_disabled_at missing")
	}
}

// ─── concurrency at the HTTP layer ───────────────────────────────────────────

// TestLegacyCredential_Migrate_ConcurrentSingleWinner fires simultaneous HTTP
// migrate calls through the real service (backed by a FOR-UPDATE-simulating
// store) and asserts exactly one 201 while the rest get the stable 409.
func TestLegacyCredential_Migrate_ConcurrentSingleWinner(t *testing.T) {
	const callers = 12

	store := newMemLegacyHTTPStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)
	svc := service.NewLegacyCredentialService(store)

	d := newLegacyCredRouterForMerchant(model.DashboardUserRoleOwner, model.MerchantStatusActive, merchantID, svc)

	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		conf409 int
		others  []string
	)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := d.post(t, legacyCredMigratePath, "")
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				if code := getStr(t, parseBody(t, w), "error", "code"); code == string(response.CodeLegacyCredentialAlreadyMigrated) {
					conf409++
				} else {
					others = append(others, code)
				}
			default:
				others = append(others, w.Body.String())
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, o := range others {
		t.Errorf("unexpected response: %s", o)
	}
	if created != 1 {
		t.Errorf("winners = %d, want exactly 1", created)
	}
	if conf409 != callers-1 {
		t.Errorf("409 losers = %d, want %d", conf409, callers-1)
	}
	if store.keyCount() != 1 {
		t.Errorf("keys minted = %d, want 1", store.keyCount())
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func assertNoCredentialMaterial(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{"pk_", "sk_", "api_secret", "secret_hash", "argon2"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body leaks %q: %s", forbidden, body)
		}
	}
}

// ─── shared in-memory store used by the HTTP tests and the e2e flow ─────────

// memLegacyHTTPStore mirrors pgLegacyCredentialStore's SELECT ... FOR UPDATE
// critical section with a mutex (see the limitation note in the service tests).
type memLegacyHTTPStore struct {
	mu        sync.Mutex
	merchants map[uuid.UUID]model.LegacyCredentialState
	disabled  map[uuid.UUID]*time.Time
	keys      map[string]*model.MerchantAPIKey
}

func newMemLegacyHTTPStore() *memLegacyHTTPStore {
	return &memLegacyHTTPStore{
		merchants: make(map[uuid.UUID]model.LegacyCredentialState),
		disabled:  make(map[uuid.UUID]*time.Time),
		keys:      make(map[string]*model.MerchantAPIKey),
	}
}

func (s *memLegacyHTTPStore) seed(id uuid.UUID, state model.LegacyCredentialState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merchants[id] = state
}

func (s *memLegacyHTTPStore) stateOf(id uuid.UUID) model.LegacyCredentialState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.merchants[id]
}

func (s *memLegacyHTTPStore) keyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

func (s *memLegacyHTTPStore) Migrate(_ context.Context, merchantID uuid.UUID, key *model.MerchantAPIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.merchants[merchantID]
	if !ok {
		return repository.ErrLegacyCredentialMerchantNotFound
	}
	if state != model.LegacyCredentialStateLegacy {
		return repository.ErrLegacyCredentialAlreadyMigrated
	}
	s.keys[key.KeyID] = key
	s.merchants[merchantID] = model.LegacyCredentialStateMigrated
	s.disabled[merchantID] = nil
	return nil
}

func (s *memLegacyHTTPStore) Disable(_ context.Context, merchantID uuid.UUID) (*repository.LegacyCredentialDisableResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.merchants[merchantID]
	if !ok {
		return nil, repository.ErrLegacyCredentialMerchantNotFound
	}
	switch state {
	case model.LegacyCredentialStateLegacy:
		return nil, repository.ErrLegacyCredentialMigrationRequired
	case model.LegacyCredentialStateLegacyDisabled:
		return &repository.LegacyCredentialDisableResult{
			State: state, DisabledAt: s.disabled[merchantID], AlreadyDisabled: true,
		}, nil
	case model.LegacyCredentialStateMigrated:
		now := time.Now().UTC()
		s.disabled[merchantID] = &now
		s.merchants[merchantID] = model.LegacyCredentialStateLegacyDisabled
		return &repository.LegacyCredentialDisableResult{
			State:           model.LegacyCredentialStateLegacyDisabled,
			DisabledAt:      &now,
			AlreadyDisabled: false,
		}, nil
	default:
		return nil, errors.New("unexpected state")
	}
}

var _ repository.LegacyCredentialStore = (*memLegacyHTTPStore)(nil)

// newLegacyCredRouterForMerchant pins the caller's merchant id so the router
// can be shared across goroutines in concurrency tests.
func newLegacyCredRouterForMerchant(
	role model.DashboardUserRole,
	merchantStatus model.MerchantStatus,
	merchantID uuid.UUID,
	svc service.LegacyCredentialService,
) *legacyCredRouter {
	gin.SetMode(gin.TestMode)

	userID := uuid.New()
	user := &model.MerchantUser{
		ID: userID, MerchantID: merchantID, Email: "caller@test.com",
		Role: role, Status: model.DashboardUserStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	authSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject:    userID.String(),
			MerchantID: merchantID.String(),
			Role:       string(role),
		},
	}
	merchantRepo := &mockMerchantRepoForMiddleware{
		resp: &model.Merchant{ID: merchantID, Status: merchantStatus},
	}

	h := handler.NewDashboardLegacyCredentialHandler(svc)
	r := gin.New()
	r.Use(middleware.RequestID())
	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, &mockUserRepoForMiddleware{user: user}, merchantRepo))
	{
		legacy := dash.Group("/legacy-credential")
		legacy.POST("/migrate",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			h.MigrateLegacyCredential,
		)
		legacy.POST("/disable",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			h.DisableLegacyCredential,
		)
	}

	return &legacyCredRouter{
		router: r, merchantID: merchantID, userID: userID, token: "test-token",
	}
}
