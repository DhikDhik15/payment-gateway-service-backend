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
	// ErrDashboardUserNotFound is returned when the target user does not exist.
	ErrDashboardUserNotFound = errors.New("dashboard user not found")

	// ErrEmailAlreadyExists is returned when the email is already registered.
	ErrEmailAlreadyExists = errors.New("email already registered")

	// ErrSelfDisable is returned when a user tries to disable their own account.
	ErrSelfDisable = errors.New("cannot disable your own account")

	// ErrInsufficientRole is returned when the caller lacks the required role.
	ErrInsufficientRole = errors.New("insufficient role for this operation")

	// ErrCrossmerchantAccess is returned when a user tries to manage a user
	// from a different merchant.
	ErrCrossmerchantAccess = errors.New("access to other merchant data is not allowed")

	// ErrLastOwnerRequired is returned when an operation would leave the
	// merchant with no active OWNER user.
	ErrLastOwnerRequired = errors.New("operation would leave merchant with no active owner")

	// ErrMerchantInactive is returned when a team mutation races with merchant
	// suspension/deactivation and the database transaction observes the new
	// lifecycle state.
	ErrMerchantInactive = errors.New("merchant account is inactive")

	// ErrInvalidCurrentPassword is returned when the supplied current password
	// does not match the stored hash during a password change.
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")

	// ErrInvalidRole is returned when a requested role is not one of
	// OWNER / ADMIN / VIEWER.
	ErrInvalidRole = errors.New("invalid dashboard user role")

	// ErrInvalidPassword is returned when a new password fails the password
	// policy (min 8, max 128 characters). Never echoes the password itself.
	ErrInvalidPassword = errors.New("invalid password")
)

// Password policy constants — must stay in sync with the binding validation on
// model.ChangePasswordRequest and model.CreateDashboardUserRequest.
const (
	passwordMinLen = 8
	passwordMaxLen = 128
)

// ─── Interface ────────────────────────────────────────────────────────────────

