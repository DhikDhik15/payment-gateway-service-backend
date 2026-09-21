package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Service errors ───────────────────────────────────────────────────────────

var (
	// ErrInvalidCredentials is returned for wrong email, wrong password, or
	// disabled user. Generic on purpose — do not distinguish between
	// "email not found" and "wrong password" to prevent user enumeration.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrUserDisabled is returned separately from ErrInvalidCredentials so
	// callers can return a distinct (but still non-leaking) HTTP status.
	ErrUserDisabled = errors.New("user account is disabled")

	// ErrInvalidEmail is returned when the email fails format validation
	// after trim+lowercase normalisation.
	ErrInvalidEmail = errors.New("invalid email")

	// ErrJWTExpiredPublic is the exported sentinel for JWT expiry, used by the
	// RequireDashboardAuth middleware to map to the correct error code.
	ErrJWTExpiredPublic = ErrJWTExpired
)

// JWTClaims is the exported view of jwt access token claims.
// The middleware uses this to extract subject/merchant/role.
type JWTClaims = jwtClaims

// ─── Config ───────────────────────────────────────────────────────────────────

// AuthConfig holds configuration for the dashboard authentication service.
type AuthConfig struct {
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// ─── Interface ────────────────────────────────────────────────────────────────

// AuthService handles dashboard login, logout, token refresh, and identity.
type AuthService interface {
	// Login authenticates a user by email+password, creates a session, and
	// returns (loginResponse, plaintextRefreshToken, error).
	// The plaintext refresh token must be delivered to the client exactly once
	// (e.g. via an HttpOnly cookie); it is not stored in the database.
	Login(ctx context.Context, req model.LoginRequest) (*model.LoginResponse, string, error)

	// Logout revokes the session identified by the SHA-256 hash of the refresh
	// token. Idempotent — not finding the session is not an error.
	Logout(ctx context.Context, refreshTokenHash string) error

	// Me returns the public profile of the authenticated user.
	Me(ctx context.Context, userID uuid.UUID) (*model.DashboardUserResponse, error)

	// RefreshAccessToken validates the plaintext refresh token, rotates the
	// session (revoke old, create new), and issues a new access token.
	// Returns (loginResponse, newPlaintextRefreshToken, error).
	RefreshAccessToken(ctx context.Context, plainRefreshToken string) (*model.LoginResponse, string, error)

	// VerifyAccessToken validates the JWT signature and expiry, returning claims.
	// Used by the RequireDashboardAuth middleware.
	VerifyAccessToken(token string) (*JWTClaims, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type authService struct {
	userRepo    repository.MerchantUserRepository
	sessionRepo repository.DashboardSessionRepository
	cfg         AuthConfig
}

// NewAuthService constructs an AuthService.
func NewAuthService(
	userRepo repository.MerchantUserRepository,
	sessionRepo repository.DashboardSessionRepository,
	cfg AuthConfig,
) AuthService {
	return &authService{
		userRepo:    userRepo,
		sessionRepo: sessionRepo,
		cfg:         cfg,
	}
}

// ─── Login ────────────────────────────────────────────────────────────────────

func (s *authService) Login(ctx context.Context, req model.LoginRequest) (*model.LoginResponse, string, error) {
	// Normalise email: trim whitespace + lowercase, then validate format.
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := validateEmail(email); err != nil {
		return nil, "", ErrInvalidEmail
	}

	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			// Run a dummy Argon2id verification to equalise timing and prevent
			// user-enumeration attacks based on response latency.
			_ = verifyPassword("dummy", "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", fmt.Errorf("login: lookup user: %w", err)
	}

	// Verify password — constant-time comparison inside verifyPassword.
	if !verifyPassword(req.Password, user.PasswordHash) {
		slog.Info("dashboard auth: password mismatch",
			slog.String("email_domain", emailDomain(email)),
			// email prefix and password are NEVER logged
		)
		return nil, "", ErrInvalidCredentials
	}

	// Status check comes AFTER password verification to avoid leaking whether
	// the account exists (both wrong-password and disabled return 401).
	if !user.IsActive() {
		slog.Info("dashboard auth: disabled user login attempt",
			slog.String("user_id", user.ID.String()),
		)
		return nil, "", ErrUserDisabled
	}

	return s.issueTokens(ctx, user)
}

// issueTokens creates a new JWT + refresh session for a user.
func (s *authService) issueTokens(ctx context.Context, user *model.MerchantUser) (*model.LoginResponse, string, error) {
	now := time.Now().UTC()

	// Build access token claims.
	claims := jwtClaims{
		Subject:    user.ID.String(),
		MerchantID: user.MerchantID.String(),
		Role:       string(user.Role),
		IssuedAt:   now.Unix(),
		ExpiresAt:  now.Add(s.cfg.AccessTokenTTL).Unix(),
		JTI:        generateJTI(),
	}

	accessToken, err := signJWT(claims, s.cfg.JWTSecret)
	if err != nil {
		return nil, "", fmt.Errorf("issue tokens: sign jwt: %w", err)
	}

	// Generate opaque refresh token and store its hash.
	plainRefresh, err := generateRefreshToken()
	if err != nil {
		return nil, "", fmt.Errorf("issue tokens: generate refresh: %w", err)
	}

	session := &model.DashboardSession{
		ID:               uuid.New(),
		MerchantUserID:   user.ID,
		RefreshTokenHash: hashRefreshToken(plainRefresh),
		ExpiresAt:        now.Add(s.cfg.RefreshTokenTTL),
		CreatedAt:        now,
	}
	if err := s.sessionRepo.Create(ctx, session); err != nil {
		return nil, "", fmt.Errorf("issue tokens: create session: %w", err)
	}

	// Best-effort last_login_at update — must not fail the login.
	if err := s.userRepo.UpdateLastLoginAt(ctx, user.ID, now); err != nil {
		slog.Warn("dashboard auth: failed to update last_login_at",
			slog.String("user_id", user.ID.String()),
			slog.String("error", err.Error()),
		)
	}

	slog.Info("dashboard auth: login successful",
		slog.String("user_id", user.ID.String()),
		slog.String("merchant_id", user.MerchantID.String()),
		slog.String("role", string(user.Role)),
		// access token and refresh token are NEVER logged
	)

	resp := &model.LoginResponse{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   int(s.cfg.AccessTokenTTL.Seconds()),
		User:        toUserResponse(user),
	}
	return resp, plainRefresh, nil
}

// ─── Logout ───────────────────────────────────────────────────────────────────

func (s *authService) Logout(ctx context.Context, refreshTokenHash string) error {
	session, err := s.sessionRepo.GetByRefreshTokenHash(ctx, refreshTokenHash)
	if err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			// Already logged out — idempotent.
			return nil
		}
		return fmt.Errorf("logout: get session: %w", err)
	}

	if err := s.sessionRepo.Delete(ctx, session.ID); err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			return nil // concurrent logout beat us here
		}
		return fmt.Errorf("logout: delete session: %w", err)
	}

	slog.Info("dashboard auth: logout",
		slog.String("user_id", session.MerchantUserID.String()),
		// session ID is internal — do not leak to client
	)
	return nil
}

