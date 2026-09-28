package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/jackc/pgx/v5"
)

// ─── In-memory repos for API key handler tests ────────────────────────────────

type apiKeyMemMerchantRepo struct {
	mu       sync.Mutex
	byID     map[uuid.UUID]*model.Merchant
	byAPIKey map[string]*model.Merchant
}

func newAPIKeyMemMerchantRepo() *apiKeyMemMerchantRepo {
	return &apiKeyMemMerchantRepo{
		byID:     make(map[uuid.UUID]*model.Merchant),
		byAPIKey: make(map[string]*model.Merchant),
	}
}

func (r *apiKeyMemMerchantRepo) Create(_ context.Context, m *model.Merchant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[m.ID] = m
	r.byAPIKey[m.APIKey] = m
	return nil
}
func (r *apiKeyMemMerchantRepo) CreateInTx(ctx context.Context, _ pgx.Tx, m *model.Merchant) error {
	return r.Create(ctx, m)
}
func (r *apiKeyMemMerchantRepo) GetByID(_ context.Context, id uuid.UUID) (*model.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[id]
	if !ok {
		return nil, repository.ErrMerchantNotFound
	}
	cp := *m
	return &cp, nil
}
func (r *apiKeyMemMerchantRepo) GetByAPIKey(_ context.Context, apiKey string) (*model.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byAPIKey[apiKey]
	if !ok {
		return nil, repository.ErrMerchantNotFound
	}
	cp := *m
	return &cp, nil
}
func (r *apiKeyMemMerchantRepo) ExistsByCode(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (r *apiKeyMemMerchantRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ model.MerchantStatus) error {
	return nil
}

type apiKeyMemKeyRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]*model.MerchantAPIKey
	byKeyID map[string]*model.MerchantAPIKey
}

func newAPIKeyMemKeyRepo() *apiKeyMemKeyRepo {
	return &apiKeyMemKeyRepo{
		byID:    make(map[uuid.UUID]*model.MerchantAPIKey),
		byKeyID: make(map[string]*model.MerchantAPIKey),
	}
}

func (r *apiKeyMemKeyRepo) clone(k *model.MerchantAPIKey) *model.MerchantAPIKey {
	cp := *k
	if k.ExpiresAt != nil {
		t := *k.ExpiresAt
		cp.ExpiresAt = &t
	}
	if k.RevokedAt != nil {
		t := *k.RevokedAt
		cp.RevokedAt = &t
	}
	if k.LastUsedAt != nil {
		t := *k.LastUsedAt
		cp.LastUsedAt = &t
	}
	return &cp
}

func (r *apiKeyMemKeyRepo) Create(_ context.Context, key *model.MerchantAPIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byKeyID[key.KeyID]; ok {
		return errors.New("duplicate key_id")
	}
	stored := r.clone(key)
	r.byID[key.ID] = stored
	r.byKeyID[key.KeyID] = stored
	return nil
}
func (r *apiKeyMemKeyRepo) CreateInTx(ctx context.Context, _ pgx.Tx, key *model.MerchantAPIKey) error {
	return r.Create(ctx, key)
}
func (r *apiKeyMemKeyRepo) GetByID(_ context.Context, merchantID, id uuid.UUID) (*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.MerchantID != merchantID {
		return nil, repository.ErrMerchantAPIKeyNotFound
	}
	return r.clone(k), nil
}
func (r *apiKeyMemKeyRepo) GetByKeyID(_ context.Context, keyID string) (*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byKeyID[keyID]
	if !ok {
		return nil, repository.ErrMerchantAPIKeyNotFound
	}
	return r.clone(k), nil
}
func (r *apiKeyMemKeyRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID) ([]*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.MerchantAPIKey
	for _, k := range r.byID {
		if k.MerchantID == merchantID {
			out = append(out, r.clone(k))
		}
	}
	return out, nil
}
func (r *apiKeyMemKeyRepo) Revoke(_ context.Context, merchantID, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.MerchantID != merchantID {
		return repository.ErrMerchantAPIKeyNotFound
	}
	if k.Status == model.MerchantAPIKeyStatusRevoked {
		return repository.ErrMerchantAPIKeyAlreadyRevoked
	}
	now := time.Now().UTC()
	k.Status = model.MerchantAPIKeyStatusRevoked
	k.RevokedAt = &now
	k.UpdatedAt = now
	return nil
}
func (r *apiKeyMemKeyRepo) Rotate(_ context.Context, merchantID, oldID uuid.UUID, newKey *model.MerchantAPIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.byID[oldID]
	if !ok || old.MerchantID != merchantID || old.Status != model.MerchantAPIKeyStatusActive {
		return repository.ErrMerchantAPIKeyNotFound
	}
	now := time.Now().UTC()
	old.Status = model.MerchantAPIKeyStatusRevoked
	old.RevokedAt = &now
	old.UpdatedAt = now
	stored := r.clone(newKey)
	r.byID[newKey.ID] = stored
	r.byKeyID[newKey.KeyID] = stored
	return nil
}
func (r *apiKeyMemKeyRepo) UpdateLastUsedAt(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok {
		return repository.ErrMerchantAPIKeyNotFound
	}
	now := time.Now().UTC()
	k.LastUsedAt = &now
	k.UpdatedAt = now
	return nil
}

