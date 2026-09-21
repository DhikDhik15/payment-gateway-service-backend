package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── Config service errors ────────────────────────────────────────────────────

var (
	ErrWebhookConfigNotFound       = errors.New("merchant webhook config not found")
	ErrWebhookInvalidURL           = errors.New("webhook url must be https")
	ErrWebhookDeliveryNotFound     = errors.New("merchant webhook delivery not found")
	ErrWebhookDeliveryNotRetryable = errors.New("delivery cannot be retried in its current status")
)

// MerchantWebhookConfigService manages merchant outbound webhook configuration.
type MerchantWebhookConfigService interface {
	Upsert(ctx context.Context, merchantID uuid.UUID, req model.UpsertMerchantWebhookRequest) (*model.MerchantWebhookConfigWithSecretResponse, error)
	Get(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigResponse, error)
	RotateSecret(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigWithSecretResponse, error)
	Disable(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigResponse, error)
	ListDeliveries(ctx context.Context, merchantID uuid.UUID, page, limit int) ([]model.MerchantWebhookDeliveryResponse, int64, error)
	GetDelivery(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDeliveryResponse, error)
	RetryDelivery(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDeliveryResponse, error)
}

type merchantWebhookConfigService struct {
	configRepo   repository.MerchantWebhookConfigRepository
	deliveryRepo repository.MerchantWebhookDeliveryRepository
	merchantRepo repository.MerchantRepository
	encKey       []byte
	requireHTTPS bool
}

// NewMerchantWebhookConfigService constructs the config/delivery management service.
// requireHTTPS should be true in production; false allows http:// for local tests.
func NewMerchantWebhookConfigService(
	configRepo repository.MerchantWebhookConfigRepository,
	deliveryRepo repository.MerchantWebhookDeliveryRepository,
	merchantRepo repository.MerchantRepository,
	encKey []byte,
	requireHTTPS bool,
) MerchantWebhookConfigService {
	return &merchantWebhookConfigService{
		configRepo:   configRepo,
		deliveryRepo: deliveryRepo,
		merchantRepo: merchantRepo,
		encKey:       encKey,
		requireHTTPS: requireHTTPS,
	}
}

func (s *merchantWebhookConfigService) Upsert(ctx context.Context, merchantID uuid.UUID, req model.UpsertMerchantWebhookRequest) (*model.MerchantWebhookConfigWithSecretResponse, error) {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, repository.ErrMerchantNotFound
		}
		return nil, fmt.Errorf("webhook upsert: merchant: %w", err)
	}
	if err := validateWebhookURL(req.URL, s.requireHTTPS); err != nil {
		return nil, err
	}

	plaintext, err := GenerateWebhookSecret()
	if err != nil {
		return nil, err
	}
	encrypted, err := EncryptWebhookSecret(s.encKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("webhook upsert: encrypt: %w", err)
	}

	now := time.Now().UTC()
	cfg := &model.MerchantWebhookConfig{
		ID:              uuid.New(),
		MerchantID:      merchantID,
		URL:             strings.TrimSpace(req.URL),
		EncryptedSecret: encrypted,
		Status:          model.MerchantWebhookConfigStatusActive,
		Description:     req.Description,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.configRepo.Upsert(ctx, cfg); err != nil {
		return nil, fmt.Errorf("webhook upsert: %w", err)
	}

	return &model.MerchantWebhookConfigWithSecretResponse{
		ID: cfg.ID, MerchantID: cfg.MerchantID, URL: cfg.URL,
		Status: cfg.Status, Description: cfg.Description,
		Secret: plaintext, CreatedAt: cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt,
	}, nil
}

func (s *merchantWebhookConfigService) Get(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigResponse, error) {
	cfg, err := s.configRepo.FindByMerchantID(ctx, merchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookConfigNotFound) {
			return nil, ErrWebhookConfigNotFound
		}
		return nil, err
	}
	return cfg.ToConfigResponse(), nil
}

