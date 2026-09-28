package handler_test

// legacy_credential_e2e_test.go — Phase 8D.3 end-to-end regression over HTTP,
// exercising the REAL Auth middleware, the REAL LegacyCredentialService and the
// REAL dashboard handlers against one shared stateful store:
//
//	legacy key works (LEGACY)
//	  → migrate: 201 + one-time secret (state MIGRATED)
//	  → legacy key STILL works (window semantics)
//	  → disable: 200 (state LEGACY_DISABLED)
//	  → legacy key now rejected forever, byte-identical to an unknown key
//	  → repeat disable is a no-op, re-migrate is 409
//
// Plus the LEGACY_API_CREDENTIALS_ENABLED off-switch: bare legacy keys are
// rejected before any lookup while compound Phase 5C keys keep working.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// legacyE2ELegacyKey is the merchant's row-level plaintext credential.
const legacyE2ELegacyKey = "pk_e2elegacycredential000000000"

// legacyE2EMerchantSvc resolves bare legacy API keys exactly the way the
// production repository does: match on the key, return the row (with its
// CURRENT LegacyCredentialState), and leave all gating to the Auth middleware.
type legacyE2EMerchantSvc struct {
	store      *memLegacyHTTPStore
	merchantID uuid.UUID
	legacyKey  string
	lookups    int
}

func (s *legacyE2EMerchantSvc) CreateMerchant(context.Context, model.CreateMerchantRequest) (*model.CreateMerchantResponse, error) {
	panic("not used")
}
func (s *legacyE2EMerchantSvc) GetMerchant(context.Context, uuid.UUID) (*model.GetMerchantResponse, error) {
	panic("not used")
}
func (s *legacyE2EMerchantSvc) GetMerchantByAPIKey(_ context.Context, apiKey string) (*model.Merchant, error) {
	s.lookups++
	if apiKey != s.legacyKey {
		return nil, repository.ErrMerchantNotFound
	}
	return &model.Merchant{
		ID:                    s.merchantID,
		Name:                  "E2E Merchant",
		Code:                  "E2E",
		APIKey:                s.legacyKey,
		Status:                model.MerchantStatusActive,
		LegacyCredentialState: s.store.stateOf(s.merchantID),
	}, nil
}
func (s *legacyE2EMerchantSvc) UpdateMerchantStatus(context.Context, uuid.UUID, model.MerchantStatus) (*model.GetMerchantResponse, error) {
	panic("not used")
}

var _ service.MerchantService = (*legacyE2EMerchantSvc)(nil)

type legacyE2EDeps struct {
	router      *gin.Engine
	store       *memLegacyHTTPStore
	merchantSvc *legacyE2EMerchantSvc
	merchantID  uuid.UUID
	token       string
}

// newLegacyE2ERouter wires BOTH surfaces onto one engine: the merchant API
// (X-API-Key → Auth) and the dashboard (Bearer → RequireDashboardAuth).
func newLegacyE2ERouter(legacyEnabled bool, apiKeySvc service.MerchantAPIKeyService) *legacyE2EDeps {
	gin.SetMode(gin.TestMode)

	store := newMemLegacyHTTPStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)

	merchantSvc := &legacyE2EMerchantSvc{
		store: store, merchantID: merchantID, legacyKey: legacyE2ELegacyKey,
	}

	// Dashboard side (mirrors main.go).
	userID := uuid.New()
	user := &model.MerchantUser{
		ID: userID, MerchantID: merchantID, Email: "owner@e2e.test",
		Role: model.DashboardUserRoleOwner, Status: model.DashboardUserStatusActive,
	}
	authSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			Subject: userID.String(), MerchantID: merchantID.String(),
			Role: string(model.DashboardUserRoleOwner),
		},
	}
	merchantRepo := &mockMerchantRepoForMiddleware{
		resp: &model.Merchant{ID: merchantID, Status: model.MerchantStatusActive},
	}

	legacySvc := service.NewLegacyCredentialService(store)
	legacyH := handler.NewDashboardLegacyCredentialHandler(legacySvc)

	r := gin.New()
	r.Use(middleware.RequestID())

	// Merchant API surface — protected by the two-stage Auth middleware.
	api := r.Group("/api/v1/payments")
	api.Use(middleware.Auth(merchantSvc, apiKeySvc, legacyEnabled))
	api.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// Dashboard surface — Bearer JWT + role gate.
	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, &mockUserRepoForMiddleware{user: user}, merchantRepo))
	{
		legacy := dash.Group("/legacy-credential")
		legacy.POST("/migrate",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			legacyH.MigrateLegacyCredential,
		)
		legacy.POST("/disable",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			legacyH.DisableLegacyCredential,
		)
	}

	return &legacyE2EDeps{
		router: r, store: store, merchantSvc: merchantSvc,
		merchantID: merchantID, token: "e2e-token",
	}
}

