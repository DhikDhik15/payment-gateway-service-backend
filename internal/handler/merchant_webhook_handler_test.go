package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// stubWebhookCfgSvc is a minimal MerchantWebhookConfigService for isolation tests.
type stubWebhookCfgSvc struct {
	owner     uuid.UUID
	cfg       *model.MerchantWebhookConfigResponse
	upsertErr error // when set, Upsert returns it (error-mapping tests)
}

func (s *stubWebhookCfgSvc) Upsert(_ context.Context, merchantID uuid.UUID, _ model.UpsertMerchantWebhookRequest) (*model.MerchantWebhookConfigWithSecretResponse, error) {
	if merchantID != s.owner {
		return nil, repository.ErrMerchantNotFound
	}
	if s.upsertErr != nil {
		return nil, s.upsertErr
	}
	return &model.MerchantWebhookConfigWithSecretResponse{
		ID: uuid.New(), MerchantID: merchantID, URL: "https://example.com/hook",
		Status: model.MerchantWebhookConfigStatusActive, Secret: "whsec_once",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil
}
func (s *stubWebhookCfgSvc) Get(_ context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigResponse, error) {
	if merchantID != s.owner {
		return nil, service.ErrWebhookConfigNotFound
	}
	return s.cfg, nil
}
func (s *stubWebhookCfgSvc) RotateSecret(context.Context, uuid.UUID) (*model.MerchantWebhookConfigWithSecretResponse, error) {
	return nil, service.ErrWebhookConfigNotFound
}
func (s *stubWebhookCfgSvc) Disable(context.Context, uuid.UUID) (*model.MerchantWebhookConfigResponse, error) {
	return nil, service.ErrWebhookConfigNotFound
}
func (s *stubWebhookCfgSvc) ListDeliveries(context.Context, uuid.UUID, int, int) ([]model.MerchantWebhookDeliveryResponse, int64, error) {
	return nil, 0, nil
}
func (s *stubWebhookCfgSvc) GetDelivery(context.Context, uuid.UUID, uuid.UUID) (*model.MerchantWebhookDeliveryResponse, error) {
	return nil, service.ErrWebhookDeliveryNotFound
}
func (s *stubWebhookCfgSvc) RetryDelivery(context.Context, uuid.UUID, uuid.UUID) (*model.MerchantWebhookDeliveryResponse, error) {
	return nil, service.ErrWebhookDeliveryNotFound
}

func TestMerchantWebhookHandler_ForbiddenCrossMerchant(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	m := &model.Merchant{ID: owner, APIKey: "pk_test", Status: model.MerchantStatusActive, LegacyCredentialState: model.LegacyCredentialStateLegacy}
	cfgSvc := &stubWebhookCfgSvc{
		owner: owner,
		cfg: &model.MerchantWebhookConfigResponse{
			ID: uuid.New(), MerchantID: owner, URL: "https://example.com/hook",
			Status: model.MerchantWebhookConfigStatusActive,
		},
	}
	h := handler.NewMerchantWebhookHandler(cfgSvc)
	r := gin.New()
	r.Use(middleware.RequestID())
	merchantSvc := &stubMerchantSvc{merchant: m}
	grp := r.Group("/api/v1/merchants/:id/webhook")
	grp.Use(middleware.Auth(merchantSvc, nil, true))
	grp.GET("", h.GetWebhook)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/merchants/"+other.String()+"/webhook", nil)
	req.Header.Set("X-API-Key", "pk_test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMerchantWebhookHandler_CreateReturnsSecretOnce(t *testing.T) {
	owner := uuid.New()
	m := &model.Merchant{ID: owner, APIKey: "pk_test", Status: model.MerchantStatusActive, LegacyCredentialState: model.LegacyCredentialStateLegacy}
	cfgSvc := &stubWebhookCfgSvc{owner: owner}
	h := handler.NewMerchantWebhookHandler(cfgSvc)
	r := gin.New()
	r.Use(middleware.RequestID())
	grp := r.Group("/api/v1/merchants/:id/webhook")
	grp.Use(middleware.Auth(&stubMerchantSvc{merchant: m}, nil, true))
	grp.POST("", h.UpsertWebhook)

	body := []byte(`{"url":"https://merchant.example.com/webhooks/payment"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/merchants/"+owner.String()+"/webhook", bytes.NewReader(body))
	req.Header.Set("X-API-Key", "pk_test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Data["secret"] != "whsec_once" {
		t.Fatalf("expected secret once, got %#v", env.Data["secret"])
	}
}

// Phase 8D.2: service-level destination rejections map to the stable
// WEBHOOK_DESTINATION_BLOCKED error code (400), without network detail.
func TestMerchantWebhookHandler_DestinationBlockedMapsToStableCode(t *testing.T) {
	owner := uuid.New()
	m := &model.Merchant{ID: owner, APIKey: "pk_test", Status: model.MerchantStatusActive, LegacyCredentialState: model.LegacyCredentialStateLegacy}
	cfgSvc := &stubWebhookCfgSvc{owner: owner, upsertErr: service.ErrWebhookDestinationBlocked}
	h := handler.NewMerchantWebhookHandler(cfgSvc)
	r := gin.New()
	r.Use(middleware.RequestID())
	grp := r.Group("/api/v1/merchants/:id/webhook")
	grp.Use(middleware.Auth(&stubMerchantSvc{merchant: m}, nil, true))
	grp.POST("", h.UpsertWebhook)

	body := []byte(`{"url":"http://169.254.169.254/latest/meta-data"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/merchants/"+owner.String()+"/webhook", bytes.NewReader(body))
	req.Header.Set("X-API-Key", "pk_test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Success || env.Error.Code != "WEBHOOK_DESTINATION_BLOCKED" {
		t.Fatalf("envelope=%+v, want success=false code=WEBHOOK_DESTINATION_BLOCKED", env)
	}
}

// Ensure CreateInTx signature stays compatible.
var _ repository.MerchantWebhookDeliveryRepository = (*noopDeliveryRepo)(nil)

type noopDeliveryRepo struct{}

func (noopDeliveryRepo) Create(context.Context, *model.MerchantWebhookDelivery) error { return nil }
func (noopDeliveryRepo) CreateInTx(context.Context, pgx.Tx, *model.MerchantWebhookDelivery) error {
	return nil
}
func (noopDeliveryRepo) FindByID(context.Context, uuid.UUID, uuid.UUID) (*model.MerchantWebhookDelivery, error) {
	return nil, repository.ErrMerchantWebhookDeliveryNotFound
}
func (noopDeliveryRepo) ListByMerchant(context.Context, uuid.UUID, int, int) ([]*model.MerchantWebhookDelivery, int64, error) {
	return nil, 0, nil
}
func (noopDeliveryRepo) ClaimPending(context.Context, int, time.Duration) ([]*model.MerchantWebhookDelivery, error) {
	return nil, nil
}
func (noopDeliveryRepo) MarkDelivered(context.Context, uuid.UUID, int, int) error { return nil }
func (noopDeliveryRepo) MarkRetry(context.Context, uuid.UUID, int, time.Time, *int, string) error {
	return nil
}
func (noopDeliveryRepo) MarkFailed(context.Context, uuid.UUID, int, *int, string) error { return nil }
func (noopDeliveryRepo) MarkDead(context.Context, uuid.UUID, int, *int, string) error   { return nil }
func (noopDeliveryRepo) ManualRetry(context.Context, uuid.UUID, uuid.UUID) (*model.MerchantWebhookDelivery, error) {
	return nil, repository.ErrMerchantWebhookDeliveryNotFound
}