func (s *merchantWebhookConfigService) RotateSecret(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigWithSecretResponse, error) {
	plaintext, err := GenerateWebhookSecret()
	if err != nil {
		return nil, err
	}
	encrypted, err := EncryptWebhookSecret(s.encKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("webhook rotate: encrypt: %w", err)
	}
	cfg, err := s.configRepo.UpdateSecret(ctx, merchantID, encrypted)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookConfigNotFound) {
			return nil, ErrWebhookConfigNotFound
		}
		return nil, err
	}
	return &model.MerchantWebhookConfigWithSecretResponse{
		ID: cfg.ID, MerchantID: cfg.MerchantID, URL: cfg.URL,
		Status: cfg.Status, Description: cfg.Description,
		Secret: plaintext, CreatedAt: cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt,
	}, nil
}

func (s *merchantWebhookConfigService) Disable(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfigResponse, error) {
	cfg, err := s.configRepo.Disable(ctx, merchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookConfigNotFound) {
			return nil, ErrWebhookConfigNotFound
		}
		return nil, err
	}
	return cfg.ToConfigResponse(), nil
}

func (s *merchantWebhookConfigService) ListDeliveries(ctx context.Context, merchantID uuid.UUID, page, limit int) ([]model.MerchantWebhookDeliveryResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit
	rows, total, err := s.deliveryRepo.ListByMerchant(ctx, merchantID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.MerchantWebhookDeliveryResponse, 0, len(rows))
	for _, d := range rows {
		out = append(out, *d.ToDeliveryResponse())
	}
	return out, total, nil
}

func (s *merchantWebhookConfigService) GetDelivery(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDeliveryResponse, error) {
	d, err := s.deliveryRepo.FindByID(ctx, merchantID, deliveryID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookDeliveryNotFound) {
			return nil, ErrWebhookDeliveryNotFound
		}
		return nil, err
	}
	return d.ToDeliveryResponse(), nil
}

func (s *merchantWebhookConfigService) RetryDelivery(ctx context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDeliveryResponse, error) {
	d, err := s.deliveryRepo.ManualRetry(ctx, merchantID, deliveryID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookDeliveryNotFound) {
			// Distinguish missing vs wrong status by probing existence.
			existing, findErr := s.deliveryRepo.FindByID(ctx, merchantID, deliveryID)
			if findErr != nil {
				return nil, ErrWebhookDeliveryNotFound
			}
			if existing.Status == model.MerchantWebhookDeliveryStatusDelivered ||
				existing.Status == model.MerchantWebhookDeliveryStatusProcessing {
				return nil, ErrWebhookDeliveryNotRetryable
			}
			return nil, ErrWebhookDeliveryNotFound
		}
		return nil, err
	}
	return d.ToDeliveryResponse(), nil
}

func validateWebhookURL(raw string, requireHTTPS bool) error {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ErrWebhookInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	if requireHTTPS {
		if scheme != "https" {
			return ErrWebhookInvalidURL
		}
	} else if scheme != "https" && scheme != "http" {
		return ErrWebhookInvalidURL
	}
	return nil
}

// ─── Outbox publisher ─────────────────────────────────────────────────────────

// MerchantWebhookPublisher enqueues outbound deliveries into the transactional outbox.
type MerchantWebhookPublisher interface {
	// Enqueue creates a PENDING delivery when an ACTIVE webhook is configured.
	// No-op when no config exists. Duplicate event_id is ignored idempotently.
	Enqueue(ctx context.Context, tx *model.Transaction, eventType model.MerchantWebhookEventType) error

	// EnqueueInTx is the same as Enqueue but joins an existing DB transaction (true outbox).
	EnqueueInTx(ctx context.Context, dbTx pgx.Tx, tx *model.Transaction, eventType model.MerchantWebhookEventType) error

	// BuildDelivery builds a delivery row without inserting (for atomic outbox helpers).
	BuildDelivery(ctx context.Context, tx *model.Transaction, eventType model.MerchantWebhookEventType) (*model.MerchantWebhookDelivery, error)

	// EnqueueRefundInTx enqueues a refund lifecycle event in an existing DB transaction.
	EnqueueRefundInTx(ctx context.Context, dbTx pgx.Tx, refund *model.Refund, tx *model.Transaction, eventType model.MerchantWebhookEventType) error
}

type merchantWebhookPublisher struct {
	configRepo   repository.MerchantWebhookConfigRepository
	deliveryRepo repository.MerchantWebhookDeliveryRepository
}

