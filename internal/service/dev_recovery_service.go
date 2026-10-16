package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// Development-only account recovery errors. None of them echo credentials,
// tokens, or password hashes.
var (
	// ErrDevRecoveryDisabled is returned when the feature is not enabled or
	// when the backend runs in production. Handlers map it to 404 so the
	// endpoint is indistinguishable from a missing route.
	ErrDevRecoveryDisabled = errors.New("development account recovery is disabled")

	// ErrDevRecoveryUnauthorized is returned when the development recovery
	// key is missing or wrong. It reveals nothing about account existence.
	ErrDevRecoveryUnauthorized = errors.New("invalid development recovery credentials")

	// ErrDevRecoveryNotFound is returned (only after successful
	// authorization) when the account does not exist or is ineligible
	// (disabled user or inactive merchant).
	ErrDevRecoveryNotFound = errors.New("account not found or ineligible for development recovery")

	// ErrDevRecoveryPasswordMismatch is returned when new_password and
	// confirm_password differ.
	ErrDevRecoveryPasswordMismatch = errors.New("password confirmation does not match")
)

// ─── Service ─────────────────────────────────────────────────────────────────

// DevRecoveryService implements the development-only account recovery flow:
// an explicitly identified existing account gets a new password after an
// independent development authorization check — without SMTP delivery.
type DevRecoveryService interface {
	// Recover sets a new password for the account identified by email.
	// providedKey is compared (constant-time) against the configured
	// development recovery key; it must come from the X-Dev-Recovery-Key
	// header, never from the JSON body.
	Recover(ctx context.Context, providedKey, email, newPassword, confirmPassword string) (*model.DashboardUserResponse, error)

	// Enabled reports whether the feature is active in this process.
	Enabled() bool
}

type devRecoveryService struct {
	userRepo     repository.MerchantUserRepository
	merchantRepo repository.MerchantRepository
	sessionRepo  repository.DashboardSessionRepository
	enabled      bool
	key          string
	appEnv       string
}

// NewDevRecoveryService wires development recovery over the user/merchant/
// session repositories. enabled/key/appEnv come from configuration:
//   - enabled=false (default) ⇒ every call fails with ErrDevRecoveryDisabled.
//   - appEnv=="production"   ⇒ every call fails with ErrDevRecoveryDisabled
//     (defense in depth; config.Load already rejects this combination).
//
// The key is held in memory only and never logged or returned.
func NewDevRecoveryService(
	userRepo repository.MerchantUserRepository,
	merchantRepo repository.MerchantRepository,
	sessionRepo repository.DashboardSessionRepository,
	enabled bool,
	key string,
	appEnv string,
) DevRecoveryService {
	return &devRecoveryService{
		userRepo:     userRepo,
		merchantRepo: merchantRepo,
		sessionRepo:  sessionRepo,
		enabled:      enabled,
		key:          key,
		appEnv:       appEnv,
	}
}

// Enabled reports whether development recovery is active.
func (s *devRecoveryService) Enabled() bool {
	if s == nil {
		return false
	}
	return s.enabled && s.appEnv != "production"
}

// authorize enforces the independent development-only authorization: the
// feature must be enabled, the environment must not be production, and the
// provided key must match the configured key in constant time. A frontend
// role/permission claim is never consulted.
func (s *devRecoveryService) authorize(providedKey string) error {
	if !s.enabled || s.appEnv == "production" {
		return ErrDevRecoveryDisabled
	}
	if s.key == "" || providedKey == "" {
		return ErrDevRecoveryUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(providedKey), []byte(s.key)) != 1 {
		return ErrDevRecoveryUnauthorized
	}
	return nil
}

// Recover validates authorization, resolves the intended account explicitly,
// enforces the existing password policy, hashes with the existing Argon2id
// service, updates only that account, and revokes its refresh sessions.
func (s *devRecoveryService) Recover(ctx context.Context, providedKey, email, newPassword, confirmPassword string) (*model.DashboardUserResponse, error) {
	if err := s.authorize(providedKey); err != nil {
		return nil, err
	}

	// Identify the intended existing account explicitly (same normalisation
	// as login so equivalent inputs resolve to the same account).
	normalised := strings.ToLower(strings.TrimSpace(email))
	if normalised == "" || len(normalised) > 254 {
		return nil, ErrDevRecoveryNotFound
	}
	user, err := s.userRepo.GetByEmail(ctx, normalised)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			return nil, ErrDevRecoveryNotFound
		}
		return nil, fmt.Errorf("dev recovery: get user: %w", err)
	}
	if user == nil || !user.IsActive() {
		return nil, ErrDevRecoveryNotFound
	}
	merchant, err := s.merchantRepo.GetByID(ctx, user.MerchantID)
	if err != nil || merchant == nil || !merchant.IsActive() {
		if err != nil && !errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, fmt.Errorf("dev recovery: get merchant: %w", err)
		}
		return nil, ErrDevRecoveryNotFound
	}

	// Validate password requirements using the existing application rules
	// (8–128 chars, no complexity rules) plus confirmation match. Errors
	// never echo the password.
	if newPassword != confirmPassword {
		return nil, ErrDevRecoveryPasswordMismatch
	}
	if len(newPassword) < passwordMinLen || len(newPassword) > passwordMaxLen {
		return nil, ErrInvalidPassword
	}

	// Hash with the existing Argon2id implementation (no second hashing
	// implementation). Hashing happens before the transaction so row locks
	// are held briefly.
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return nil, fmt.Errorf("dev recovery: hash password: %w", err)
	}

	if err := s.persistAndRevokeSessions(ctx, user.ID, newHash); err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			return nil, ErrDevRecoveryNotFound
		}
		return nil, err
	}

	slog.Info("development account recovery: password updated",
		slog.String("user_id", user.ID.String()),
		slog.String("merchant_id", user.MerchantID.String()),
		// emails, passwords, and recovery keys are NEVER logged
	)

	return &model.DashboardUserResponse{
		ID:         user.ID,
		MerchantID: user.MerchantID,
		Email:      user.Email,
		Role:       user.Role,
		Status:     user.Status,
		CreatedAt:  user.CreatedAt,
		UpdatedAt:  user.UpdatedAt,
	}, nil
}

// persistAndRevokeSessions updates only the intended account and revokes its
// refresh sessions. It prefers the transactional repository capability (one
// PostgreSQL transaction, same as password change); test doubles fall back to
// UpdatePasswordHash + DeleteByUserID. A revocation failure never reports
// success: a stale refresh token must not survive a password change.
func (s *devRecoveryService) persistAndRevokeSessions(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	if transactional, ok := s.userRepo.(repository.TransactionalPasswordUpdateRepository); ok {
		if err := transactional.UpdatePasswordHashAndRevokeSessions(ctx, userID, passwordHash); err != nil {
			return fmt.Errorf("dev recovery: persist and revoke sessions: %w", err)
		}
		return nil
	}
	if s.sessionRepo == nil {
		return fmt.Errorf("dev recovery: session revocation repository unavailable")
	}
	if err := s.userRepo.UpdatePasswordHash(ctx, userID, passwordHash); err != nil {
		return fmt.Errorf("dev recovery: persist: %w", err)
	}
	if err := s.sessionRepo.DeleteByUserID(ctx, userID); err != nil {
		return fmt.Errorf("dev recovery: revoke sessions: %w", err)
	}
	return nil
}
