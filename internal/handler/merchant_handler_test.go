package handler_test

import (
	"context"
	"errors"
	"net/http"
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

	r.POST("/api/v1/merchants", h.Create)
	r.GET("/api/v1/merchants/:id", h.GetByID)
	return r
}

// ─── POST /api/v1/merchants ───────────────────────────────────────────────────

func TestCreateMerchantHandler_Success(t *testing.T) {
	repo := newMemMerchantRepo()
	r := newMerchantTestRouter(t, repo)

	w := doRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko Maju",
		"code": "TOKO001",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d\nbody: %s", w.Code, w.Body)
	}

	body := parseBody(t, w)

	// Response envelope must signal success.
	if body["success"] != true {
		t.Error("expected success:true")
	}
	// data.api_key must be non-empty (returned only at creation time).
	if getStr(t, body, "data", "api_key") == "" {
		t.Error("expected non-empty api_key in response")
	}
	// data.id must be a valid UUID.
	idStr := getStr(t, body, "data", "id")
	if _, err := uuid.Parse(idStr); err != nil {
		t.Errorf("data.id is not a valid UUID: %s", idStr)
	}
	// data.status must be ACTIVE.
	if getStr(t, body, "data", "status") != string(model.MerchantStatusActive) {
		t.Errorf("expected status ACTIVE, got %s", getStr(t, body, "data", "status"))
	}
	// meta.request_id must be present.
	if getStr(t, body, "meta", "request_id") == "" {
		t.Error("expected non-empty request_id in meta")
	}
}

func TestCreateMerchantHandler_ValidationError_MissingName(t *testing.T) {
	r := newMerchantTestRouter(t, newMemMerchantRepo())

	w := doRequest(r, "POST", "/api/v1/merchants", map[string]any{
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

	w := doRequest(r, "POST", "/api/v1/merchants", map[string]any{
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

	w := doRequest(r, "POST", "/api/v1/merchants", map[string]any{
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

func TestCreateMerchantHandler_DuplicateCode(t *testing.T) {
	repo := newMemMerchantRepo()
	r := newMerchantTestRouter(t, repo)

	body := map[string]any{"name": "Toko A", "code": "DUP001"}

	// First registration must succeed.
	w1 := doRequest(r, "POST", "/api/v1/merchants", body)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d", w1.Code)
	}

	// Second registration with the same code must return 409.
	w2 := doRequest(r, "POST", "/api/v1/merchants", body)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate: expected 409, got %d\nbody: %s", w2.Code, w2.Body)
	}
	if getStr(t, parseBody(t, w2), "error", "code") != string(response.CodeDuplicateMerchantCode) {
		t.Error("expected DUPLICATE_MERCHANT_CODE")
	}
}

func TestCreateMerchantHandler_InternalError(t *testing.T) {
	// Use a repo that blows up on ExistsByCode.
	r := newMerchantTestRouter(t, &errorMerchantRepo{})

	w := doRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Toko Error",
		"code": "ERR001",
	})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d\nbody: %s", w.Code, w.Body)
	}
	if getStr(t, parseBody(t, w), "error", "code") != string(response.CodeInternalError) {
		t.Error("expected INTERNAL_ERROR")
	}
}

// ─── GET /api/v1/merchants/:id ────────────────────────────────────────────────

func TestGetMerchantHandler_Success(t *testing.T) {
	repo := newMemMerchantRepo()
	r := newMerchantTestRouter(t, repo)

	// Create a merchant first.
	w := doRequest(r, "POST", "/api/v1/merchants", map[string]any{
		"name": "Warung Jaya",
		"code": "WJ001",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d", w.Code)
	}
	idStr := getStr(t, parseBody(t, w), "data", "id")

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
	// api_key must NOT appear in the GET response (security requirement).
	if data, ok := body["data"].(map[string]any); ok {
		if _, hasKey := data["api_key"]; hasKey {
			t.Error("api_key must not be returned by GET /merchants/:id")
		}
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
