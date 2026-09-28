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

	// LogoutSession revokes the exact dashboard session identified by a valid
	// access token's stable sid claim and subject. It is idempotent and never
	// crosses the user/tenant boundary.
	LogoutSession(ctx context.Context, sessionID uuid.UUID, userID uuid.UUID) error

	// Me returns the public profile of the authenticated user.
	Me(ctx context.Context, userID uuid.UUID) (*model.DashboardUserResponse, error)

	// RefreshAccessToken validates the plaintext refresh token, atomically
	// rotates the session, and issues a new access token.
	// Returns (loginResponse, newPlaintextRefreshToken, error).
	RefreshAccessToken(ctx context.Context, plainRefreshToken string) (*model.LoginResponse, string, error)

	// VerifyAccessToken validates the JWT signature and expiry, returning claims.
	// Used by the RequireDashboardAuth middleware.
	VerifyAccessToken(token string) (*JWTClaims, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type authService struct {
	userRepo     repository.MerchantUserRepository
	sessionRepo  repository.DashboardSessionRepository
	merchantRepo repository.MerchantRepository
	cfg          AuthConfig
}

// NewAuthService constructs an AuthService.
//
// merchantRepo is optional for backwards-compatible unit-test construction;
// the production wiring supplies it so login and refresh enforce the merchant
// lifecycle check. When present, refresh resolves the merchant from the
// server-side session's user and never from client input.
func NewAuthService(
	userRepo repository.MerchantUserRepository,
	sessionRepo repository.DashboardSessionRepository,
	cfg AuthConfig,
	merchantRepos ...repository.MerchantRepository,
) AuthService {
	var merchantRepo repository.MerchantRepository
	if len(merchantRepos) > 0 {
		merchantRepo = merchantRepos[0]
	}
	return &authService{
		userRepo:     userRepo,
		sessionRepo:  sessionRepo,
		merchantRepo: merchantRepo,
		cfg:          cfg,
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

	// Login is an unauthenticated endpoint, so it must enforce the merchant
	// lifecycle boundary itself. The refresh path repeats this check under the
	// session/merchant transaction; this early check avoids issuing a new
	// cookie for an already inactive tenant.
	if s.merchantRepo != nil {
		merchant, merchantErr := s.merchantRepo.GetByID(ctx, user.MerchantID)
		if merchantErr != nil {
			if errors.Is(merchantErr, repository.ErrMerchantNotFound) {
				return nil, "", ErrInvalidCredentials
			}
			return nil, "", fmt.Errorf("login: get merchant: %w", merchantErr)
		}
		if !merchant.IsActive() {
			return nil, "", ErrInvalidCredentials
		}
	}

	return s.issueTokens(ctx, user)
}

type issuedTokenPair struct {
	response *model.LoginResponse
	refresh  string
	session  *model.DashboardSession
}

// buildTokenPair creates the access token and replacement-session material but
// does not persist anything. Keeping persistence separate lets refresh commit
// the old-session claim and replacement update in one database transaction.
//
// sessionID is the stable logical dashboard-session identity. A zero value
// creates a new session ID for login; refresh passes the existing row ID so
// every access token issued during the session can safely target logout.
func (s *authService) buildTokenPair(user *model.MerchantUser, now time.Time, sessionID uuid.UUID) (*issuedTokenPair, error) {
	// Work from a value snapshot so a concurrent status/role change cannot
	// mutate the claims while they are being assembled.
	userSnapshot := *user
	if sessionID == uuid.Nil {
		sessionID = uuid.New()
	}
	claims := jwtClaims{
		Subject:    userSnapshot.ID.String(),
		MerchantID: userSnapshot.MerchantID.String(),
		Role:       string(userSnapshot.Role),
		IssuedAt:   now.Unix(),
		ExpiresAt:  now.Add(s.cfg.AccessTokenTTL).Unix(),
		JTI:        generateJTI(),
		SessionID:  sessionID.String(),
	}

	accessToken, err := signJWT(claims, s.cfg.JWTSecret)
	if err != nil {
		return nil, fmt.Errorf("issue tokens: sign jwt: %w", err)
	}

	plainRefresh, err := generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("issue tokens: generate refresh: %w", err)
	}

	session := &model.DashboardSession{
		ID:               sessionID,
		MerchantUserID:   userSnapshot.ID,
		RefreshTokenHash: hashRefreshToken(plainRefresh),
		ExpiresAt:        now.Add(s.cfg.RefreshTokenTTL),
		CreatedAt:        now,
	}

	return &issuedTokenPair{
		response: &model.LoginResponse{
			AccessToken: accessToken,
			TokenType:   "Bearer",
			ExpiresIn:   int(s.cfg.AccessTokenTTL.Seconds()),
			User:        toUserResponse(&userSnapshot),
		},
		refresh: plainRefresh,
		session: session,
	}, nil
}

// recordTokenIssue performs the existing best-effort last-login update and
// safe event logging. It is called only after the relevant session transaction
// has committed.
func (s *authService) recordTokenIssue(ctx context.Context, user *model.MerchantUser, now time.Time) {
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
}

// issueTokens creates a new JWT + refresh session for a user during login.
func (s *authService) issueTokens(ctx context.Context, user *model.MerchantUser) (*model.LoginResponse, string, error) {
	now := time.Now().UTC()
	pair, err := s.buildTokenPair(user, now, uuid.Nil)
	if err != nil {
		return nil, "", err
	}
	if err := s.sessionRepo.Create(ctx, pair.session); err != nil {
		return nil, "", fmt.Errorf("issue tokens: create session: %w", err)
	}

	s.recordTokenIssue(ctx, user, now)
	return pair.response, pair.refresh, nil
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

// LogoutSession revokes the logical session addressed by a valid access token.
// Unlike the legacy refresh-hash path, the session ID is stable across refresh
// rotation, so a concurrent refresh cannot leave a replacement session behind.
func (s *authService) LogoutSession(ctx context.Context, sessionID uuid.UUID, userID uuid.UUID) error {
	if sessionID == uuid.Nil || userID == uuid.Nil {
		return ErrInvalidCredentials
	}
	if err := s.sessionRepo.DeleteByIDAndUser(ctx, sessionID, userID); err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			// Idempotent logout: the session was already revoked or replaced and
			// subsequently removed by another lifecycle operation.
			return nil
		}
		return fmt.Errorf("logout: delete session by id: %w", err)
	}
	slog.Info("dashboard auth: logout",
		slog.String("user_id", userID.String()),
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

	// These checks provide the existing error semantics and avoid expensive
	// token generation for obviously invalid credentials. The production
	// rotation transaction repeats the lifecycle checks under row locks before
	// committing the replacement.
	if session.IsExpired() {
		// Do not delete by the pre-read session ID here. A concurrent refresh
		// may already have rotated that row; the transactional CAS path is
		// the sole authority for replacement/revocation.
		return nil, "", ErrInvalidCredentials
	}

	user, err := s.userRepo.GetByID(ctx, session.MerchantUserID)
	if err != nil {
		return nil, "", fmt.Errorf("refresh: get user: %w", err)
	}
	if !user.IsActive() {
		// Lifecycle invalidation is performed by the transactional status
		// operation. Never remove a possibly rotated replacement by a stale
		// pre-read ID here.
		return nil, "", ErrUserDisabled
	}

	// A refresh must obey the same merchant lifecycle boundary as an
	// authenticated dashboard request. The merchant is derived from the
	// server-side user/session; no request field participates in this lookup.
	if s.merchantRepo != nil {
		merchant, merchantErr := s.merchantRepo.GetByID(ctx, user.MerchantID)
		if merchantErr != nil {
			if errors.Is(merchantErr, repository.ErrMerchantNotFound) {
				return nil, "", ErrInvalidCredentials
			}
			return nil, "", fmt.Errorf("refresh: get merchant: %w", merchantErr)
		}
		if !merchant.IsActive() {
			return nil, "", ErrInvalidCredentials
		}
	}

	now := time.Now().UTC()
	pair, err := s.buildTokenPair(user, now, session.ID)
	if err != nil {
		return nil, "", err
	}

	if err := s.rotateRefreshSession(ctx, tokenHash, pair.session); err != nil {
		return nil, "", err
	}

	// Only report success after the replacement transaction has committed.
	s.recordTokenIssue(ctx, user, now)
	return pair.response, pair.refresh, nil
}

// rotateRefreshSession requires the database transaction capability. A
// missing capability is a hard configuration error: falling back to a
// delete-then-create sequence would reintroduce the partial-rotation race this
// phase is intended to eliminate.
func (s *authService) rotateRefreshSession(
	ctx context.Context,
	oldTokenHash string,
	replacement *model.DashboardSession,
) error {
	rotator, ok := s.sessionRepo.(repository.AtomicRefreshSessionRepository)
	if !ok {
		return fmt.Errorf("refresh: transactional session repository unavailable")
	}
	_, err := rotator.RotateByRefreshTokenHash(ctx, oldTokenHash, replacement)
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, repository.ErrSessionNotFound),
		errors.Is(err, repository.ErrSessionExpired),
		errors.Is(err, repository.ErrSessionMerchantInactive):
		return ErrInvalidCredentials
	case errors.Is(err, repository.ErrSessionUserDisabled):
		return ErrUserDisabled
	default:
		return fmt.Errorf("refresh: rotate session: %w", err)
	}
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
