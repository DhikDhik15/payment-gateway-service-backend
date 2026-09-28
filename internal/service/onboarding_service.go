package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const initialAPIKeyName = "Initial API Key"

// OnboardingService atomically provisions a new SaaS tenant:
// merchant row + OWNER dashboard user + initial Phase 5C API credential.
type OnboardingService interface {
	OnboardMerchant(ctx context.Context, req model.OnboardMerchantRequest) (*model.OnboardMerchantResponse, error)
}

// OnboardingProvisioner persists merchant + OWNER + API key in one atomic unit.
// Production uses PostgreSQL transactions; tests inject a mem implementation.
type OnboardingProvisioner interface {
	ExistsByCode(ctx context.Context, code string) (bool, error)
	Provision(ctx context.Context, merchant *model.Merchant, owner *model.MerchantUser, key *model.MerchantAPIKey) error
}

// ─── Implementation ───────────────────────────────────────────────────────────

type onboardingService struct {
	provisioner OnboardingProvisioner
}

// NewOnboardingService constructs an OnboardingService backed by the given provisioner.
func NewOnboardingService(provisioner OnboardingProvisioner) OnboardingService {
	return &onboardingService{provisioner: provisioner}
}

// NewPGOnboardingProvisioner returns a PostgreSQL-backed atomic provisioner.
func NewPGOnboardingProvisioner(
	pool *pgxpool.Pool,
	merchantRepo repository.MerchantRepository,
	userRepo repository.MerchantUserRepository,
	keyRepo repository.MerchantAPIKeyRepository,
	recorders ...audit.Recorder,
) OnboardingProvisioner {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgOnboardingProvisioner{
		pool:         pool,
		merchantRepo: merchantRepo,
		userRepo:     userRepo,
		keyRepo:      keyRepo,
		auditor:      recorder,
	}
}

type pgOnboardingProvisioner struct {
	pool         *pgxpool.Pool
	merchantRepo repository.MerchantRepository
	userRepo     repository.MerchantUserRepository
	keyRepo      repository.MerchantAPIKeyRepository
	auditor      audit.Recorder
}

func (p *pgOnboardingProvisioner) ExistsByCode(ctx context.Context, code string) (bool, error) {
	return p.merchantRepo.ExistsByCode(ctx, code)
}

// Provision inserts merchant, OWNER, and API key inside a single PostgreSQL transaction.
// On any failure the transaction is rolled back — no partial tenant remains.
func (p *pgOnboardingProvisioner) Provision(
	ctx context.Context,
	merchant *model.Merchant,
	owner *model.MerchantUser,
	key *model.MerchantAPIKey,
) error {
	if err := audit.RequireRecorder(ctx, p.auditor); err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("onboarding begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := p.merchantRepo.CreateInTx(ctx, tx, merchant); err != nil {
		return err
	}
	if err := p.userRepo.CreateInTx(ctx, tx, owner); err != nil {
		return err
	}
	if err := p.keyRepo.CreateInTx(ctx, tx, key); err != nil {
		return err
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, p.auditor); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("onboarding commit: %w", err)
	}
	return nil
}

