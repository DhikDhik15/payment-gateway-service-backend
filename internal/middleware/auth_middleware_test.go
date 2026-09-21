package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── Stub MerchantService ─────────────────────────────────────────────────────

type stubMerchantService struct {
	merchant      *model.Merchant
	internalError bool
}

func (s *stubMerchantService) CreateMerchant(_ context.Context, _ model.CreateMerchantRequest) (*model.CreateMerchantResponse, error) {
	panic("not used in auth middleware tests")
}
func (s *stubMerchantService) GetMerchant(_ context.Context, _ uuid.UUID) (*model.GetMerchantResponse, error) {
	panic("not used in auth middleware tests")
}
func (s *stubMerchantService) GetMerchantByAPIKey(_ context.Context, apiKey string) (*model.Merchant, error) {
	if s.internalError {
		return nil, errors.New("database connection lost")
	}
	if s.merchant != nil && s.merchant.APIKey == apiKey {
		return s.merchant, nil
	}
	return nil, repository.ErrMerchantNotFound
}

var _ service.MerchantService = (*stubMerchantService)(nil)

// ─── Stub MerchantAPIKeyService ───────────────────────────────────────────────

// stubAPIKeyService is a controllable MerchantAPIKeyService for auth tests.
type stubAPIKeyService struct {
	// merchant returned on successful authentication.
	merchant *model.Merchant
	// authErr overrides the returned error (nil = success).
	authErr error
	// lastUsedAtUpdated is set to true when UpdateLastUsedAt would be called.
	// We track calls via AuthenticateByAPIKey since that's what the middleware uses.
	callCount int
}

func (s *stubAPIKeyService) CreateKey(_ context.Context, _ uuid.UUID, _ model.CreateMerchantAPIKeyRequest) (*model.CreateMerchantAPIKeyResponse, error) {
	panic("not used in auth tests")
}
func (s *stubAPIKeyService) ListKeys(_ context.Context, _ uuid.UUID) ([]model.MerchantAPIKeyResponse, error) {
	panic("not used in auth tests")
}
func (s *stubAPIKeyService) RevokeKey(_ context.Context, _, _ uuid.UUID) error {
	panic("not used in auth tests")
}
func (s *stubAPIKeyService) RotateKey(_ context.Context, _, _ uuid.UUID) (*model.RotateMerchantAPIKeyResponse, error) {
	panic("not used in auth tests")
}
func (s *stubAPIKeyService) AuthenticateByAPIKey(_ context.Context, _ string) (*model.Merchant, error) {
	s.callCount++
	if s.authErr != nil {
		return nil, s.authErr
	}
	return s.merchant, nil
}

var _ service.MerchantAPIKeyService = (*stubAPIKeyService)(nil)

// ─── router factory ───────────────────────────────────────────────────────────

// newAuthTestRouter returns a minimal Gin engine with the Auth middleware.
// Both merchant and API key services are provided.
func newAuthTestRouter(t *testing.T, merchantSvc service.MerchantService, apiKeySvc service.MerchantAPIKeyService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.Auth(merchantSvc, apiKeySvc))

	r.GET("/protected", func(c *gin.Context) {
		m := middleware.MerchantFromContext(c)
		if m == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no merchant in context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"merchant_id": m.ID.String()})
	})
	return r
}

