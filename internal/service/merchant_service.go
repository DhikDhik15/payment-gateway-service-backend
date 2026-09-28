package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// MerchantService defines the business operations for merchants.
type MerchantService interface {
	CreateMerchant(ctx context.Context, req model.CreateMerchantRequest) (*model.CreateMerchantResponse, error)
	GetMerchant(ctx context.Context, id uuid.UUID) (*model.GetMerchantResponse, error)
	GetMerchantByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error)
	// UpdateMerchantStatus changes the lifecycle status of a merchant.
	// It validates the requested transition, persists the change, and revokes
	// all active dashboard sessions when the merchant becomes non-ACTIVE.
	// Returns ErrInvalidStatusTransition for disallowed transitions.
	// Returns repository.ErrMerchantNotFound when the merchant does not exist.
	UpdateMerchantStatus(ctx context.Context, merchantID uuid.UUID, newStatus model.MerchantStatus) (*model.GetMerchantResponse, error)
}

// ─── Service errors ──────────────────────────────────────────────────────────

// ErrDuplicateMerchantCode is returned when a merchant with the same code exists.
var ErrDuplicateMerchantCode = errors.New("merchant code already exists")

// ErrInvalidStatusTransition is returned when the requested status transition
// is not permitted by the merchant lifecycle rules.
var ErrInvalidStatusTransition = errors.New("invalid merchant status transition")

// ─── Implementation ──────────────────────────────────────────────────────────

type merchantService struct {
	repo        repository.MerchantRepository
	sessionRepo repository.DashboardSessionRepository
}

// NewMerchantService constructs a MerchantService backed by the given repository.
func NewMerchantService(repo repository.MerchantRepository) MerchantService {
	return &merchantService{repo: repo}
}

// NewMerchantServiceWithSessions constructs a MerchantService that can also
// revoke dashboard sessions when merchant status changes to non-ACTIVE.
func NewMerchantServiceWithSessions(repo repository.MerchantRepository, sessionRepo repository.DashboardSessionRepository) MerchantService {
	return &merchantService{repo: repo, sessionRepo: sessionRepo}
}

// CreateMerchant is FROZEN (Phase 8D.3).
//
// Creation of new row-level legacy plaintext credentials was permanently
// disabled: this endpoint always fails with ErrLegacyCredentialCreationDisabled
// (mapped to 409 LEGACY_CREDENTIAL_CREATION_DISABLED). It is kept only so
// existing clients receive a stable, documented error instead of a 404.
//
// New tenants must use POST /api/v1/admin/onboarding/merchants, which
// provisions a Phase 5C credential and marks the merchant MIGRATED. Existing
// tenants migrate via POST /api/v1/dashboard/legacy-credential/migrate.
func (s *merchantService) CreateMerchant(_ context.Context, _ model.CreateMerchantRequest) (*model.CreateMerchantResponse, error) {
	return nil, ErrLegacyCredentialCreationDisabled
}