// OnboardMerchant creates merchant + OWNER + Phase 5C API credential atomically.
//
// Credential model (Phase 8D.3 — legacy credential freeze):
//   - NO row-level legacy plaintext credential is generated. api_key/api_secret
//     are stored as NULL and the merchant is created directly in the MIGRATED
//     state, so Auth middleware stage-2 (legacy key lookup) can never match it.
//   - The Phase 5C merchant_api_keys credential is the only credential; its
//     plaintext secret is returned ONCE in the response as the handoff
//     credential for new tenants.
func (s *onboardingService) OnboardMerchant(ctx context.Context, req model.OnboardMerchantRequest) (*model.OnboardMerchantResponse, error) {
	slog.Info("merchant onboarding started",
		slog.String("merchant_code", req.Code),
	)

	exists, err := s.provisioner.ExistsByCode(ctx, req.Code)
	if err != nil {
		slog.Error("merchant onboarding failed",
			slog.String("merchant_code", req.Code),
			slog.String("stage", "check_duplicate_code"),
			slog.String("error", err.Error()),
		)
		return nil, fmt.Errorf("onboarding: check duplicate code: %w", err)
	}
	if exists {
		slog.Info("merchant onboarding failed",
			slog.String("merchant_code", req.Code),
			slog.String("stage", "duplicate_code"),
		)
		return nil, ErrDuplicateMerchantCode
	}

	email := strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	if err := validateEmail(email); err != nil {
		return nil, ErrInvalidEmail
	}

	passwordHash, err := hashPassword(req.OwnerPassword)
	if err != nil {
		return nil, fmt.Errorf("onboarding: hash owner password: %w", err)
	}

	keyID, plaintextSecret, secretHash, err := generateAPIKeyPair()
	if err != nil {
		return nil, fmt.Errorf("onboarding: generate api credential: %w", err)
	}

	now := time.Now().UTC()
	merchantID := uuid.New()

	// Phase 8D.3: no legacy credential. Empty APIKey/APISecret are persisted as
	// NULL and the merchant starts in the MIGRATED state (Phase 5C only).
	merchant := &model.Merchant{
		ID:                    merchantID,
		Name:                  req.Name,
		Code:                  req.Code,
		APIKey:                "",
		APISecret:             "",
		Status:                model.MerchantStatusActive,
		LegacyCredentialState: model.LegacyCredentialStateMigrated,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	owner := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   merchantID, // always from the merchant created above — never client-supplied
		Email:        email,
		PasswordHash: passwordHash,
		Role:         model.DashboardUserRoleOwner,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	apiKey := &model.MerchantAPIKey{
		ID:         uuid.New(),
		MerchantID: merchantID, // same tenant — never client-supplied
		KeyID:      keyID,
		SecretHash: secretHash,
		Name:       initialAPIKeyName,
		Status:     model.MerchantAPIKeyStatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionMerchantCreated,
		audit.TargetMerchant,
		audit.UUIDPtr(merchantID),
		audit.UUIDPtr(merchantID),
		map[string]any{
			"credential_type": "API_KEY",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("onboarding: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)
	if err := s.provisioner.Provision(ctx, merchant, owner, apiKey); err != nil {
		slog.Info("merchant onboarding failed",
			slog.String("merchant_code", req.Code),
			slog.String("stage", "provision"),
			slog.String("error", err.Error()),
		)
		if errors.Is(err, repository.ErrMerchantCodeExists) {
			return nil, ErrDuplicateMerchantCode
		}
		if errors.Is(err, repository.ErrMerchantUserEmailExists) {
			return nil, ErrEmailAlreadyExists
		}
		return nil, fmt.Errorf("onboarding: provision: %w", err)
	}

	slog.Info("merchant onboarding succeeded",
		slog.String("merchant_id", merchantID.String()),
		slog.String("merchant_code", merchant.Code),
		slog.String("owner_id", owner.ID.String()),
		slog.String("api_key_id", keyID),
		// password, secret, and hashes are NEVER logged
	)
	return &model.OnboardMerchantResponse{
		Merchant: model.OnboardedMerchant{
			ID:        merchant.ID,
			Name:      merchant.Name,
			Code:      merchant.Code,
			Status:    merchant.Status,
			CreatedAt: merchant.CreatedAt,
		}, Owner: model.OnboardedOwner{
			ID:    owner.ID,
			Email: owner.Email,
			Role:  owner.Role,
		},
		APICredential: model.OnboardedAPICredential{
			ID:        apiKey.ID,
			KeyID:     keyID,
			Secret:    plaintextSecret, // one-time disclosure
			Name:      apiKey.Name,
			Status:    apiKey.Status,
			CreatedAt: apiKey.CreatedAt,
		},
	}, nil
}
