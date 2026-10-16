package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

var (
	// ErrPasswordResetInvalid is returned when no token row matches the hash.
	ErrPasswordResetInvalid = errors.New("password reset token is invalid")

	// ErrPasswordResetExpired is returned when the token is past its expiry.
	ErrPasswordResetExpired = errors.New("password reset token has expired")

	// ErrPasswordResetConsumed is returned when the token was already used or
	// revoked (including a lost single-use race).
	ErrPasswordResetConsumed = errors.New("password reset token has already been used")
)

// ─── Service ─────────────────────────────────────────────────────────────────

// PasswordResetService implements the forgot-password flow: generic reset
// requests and single-use token consumption with session revocation.
type PasswordResetService interface {
	// RequestPasswordReset records a reset request and queues the reset email
	// for eligible accounts. Always succeeds from the caller's perspective —
	// unknown or ineligible emails produce the same nil outcome — so account
	// existence is never revealed.
	RequestPasswordReset(ctx context.Context, email string) error

	// ConfirmPasswordReset consumes the token and sets the new password,
	// revoking the user's other reset tokens and refresh sessions.
	ConfirmPasswordReset(ctx context.Context, token, password string) error
}

type passwordResetService struct {
	userRepo         repository.MerchantUserRepository
	merchantRepo     repository.MerchantRepository
	resets           repository.PasswordResetRepository
	tokenTTL         time.Duration
	dashboardBaseURL string
}

// NewPasswordResetService wires the reset flow over the user/merchant/reset
// repositories. tokenTTL bounds token validity (short, e.g. 1h);
// dashboardBaseURL builds the reset link (same variable as invitations).
func NewPasswordResetService(
	userRepo repository.MerchantUserRepository,
	merchantRepo repository.MerchantRepository,
	resets repository.PasswordResetRepository,
	tokenTTL time.Duration,
	dashboardBaseURL string,
) PasswordResetService {
	return &passwordResetService{
		userRepo:         userRepo,
		merchantRepo:     merchantRepo,
		resets:           resets,
		tokenTTL:         tokenTTL,
		dashboardBaseURL: dashboardBaseURL,
	}
}

// generateResetToken mints a cryptographically secure opaque token: 32 random
// bytes as lowercase hex (64 chars), same strength as refresh tokens. Only
// its SHA-256 hash is persisted.
func generateResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// eligibleForReset reports whether the account may receive a reset email:
// it must exist, be ACTIVE, and belong to an ACTIVE merchant. Anything else
// yields the generic response with no email and no token row.
func (s *passwordResetService) eligibleForReset(ctx context.Context, email string) (*model.MerchantUser, bool) {
	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return nil, false
	}
	if user == nil || !user.IsActive() {
		return nil, false
	}
	merchant, err := s.merchantRepo.GetByID(ctx, user.MerchantID)
	if err != nil || merchant == nil || !merchant.IsActive() {
		return nil, false
	}
	return user, true
}

func (s *passwordResetService) RequestPasswordReset(ctx context.Context, email string) error {
	// Normalise exactly like login (trim + lowercase) so equivalent inputs
	// resolve to the same account.
	normalised := strings.ToLower(strings.TrimSpace(email))
	user, ok := s.eligibleForReset(ctx, normalised)
	if !ok {
		// Generic outcome: unknown, disabled, or inactive-merchant accounts
		// are indistinguishable from success. No token row, no email.
		return nil
	}

	token, err := generateResetToken()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(s.tokenTTL)

	resetURL, err := buildResetURL(s.dashboardBaseURL, token)
	if err != nil {
		return err
	}
	msg, err := buildResetEmail(user.Email, resetURL, expiresAt, now)
	if err != nil {
		return err
	}

	tokenID := uuid.New()
	entry := newResetEmailOutbox(user.MerchantID, tokenID, msg, now)
	if err := s.resets.CreateWithEmailOutbox(ctx, &model.PasswordResetToken{
		ID:        tokenID,
		UserID:    user.ID,
		TokenHash: HashRefreshTokenPublic(token),
		ExpiresAt: expiresAt,
	}, entry); err != nil {
		if errors.Is(err, repository.ErrPasswordResetAlreadyPending) {
			// A concurrent request won the race; its email is on the way.
			// Still a generic success.
			return nil
		}
		return err
	}
	return nil
}

func (s *passwordResetService) ConfirmPasswordReset(ctx context.Context, token, password string) error {
	// Enforce the project password policy (8–128 chars, no complexity rules),
	// mirroring invitation acceptance and password change.
	if len(password) < passwordMinLen || len(password) > passwordMaxLen {
		return ErrInvalidPassword
	}
	hash, err := hashPassword(password)
	if err != nil {
		return fmt.Errorf("confirm password reset: hash password: %w", err)
	}
	// Hashing happens before the transaction so row locks are held briefly.
	_, err = s.resets.ConsumeWithPasswordUpdate(ctx, HashRefreshTokenPublic(strings.TrimSpace(token)), hash)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrPasswordResetNotFound):
			return ErrPasswordResetInvalid
		case errors.Is(err, repository.ErrPasswordResetExpired):
			return ErrPasswordResetExpired
		case errors.Is(err, repository.ErrPasswordResetConsumed):
			return ErrPasswordResetConsumed
		default:
			return err
		}
	}
	return nil
}
