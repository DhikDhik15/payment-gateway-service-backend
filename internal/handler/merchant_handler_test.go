package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5"
)

// ─── In-memory merchant repository ───────────────────────────────────────────

type memMerchantRepo struct {
	byID     map[uuid.UUID]*model.Merchant
	byAPIKey map[string]*model.Merchant
	codes    map[string]bool
}

func newMemMerchantRepo() *memMerchantRepo {
	return &memMerchantRepo{
		byID:     make(map[uuid.UUID]*model.Merchant),
		byAPIKey: make(map[string]*model.Merchant),
		codes:    make(map[string]bool),
	}
}

func (r *memMerchantRepo) Create(_ context.Context, m *model.Merchant) error {
	r.byID[m.ID] = m
	r.byAPIKey[m.APIKey] = m
	r.codes[m.Code] = true
	return nil
}

func (r *memMerchantRepo) CreateInTx(ctx context.Context, _ pgx.Tx, m *model.Merchant) error {
	return r.Create(ctx, m)
}

func (r *memMerchantRepo) GetByID(_ context.Context, id uuid.UUID) (*model.Merchant, error) {
	m, ok := r.byID[id]
	if !ok {
		return nil, repository.ErrMerchantNotFound
	}
	return m, nil
}

func (r *memMerchantRepo) GetByAPIKey(_ context.Context, apiKey string) (*model.Merchant, error) {
	m, ok := r.byAPIKey[apiKey]
	if !ok {
		return nil, repository.ErrMerchantNotFound
	}
	return m, nil
}

func (r *memMerchantRepo) ExistsByCode(_ context.Context, code string) (bool, error) {
	return r.codes[code], nil
}

func (r *memMerchantRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ model.MerchantStatus) error {
	return nil
}

// ─── erroring repo — forces service to return an internal error ──────────────

type errorMerchantRepo struct{ memMerchantRepo }

func (r *errorMerchantRepo) ExistsByCode(_ context.Context, _ string) (bool, error) {
	return false, errors.New("db exploded")
}

// ─── test router factory ──────────────────────────────────────────────────────

func newMerchantTestRouter(t *testing.T, repo repository.MerchantRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	svc := service.NewMerchantService(repo)
	h := handler.NewMerchantHandler(svc)

	r := gin.New()
	r.Use(middleware.RequestID())

	// POST create is admin-protected (matches production wiring).
	r.POST("/api/v1/merchants", middleware.AdminAuth("test-admin-key"), h.Create)
	r.GET("/api/v1/merchants/:id", h.GetByID)
	return r
}

func doMerchantAdminRequest(r *gin.Engine, method, url string, body any) *httptest.ResponseRecorder {
	return doMerchantRequest(r, method, url, body, map[string]string{"X-Admin-Key": "test-admin-key"})
}

func doMerchantRequest(r *gin.Engine, method, url string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── POST /api/v1/merchants ───────────────────────────────────────────────────

// TestCreateMerchantHandler_Frozen_CreationDisabled is the Phase 8D.3 freeze
// test: creation of new legacy plaintext credentials always fails with
// 409 LEGACY_CREDENTIAL_CREATION_DISABLED and writes nothing.
func TestCreateMerchantHandler_Frozen_CreationDisabled(t *testing.T) {
	repo := newMemMerchantRepo()
	r := newMerchantTestRouter(t, repo)

	w := doMerchantAdminRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko Maju",
		"code": "TOKO001",
	})

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (frozen), got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)
	if body["success"] != false {
		t.Error("expected success:false")
	}
	if getStr(t, body, "error", "code") != string(response.CodeLegacyCredentialCreationDisabled) {
		t.Errorf("expected LEGACY_CREDENTIAL_CREATION_DISABLED, got %s",
			getStr(t, body, "error", "code"))
	}
	// meta.request_id must still be present on error envelopes.
	if getStr(t, body, "meta", "request_id") == "" {
		t.Error("expected non-empty request_id in meta")
	}
	// Nothing may be persisted: the freeze short-circuits before any write.
	if exists, err := repo.ExistsByCode(context.Background(), "TOKO001"); err != nil || exists {
		t.Errorf("expected no merchant row created (exists=%v err=%v)", exists, err)
	}
	// No credential material of any kind may leak back to the client.
	raw := w.Body.String()
	for _, leak := range []string{"api_key", "api_secret", "pk_", "sk_"} {
		if strings.Contains(raw, leak) {
			t.Errorf("frozen response must not contain %q: %s", leak, raw)
		}
	}
}

// TestCreateMerchantHandler_Frozen_PrecedesDuplicateCheck proves the freeze is
// evaluated BEFORE the duplicate-code check: an existing code yields the stable
// freeze error, never DUPLICATE_MERCHANT_CODE.
func TestCreateMerchantHandler_Frozen_PrecedesDuplicateCheck(t *testing.T) {
	repo := newMemMerchantRepo()
	r := newMerchantTestRouter(t, repo)

	// Pre-seed a merchant so the code is already taken.
	seeded := &model.Merchant{
		ID: uuid.New(), Name: "Existing", Code: "DUP001",
		APIKey: "pk_existing", Status: model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateLegacy,
	}
	if err := repo.Create(context.Background(), seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := doMerchantAdminRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko A", "code": "DUP001",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeLegacyCredentialCreationDisabled) {
		t.Errorf("expected LEGACY_CREDENTIAL_CREATION_DISABLED (freeze first), got %s", code)
	}
}