// ─── Router factory ───────────────────────────────────────────────────────────

type apiKeyTestDeps struct {
	router   *gin.Engine
	merchant *model.Merchant
	other    *model.Merchant
	legacy   string
	keySvc   service.MerchantAPIKeyService
}

func newAPIKeyTestRouter(t *testing.T) *apiKeyTestDeps {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mRepo := newAPIKeyMemMerchantRepo()
	kRepo := newAPIKeyMemKeyRepo()

	legacyA := "pk_legacy_alpha_" + uuid.New().String()
	merchantA := &model.Merchant{
		ID: uuid.New(), Name: "Alpha", Code: "ALPHA",
		APIKey: legacyA, Status: model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateLegacy,
		CreatedAt:             time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	legacyB := "pk_legacy_beta_" + uuid.New().String()
	merchantB := &model.Merchant{
		ID: uuid.New(), Name: "Beta", Code: "BETA",
		APIKey: legacyB, Status: model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateLegacy,
		CreatedAt:             time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = mRepo.Create(context.Background(), merchantA)
	_ = mRepo.Create(context.Background(), merchantB)

	merchantSvc := service.NewMerchantService(mRepo)
	keySvc := service.NewMerchantAPIKeyService(kRepo, mRepo)
	h := handler.NewMerchantAPIKeyHandler(keySvc)

	r := gin.New()
	r.Use(middleware.RequestID())
	// Mirror production routing: share :id with GET /merchants/:id; key uses :key_id.
	merchants := r.Group("/api/v1/merchants")
	apiKeys := merchants.Group("/:id/api-keys")
	apiKeys.Use(middleware.Auth(merchantSvc, keySvc, true))
	{
		apiKeys.POST("", h.CreateAPIKey)
		apiKeys.GET("", h.ListAPIKeys)
		apiKeys.DELETE("/:key_id", h.RevokeAPIKey)
		apiKeys.POST("/:key_id/rotate", h.RotateAPIKey)
	}

	return &apiKeyTestDeps{
		router:   r,
		merchant: merchantA,
		other:    merchantB,
		legacy:   legacyA,
		keySvc:   keySvc,
	}
}

func apiKeyDo(r *gin.Engine, method, url, apiKey string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func apiKeyPath(merchantID uuid.UUID) string {
	return "/api/v1/merchants/" + merchantID.String() + "/api-keys"
}

// ─── Create ───────────────────────────────────────────────────────────────────

func TestCreateAPIKeyHandler_Success(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{
		"name": "Production Backend",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	if getStr(t, body, "data", "secret") == "" {
		t.Error("secret must be returned on create")
	}
	if !strings.HasPrefix(getStr(t, body, "data", "key_id"), "pk_") {
		t.Error("expected pk_ key_id")
	}
	if getStr(t, body, "meta", "request_id") == "" {
		t.Error("missing request_id")
	}
	if _, has := body["data"].(map[string]any)["secret_hash"]; has {
		t.Error("secret_hash must not appear in response")
	}
}

func TestCreateAPIKeyHandler_InvalidMerchantID(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "POST", "/api/v1/merchants/not-a-uuid/api-keys", d.legacy, map[string]any{"name": "X"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreateAPIKeyHandler_InvalidBody(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeValidationError) {
		t.Error("expected VALIDATION_ERROR")
	}
}

func TestCreateAPIKeyHandler_PastExpiration(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{
		"name": "Old", "expires_at": past,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d\nbody: %s", w.Code, w.Body)
	}
}

func TestCreateAPIKeyHandler_Unauthorized(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), "", map[string]any{"name": "X"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestCreateAPIKeyHandler_CrossMerchant(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	// Authenticated as Alpha, targeting Beta's path.
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.other.ID), d.legacy, map[string]any{"name": "Hack"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeForbidden) {
		t.Error("expected FORBIDDEN")
	}
}

// ─── List ─────────────────────────────────────────────────────────────────────

func TestListAPIKeysHandler_SuccessAndHidesSecrets(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	create := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "L1"})
	if create.Code != http.StatusCreated {
		t.Fatalf("create failed: %d", create.Code)
	}

	w := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), d.legacy, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
	body := parseBody(t, w)
	data, ok := body["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected 1 key, got %#v", body["data"])
	}
	item := data[0].(map[string]any)
	if _, has := item["secret"]; has {
		t.Error("list must not include secret")
	}
	if _, has := item["secret_hash"]; has {
		t.Error("list must not include secret_hash")
	}
	if item["key_id"] == nil || item["key_id"] == "" {
		t.Error("key_id should be present")
	}
}

func TestListAPIKeysHandler_Empty(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), d.legacy, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data := parseBody(t, w)["data"].([]any)
	if len(data) != 0 {
		t.Errorf("expected empty list, got %d", len(data))
	}
}

