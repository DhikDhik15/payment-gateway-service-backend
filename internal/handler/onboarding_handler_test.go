package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// ─── Mem provisioner (handler-level) ──────────────────────────────────────────

type onboardMemProvisioner struct {
	mu        sync.Mutex
	merchants map[uuid.UUID]*model.Merchant
	users     map[uuid.UUID]*model.MerchantUser
	keys      map[uuid.UUID]*model.MerchantAPIKey
	codes     map[string]bool
	emails    map[string]bool
}

func newOnboardMemProvisioner() *onboardMemProvisioner {
	return &onboardMemProvisioner{
		merchants: make(map[uuid.UUID]*model.Merchant),
		users:     make(map[uuid.UUID]*model.MerchantUser),
		keys:      make(map[uuid.UUID]*model.MerchantAPIKey),
		codes:     make(map[string]bool),
		emails:    make(map[string]bool),
	}
}

func (p *onboardMemProvisioner) ExistsByCode(_ context.Context, code string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.codes[code], nil
}

func (p *onboardMemProvisioner) Provision(
	_ context.Context,
	merchant *model.Merchant,
	owner *model.MerchantUser,
	key *model.MerchantAPIKey,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codes[merchant.Code] {
		return repository.ErrMerchantCodeExists
	}
	if p.emails[owner.Email] {
		return repository.ErrMerchantUserEmailExists
	}
	mc, oc, kc := *merchant, *owner, *key
	p.merchants[mc.ID] = &mc
	p.codes[mc.Code] = true
	p.users[oc.ID] = &oc
	p.emails[oc.Email] = true
	p.keys[kc.ID] = &kc
	return nil
}

func newOnboardingTestRouter(adminKey string, prov service.OnboardingProvisioner) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())

	svc := service.NewOnboardingService(prov)
	h := handler.NewOnboardingHandler(svc)

	admin := r.Group("/api/v1/admin")
	admin.Use(middleware.AdminAuth(adminKey))
	{
		admin.POST("/onboarding/merchants", h.OnboardMerchant)
	}
	return r
}

func doOnboardRequest(r *gin.Engine, body any, headers map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/onboarding/merchants", &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func onboardBody() map[string]any {
	return map[string]any{
		"name":           "Merchant Alpha",
		"code":           "ALPHA01",
		"owner_email":    "alpha.owner@example.com",
		"owner_password": "password123",
	}
}

// ─── Auth / authorization ─────────────────────────────────────────────────────

func TestOnboardMerchantHandler_Unauthenticated(t *testing.T) {
	r := newOnboardingTestRouter("test-admin-key", newOnboardMemProvisioner())
	w := doOnboardRequest(r, onboardBody(), nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeAdminUnauthorized) {
		t.Error("expected ADMIN_UNAUTHORIZED")
	}
}