// noopAPIKeySvc is used where new-key auth should never trigger
// (e.g., for legacy credential tests).
func noopAPIKeySvc() service.MerchantAPIKeyService {
	return &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func authRequest(r *gin.Engine, apiKey string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func parseJSONBody(w *httptest.ResponseRecorder, v any) error {
	return json.Unmarshal(w.Body.Bytes(), v)
}

func assertErrorCode(t *testing.T, w *httptest.ResponseRecorder, want response.ErrorCode) {
	t.Helper()
	var body map[string]any
	if err := parseJSONBody(w, &body); err != nil {
		t.Fatalf("parse body: %v\nbody: %s", err, w.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code != string(want) {
		t.Errorf("error code: got %q, want %q\nbody: %s", code, want, w.Body.String())
	}
}

func getMerchantID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := parseJSONBody(w, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	return body["merchant_id"].(string)
}

// ─── Legacy authentication tests ─────────────────────────────────────────────

// TestAuth_LegacyKey_Valid ensures a bare legacy api_key works.
// Legacy keys use the "pk_" prefix but have NO ":sk_" secret segment.
func TestAuth_LegacyKey_Valid(t *testing.T) {
	merchant := &model.Merchant{
		ID: uuid.New(), Name: "Legacy", Code: "LEG001",
		APIKey: "pk_legacybarekeyhexvalue", Status: model.MerchantStatusActive,
	}
	r := newAuthTestRouter(t, &stubMerchantService{merchant: merchant}, noopAPIKeySvc())

	w := authRequest(r, "pk_legacybarekeyhexvalue")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	if getMerchantID(t, w) != merchant.ID.String() {
		t.Error("wrong merchant ID in context")
	}
}

// TestAuth_LegacyKey_PkPrefix_DoesNotEnterNewPath ensures bare "pk_…" legacy
// credentials are NOT routed into MerchantAPIKeyService.
func TestAuth_LegacyKey_PkPrefix_DoesNotEnterNewPath(t *testing.T) {
	merchant := &model.Merchant{
		ID: uuid.New(), Name: "Legacy", Code: "LEG002",
		APIKey: "pk_abcdef0123456789", Status: model.MerchantStatusActive,
	}
	apiKeySvc := &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}
	r := newAuthTestRouter(t, &stubMerchantService{merchant: merchant}, apiKeySvc)

	w := authRequest(r, "pk_abcdef0123456789")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 via legacy path, got %d\nbody: %s", w.Code, w.Body)
	}
	if apiKeySvc.callCount != 0 {
		t.Errorf("AuthenticateByAPIKey must not be called for bare legacy pk_ keys, got %d calls", apiKeySvc.callCount)
	}
}

// TestAuth_LegacyKey_Invalid — unknown legacy key → 401.
func TestAuth_LegacyKey_Invalid(t *testing.T) {
	r := newAuthTestRouter(t, &stubMerchantService{}, noopAPIKeySvc())

	w := authRequest(r, "legacy_bad_key")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	assertErrorCode(t, w, response.CodeInvalidAPIKey)
}

// ─── New API key authentication tests ────────────────────────────────────────

// TestAuth_NewKey_Valid ensures a new-style key ("pk_...") succeeds.
func TestAuth_NewKey_Valid(t *testing.T) {
	merchant := &model.Merchant{
		ID: uuid.New(), Name: "New Merchant", Code: "NEW001",
		Status: model.MerchantStatusActive,
	}
	apiKeySvc := &stubAPIKeyService{merchant: merchant}
	r := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvc)

	w := authRequest(r, "pk_abc123:sk_secretvalue")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	if getMerchantID(t, w) != merchant.ID.String() {
		t.Error("wrong merchant ID in context")
	}
	if apiKeySvc.callCount != 1 {
		t.Errorf("expected 1 AuthenticateByAPIKey call, got %d", apiKeySvc.callCount)
	}
}

// TestAuth_NewKey_Invalid — "pk_" prefix but invalid key → 401, no legacy fallback.
func TestAuth_NewKey_Invalid(t *testing.T) {
	legacySvc := &stubMerchantService{} // would succeed if legacy auth was tried
	apiKeySvc := &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}
	r := newAuthTestRouter(t, legacySvc, apiKeySvc)

	w := authRequest(r, "pk_bad:sk_bad")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	assertErrorCode(t, w, response.CodeInvalidAPIKey)
}

// TestAuth_NewKey_RevokedKey — revoked key → 401.
func TestAuth_NewKey_RevokedKey(t *testing.T) {
	apiKeySvc := &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}
	r := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvc)

	w := authRequest(r, "pk_revoked:sk_secret")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	assertErrorCode(t, w, response.CodeInvalidAPIKey)
}