// DashboardUserService manages dashboard user accounts.
type DashboardUserService interface {
	// CreateUser creates a new dashboard user for the given merchant.
	// This is an admin-bootstrap operation; the caller must already hold the
	// admin key (enforced at the handler level via middleware).
	// Returns ErrEmailAlreadyExists for duplicate email.
	// Returns ErrMerchantNotFound when merchantID does not exist.
	CreateUser(ctx context.Context, merchantID uuid.UUID, req model.CreateDashboardUserRequest) (*model.DashboardUserResponse, error)

	// ListUsers returns all dashboard users belonging to merchantID.
	// Caller's merchantID must match (enforced by caller).
	ListUsers(ctx context.Context, merchantID uuid.UUID) ([]model.DashboardUserResponse, error)

	// UpdateUserStatus changes the status of a target user.
	// Authorization rules:
	//   - Only OWNER role may change user status.
	//   - A user cannot disable themselves.
	//   - Target user must belong to the same merchant as the caller.
	//   - The final ACTIVE OWNER of a merchant cannot be disabled
	//     (ErrLastOwnerRequired).
	UpdateUserStatus(
		ctx context.Context,
		callerUserID uuid.UUID,
		callerRole model.DashboardUserRole,
		callerMerchantID uuid.UUID,
		targetUserID uuid.UUID,
		status model.DashboardUserStatus,
	) (*model.DashboardUserResponse, error)

	// UpdateUserRole changes the role of a target user.
	// Authorization rules:
	//   - Only OWNER role may change user roles (ADMIN and VIEWER are rejected).
	//   - Target user must belong to the same merchant as the caller; a
	//     cross-merchant target returns ErrCrossmerchantAccess (mapped to 404
	//     so the existence of other tenants' users is not confirmed).
	//   - All transitions between OWNER / ADMIN / VIEWER are allowed, EXCEPT
	//     when demoting an ACTIVE OWNER would leave the merchant with zero
	//     ACTIVE OWNERs (ErrLastOwnerRequired). An OWNER may demote themselves
	//     only when another ACTIVE OWNER remains.
	//   - No session invalidation is performed: the role is reloaded from the
	//     database on every authenticated request, so the new role takes effect
	//     on the target's very next request.
	UpdateUserRole(
		ctx context.Context,
		callerUserID uuid.UUID,
		callerRole model.DashboardUserRole,
		callerMerchantID uuid.UUID,
		targetUserID uuid.UUID,
		newRole model.DashboardUserRole,
	) (*model.DashboardUserResponse, error)

	// ChangePassword changes the calling user's own password.
	// The caller identity MUST come from the authenticated session/JWT —
	// user_id is never accepted from the request body.
	// Rules:
	//   - Caller must be ACTIVE (ErrUserDisabled — defense in depth; the
	//     RequireDashboardAuth middleware already rejects disabled users).
	//   - current_password must verify against the stored Argon2id hash
	//     (ErrInvalidCurrentPassword).
	//   - new_password must satisfy the password policy (ErrInvalidPassword).
	//   - On success all refresh sessions of the caller are revoked
	//     (DeleteByUserID) so no stale refresh token survives the change; the
	//     caller must log in again to obtain a new session. The short-lived
	//     access token of the in-flight request remains valid until its TTL.
	ChangePassword(
		ctx context.Context,
		callerUserID uuid.UUID,
		req model.ChangePasswordRequest,
	) (*model.DashboardUserResponse, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type dashboardUserService struct {
	userRepo     repository.MerchantUserRepository
	sessionRepo  repository.DashboardSessionRepository
	merchantRepo repository.MerchantRepository
}

// NewDashboardUserService constructs a DashboardUserService.
// sessionRepo is used to revoke refresh sessions when a user is DISABLED.
func NewDashboardUserService(
	userRepo repository.MerchantUserRepository,
	sessionRepo repository.DashboardSessionRepository,
	merchantRepo repository.MerchantRepository,
) DashboardUserService {
	return &dashboardUserService{
		userRepo:     userRepo,
		sessionRepo:  sessionRepo,
		merchantRepo: merchantRepo,
	}
}

// CreateUser creates a new dashboard user after verifying the merchant exists.
func (s *dashboardUserService) CreateUser(ctx context.Context, merchantID uuid.UUID, req model.CreateDashboardUserRequest) (*model.DashboardUserResponse, error) {
	// Verify merchant exists.
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, repository.ErrMerchantNotFound
		}
		return nil, fmt.Errorf("create dashboard user: verify merchant: %w", err)
	}

	// Normalise email.
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if err := validateEmail(email); err != nil {
		return nil, ErrInvalidEmail
	}

	// Hash password — never store plaintext.
	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		return nil, fmt.Errorf("create dashboard user: hash password: %w", err)
	}

	now := time.Now().UTC()
	user := &model.MerchantUser{
		ID:           uuid.New(),
		MerchantID:   merchantID,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         req.Role,
		Status:       model.DashboardUserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionUserCreated,
		audit.TargetUser,
		audit.UUIDPtr(user.ID),
		audit.UUIDPtr(merchantID),
		map[string]any{
			"role": req.Role,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create dashboard user: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)
	if err := s.userRepo.Create(ctx, user); err != nil {
		if errors.Is(err, repository.ErrMerchantUserEmailExists) {
			return nil, ErrEmailAlreadyExists
		}
		return nil, fmt.Errorf("create dashboard user: persist: %w", err)
	}

	slog.Info("dashboard user created",
		slog.String("user_id", user.ID.String()),
		slog.String("merchant_id", merchantID.String()),
		slog.String("role", string(req.Role)),
		// email is logged at domain-level only to avoid leaking PII in aggregate logs
	)

	resp := toUserResponse(user)
	return &resp, nil
}

// ListUsers returns all users for a merchant.
func (s *dashboardUserService) ListUsers(ctx context.Context, merchantID uuid.UUID) ([]model.DashboardUserResponse, error) {
	users, err := s.userRepo.ListByMerchant(ctx, merchantID)
	if err != nil {
		return nil, fmt.Errorf("list dashboard users: %w", err)
	}

	out := make([]model.DashboardUserResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
	}
	return out, nil
}

