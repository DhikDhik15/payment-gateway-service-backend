package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// MerchantService defines the business operations for merchants.
type MerchantService interface {
	CreateMerchant(ctx context.Context, req model.CreateMerchantRequest) (*model.CreateMerchantResponse, error)
	GetMerchant(ctx context.Context, id uuid.UUID) (*model.GetMerchantResponse, error)
	GetMerchantByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error)
}

// ─── Service errors ──────────────────────────────────────────────────────────

// ErrDuplicateMerchantCode is returned when a merchant with the same code exists.
var ErrDuplicateMerchantCode = errors.New("merchant code already exists")

// ─── Implementation ──────────────────────────────────────────────────────────

type merchantService struct {
	repo repository.MerchantRepository
}

// NewMerchantService constructs a MerchantService backed by the given repository.
func NewMerchantService(repo repository.MerchantRepository) MerchantService {
	return &merchantService{repo: repo}
}

// CreateMerchant registers a new merchant, generating a unique API key and
// a hashed API secret. The plain-text secret is returned only at creation time
// and is never stored or returned again.
func (s *merchantService) CreateMerchant(ctx context.Context, req model.CreateMerchantRequest) (*model.CreateMerchantResponse, error) {
	// Check for duplicate code.
	exists, err := s.repo.ExistsByCode(ctx, req.Code)
	if err != nil {
		return nil, fmt.Errorf("merchant service create: check duplicate: %w", err)
	}
	if exists {
		return nil, ErrDuplicateMerchantCode
	}

	// Generate credentials.
	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("merchant service create: generate api key: %w", err)
	}

	apiSecret, hashedSecret, err := generateAPISecret()
	if err != nil {
		return nil, fmt.Errorf("merchant service create: generate api secret: %w", err)
	}

	// — The plain-text apiSecret is intentionally discarded after this point.
	// — Only the hashed version is stored.
	_ = apiSecret

	now := time.Now().UTC()
	m := &model.Merchant{
		ID:        uuid.New(),
		Name:      req.Name,
		Code:      req.Code,
		APIKey:    apiKey,
		APISecret: hashedSecret,
		Status:    model.MerchantStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Create(ctx, m); err != nil {
		return nil, fmt.Errorf("merchant service create: persist: %w", err)
	}

	slog.Info("merchant created",
		slog.String("merchant_id", m.ID.String()),
		slog.String("merchant_code", m.Code),
	)

	return &model.CreateMerchantResponse{
		ID:        m.ID,
		Name:      m.Name,
		Code:      m.Code,
		APIKey:    m.APIKey,
		Status:    m.Status,
		CreatedAt: m.CreatedAt,
	}, nil
}

// GetMerchant returns public merchant information by ID.
// api_key and api_secret are never included in the response.
func (s *merchantService) GetMerchant(ctx context.Context, id uuid.UUID) (*model.GetMerchantResponse, error) {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("merchant service get: %w", err)
	}

	return &model.GetMerchantResponse{
		ID:        m.ID,
		Name:      m.Name,
		Code:      m.Code,
		Status:    m.Status,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

// GetMerchantByAPIKey looks up a merchant by API key.
// Used by the authentication middleware.
func (s *merchantService) GetMerchantByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error) {
	m, err := s.repo.GetByAPIKey(ctx, apiKey)
	if err != nil {
		return nil, err // propagate ErrMerchantNotFound as-is
	}
	return m, nil
}

// ─── Credential generation ───────────────────────────────────────────────────

const (
	apiKeyPrefix    = "pk_"
	apiSecretPrefix = "sk_"
	credentialBytes = 32 // 256-bit entropy
)

// generateAPIKey creates a public API key of the form "pk_<hex>".
func generateAPIKey() (string, error) {
	b := make([]byte, credentialBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return apiKeyPrefix + hex.EncodeToString(b), nil
}

// generateAPISecret creates a random secret and returns both the plain-text
// version (to hand to the merchant once) and its SHA-256 hash (to store).
func generateAPISecret() (plaintext, hashed string, err error) {
	b := make([]byte, credentialBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate api secret: %w", err)
	}
	plaintext = apiSecretPrefix + hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(plaintext))
	hashed = hex.EncodeToString(sum[:])
	return plaintext, hashed, nil
}
