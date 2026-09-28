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
)

// ─── Service errors ───────────────────────────────────────────────────────────

var (
	// ErrInvitationNotFound is returned for an unknown, expired, revoked, or
	// otherwise invalid invitation token. Deliberately non-distinguishing so a
	// caller cannot probe which failure occurred.
	ErrInvitationNotFound = errors.New("invitation not found or no longer valid")

	// ErrInvitationAlreadyPending is returned when a PENDING invitation already
	// exists for the same merchant + email (one active invitation per email).
	ErrInvitationAlreadyPending = errors.New("an invitation is already pending for this email")

	// ErrInvitationAlreadyAccepted is returned when a single-use token is
	// replayed after acceptance (including the concurrent-accept race).
	ErrInvitationAlreadyAccepted = errors.New("invitation has already been accepted")

	// ErrMerchantNotActive is returned when invitation creation or acceptance
	// is attempted while the merchant is SUSPENDED or INACTIVE.
	ErrMerchantNotActive = errors.New("merchant is not active")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// InvitationService manages Phase 8B self-service team invitations.
//
// Phase 8B is API-first: the plaintext token is returned exactly once at
// creation so an external system/frontend can construct and deliver the
// invitation link. Email delivery is Phase 8C.
type InvitationService interface {
	// CreateInvitation creates a PENDING invitation for the caller's merchant
	// and returns the plaintext token ONCE (never retrievable again).
	// Authorization rules (Phase 8A policy preserved):
	//   - Only OWNER may create invitations (ADMIN and VIEWER →
	//     ErrInsufficientRole). Phase 8A made all team mutations OWNER-only;
	//     invitations follow the same rule.
	//   - The invited role must be a valid OWNER/ADMIN/VIEWER value
	//     (ErrInvalidRole). Role defaults are never applied — it is always
	//     explicitly caller-supplied.
	//   - The invited role cannot bypass Phase 8A role-management rules:
	//     only an OWNER caller can invite anyone (incl. OWNER), mirroring the
	//     OWNER-only UpdateUserRole promotion path.
	// Rules:
	//   - Merchant must be ACTIVE (ErrMerchantNotActive). Route middleware
	//     already enforces this; the service check is defense in depth.
	//   - Email is normalised (lowercase/trimmed) and format-validated
	//     (ErrInvalidEmail).
	//   - Email must not already be registered by any dashboard user
	//     (ErrEmailAlreadyExists — global email uniqueness preserved; a
	//     DISABLED user's email must be re-enabled, not re-invited).
	//   - One active invitation per (merchant, email): a stale PENDING
	//     invitation is lazily EXPIRED first; an unexpired PENDING one →
	//     ErrInvitationAlreadyPending.
	//   - Merchant identity comes exclusively from callerMerchantID — no
	//     merchant_id is accepted from the request body.
	// Phase 8C.3A (atomic email outbox, downstream of ALL checks above):
	//   - The invitation email is fully rendered BEFORE the transaction and
	//     committed into email_outbox in the SAME transaction as the
	//     invitation row (invitation exists ⇔ queued email exists).
	//   - The email carries the SAME plaintext token that is returned in the
	//     response (one token generation; the outbox holds the rendered
	//     bodies, the invitation row holds only token_hash).
	//   - NO EmailSender/SMTP call happens in this request — delivery is the
	//     Phase 8C.3B worker's job. Any DB error (invitation INSERT, outbox
	//     INSERT, COMMIT) rolls back BOTH rows and surfaces as an error.
	//   - Duplicate pending still maps to ErrInvitationAlreadyPending → 409;
	//     the rollback leaves no orphan outbox row.
	CreateInvitation(
		ctx context.Context,
		callerRole model.DashboardUserRole,
		callerMerchantID uuid.UUID,
		req model.CreateInvitationRequest,
	) (*model.CreateInvitationResponse, error)

	// GetInvitationByToken returns public metadata (email, role, merchant
	// name, expiry) for a valid PENDING token — the invitee preview step.
	// Unknown, expired, accepted, or revoked tokens all return
	// ErrInvitationNotFound (deliberately indistinguishable).
	// Reading metadata is not a mutation, so a SUSPENDED merchant does not
	// block it; acceptance does enforce merchant ACTIVE.
	GetInvitationByToken(ctx context.Context, token string) (*model.InvitationMetadataResponse, error)

	// AcceptInvitation creates the dashboard user and marks the invitation
	// ACCEPTED in one atomic transaction (single-use token).
	// Rules:
	//   - Token must be valid and PENDING (ErrInvitationNotFound for
	//     unknown/expired/revoked; ErrInvitationAlreadyAccepted for replay).
	//   - Merchant must still be ACTIVE at acceptance time
	//     (ErrMerchantNotActive) — an invitation created before a suspension
	//     cannot be accepted while suspended.
	//   - The invited email must still be unregistered
	//     (ErrEmailAlreadyExists — includes the acceptance race).
	//   - password must satisfy the password policy (min 8, max 128 —
	//     ErrInvalidPassword) and is stored only as an Argon2id hash.
	//     Plaintext passwords are NEVER logged.
	//   - The new user is created ACTIVE with the invited role, belonging to
	//     the invitation's merchant (never client-supplied).
	AcceptInvitation(
		ctx context.Context,
		token string,
		req model.AcceptInvitationRequest,
	) (*model.DashboardUserResponse, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type invitationService struct {
	invRepo      repository.MerchantInvitationRepository
	userRepo     repository.MerchantUserRepository
	merchantRepo repository.MerchantRepository
	tokenTTL     time.Duration
	// Phase 8C.3A: the rendered invitation email is committed into
	// email_outbox inside the invitation transaction — this service no longer
	// depends on EmailSender (SMTP is out of the request path; the Phase
	// 8C.3B worker consumes EmailSender instead).
	// dashboardBaseURL is the public dashboard SPA origin used to build the
	// acceptance link ({base}/accept-invitation?token=…).
	dashboardBaseURL string
}

// NewInvitationService constructs an InvitationService.
// tokenTTL controls invitation validity (config INVITATION_TOKEN_TTL,
// default 48h — audit recommendation window is 48–72h).
// dashboardBaseURL powers the invitation acceptance link rendered into the
// queued email (wired from cfg.App.DashboardBaseURL in main.go).
func NewInvitationService(
	invRepo repository.MerchantInvitationRepository,
	userRepo repository.MerchantUserRepository,
	merchantRepo repository.MerchantRepository,
	tokenTTL time.Duration,
	dashboardBaseURL string,
) InvitationService {
	return &invitationService{
		invRepo:          invRepo,
		userRepo:         userRepo,
		merchantRepo:     merchantRepo,
		tokenTTL:         tokenTTL,
		dashboardBaseURL: dashboardBaseURL,
	}
}

// CreateInvitation creates a PENDING invitation and returns the token once.
func (s *invitationService) CreateInvitation(
	ctx context.Context,
	callerRole model.DashboardUserRole,
	callerMerchantID uuid.UUID,
	req model.CreateInvitationRequest,
) (*model.CreateInvitationResponse, error) {
	// Authorization: only OWNER may manage the team (Phase 8A policy).
	// Defense in depth — the route is already gated by RequireRole(OWNER).
	if callerRole != model.DashboardUserRoleOwner {
		return nil, ErrInsufficientRole
	}

	// Role must be explicit and valid — no silent defaulting.
	// (Handler binding already enforces oneof=OWNER ADMIN VIEWER.)
	if !req.Role.IsValid() {
		return nil, ErrInvalidRole
	}

	// Merchant must be ACTIVE for invitation mutations.
	// RequireDashboardAuth already rejects non-ACTIVE merchants with
	// 401 MERCHANT_INACTIVE; this is the service-level second line of defense.
	merchant, err := s.merchantRepo.GetByID(ctx, callerMerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, repository.ErrMerchantNotFound
		}
		return nil, fmt.Errorf("create invitation: get merchant: %w", err)
	}
	if !merchant.IsActive() {
		return nil, ErrMerchantNotActive
	}

	// Normalise + validate the invited email (same rules as user creation).
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := validateEmail(email); err != nil {
		return nil, ErrInvalidEmail
	}

	// Global email uniqueness: the email must not already belong to a
	// dashboard user (ACTIVE or DISABLED, any merchant — Phase 8 keeps the
	// one-email-one-merchant model).
	if _, err := s.userRepo.GetByEmail(ctx, email); err == nil {
		return nil, ErrEmailAlreadyExists
	} else if !errors.Is(err, repository.ErrMerchantUserNotFound) {
		return nil, fmt.Errorf("create invitation: check email: %w", err)
	}

	// Lazily expire a stale PENDING invitation for this email so it no longer
	// occupies the one-active-invitation-per-email slot.
	if _, err := s.invRepo.ExpireStalePending(ctx, callerMerchantID, email); err != nil {
		return nil, fmt.Errorf("create invitation: expire stale: %w", err)
	}

	// One active invitation per (merchant, email).
	if existing, err := s.invRepo.GetPendingByMerchantEmail(ctx, callerMerchantID, email); err != nil {
		if !errors.Is(err, repository.ErrInvitationNotFound) {
			return nil, fmt.Errorf("create invitation: check pending: %w", err)
		}
	} else if existing != nil {
		return nil, ErrInvitationAlreadyPending
	}

	// Opaque 32-byte token (hex) — same generator as refresh tokens.
	// Only its SHA-256 hash is persisted; the plaintext is returned once.
	token, err := generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("create invitation: generate token: %w", err)
	}

	now := time.Now().UTC()
	inv := &model.MerchantInvitation{
		ID:         uuid.New(),
		MerchantID: callerMerchantID, // from the authenticated caller — never client-supplied
		Email:      email,
		Role:       req.Role,
		TokenHash:  hashRefreshToken(token),
		Status:     model.InvitationStatusPending,
		ExpiresAt:  now.Add(s.tokenTTL),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// ── Phase 8C.3A — render the email BEFORE the transaction ──────────────
	// The email is a pure build (no network, no DB): URL assembly then
	// template rendering, both using the ONE token generated above. Building
	// before the transaction means the outbox row commits atomically with
	// the invitation — a build failure (only possible if config escaped
	// startup validation) aborts BEFORE any row is written, so nothing is
	// ever half-created. Neither error text may contain the token
	// (buildInvitationURL builds URLs token-last).
	invitationURL, err := buildInvitationURL(s.dashboardBaseURL, token)
	if err != nil {
		return nil, fmt.Errorf("create invitation: build url: %w", err)
	}
	msg, err := buildInvitationEmail(
		merchant.Name,
		inv.Email, // recipient is the invitation record's address — never client-selectable
		inv.Role,
		invitationURL,
		inv.ExpiresAt,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("create invitation: build email: %w", err)
	}
	entry := newInvitationEmailOutbox(inv, msg, now)

	// The audit event is explicit and contains only IDs/role. In particular,
	// the plaintext token, token hash, email, and rendered outbox body are not
	// eligible metadata fields.
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionInvitationCreated,
		audit.TargetInvitation,
		audit.UUIDPtr(inv.ID),
		audit.UUIDPtr(callerMerchantID),
		map[string]any{
			"invitation_id": inv.ID,
			"role":          inv.Role,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create invitation: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)

	// ONE transaction: INSERT invitation + INSERT email_outbox → COMMIT.
	// Any error rolls back BOTH rows (the repository owns the tx;
	// EmailSender is not involved at all — SMTP is out of the HTTP request
	// path and delivery is the Phase 8C.3B worker's job).
	if err := s.invRepo.CreateWithEmailOutbox(ctx, inv, entry); err != nil {
		if errors.Is(err, repository.ErrInvitationAlreadyPending) {
			// Concurrent invite for the same email won the race — full
			// rollback leaves no orphan outbox row, mapped to 409.
			return nil, ErrInvitationAlreadyPending
		}
		if errors.Is(err, repository.ErrMerchantInactive) {
			return nil, ErrMerchantNotActive
		}
		return nil, fmt.Errorf("create invitation: persist: %w", err)
	}

	slog.Info("invitation created",
		slog.String("invitation_id", inv.ID.String()),
		slog.String("merchant_id", callerMerchantID.String()),
		slog.String("role", string(inv.Role)),
		slog.Time("expires_at", inv.ExpiresAt),
		// the plaintext token, email, and outbox bodies are NEVER logged
	)

	resp := model.CreateInvitationResponse{
		ID:         inv.ID,
		MerchantID: inv.MerchantID,
		Email:      inv.Email,
		Role:       inv.Role,
		Status:     inv.Status,
		ExpiresAt:  inv.ExpiresAt,
		CreatedAt:  inv.CreatedAt,
		Token:      token, // one-time disclosure for link construction
	}

	return &resp, nil
}

// GetInvitationByToken returns public metadata for a valid PENDING token.
func (s *invitationService) GetInvitationByToken(ctx context.Context, token string) (*model.InvitationMetadataResponse, error) {
	inv, err := s.loadInvitation(ctx, token)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// Expired PENDING → lazily mark EXPIRED (best-effort) and treat as invalid.
	if inv.IsPending() && inv.IsExpired(now) {
		s.markExpiredBestEffort(ctx, inv)
		return nil, ErrInvitationNotFound
	}
	// ACCEPTED / EXPIRED / REVOKED — all deliberately look the same from
	// outside so a token probe cannot distinguish these states.
	if !inv.IsPending() {
		return nil, ErrInvitationNotFound
	}

	merchant, err := s.merchantRepo.GetByID(ctx, inv.MerchantID)
	if err != nil {
		// FK cascade means a missing merchant cannot coexist with an
		// invitation row — treat defensively as invalid.
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, ErrInvitationNotFound
		}
		return nil, fmt.Errorf("get invitation: merchant: %w", err)
	}

	return &model.InvitationMetadataResponse{
		Email:        inv.Email,
		Role:         inv.Role,
		MerchantName: merchant.Name,
		Status:       inv.Status,
		ExpiresAt:    inv.ExpiresAt,
	}, nil
}

// AcceptInvitation creates the user and consumes the token atomically.
func (s *invitationService) AcceptInvitation(
	ctx context.Context,
	token string,
	req model.AcceptInvitationRequest,
) (*model.DashboardUserResponse, error) {
	inv, err := s.loadInvitation(ctx, token)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// Replay of an already-accepted token → 409 (audit R5), not a 404, so the
	// client can tell the invitation was consumed.
	if inv.Status == model.InvitationStatusAccepted {
		return nil, ErrInvitationAlreadyAccepted
	}

	// Expired (lazily transition) or revoked → invalid, indistinguishable.
	if inv.IsPending() && inv.IsExpired(now) {
		s.markExpiredBestEffort(ctx, inv)
		return nil, ErrInvitationNotFound
	}
	if !inv.IsPending() {
		return nil, ErrInvitationNotFound
	}

	// Merchant lifecycle: the invitation may have been created while ACTIVE
	// and accepted later. Acceptance is only allowed while ACTIVE.
	merchant, err := s.merchantRepo.GetByID(ctx, inv.MerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, ErrInvitationNotFound
		}
		return nil, fmt.Errorf("accept invitation: merchant: %w", err)
	}
	if !merchant.IsActive() {
		return nil, ErrMerchantNotActive
	}

	// Global email uniqueness re-check at acceptance time (the email may have
	// been registered since the invitation was created).
	if _, err := s.userRepo.GetByEmail(ctx, inv.Email); err == nil {
		return nil, ErrEmailAlreadyExists
	} else if !errors.Is(err, repository.ErrMerchantUserNotFound) {
		return nil, fmt.Errorf("accept invitation: check email: %w", err)
	}

	// Password policy — identical to ChangePassword / CreateUser.
	// The error never echoes the password.
	if len(req.Password) < passwordMinLen || len(req.Password) > passwordMaxLen {
		return nil, ErrInvalidPassword
	}

	// Argon2id hashing is intentionally OUTSIDE the acceptance transaction —
	// it is expensive and must not hold row locks while hashing.
	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		return nil, fmt.Errorf("accept invitation: hash password: %w", err)
	}

	acceptedAt := time.Now().UTC()
	user := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   inv.MerchantID, // from the invitation row — never client-supplied
		Email:        inv.Email,
		PasswordHash: passwordHash,
		Role:         inv.Role,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    acceptedAt,
		UpdatedAt:    acceptedAt,
	}

	// Acceptance is unauthenticated: the invitee has no existing dashboard
	// identity. Record the resulting user ID, never the bearer token.
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorTypeUnauthenticated})
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionInvitationAccepted,
		audit.TargetInvitation,
		audit.UUIDPtr(inv.ID),
		audit.UUIDPtr(inv.MerchantID),
		map[string]any{
			"invitation_id": inv.ID,
			"user_id":       user.ID,
			"role":          user.Role,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("accept invitation: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)

	// Atomic single-use claim + user insert (one transaction).
	if err := s.invRepo.Accept(ctx, inv.ID, user); err != nil {
		switch {
		case errors.Is(err, repository.ErrInvitationAlreadyAccepted):
			return nil, ErrInvitationAlreadyAccepted
		case errors.Is(err, repository.ErrInvitationNotFound):
			return nil, ErrInvitationNotFound
		case errors.Is(err, repository.ErrMerchantInactive):
			return nil, ErrMerchantNotActive
		case errors.Is(err, repository.ErrMerchantUserEmailExists):
			// Acceptance race: someone registered the email first. The
			// transaction rolled back — the invitation stays PENDING.
			return nil, ErrEmailAlreadyExists
		default:
			return nil, fmt.Errorf("accept invitation: persist: %w", err)
		}
	}

	slog.Info("invitation accepted",
		slog.String("invitation_id", inv.ID.String()),
		slog.String("user_id", user.ID.String()),
		slog.String("merchant_id", user.MerchantID.String()),
		slog.String("role", string(user.Role)),
		// the email and plaintext password are NEVER logged
	)

	resp := toUserResponse(user)
	return &resp, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// loadInvitation resolves a plaintext token to its invitation row.
// Only lookup errors are classified here — state checks (pending/expiry/
// replay) differ between Get and Accept and are done by the caller.
func (s *invitationService) loadInvitation(ctx context.Context, token string) (*model.MerchantInvitation, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvitationNotFound
	}
	inv, err := s.invRepo.GetByTokenHash(ctx, hashRefreshToken(token))
	if err != nil {
		if errors.Is(err, repository.ErrInvitationNotFound) {
			return nil, ErrInvitationNotFound
		}
		return nil, fmt.Errorf("load invitation: %w", err)
	}
	return inv, nil
}

// markExpiredBestEffort lazily persists the EXPIRED transition when an
// expired PENDING invitation is encountered. Failures are logged, not fatal —
// the expiry check itself already succeeded in memory.
func (s *invitationService) markExpiredBestEffort(ctx context.Context, inv *model.MerchantInvitation) {
	if err := s.invRepo.MarkExpired(ctx, inv.ID); err != nil {
		slog.Warn("invitation: failed to persist lazy expiry",
			slog.String("invitation_id", inv.ID.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	inv.Status = model.InvitationStatusExpired
}