func TestOnboardMerchantHandler_WrongAdminKey(t *testing.T) {
	r := newOnboardingTestRouter("test-admin-key", newOnboardMemProvisioner())
	w := doOnboardRequest(r, onboardBody(), map[string]string{"X-Admin-Key": "wrong"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestOnboardMerchantHandler_MerchantAPIKeyRejected(t *testing.T) {
	// Merchant API key header must not satisfy AdminAuth.
	r := newOnboardingTestRouter("test-admin-key", newOnboardMemProvisioner())
	w := doOnboardRequest(r, onboardBody(), map[string]string{
		"X-API-Key": "pk_fake:sk_fake",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when using merchant API key, got %d", w.Code)
	}
}

func TestOnboardMerchantHandler_AuthorizedSuccess(t *testing.T) {
	prov := newOnboardMemProvisioner()
	r := newOnboardingTestRouter("test-admin-key", prov)

	w := doOnboardRequest(r, onboardBody(), map[string]string{"X-Admin-Key": "test-admin-key"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)
	if body["success"] != true {
		t.Error("expected success:true")
	}

	merchantID := getStr(t, body, "data", "merchant", "id")
	ownerID := getStr(t, body, "data", "owner", "id")
	keyID := getStr(t, body, "data", "api_credential", "key_id")
	secret := getStr(t, body, "data", "api_credential", "secret")

	if _, err := uuid.Parse(merchantID); err != nil {
		t.Fatalf("invalid merchant id: %s", merchantID)
	}
	if _, err := uuid.Parse(ownerID); err != nil {
		t.Fatalf("invalid owner id: %s", ownerID)
	}
	if !strings.HasPrefix(keyID, "pk_") {
		t.Fatalf("expected pk_ key_id, got %s", keyID)
	}
	if !strings.HasPrefix(secret, "sk_") {
		t.Fatalf("expected sk_ secret, got %s", secret)
	}

	// Password must never appear in response JSON.
	raw := w.Body.String()
	if strings.Contains(raw, "password123") {
		t.Fatal("plaintext password must not appear in response")
	}
	if strings.Contains(raw, "password_hash") || strings.Contains(raw, "$argon2id$") {
		t.Fatal("hashes must not appear in response")
	}

	// Tenant isolation in store.
	mid, _ := uuid.Parse(merchantID)
	oid, _ := uuid.Parse(ownerID)
	prov.mu.Lock()
	defer prov.mu.Unlock()
	u := prov.users[oid]
	if u == nil || u.MerchantID != mid {
		t.Fatal("owner must belong to created merchant")
	}
	var matchedKey *model.MerchantAPIKey
	for _, k := range prov.keys {
		if k.KeyID == keyID {
			matchedKey = k
			break
		}
	}
	if matchedKey == nil || matchedKey.MerchantID != mid {
		t.Fatal("api credential must belong to created merchant")
	}
	if matchedKey.SecretHash == secret {
		t.Fatal("secret must not be stored plaintext")
	}
}

func TestOnboardMerchantHandler_ValidationError(t *testing.T) {
	r := newOnboardingTestRouter("test-admin-key", newOnboardMemProvisioner())
	w := doOnboardRequest(r, map[string]any{
		"name": "X",
		"code": "AB",
	}, map[string]string{"X-Admin-Key": "test-admin-key"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestOnboardMerchantHandler_DuplicateCode(t *testing.T) {
	prov := newOnboardMemProvisioner()
	r := newOnboardingTestRouter("test-admin-key", prov)
	headers := map[string]string{"X-Admin-Key": "test-admin-key"}

	w1 := doOnboardRequest(r, onboardBody(), headers)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first: expected 201, got %d", w1.Code)
	}

	body2 := onboardBody()
	body2["owner_email"] = "other@example.com"
	w2 := doOnboardRequest(r, body2, headers)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate: expected 409, got %d\nbody: %s", w2.Code, w2.Body)
	}
	if getStr(t, parseBody(t, w2), "error", "code") != string(response.CodeDuplicateMerchantCode) {
		t.Error("expected DUPLICATE_MERCHANT_CODE")
	}
}

func TestOnboardMerchantHandler_DuplicateEmail(t *testing.T) {
	prov := newOnboardMemProvisioner()
	r := newOnboardingTestRouter("test-admin-key", prov)
	headers := map[string]string{"X-Admin-Key": "test-admin-key"}

	w1 := doOnboardRequest(r, onboardBody(), headers)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first: expected 201, got %d", w1.Code)
	}

	body2 := onboardBody()
	body2["code"] = "ALPHA02"
	w2 := doOnboardRequest(r, body2, headers)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate email: expected 409, got %d\nbody: %s", w2.Code, w2.Body)
	}
	if getStr(t, parseBody(t, w2), "error", "code") != string(response.CodeEmailAlreadyExists) {
		t.Error("expected EMAIL_ALREADY_EXISTS")
	}
}

func TestOnboardMerchantHandler_TwoTenantsIsolated(t *testing.T) {
	prov := newOnboardMemProvisioner()
	r := newOnboardingTestRouter("test-admin-key", prov)
	headers := map[string]string{"X-Admin-Key": "test-admin-key"}

	bodyA := onboardBody()
	bodyB := map[string]any{
		"name":           "Merchant Beta",
		"code":           "BETA01",
		"owner_email":    "beta.owner@example.com",
		"owner_password": "password456",
	}

	wA := doOnboardRequest(r, bodyA, headers)
	wB := doOnboardRequest(r, bodyB, headers)
	if wA.Code != http.StatusCreated || wB.Code != http.StatusCreated {
		t.Fatalf("expected both 201; got A=%d B=%d", wA.Code, wB.Code)
	}

	idA := getStr(t, parseBody(t, wA), "data", "merchant", "id")
	idB := getStr(t, parseBody(t, wB), "data", "merchant", "id")
	if idA == idB {
		t.Fatal("merchants must have distinct ids")
	}
	keyA := getStr(t, parseBody(t, wA), "data", "api_credential", "key_id")
	keyB := getStr(t, parseBody(t, wB), "data", "api_credential", "key_id")
	if keyA == keyB {
		t.Fatal("api credentials must not overlap")
	}

	midA, _ := uuid.Parse(idA)
	midB, _ := uuid.Parse(idB)
	prov.mu.Lock()
	defer prov.mu.Unlock()
	for _, u := range prov.users {
		if u.Email == "alpha.owner@example.com" && u.MerchantID != midA {
			t.Fatal("owner A must belong to merchant A")
		}
		if u.Email == "beta.owner@example.com" && u.MerchantID != midB {
			t.Fatal("owner B must belong to merchant B")
		}
	}
	for _, k := range prov.keys {
		if k.KeyID == keyA && k.MerchantID != midA {
			t.Fatal("key A must belong to merchant A")
		}
		if k.KeyID == keyB && k.MerchantID != midB {
			t.Fatal("key B must belong to merchant B")
		}
	}
}