// TestCreateMerchantHandler_Frozen_PrecedesRepoAccess proves the freeze
// short-circuits before touching the repository at all — a repo that errors
// on every call still produces 409, never 500.
func TestCreateMerchantHandler_Frozen_PrecedesRepoAccess(t *testing.T) {
	r := newMerchantTestRouter(t, &errorMerchantRepo{})

	w := doMerchantAdminRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko Error",
		"code": "ERR001",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (freeze short-circuits), got %d\nbody: %s", w.Code, w.Body)
	}
	if code := getStr(t, parseBody(t, w), "error", "code"); code != string(response.CodeLegacyCredentialCreationDisabled) {
		t.Errorf("expected LEGACY_CREDENTIAL_CREATION_DISABLED, got %s", code)
	}
}

func TestCreateMerchantHandler_ValidationError_MissingName(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doMerchantAdminRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"code": "TOKO002",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	body := parseBody(t, w)
	if getStr(t, body, "error", "code") != string(response.CodeValidationError) {
		t.Errorf("expected VALIDATION_ERROR, got %s", getStr(t, body, "error", "code"))
	}
}

func TestCreateMerchantHandler_ValidationError_MissingCode(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doMerchantAdminRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko Maju",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeValidationError) {
		t.Error("expected VALIDATION_ERROR")
	}
}

func TestCreateMerchantHandler_ValidationError_ShortName(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doMerchantAdminRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "X", // min=2
		"code": "TOKO003",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeValidationError) {
		t.Error("expected VALIDATION_ERROR for short name")
	}
}

func TestCreateMerchantHandler_Unauthorized(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doMerchantRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko Maju",
		"code": "TOKOUNAUTH",
	}, nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeAdminUnauthorized) {
		t.Error("expected ADMIN_UNAUTHORIZED")
	}
}

// ─── GET /api/v1/merchants/:id ────────────────────────────────────────────────

func TestGetMerchantHandler_Success(t *testing.T) {
	repo := newMemMerchantRepo()
	r := newMerchantTestRouter(t, repo)

	// Seed directly: POST /merchants is frozen since Phase 8D.3, so the
	// repository is the only way to place a merchant row in the fixture.
	now := time.Now().UTC()
	seeded := &model.Merchant{
		ID: uuid.New(), Name: "Warung Jaya", Code: "WJ001",
		APIKey: "pk_wj001", APISecret: "hashed",
		Status:                model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateLegacy,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := repo.Create(context.Background(), seeded); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	idStr := seeded.ID.String()

	// Fetch by ID.
	wGet := doRequest(r, "GET", "/api/v1/merchants/"+idStr, nil)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", wGet.Code, wGet.Body)
	}

	body := parseBody(t, wGet)
	if getStr(t, body, "data", "id") != idStr {
		t.Error("id mismatch in GET response")
	}
	if getStr(t, body, "data", "name") != "Warung Jaya" {
		t.Errorf("name mismatch: got %s", getStr(t, body, "data", "name"))
	}
	if getStr(t, body, "data", "code") != "WJ001" {
		t.Errorf("code mismatch: got %s", getStr(t, body, "data", "code"))
	}
	// Phase 8D.3: the migration state IS exposed (it is a label, not a secret).
	if getStr(t, body, "data", "legacy_credential_state") != string(model.LegacyCredentialStateLegacy) {
		t.Errorf("expected legacy_credential_state LEGACY, got %s",
			getStr(t, body, "data", "legacy_credential_state"))
	}
	// api_key / api_secret must NEVER appear in the GET response.
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %v", body["data"])
	}
	for _, secret := range []string{"api_key", "api_secret", "secret", "key_id"} {
		if _, has := data[secret]; has {
			t.Errorf("%q must not be returned by GET /merchants/:id", secret)
		}
	}
	// Belt and braces: no raw credential material anywhere in the payload.
	raw := wGet.Body.String()
	if strings.Contains(raw, "pk_wj001") || strings.Contains(raw, "hashed") {
		t.Errorf("GET response leaks credential material: %s", raw)
	}
}

func TestGetMerchantHandler_NotFound(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doRequest(r, "GET", "/api/v1/merchants/"+uuid.New().String(), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeMerchantNotFound) {
		t.Error("expected MERCHANT_NOT_FOUND")
	}
}

func TestGetMerchantHandler_InvalidUUID(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doRequest(r, "GET", "/api/v1/merchants/not-a-uuid", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInvalidRequest) {
		t.Error("expected INVALID_REQUEST")
	}
}
