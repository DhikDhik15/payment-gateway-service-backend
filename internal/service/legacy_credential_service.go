package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Service errors ───────────────────────────────────────────────────────────

var (
	// ErrLegacyCredentialAlreadyMigrated is returned when migrate is called for
	// a merchant that is no longer in the LEGACY state (already MIGRATED or
	// LEGACY_DISABLED), including the losing side of a concurrent migrate.
	ErrLegacyCredentialAlreadyMigrated = errors.New("legacy credential already migrated")

	// ErrLegacyCredentialMigrationRequired is returned when disable is called
	// before the merchant has migrated to a Phase 5C credential.
	ErrLegacyCredentialMigrationRequired = errors.New("legacy credential migration required")

	// ErrLegacyCredentialCreationDisabled is returned by CreateMerchant: Phase
	// 8D.3 freezes creation of new legacy plaintext credentials permanently.
	ErrLegacyCredentialCreationDisabled = errors.New("legacy credential creation is disabled")
)

// migratedAPIKeyName labels the Phase 5C credential minted by the migration.
const migratedAPIKeyName = "Migrated API Key"

// ─── Interface ────────────────────────────────────────────────────────────────

// LegacyCredentialService exposes the Phase 8D.3 migration lifecycle to the
// dashboard: migrate a tenant off its row-level legacy plaintext credential,
// then disable it once the tenant is on the canonical Phase 5C key system.
type LegacyCredentialService interface {
	// Migrate creates the canonical Phase 5C credential for merchantID and
	// flips its state LEGACY → MIGRATED atomically. The plaintext secret is
	// returned exactly once and is never stored or logged.
	// Returns ErrLegacyCredentialAlreadyMigrated when the merchant is not in
	// the LEGACY state.
	Migrate(ctx context.Context, merchantID uuid.UUID) (*model.MigrateLegacyCredentialResponse, error)

	// Disable flips MIGRATED → LEGACY_DISABLED atomically so the legacy
	// plaintext credential can never authenticate again. It is idempotent:
	// repeating it returns AlreadyDisabled=true without rewriting the original
	// disabled_at timestamp.
	// Returns ErrLegacyCredentialMigrationRequired when still LEGACY.
	Disable(ctx context.Context, merchantID uuid.UUID) (*model.LegacyCredentialStatusResponse, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type legacyCredentialService struct {
	store repository.LegacyCredentialStore
}

// NewLegacyCredentialService constructs a LegacyCredentialService backed by store.
func NewLegacyCredentialService(store repository.LegacyCredentialStore) LegacyCredentialService {
	return &legacyCredentialService{store: store}
}

func (s *legacyCredentialService) Migrate(ctx context.Context, merchantID uuid.UUID) (*model.MigrateLegacyCredentialResponse, error) {
	// Reuse the Phase 5C generator: one key format for the whole system.
	// The plaintext secret exists only in this stack frame and the response.
	keyID, plaintextSecret, secretHash, err := generateAPIKeyPair()
	if err != nil {
		return nil, fmt.Errorf("migrate legacy credential: generate key pair: %w", err)
	}

	now := time.Now().UTC()
	key := &model.MerchantAPIKey{
		ID:         uuid.New(),
		MerchantID: merchantID, // always caller-derived — never client-supplied
		KeyID:      keyID,
		SecretHash: secretHash, // Argon2id — plaintext is never persisted
		Name:       migratedAPIKeyName,
		Status:     model.MerchantAPIKeyStatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionLegacyCredentialMigrated,
		audit.TargetLegacyCredential,
		nil,
		audit.UUIDPtr(merchantID),
		map[string]any{
			"credential_id":   key.ID,
			"credential_type": "LEGACY_CREDENTIAL",
			"key_id":          key.KeyID,
			"migration_state": string(model.LegacyCredentialStateMigrated),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("migrate legacy credential: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)
	if err := s.store.Migrate(ctx, merchantID, key); err != nil {
		switch {
		case errors.Is(err, repository.ErrLegacyCredentialAlreadyMigrated):
			return nil, ErrLegacyCredentialAlreadyMigrated
		case errors.Is(err, repository.ErrLegacyCredentialMerchantNotFound):
			return nil, repository.ErrMerchantNotFound
		default:
			return nil, fmt.Errorf("migrate legacy credential: %w", err)
		}
	}

	slog.Info("legacy credential migrated",
		slog.String("merchant_id", merchantID.String()),
		slog.String("key_id", keyID),
		// the plaintext secret is NEVER logged
	)

	return &model.MigrateLegacyCredentialResponse{
		ID:                    key.ID,
		MerchantID:            key.MerchantID,
		Name:                  key.Name,
		KeyID:                 keyID,
		Secret:                plaintextSecret, // one-time disclosure
		Status:                key.Status,
		CreatedAt:             key.CreatedAt,
		LegacyCredentialState: model.LegacyCredentialStateMigrated,
		LegacyDisabled:        false, // legacy key (if any) still works until disable
	}, nil
}

func (s *legacyCredentialService) Disable(ctx context.Context, merchantID uuid.UUID) (*model.LegacyCredentialStatusResponse, error) {
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionLegacyCredentialDisabled,
		audit.TargetLegacyCredential,
		nil,
		audit.UUIDPtr(merchantID),
		map[string]any{
			"credential_type": "LEGACY_CREDENTIAL",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("disable legacy credential: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)
	res, err := s.store.Disable(ctx, merchantID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrLegacyCredentialMigrationRequired):
			return nil, ErrLegacyCredentialMigrationRequired
		case errors.Is(err, repository.ErrLegacyCredentialMerchantNotFound):
			return nil, repository.ErrMerchantNotFound
		default:
			return nil, fmt.Errorf("disable legacy credential: %w", err)
		}
	}

	slog.Info("legacy credential disabled",
		slog.String("merchant_id", merchantID.String()),
		slog.Bool("already_disabled", res.AlreadyDisabled),
		// no credential material exists in this path
	)

	return &model.LegacyCredentialStatusResponse{
		LegacyCredentialState:      res.State,
		LegacyCredentialDisabledAt: res.DisabledAt,
		AlreadyDisabled:            res.AlreadyDisabled,
	}, nil
}