func TestListAPIKeysHandler_CrossMerchant(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "GET", apiKeyPath(d.other.ID), d.legacy, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestListAPIKeysHandler_Unauthorized(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// ─── Revoke ───────────────────────────────────────────────────────────────────

func TestRevokeAPIKeyHandler_Success(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	created := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "R"})
	id := getStr(t, parseBody(t, created), "data", "id")
	secret := getStr(t, parseBody(t, created), "data", "secret")
	keyID := getStr(t, parseBody(t, created), "data", "key_id")

	w := apiKeyDo(d.router, "DELETE", apiKeyPath(d.merchant.ID)+"/"+id, d.legacy, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body)
	}

	// New-style credential must now fail auth against a protected endpoint.
	cred := keyID + ":" + secret
	w2 := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), cred, nil)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key must 401, got %d", w2.Code)
	}
}

func TestRevokeAPIKeyHandler_NotFound(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "DELETE", apiKeyPath(d.merchant.ID)+"/"+uuid.New().String(), d.legacy, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeAPIKeyNotFound) {
		t.Error("expected API_KEY_NOT_FOUND")
	}
}

func TestRevokeAPIKeyHandler_AlreadyRevoked(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	created := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "R2"})
	id := getStr(t, parseBody(t, created), "data", "id")
	_ = apiKeyDo(d.router, "DELETE", apiKeyPath(d.merchant.ID)+"/"+id, d.legacy, nil)

	w := apiKeyDo(d.router, "DELETE", apiKeyPath(d.merchant.ID)+"/"+id, d.legacy, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d\nbody: %s", w.Code, w.Body)
	}
}

func TestRevokeAPIKeyHandler_CrossMerchant(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	created := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "RX"})
	id := getStr(t, parseBody(t, created), "data", "id")

	// Beta tries to revoke Alpha's key.
	w := apiKeyDo(d.router, "DELETE", apiKeyPath(d.other.ID)+"/"+id, d.other.APIKey, nil)
	// Path merchant is Beta (authenticated), but key belongs to Alpha → not found
	// OR Alpha's path with Beta auth → forbidden. Using Beta path + Beta auth + Alpha key id.
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for foreign key id, got %d\nbody: %s", w.Code, w.Body)
	}
}

// ─── Rotate ───────────────────────────────────────────────────────────────────

func TestRotateAPIKeyHandler_Success(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	created := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "Rot"})
	body := parseBody(t, created)
	id := getStr(t, body, "data", "id")
	oldSecret := getStr(t, body, "data", "secret")
	oldKeyID := getStr(t, body, "data", "key_id")

	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID)+"/"+id+"/rotate", d.legacy, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}
	rot := parseBody(t, w)
	newSecret := getStr(t, rot, "data", "secret")
	newKeyID := getStr(t, rot, "data", "key_id")
	if newSecret == "" || newSecret == oldSecret {
		t.Error("rotate must return a new one-time secret")
	}
	if newKeyID == oldKeyID {
		t.Error("rotate must return a new key_id")
	}

	// Old credential fails.
	oldCred := oldKeyID + ":" + oldSecret
	if wOld := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), oldCred, nil); wOld.Code != http.StatusUnauthorized {
		t.Fatalf("old key must 401, got %d", wOld.Code)
	}
	// New credential works.
	newCred := newKeyID + ":" + newSecret
	if wNew := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), newCred, nil); wNew.Code != http.StatusOK {
		t.Fatalf("new key must 200, got %d\nbody: %s", wNew.Code, wNew.Body)
	}
}

func TestRotateAPIKeyHandler_NotFound(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID)+"/"+uuid.New().String()+"/rotate", d.legacy, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRotateAPIKeyHandler_Unauthorized(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	w := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID)+"/"+uuid.New().String()+"/rotate", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRotateAPIKeyHandler_CrossMerchant(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	created := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "RotX"})
	id := getStr(t, parseBody(t, created), "data", "id")

	w := apiKeyDo(d.router, "POST", apiKeyPath(d.other.ID)+"/"+id+"/rotate", d.other.APIKey, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d\nbody: %s", w.Code, w.Body)
	}
}

func TestCreateAPIKeyHandler_ThenAuthWithNewKey(t *testing.T) {
	d := newAPIKeyTestRouter(t)
	created := apiKeyDo(d.router, "POST", apiKeyPath(d.merchant.ID), d.legacy, map[string]any{"name": "Auth"})
	body := parseBody(t, created)
	cred := getStr(t, body, "data", "key_id") + ":" + getStr(t, body, "data", "secret")

	w := apiKeyDo(d.router, "GET", apiKeyPath(d.merchant.ID), cred, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("new key auth expected 200, got %d\nbody: %s", w.Code, w.Body)
	}
}