// TestAuth_NewKey_ExpiredKey — expired key → 401.
func TestAuth_NewKey_ExpiredKey(t *testing.T) {
	apiKeySvc := &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}
	r := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvc)

	w := authRequest(r, "pk_expired:sk_secret")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	assertErrorCode(t, w, response.CodeInvalidAPIKey)
}

// TestAuth_NewKey_InactiveMerchant — valid new key but merchant is not ACTIVE → 401.
func TestAuth_NewKey_InactiveMerchant(t *testing.T) {
	merchant := &model.Merchant{
		ID: uuid.New(), Status: model.MerchantStatusSuspended,
	}
	apiKeySvc := &stubAPIKeyService{merchant: merchant}
	r := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvc)

	w := authRequest(r, "pk_validkey:sk_validSecret")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for inactive merchant, got %d\nbody: %s", w.Code, w.Body)
	}
	assertErrorCode(t, w, response.CodeMerchantInactive)
}

// TestAuth_NewKey_DoesNotFallbackToLegacy — if key starts with "pk_" but is
// invalid, legacy auth must NOT be tried (even if legacy credential would succeed).
func TestAuth_NewKey_DoesNotFallbackToLegacy(t *testing.T) {
	// Legacy service has a matching merchant for the same key value.
	legacyMerchant := &model.Merchant{
		ID: uuid.New(), Status: model.MerchantStatusActive,
		APIKey: "pk_shouldnotfallback:sk_shouldnotfallback",
	}
	legacySvc := &stubMerchantService{merchant: legacyMerchant}
	// New key service returns not-found.
	apiKeySvc := &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}

	r := newAuthTestRouter(t, legacySvc, apiKeySvc)
	w := authRequest(r, "pk_shouldnotfallback:sk_shouldnotfallback")

	// Must be 401 — new-key path is used, fails, and legacy is NOT tried.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (no fallback), got %d\nbody: %s", w.Code, w.Body)
	}
}

// ─── Common tests ─────────────────────────────────────────────────────────────

// TestAuth_MissingKey — no X-API-Key header → 401.
func TestAuth_MissingKey(t *testing.T) {
	r := newAuthTestRouter(t, &stubMerchantService{}, noopAPIKeySvc())
	w := authRequest(r, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	assertErrorCode(t, w, response.CodeInvalidAPIKey)
}

// TestAuth_InactiveMerchant_Legacy — inactive merchant via legacy auth → 401.
func TestAuth_InactiveMerchant_Legacy(t *testing.T) {
	for _, status := range []model.MerchantStatus{
		model.MerchantStatusInactive,
		model.MerchantStatusSuspended,
	} {
		t.Run(string(status), func(t *testing.T) {
			merchant := &model.Merchant{
				ID: uuid.New(), Name: "Inactive", Code: "INA",
				APIKey: "legacy_inactive_key", Status: status,
			}
			r := newAuthTestRouter(t, &stubMerchantService{merchant: merchant}, noopAPIKeySvc())

			w := authRequest(r, "legacy_inactive_key")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", w.Code)
			}
			assertErrorCode(t, w, response.CodeMerchantInactive)
		})
	}
}

// TestAuth_ServiceInternalError — DB error on legacy lookup → 500.
func TestAuth_ServiceInternalError(t *testing.T) {
	r := newAuthTestRouter(t, &stubMerchantService{internalError: true}, noopAPIKeySvc())

	w := authRequest(r, "legacy_any_key")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	assertErrorCode(t, w, response.CodeInternalError)
}

// TestAuth_RequestAborted — after 401, downstream handler must NOT be called.
func TestAuth_RequestAborted(t *testing.T) {
	called := false
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.Auth(&stubMerchantService{}, noopAPIKeySvc()))
	r.GET("/protected", func(c *gin.Context) {
		called = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("X-API-Key", "legacy_bad")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Error("downstream handler should NOT be called after auth failure")
	}
}

