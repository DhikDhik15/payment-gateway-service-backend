package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	UpdateUserStatus(
		ctx context.Context,
		callerUserID uuid.UUID,
		callerRole model.DashboardUserRole,
		callerMerchantID uuid.UUID,
		targetUserID uuid.UUID,
		status model.DashboardUserStatus,
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

	// Load target user.
	target, err := s.userRepo.GetByID(ctx, targetUserID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			return nil, ErrDashboardUserNotFound
		}
		return nil, fmt.Errorf("update user status: get user: %w", err)
	}

	// Merchant isolation: target must belong to the caller's merchant.
	if target.MerchantID != callerMerchantID {
		return nil, ErrCrossmerchantAccess
	}

	// Persist the status change.
	if err := s.userRepo.UpdateStatus(ctx, targetUserID, status); err != nil {
		if errors.Is(err, repository.ErrMerchantUserNotFound) {
			return nil, ErrDashboardUserNotFound
		}
		return nil, fmt.Errorf("update user status: persist: %w", err)
	}

	// Disabling a user must revoke all refresh sessions so logout is immediate
	// for refresh/rotation; short-lived access JWTs still expire via TTL and
	// are rejected by RequireDashboardAuth once status is DISABLED.
	if status == model.DashboardUserStatusDisabled && s.sessionRepo != nil {
		if err := s.sessionRepo.DeleteByUserID(ctx, targetUserID); err != nil {
			slog.Warn("dashboard user: failed to revoke sessions after disable",
				slog.String("target_user_id", targetUserID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	slog.Info("dashboard user status changed",
		slog.String("target_user_id", targetUserID.String()),
		slog.String("new_status", string(status)),
		slog.String("caller_user_id", callerUserID.String()),
		slog.String("merchant_id", callerMerchantID.String()),
	)

	// Return fresh DTO with updated status.
	target.Status = status
	target.UpdatedAt = time.Now().UTC()
	resp := toUserResponse(target)
	return &resp, nil
}
