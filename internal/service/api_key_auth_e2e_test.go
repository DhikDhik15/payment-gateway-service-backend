package service_test

// Phase 8D.1 end-to-end regression for merchant API keys (auth matrix rows:
// "Valid API key → accepted" / "Revoked API key → rejected").
//
// The same credential must authenticate BEFORE revocation and be rejected
// AFTER it; a credential issued to one merchant must not reach another
// merchant's resources; and suspending the merchant must block API access
// even while the key itself is unrevoked.
//
// This exercises the real MerchantAPIKeyService (Argon2id secret
// verification), the real Auth middleware, and the real MerchantAPIKeyHandler
// over in-memory repositories — no live database required. Credentials and
// one-time secrets are never logged or echoed in failure messages.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
)

// apiKeyE2EEnvelope is the standard success/error envelope (never logged raw:
// the create response carries the one-time secret).
type apiKeyE2EEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code string `json:"code"`
	} `json:"error"`
}

// newAPIKeyE2ERouter mirrors the main.go api-keys group: Auth middleware in
// front of the real MerchantAPIKeyHandler.
func newAPIKeyE2ERouter(keySvc service.MerchantAPIKeyService, merchantSvc service.MerchantService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	h := handler.NewMerchantAPIKeyHandler(keySvc)
	g := r.Group("/api/v1/merchants/:id/api-keys")
	g.Use(middleware.Auth(merchantSvc, keySvc, true))
	{
		g.POST("", h.CreateAPIKey)
		g.GET("", h.ListAPIKeys)
		g.DELETE("/:key_id", h.RevokeAPIKey)
		g.POST("/:key_id/rotate", h.RotateAPIKey)
	}
	return r
}

func apiKeyE2EDo(r *gin.Engine, method, url, credential, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var buf bytes.Buffer
	buf.WriteString(body)
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		panic(err)
	}
	if credential != "" {
		req.Header.Set("X-API-Key", credential)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	return w
}

func apiKeyE2ECode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env apiKeyE2EEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("parse error envelope (status %d): %v", w.Code, err)
	}
	return env.Error.Code
}

func TestAPIKeyE2E_RevocationTenantIsolationAndSuspension(t *testing.T) {
	ctx := context.Background()

	mRepo := newMemMerchantRepoAPIKey()
	kRepo := newMemKeyRepo()
	keySvc := service.NewMerchantAPIKeyService(kRepo, mRepo)
	merchantSvc := service.NewMerchantService(mRepo)

	merchantA := seedMerchant(t, mRepo)
	merchantB := seedMerchant(t, mRepo)

	// Bootstrap credential for merchant A — created out-of-band, exactly how
	// an operator provisions the first key.
	boot, err := keySvc.CreateKey(ctx, merchantA.ID, model.CreateMerchantAPIKeyRequest{Name: "e2e bootstrap"})
	if err != nil {
		t.Fatalf("create bootstrap key: %v", err)
	}
	bootCred := fullCredential(boot.KeyID, boot.Secret)

	// Merchant B's own credential.
	bKey, err := keySvc.CreateKey(ctx, merchantB.ID, model.CreateMerchantAPIKeyRequest{Name: "e2e merchant b"})
	if err != nil {
		t.Fatalf("create merchant B key: %v", err)
	}
	bCred := fullCredential(bKey.KeyID, bKey.Secret)

	r := newAPIKeyE2ERouter(keySvc, merchantSvc)
	urlA := "/api/v1/merchants/" + merchantA.ID.String() + "/api-keys"
	urlB := "/api/v1/merchants/" + merchantB.ID.String() + "/api-keys"

	// The second credential is created over the authenticated API and then
	// used for the before/after revocation comparison.
	var secondID string
	var secondCred string

	t.Run("valid key authenticates", func(t *testing.T) {
		w := apiKeyE2EDo(r, http.MethodGet, urlA, bootCred, "")
		if w.Code != http.StatusOK {
			t.Fatalf("list with valid key: got %d, want 200 (code=%s)", w.Code, apiKeyE2ECode(t, w))
		}
	})

	t.Run("second key created through authenticated API", func(t *testing.T) {
		w := apiKeyE2EDo(r, http.MethodPost, urlA, bootCred, `{"name":"e2e second"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create with valid key: got %d, want 201 (code=%s)", w.Code, apiKeyE2ECode(t, w))
		}
		var env apiKeyE2EEnvelope
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("parse create envelope: %v (body not dumped: it contains the one-time secret)", err)
		}
		var created model.CreateMerchantAPIKeyResponse
		if err := json.Unmarshal(env.Data, &created); err != nil {
			t.Fatalf("parse create data: %v", err)
		}
		if created.KeyID == "" || created.Secret == "" {
			t.Fatal("create response missing key_id/secret (not dumping body: it contains the one-time secret)")
		}
		secondID = created.ID.String()
		secondCred = fullCredential(created.KeyID, created.Secret)
	})

	t.Run("second key authenticates before revocation", func(t *testing.T) {
		if secondCred == "" {
			t.Fatal("second credential missing — previous subtest must run first")
		}
		w := apiKeyE2EDo(r, http.MethodGet, urlA, secondCred, "")
		if w.Code != http.StatusOK {
			t.Fatalf("list with second key before revocation: got %d, want 200 (code=%s)", w.Code, apiKeyE2ECode(t, w))
		}
	})

	t.Run("revoke second key", func(t *testing.T) {
		if secondID == "" {
			t.Fatal("second key id missing — previous subtest must run first")
		}
		w := apiKeyE2EDo(r, http.MethodDelete, urlA+"/"+secondID, bootCred, "")
		if w.Code < 200 || w.Code >= 300 {
			t.Fatalf("revoke with valid key: got %d, want 2xx (code=%s)", w.Code, apiKeyE2ECode(t, w))
		}
	})

	t.Run("revoked key is rejected on the same request it served before", func(t *testing.T) {
		if secondCred == "" {
			t.Fatal("second credential missing — previous subtest must run first")
		}
		w := apiKeyE2EDo(r, http.MethodGet, urlA, secondCred, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("list with revoked key: got %d, want 401", w.Code)
		}
		if got := apiKeyE2ECode(t, w); got != "INVALID_API_KEY" {
			t.Errorf("revoked key code: got %s, want INVALID_API_KEY", got)
		}
	})

	t.Run("another merchant's credential cannot access this merchant", func(t *testing.T) {
		w := apiKeyE2EDo(r, http.MethodGet, urlA, bCred, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("merchant B key against merchant A path: got %d, want 403", w.Code)
		}
		if got := apiKeyE2ECode(t, w); got != "FORBIDDEN" {
			t.Errorf("code: got %s, want FORBIDDEN", got)
		}
		// Merchant B's own access still works — the rejection above is
		// tenant isolation, not a broken credential.
		w = apiKeyE2EDo(r, http.MethodGet, urlB, bCred, "")
		if w.Code != http.StatusOK {
			t.Fatalf("merchant B key against merchant B path: got %d, want 200", w.Code)
		}
	})

	t.Run("suspending the merchant blocks API access with an unrevoked key", func(t *testing.T) {
		if err := mRepo.UpdateStatus(ctx, merchantA.ID, model.MerchantStatusSuspended); err != nil {
			t.Fatalf("suspend merchant A: %v", err)
		}
		w := apiKeyE2EDo(r, http.MethodGet, urlA, bootCred, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("list with valid key after suspension: got %d, want 401", w.Code)
		}
		if got := apiKeyE2ECode(t, w); got != "MERCHANT_INACTIVE" {
			t.Errorf("code: got %s, want MERCHANT_INACTIVE", got)
		}
	})
}