// TestAuth_MerchantFromContext_OutsideAuth — MerchantFromContext returns nil
// outside an Auth-protected route.
func TestAuth_MerchantFromContext_OutsideAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotMerchant *model.Merchant

	r := gin.New()
	r.GET("/unprotected", func(c *gin.Context) {
		gotMerchant = middleware.MerchantFromContext(c)
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest(http.MethodGet, "/unprotected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if gotMerchant != nil {
		t.Error("expected nil merchant from unprotected context")
	}
}

// TestAuth_CrossMerchant — Merchant A key returns Merchant A, not Merchant B.
func TestAuth_CrossMerchant(t *testing.T) {
	merchantA := &model.Merchant{
		ID: uuid.New(), Name: "A", Code: "MA", Status: model.MerchantStatusActive,
	}
	merchantB := &model.Merchant{
		ID: uuid.New(), Name: "B", Code: "MB", Status: model.MerchantStatusActive,
	}

	// Two separate routers with different API key services.
	apiKeySvcA := &stubAPIKeyService{merchant: merchantA}
	apiKeySvcB := &stubAPIKeyService{merchant: merchantB}

	rA := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvcA)
	rB := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvcB)

	// Merchant A key on router A → Merchant A.
	wA := authRequest(rA, "pk_keya:sk_secreta")
	if wA.Code != http.StatusOK {
		t.Fatalf("merchant A: expected 200, got %d", wA.Code)
	}
	if getMerchantID(t, wA) != merchantA.ID.String() {
		t.Error("merchant A key returned wrong merchant")
	}

	// Merchant B key on router B → Merchant B.
	wB := authRequest(rB, "pk_keyb:sk_secretb")
	if wB.Code != http.StatusOK {
		t.Fatalf("merchant B: expected 200, got %d", wB.Code)
	}
	if getMerchantID(t, wB) != merchantB.ID.String() {
		t.Error("merchant B key returned wrong merchant")
	}
}

// TestAuth_NewKey_LastUsedAt_OnlyOnSuccess — confirms AuthenticateByAPIKey is
// not called on missing key (so last_used_at not updated on auth failure).
func TestAuth_NewKey_LastUsedAt_OnlyOnSuccess(t *testing.T) {
	apiKeySvc := &stubAPIKeyService{authErr: service.ErrAPIKeyNotFound}
	r := newAuthTestRouter(t, &stubMerchantService{}, apiKeySvc)

	// Send a request with no key at all — auth fails before apiKeySvc is called.
	authRequest(r, "")
	if apiKeySvc.callCount != 0 {
		t.Errorf("AuthenticateByAPIKey should not be called on missing key, got %d calls", apiKeySvc.callCount)
	}

	// Send a legacy key (no pk_ prefix) — apiKeySvc should NOT be called.
	authRequest(r, "legacy_key")
	if apiKeySvc.callCount != 0 {
		t.Errorf("AuthenticateByAPIKey should not be called for legacy keys, got %d calls", apiKeySvc.callCount)
	}
}

// TestAuth_NewKey_ExpiresAt_Enforced — uses an actual MerchantAPIKey with IsActive()
// to verify expiry enforcement at the model level.
func TestAuth_NewKey_ExpiresAt_Enforced(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)

	key := &model.MerchantAPIKey{
		ID:         uuid.New(),
		MerchantID: uuid.New(),
		KeyID:      "pk_expkey",
		Status:     model.MerchantAPIKeyStatusActive,
		ExpiresAt:  &past, // already expired
	}
	// IsActive() must return false.
	if key.IsActive() {
		t.Error("expired key should not be active")
	}

	future := time.Now().UTC().Add(24 * time.Hour)
	key.ExpiresAt = &future
	if !key.IsActive() {
		t.Error("future expiry key should be active")
	}

	key.ExpiresAt = nil
	if !key.IsActive() {
		t.Error("no-expiry key should be active")
	}

	key.Status = model.MerchantAPIKeyStatusRevoked
	if key.IsActive() {
		t.Error("revoked key should not be active")
	}
}