// authGet calls the merchant API with an X-API-Key credential.
func authGet(r *gin.Engine, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// dashPost calls a dashboard endpoint with the Bearer token.
func dashPost(r *gin.Engine, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── the canonical lifecycle ─────────────────────────────────────────────────

// TestLegacyCredential_E2E_MigrateThenDisable walks the whole state machine
// over HTTP and asserts every observable contract along the way.
func TestLegacyCredential_E2E_MigrateThenDisable(t *testing.T) {
	d := newLegacyE2ERouter(true, noopAPIKeySvc())

	// 1. The legacy credential works up front (LEGACY, window open).
	if w := authGet(d.router, legacyE2ELegacyKey); w.Code != http.StatusOK {
		t.Fatalf("step 1: legacy key must authenticate, got %d\nbody: %s", w.Code, w.Body)
	}
	if got := d.store.stateOf(d.merchantID); got != model.LegacyCredentialStateLegacy {
		t.Fatalf("step 1: state = %q, want LEGACY", got)
	}

	// 2. Migrate: 201 + the one-time Phase 5C secret.
	wMig := dashPost(d.router, d.token, "/api/v1/dashboard/legacy-credential/migrate")
	if wMig.Code != http.StatusCreated {
		t.Fatalf("step 2: expected 201, got %d\nbody: %s", wMig.Code, wMig.Body)
	}
	migBody := parseBody(t, wMig)
	secret := getStr(t, migBody, "data", "secret")
	keyID := getStr(t, migBody, "data", "key_id")
	if secret == "" || keyID == "" {
		t.Fatalf("step 2: missing one-time credential: %s", wMig.Body)
	}
	if !strings.HasPrefix(secret, "sk_") || !strings.HasPrefix(keyID, "pk_") {
		t.Errorf("step 2: Phase 5C format expected, got key_id=%q secret=%q", keyID, secret[:3]+"…")
	}
	if got := getStr(t, migBody, "data", "legacy_credential_state"); got != string(model.LegacyCredentialStateMigrated) {
		t.Errorf("step 2: state = %q, want MIGRATED", got)
	}
	if d.store.keyCount() != 1 {
		t.Fatalf("step 2: keys = %d, want 1", d.store.keyCount())
	}
	// The stored row must not hold the plaintext.
	d.store.mu.Lock()
	storedHash := d.store.keys[keyID].SecretHash
	d.store.mu.Unlock()
	if storedHash == secret {
		t.Fatal("step 2: plaintext secret persisted")
	}

	// 3. Migration does NOT cut the legacy key off — that is disable's job.
	if w := authGet(d.router, legacyE2ELegacyKey); w.Code != http.StatusOK {
		t.Fatalf("step 3: legacy key must still work after migrate, got %d\nbody: %s", w.Code, w.Body)
	}

	// 4. Disable: 200, state LEGACY_DISABLED, timestamp set.
	wDis := dashPost(d.router, d.token, "/api/v1/dashboard/legacy-credential/disable")
	if wDis.Code != http.StatusOK {
		t.Fatalf("step 4: expected 200, got %d\nbody: %s", wDis.Code, wDis.Body)
	}
	disBody := parseBody(t, wDis)
	if got := getStr(t, disBody, "data", "legacy_credential_state"); got != string(model.LegacyCredentialStateLegacyDisabled) {
		t.Errorf("step 4: state = %q, want LEGACY_DISABLED", got)
	}
	if data, _ := disBody["data"].(map[string]any); data["already_disabled"] != false {
		t.Errorf("step 4: already_disabled = %v, want false", data["already_disabled"])
	}
	firstStamp, _ := data2str(disBody, "data", "legacy_credential_disabled_at")
	if firstStamp == "" {
		t.Error("step 4: legacy_credential_disabled_at missing")
	}

	// 5. The legacy credential is dead — and indistinguishable from an unknown key.
	wAfter := authGet(d.router, legacyE2ELegacyKey)
	if wAfter.Code != http.StatusUnauthorized {
		t.Fatalf("step 5: legacy key must be rejected after disable, got %d", wAfter.Code)
	}
	if code := getStr(t, parseBody(t, wAfter), "error", "code"); code != string(response.CodeInvalidAPIKey) {
		t.Errorf("step 5: expected INVALID_API_KEY, got %s", code)
	}
	wUnknown := authGet(d.router, "pk_neverexistedvalue00000000000")
	if comparableStatusBody(t, wAfter) != comparableStatusBody(t, wUnknown) {
		t.Errorf("step 5: disabled key distinguishable from unknown key:\n after:   %s\n unknown: %s",
			comparableStatusBody(t, wAfter), comparableStatusBody(t, wUnknown))
	}

	// 6. Repeats are safe: disable is idempotent, migrate is a stable 409.
	wDis2 := dashPost(d.router, d.token, "/api/v1/dashboard/legacy-credential/disable")
	if wDis2.Code != http.StatusOK {
		t.Fatalf("step 6: repeat disable expected 200, got %d", wDis2.Code)
	}
	if data, _ := parseBody(t, wDis2)["data"].(map[string]any); data["already_disabled"] != true {
		t.Errorf("step 6: already_disabled = %v, want true", data["already_disabled"])
	}
	secondStamp, _ := data2str(parseBody(t, wDis2), "data", "legacy_credential_disabled_at")
	if secondStamp != firstStamp {
		t.Errorf("step 6: disabled_at rewritten: %q != %q", secondStamp, firstStamp)
	}
	wMig2 := dashPost(d.router, d.token, "/api/v1/dashboard/legacy-credential/migrate")
	if wMig2.Code != http.StatusConflict {
		t.Fatalf("step 6: re-migrate expected 409, got %d", wMig2.Code)
	}
	if code := getStr(t, parseBody(t, wMig2), "error", "code"); code != string(response.CodeLegacyCredentialAlreadyMigrated) {
		t.Errorf("step 6: expected LEGACY_CREDENTIAL_ALREADY_MIGRATED, got %s", code)
	}
	if d.store.keyCount() != 1 {
		t.Errorf("step 6: keys = %d, want 1 (no second issuance)", d.store.keyCount())
	}

	// 7. No credential material in any dashboard response except the one-time
	//    secret in step 2.
	for _, w := range []*httptest.ResponseRecorder{wDis, wDis2, wMig2, wAfter} {
		assertNoCredentialMaterial(t, w.Body.String())
	}
	// The one-time secret must never be re-readable.
	if strings.Contains(wMig2.Body.String(), secret) {
		t.Error("step 7: the one-time secret was disclosed a second time")
	}
}

// ─── migration window off-switch ────────────────────────────────────────────

// TestLegacyCredential_E2E_WindowClosed rejects bare legacy keys before any
// lookup while compound Phase 5C credentials continue to work.
func TestLegacyCredential_E2E_WindowClosed(t *testing.T) {
	compoundMerchant := &model.Merchant{
		ID: uuid.New(), Name: "Compound", Code: "CPD",
		Status:                model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateLegacyDisabled,
	}
	keySvc := &stubAPIKeySvc{merchant: compoundMerchant}
	d := newLegacyE2ERouter(false, keySvc)

	lookupsBefore := d.merchantSvc.lookups
	w := authGet(d.router, legacyE2ELegacyKey)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("closed window: expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeLegacyCredentialsNotEnabled) {
		t.Errorf("expected LEGACY_CREDENTIALS_NOT_ENABLED, got %s", code)
	}
	if d.merchantSvc.lookups != lookupsBefore {
		t.Errorf("closed window must not perform a credential lookup (before=%d after=%d)",
			lookupsBefore, d.merchantSvc.lookups)
	}

	// Compound Phase 5C credentials are unaffected by the flag.
	wCompound := authGet(d.router, "pk_compound:sk_compoundsecret")
	if wCompound.Code != http.StatusOK {
		t.Fatalf("compound key must work with the window closed, got %d\nbody: %s", wCompound.Code, wCompound.Body)
	}

	// Reopening (new router with the flag on) restores legacy auth: the flag is
	// the only difference.
	dOpen := newLegacyE2ERouter(true, noopAPIKeySvc())
	if wOpen := authGet(dOpen.router, legacyE2ELegacyKey); wOpen.Code != http.StatusOK {
		t.Fatalf("open window: expected 200, got %d\nbody: %s", wOpen.Code, wOpen.Body)
	}
}

// TestLegacyCredential_E2E_WindowClosedSameResponseForKnownAndUnknown proves
// the closed-window rejection never confirms credential existence.
func TestLegacyCredential_E2E_WindowClosedSameResponseForKnownAndUnknown(t *testing.T) {
	d := newLegacyE2ERouter(false, noopAPIKeySvc())

	known := authGet(d.router, legacyE2ELegacyKey)
	unknown := authGet(d.router, "pk_neverexistedvalue00000000000")

	if known.Code != unknown.Code {
		t.Fatalf("status oracle: known=%d unknown=%d", known.Code, unknown.Code)
	}
	if comparableStatusBody(t, known) != comparableStatusBody(t, unknown) {
		t.Errorf("body oracle:\n known:   %s\n unknown: %s",
			comparableStatusBody(t, known), comparableStatusBody(t, unknown))
	}
	if d.merchantSvc.lookups != 0 {
		t.Errorf("no lookups may occur with the window closed (got %d)", d.merchantSvc.lookups)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// data2str extracts a nested string field, tolerating a missing key.
func data2str(body map[string]any, keys ...string) (string, bool) {
	var cur any = body
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = mm[k]
	}
	s, ok := cur.(string)
	return s, ok
}

// comparableStatusBody is the request-id-stripped payload used for oracle
// comparisons (request_id is legitimately unique per request).
func comparableStatusBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	body := parseBody(t, w)
	delete(body, "meta")
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}