// NewMerchantWebhookPublisher constructs an outbox publisher.
func NewMerchantWebhookPublisher(
	configRepo repository.MerchantWebhookConfigRepository,
	deliveryRepo repository.MerchantWebhookDeliveryRepository,
) MerchantWebhookPublisher {
	return &merchantWebhookPublisher{configRepo: configRepo, deliveryRepo: deliveryRepo}
}

func (p *merchantWebhookPublisher) Enqueue(ctx context.Context, tx *model.Transaction, eventType model.MerchantWebhookEventType) error {
	d, err := p.BuildDelivery(ctx, tx, eventType)
	if err != nil || d == nil {
		return err
	}
	if err := p.deliveryRepo.Create(ctx, d); err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookDeliveryDuplicate) {
			return nil
		}
		return err
	}
	slog.Info("merchant webhook enqueued",
		slog.String("event_id", d.EventID),
		slog.String("event_type", string(eventType)),
		slog.String("transaction_id", tx.ID.String()),
	)
	return nil
}

func (p *merchantWebhookPublisher) EnqueueInTx(ctx context.Context, dbTx pgx.Tx, tx *model.Transaction, eventType model.MerchantWebhookEventType) error {
	d, err := p.BuildDelivery(ctx, tx, eventType)
	if err != nil || d == nil {
		return err
	}
	if err := p.deliveryRepo.CreateInTx(ctx, dbTx, d); err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookDeliveryDuplicate) {
			return nil
		}
		return err
	}
	return nil
}

func (p *merchantWebhookPublisher) EnqueueRefundInTx(ctx context.Context, dbTx pgx.Tx, refund *model.Refund, tx *model.Transaction, eventType model.MerchantWebhookEventType) error {
	d, err := p.BuildRefundDelivery(ctx, refund, tx, eventType)
	if err != nil || d == nil {
		return err
	}
	if err := p.deliveryRepo.CreateInTx(ctx, dbTx, d); err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookDeliveryDuplicate) {
			return nil
		}
		return err
	}
	return nil
}

func (p *merchantWebhookPublisher) BuildRefundDelivery(ctx context.Context, refund *model.Refund, tx *model.Transaction, eventType model.MerchantWebhookEventType) (*model.MerchantWebhookDelivery, error) {
	cfg, err := p.configRepo.FindActiveByMerchantID(ctx, refund.MerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookConfigNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("webhook publisher: load config: %w", err)
	}
	eventID, err := GenerateWebhookEventID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	refundData := model.RefundDataFromRefund(refund, tx)
	payload := map[string]any{
		"id":         eventID,
		"type":       eventType,
		"version":    model.MerchantWebhookPayloadVersion,
		"created_at": now,
		"data":       refundData,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webhook publisher: marshal refund payload: %w", err)
	}
	configID := cfg.ID
	return &model.MerchantWebhookDelivery{
		ID:            uuid.New(),
		MerchantID:    refund.MerchantID,
		ConfigID:      &configID,
		EventID:       eventID,
		EventType:     eventType,
		TransactionID: refund.TransactionID,
		EndpointURL:   cfg.URL,
		Payload:       raw,
		AttemptCount:  0,
		Status:        model.MerchantWebhookDeliveryStatusPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (p *merchantWebhookPublisher) BuildDelivery(ctx context.Context, tx *model.Transaction, eventType model.MerchantWebhookEventType) (*model.MerchantWebhookDelivery, error) {
	cfg, err := p.configRepo.FindActiveByMerchantID(ctx, tx.MerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantWebhookConfigNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("webhook publisher: load config: %w", err)
	}

	eventID, err := GenerateWebhookEventID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	payload := model.MerchantWebhookEventPayload{
		ID:        eventID,
		Type:      eventType,
		Version:   model.MerchantWebhookPayloadVersion,
		CreatedAt: now,
		Data:      model.PaymentDataFromTransaction(tx),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webhook publisher: marshal payload: %w", err)
	}
	configID := cfg.ID
	return &model.MerchantWebhookDelivery{
		ID:            uuid.New(),
		MerchantID:    tx.MerchantID,
		ConfigID:      &configID,
		EventID:       eventID,
		EventType:     eventType,
		TransactionID: tx.ID,
		EndpointURL:   cfg.URL,
		Payload:       raw,
		AttemptCount:  0,
		Status:        model.MerchantWebhookDeliveryStatusPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}