// ─── Me ───────────────────────────────────────────────────────────────────────

func (s *authService) Me(ctx context.Context, userID uuid.UUID) (*model.DashboardUserResponse, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			return nil, ErrInvalidCredentials // user was deleted after token was issued
		}
		return nil, fmt.Errorf("me: %w", err)
	}
	resp := toUserResponse(user)
	return &resp, nil
}

// ─── RefreshAccessToken ───────────────────────────────────────────────────────

func (s *authService) RefreshAccessToken(ctx context.Context, plainRefreshToken string) (*model.LoginResponse, string, error) {
	tokenHash := hashRefreshToken(plainRefreshToken)

	session, err := s.sessionRepo.GetByRefreshTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", fmt.Errorf("refresh: get session: %w", err)
	}

	if session.IsExpired() {
		// Clean up the expired session.
		_ = s.sessionRepo.Delete(ctx, session.ID)
		return nil, "", ErrInvalidCredentials
	}

	user, err := s.userRepo.GetByID(ctx, session.MerchantUserID)
	if err != nil {
		return nil, "", fmt.Errorf("refresh: get user: %w", err)
	}

	if !user.IsActive() {
		// User was disabled after the session was created — revoke.
		_ = s.sessionRepo.Delete(ctx, session.ID)
		return nil, "", ErrUserDisabled
	}

	// Rotate: delete old session before issuing new tokens.
	// If this fails, the old session remains valid — acceptable because
	// the new session would simply be a second valid slot.
	_ = s.sessionRepo.Delete(ctx, session.ID)

	return s.issueTokens(ctx, user)
}

// ─── VerifyAccessToken ────────────────────────────────────────────────────────

// VerifyAccessToken validates the JWT and returns the claims.
// Used by RequireDashboardAuth middleware.
func (s *authService) VerifyAccessToken(token string) (*JWTClaims, error) {
	return verifyJWT(token, s.cfg.JWTSecret)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// toUserResponse converts a MerchantUser to a safe DTO (no password_hash).
func toUserResponse(u *model.MerchantUser) model.DashboardUserResponse {
	return model.DashboardUserResponse{
		ID:          u.ID,
		MerchantID:  u.MerchantID,
		Email:       u.Email,
		Role:        u.Role,
		Status:      u.Status,
		LastLoginAt: u.LastLoginAt,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}

// emailDomain returns only the domain part of an email for safe logging.
func emailDomain(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) == 2 {
		return "@" + parts[1]
	}
	return "<invalid>"
}

// validateEmail checks a already-normalised email address.
func validateEmail(email string) error {
	if email == "" || len(email) > 254 {
		return ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return ErrInvalidEmail
	}
	return nil
}