// toGetMerchantResponse maps a merchant row to its public DTO.
// api_key / api_secret are never included; the migration state is included so
// clients can prompt for migration.
func toGetMerchantResponse(m *model.Merchant) *model.GetMerchantResponse {
	return &model.GetMerchantResponse{
		ID:                         m.ID,
		Name:                       m.Name,
		Code:                       m.Code,
		Status:                     m.Status,
		CreatedAt:                  m.CreatedAt,
		UpdatedAt:                  m.UpdatedAt,
		LegacyCredentialState:      m.LegacyCredentialState,
		LegacyCredentialDisabledAt: m.LegacyCredentialDisabledAt,
	}
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

	return toGetMerchantResponse(m), nil
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

// UpdateMerchantStatus validates the requested lifecycle transition, persists
// the new status, and revokes all active dashboard sessions when the merchant
// becomes non-ACTIVE (SUSPENDED or INACTIVE).
//
// Allowed transitions:
//
//	ACTIVE    → SUSPENDED  (temporary block)
//	ACTIVE    → INACTIVE   (permanent deactivation)
//	SUSPENDED → ACTIVE     (reinstate)
//	INACTIVE  → ACTIVE     (reactivate)
//
// Same-status requests are treated as idempotent success — no DB write,
// no session revocation.
//
// Disallowed transitions:
//
//	SUSPENDED → INACTIVE
//	INACTIVE  → SUSPENDED
//
// Returns ErrInvalidStatusTransition for disallowed transitions.
// Returns repository.ErrMerchantNotFound when the merchant does not exist.
func (s *merchantService) UpdateMerchantStatus(ctx context.Context, merchantID uuid.UUID, newStatus model.MerchantStatus) (*model.GetMerchantResponse, error) {
	m, err := s.repo.GetByID(ctx, merchantID)
	if err != nil {
		return nil, err // ErrMerchantNotFound propagated as-is
	}

	current := m.Status

	// Idempotent: same status requested — nothing to do.
	if current == newStatus {
		return toGetMerchantResponse(m), nil
	}

	// Validate transition.
	if !isAllowedMerchantTransition(current, newStatus) {
		return nil, fmt.Errorf("%w: %s → %s", ErrInvalidStatusTransition, current, newStatus)
	}

	// A non-ACTIVE transition must be able to revoke sessions. PostgreSQL does
	// this atomically; compatibility repositories need the explicit session
	// dependency. Refuse to mutate if neither is available.
	if newStatus != model.MerchantStatusActive {
		if _, transactional := s.repo.(repository.TransactionalMerchantStatusRepository); !transactional && s.sessionRepo == nil {
			return nil, fmt.Errorf("merchant service: session revocation repository unavailable")
		}
	}

	// Persist. The repository couples this event to the status UPDATE when an
	// audit recorder is configured (mandatory fail-closed behavior).
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionMerchantStatusChanged,
		audit.TargetMerchant,
		audit.UUIDPtr(merchantID),
		audit.UUIDPtr(merchantID),
		map[string]any{
			"old_status": current,
			"new_status": newStatus,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("merchant service update status: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)
	statusRepo, transactionalStatus := s.repo.(repository.TransactionalMerchantStatusRepository)
	var updateErr error
	if transactionalStatus {
		updateErr = statusRepo.UpdateStatusWithSessionRevocation(ctx, merchantID, newStatus)
	} else {
		updateErr = s.repo.UpdateStatus(ctx, merchantID, newStatus)
	}
	if updateErr != nil {
		if errors.Is(updateErr, repository.ErrMerchantStatusTransitionInvalid) {
			return nil, fmt.Errorf("%w: %s → %s", ErrInvalidStatusTransition, current, newStatus)
		}
		return nil, fmt.Errorf("merchant service update status: %w", updateErr)
	}

	slog.Info("merchant status changed",
		slog.String("merchant_id", merchantID.String()),
		slog.String("from", string(current)),
		slog.String("to", string(newStatus)),
	)

	// PostgreSQL revokes all dashboard sessions in the same transaction as the
	// status update. Compatibility repositories use the explicit fallback below;
	// a failed fallback is surfaced rather than silently reporting success.
	// Activation does not restore deleted sessions: users must log in fresh.
	if newStatus != model.MerchantStatusActive && s.sessionRepo != nil && !transactionalStatus {
		if revokeErr := s.sessionRepo.DeleteByMerchantID(ctx, merchantID); revokeErr != nil {
			return nil, fmt.Errorf("merchant service: revoke dashboard sessions: %w", revokeErr)
		}
	}

	resp := toGetMerchantResponse(m)
	resp.Status = newStatus
	// UpdatedAt will reflect NOW() from the DB; we approximate here since
	// we do not re-fetch the row to avoid an extra round-trip.
	return resp, nil
}

// isAllowedMerchantTransition returns true when moving from current to next
// is a permitted lifecycle transition.
//
// Allowed:
//
//	ACTIVE    → SUSPENDED
//	ACTIVE    → INACTIVE
//	SUSPENDED → ACTIVE
//	INACTIVE  → ACTIVE
//
// Denied:
//
//	SUSPENDED → INACTIVE
//	INACTIVE  → SUSPENDED
//	Any unknown status
func isAllowedMerchantTransition(current, next model.MerchantStatus) bool {
	return current.CanTransitionTo(next)
}

// ─── Credential generation (REMOVED — Phase 8D.3) ────────────────────────────
//
// generateAPIKey / generateAPISecret were deleted in Phase 8D.3: creation of
// new row-level legacy plaintext credentials is permanently frozen
// (see CreateMerchant and ErrLegacyCredentialCreationDisabled). The canonical
// credential generator is generateAPIKeyPair in merchant_api_key_service.go.