// mapOwnerMutationError translates repository transaction sentinels to the
// existing service/API errors without exposing SQL or lock details.
func mapOwnerMutationError(err error) error {
	switch {
	case errors.Is(err, repository.ErrLastActiveOwnerRequired):
		return ErrLastOwnerRequired
	case errors.Is(err, repository.ErrMerchantInactive):
		return ErrMerchantInactive
	case errors.Is(err, repository.ErrMerchantUserCrossTenant):
		return ErrCrossmerchantAccess
	case errors.Is(err, repository.ErrMerchantUserNotFound), errors.Is(err, repository.ErrMerchantNotFound):
		return ErrDashboardUserNotFound
	default:
		return err
	}
}

// UpdateUserStatus changes the status of a dashboard user with authorization checks.
func (s *dashboardUserService) UpdateUserStatus(
	ctx context.Context,
	callerUserID uuid.UUID,
	callerRole model.DashboardUserRole,
	callerMerchantID uuid.UUID,
	targetUserID uuid.UUID,
	status model.DashboardUserStatus,
) (*model.DashboardUserResponse, error) {
	// Authorization: only OWNER may manage user status.
	if callerRole != model.DashboardUserRoleOwner {
		return nil, ErrInsufficientRole
	}

	// Self-disable guard.
	if callerUserID == targetUserID && status == model.DashboardUserStatusDisabled {
		return nil, ErrSelfDisable
	}

	ownerRepo, ok := s.userRepo.(repository.OwnerInvariantRepository)
	if !ok {
		// Fail closed: a check-then-act fallback would reintroduce the TOCTOU
		// race this phase is intended to eliminate.
		return nil, fmt.Errorf("update user status: transactional owner repository unavailable")
	}

	// Capture the previous value for safe audit metadata. The repository still
	// re-reads and locks the target inside the mutation transaction; this read is
	// only contextual and never participates in the OWNER decision.
	previous, err := s.userRepo.GetByID(ctx, targetUserID)
	if err != nil {
		if mapped := mapOwnerMutationError(err); mapped != err {
			return nil, mapped
		}
		return nil, fmt.Errorf("update user status: get target: %w", err)
	}
	if previous.MerchantID != callerMerchantID {
		return nil, ErrCrossmerchantAccess
	}
	if previous.Status == status {
		response := toUserResponse(previous)
		return &response, nil
	}
	if status == model.DashboardUserStatusDisabled {
		if _, transactional := s.userRepo.(repository.TransactionalUserStatusRepository); !transactional && s.sessionRepo == nil {
			return nil, fmt.Errorf("update user status: session revocation repository unavailable")
		}
	}
	ctx = audit.WithActor(ctx, audit.Actor{
		Type:       audit.ActorTypeDashboardUser,
		UserID:     audit.UUIDPtr(callerUserID),
		MerchantID: audit.UUIDPtr(callerMerchantID),
	})
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionUserStatusChanged,
		audit.TargetUser,
		audit.UUIDPtr(targetUserID),
		audit.UUIDPtr(callerMerchantID),
		map[string]any{
			"old_status": previous.Status,
			"new_status": status,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("update user status: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)

	// PostgreSQL path: merchant row lock -> target re-read/count -> update,
	// session revocation, audit, and COMMIT are one transaction. No pre-lock
	// count is used.
	var target *model.MerchantUser
	if status == model.DashboardUserStatusDisabled {
		if transactional, ok := s.userRepo.(repository.TransactionalUserStatusRepository); ok {
			target, err = transactional.UpdateStatusWithOwnerLockAndRevokeSessions(ctx, callerMerchantID, targetUserID, status)
		} else {
			target, err = ownerRepo.UpdateStatusWithOwnerLock(ctx, callerMerchantID, targetUserID, status)
		}
	} else {
		target, err = ownerRepo.UpdateStatusWithOwnerLock(ctx, callerMerchantID, targetUserID, status)
	}
	if err != nil {
		if mapped := mapOwnerMutationError(err); mapped != err {
			return nil, mapped
		}
		return nil, fmt.Errorf("update user status: persist: %w", err)
	}

	// Compatibility fallback for non-PostgreSQL/test repositories. Production
	// PostgreSQL performs this DELETE inside updateWithOwnerLock's transaction.
	if status == model.DashboardUserStatusDisabled && s.sessionRepo != nil {
		if _, transactional := s.userRepo.(repository.TransactionalUserStatusRepository); !transactional {
			if revokeErr := s.sessionRepo.DeleteByUserID(ctx, targetUserID); revokeErr != nil {
				return nil, fmt.Errorf("update user status: revoke sessions: %w", revokeErr)
			}
		}
	}

	slog.Info("dashboard user status changed",
		slog.String("target_user_id", targetUserID.String()),
		slog.String("new_status", string(status)),
		slog.String("caller_user_id", callerUserID.String()),
		slog.String("merchant_id", callerMerchantID.String()),
	)

	resp := toUserResponse(target)
	return &resp, nil
}

// UpdateUserRole changes the role of a dashboard user with authorization checks.
func (s *dashboardUserService) UpdateUserRole(
	ctx context.Context,
	callerUserID uuid.UUID,
	callerRole model.DashboardUserRole,
	callerMerchantID uuid.UUID,
	targetUserID uuid.UUID,
	newRole model.DashboardUserRole,
) (*model.DashboardUserResponse, error) {
	// Authorization: only OWNER may manage member roles.
	if callerRole != model.DashboardUserRoleOwner {
		return nil, ErrInsufficientRole
	}

	// Role must be a recognised value (defense in depth — the handler binding
	// already enforces oneof=OWNER ADMIN VIEWER).
	if !newRole.IsValid() {
		return nil, ErrInvalidRole
	}

	ownerRepo, ok := s.userRepo.(repository.OwnerInvariantRepository)
	if !ok {
		// Fail closed: a check-then-act fallback would reintroduce the TOCTOU
		// race this phase is intended to eliminate.
		return nil, fmt.Errorf("update user role: transactional owner repository unavailable")
	}

	previous, err := s.userRepo.GetByID(ctx, targetUserID)
	if err != nil {
		if mapped := mapOwnerMutationError(err); mapped != err {
			return nil, mapped
		}
		return nil, fmt.Errorf("update user role: get target: %w", err)
	}
	if previous.MerchantID != callerMerchantID {
		return nil, ErrCrossmerchantAccess
	}
	if previous.Role == newRole {
		response := toUserResponse(previous)
		return &response, nil
	}
	ctx = audit.WithActor(ctx, audit.Actor{
		Type:       audit.ActorTypeDashboardUser,
		UserID:     audit.UUIDPtr(callerUserID),
		MerchantID: audit.UUIDPtr(callerMerchantID),
	})
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionUserRoleChanged,
		audit.TargetUser,
		audit.UUIDPtr(targetUserID),
		audit.UUIDPtr(callerMerchantID),
		map[string]any{
			"old_role": previous.Role,
			"new_role": newRole,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("update user role: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)

	// The role mutation and last-OWNER decision share the same merchant row
	// lock and transaction; this path is used by PostgreSQL.
	target, err := ownerRepo.UpdateRoleWithOwnerLock(ctx, callerMerchantID, targetUserID, newRole)
	if err != nil {
		if mapped := mapOwnerMutationError(err); mapped != err {
			return nil, mapped
		}
		return nil, fmt.Errorf("update user role: persist: %w", err)
	}

	// Deliberately NO session invalidation here: RequireDashboardAuth reloads
	// the user (including role) from the database on every request, so the new
	// role takes effect on the target's next request without revoking sessions.
	slog.Info("dashboard user role changed",
		slog.String("target_user_id", targetUserID.String()),
		slog.String("new_role", string(newRole)),
		slog.String("caller_user_id", callerUserID.String()),
		slog.String("merchant_id", callerMerchantID.String()),
	)

	resp := toUserResponse(target)
	return &resp, nil
}

// ChangePassword changes the calling user's own password.
// The caller identity is taken exclusively from the authenticated context —
// there is no way to target another user from this method.
func (s *dashboardUserService) ChangePassword(
	ctx context.Context,
	callerUserID uuid.UUID,
	req model.ChangePasswordRequest,
) (*model.DashboardUserResponse, error) {
	// Load the authenticated caller.
	user, err := s.userRepo.GetByID(ctx, callerUserID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			return nil, ErrDashboardUserNotFound
		}
		return nil, fmt.Errorf("change password: get user: %w", err)
	}

	// Defense in depth: a DISABLED user must not be able to change their
	// password even if a request somehow bypasses RequireDashboardAuth.
	if !user.IsActive() {
		return nil, ErrUserDisabled
	}

	// New password must satisfy the existing password policy.
	// This also rejects empty passwords. The error never echoes the password.
	if len(req.NewPassword) < passwordMinLen || len(req.NewPassword) > passwordMaxLen {
		return nil, ErrInvalidPassword
	}

	// Verify the current password against the stored Argon2id hash.
	// verifyPassword is constant-time and never logs the plaintext.
	if !verifyPassword(req.CurrentPassword, user.PasswordHash) {
		slog.Info("dashboard password change: current password mismatch",
			slog.String("user_id", user.ID.String()),
			// passwords are NEVER logged
		)
		return nil, ErrInvalidCurrentPassword
	}

	if _, transactional := s.userRepo.(repository.TransactionalPasswordUpdateRepository); !transactional && s.sessionRepo == nil {
		return nil, fmt.Errorf("change password: session revocation repository unavailable")
	}

	// Hash the new password — never store plaintext.
	newHash, err := hashPassword(req.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("change password: hash password: %w", err)
	}

	ctx = audit.WithActor(ctx, audit.Actor{
		Type:       audit.ActorTypeDashboardUser,
		UserID:     audit.UUIDPtr(callerUserID),
		MerchantID: audit.UUIDPtr(user.MerchantID),
	})
	event, err := audit.NewEventFromContext(
		ctx,
		audit.ActionPasswordChanged,
		audit.TargetUser,
		audit.UUIDPtr(callerUserID),
		audit.UUIDPtr(user.MerchantID),
		map[string]any{
			"target_user_id": callerUserID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("change password: construct audit event: %w", err)
	}
	ctx = audit.WithEvent(ctx, event)
	if transactional, ok := s.userRepo.(repository.TransactionalPasswordUpdateRepository); ok {
		// PostgreSQL changes the password, revokes every refresh session, and
		// records the audit event in one transaction. A session-revocation or
		// audit failure therefore rolls back the password change as well.
		if err := transactional.UpdatePasswordHashAndRevokeSessions(ctx, callerUserID, newHash); err != nil {
			if errors.Is(err, repository.ErrMerchantUserNotFound) {
				return nil, ErrDashboardUserNotFound
			}
			return nil, fmt.Errorf("change password: persist and revoke sessions: %w", err)
		}
	} else {
		if err := s.userRepo.UpdatePasswordHash(ctx, callerUserID, newHash); err != nil {
			if errors.Is(err, repository.ErrMerchantUserNotFound) {
				return nil, ErrDashboardUserNotFound
			}
			return nil, fmt.Errorf("change password: persist: %w", err)
		}
		// Compatibility fallback for non-PostgreSQL/test repositories. Do not
		// report success if revocation fails: an old refresh token must not be
		// silently left usable after a password change.
		if s.sessionRepo != nil {
			if err := s.sessionRepo.DeleteByUserID(ctx, callerUserID); err != nil {
				return nil, fmt.Errorf("change password: revoke sessions: %w", err)
			}
		}
	}

	slog.Info("dashboard password changed",
		slog.String("user_id", callerUserID.String()),
		slog.String("merchant_id", user.MerchantID.String()),
		// the plaintext passwords are NEVER logged
	)

	user.UpdatedAt = time.Now().UTC()
	resp := toUserResponse(user)
	return &resp, nil
}
