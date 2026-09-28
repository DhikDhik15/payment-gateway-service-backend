package model

import (
	"time"

	"github.com/google/uuid"
)

// ContextKeyDashboardUser is the Gin context key under which the authenticated
// dashboard user is stored by the RequireDashboardAuth middleware.
const ContextKeyDashboardUser = "dashboard_user"

// ─── Role ────────────────────────────────────────────────────────────────────

// DashboardUserRole represents the authorization role of a dashboard user.
type DashboardUserRole string

const (
	// DashboardUserRoleOwner has full access: user management, API keys, webhooks,
	// payments, refunds, settlements, reconciliation.
	DashboardUserRoleOwner DashboardUserRole = "OWNER"

	// DashboardUserRoleAdmin has operational access: payments, refunds, config,
	// settlements, reconciliation. Cannot manage users or perform destructive
	// account-level operations.
	DashboardUserRoleAdmin DashboardUserRole = "ADMIN"

	// DashboardUserRoleViewer has read-only access to the dashboard.
	DashboardUserRoleViewer DashboardUserRole = "VIEWER"
)

// IsValid returns true when the role is one of the recognised values.
func (r DashboardUserRole) IsValid() bool {
	switch r {
	case DashboardUserRoleOwner, DashboardUserRoleAdmin, DashboardUserRoleViewer:
		return true
	}
	return false
}

// ─── Status ──────────────────────────────────────────────────────────────────

// DashboardUserStatus represents the lifecycle state of a dashboard user.
type DashboardUserStatus string

const (
	DashboardUserStatusActive   DashboardUserStatus = "ACTIVE"
	DashboardUserStatusDisabled DashboardUserStatus = "DISABLED"
)

// IsValid returns true when the status is one of the recognised values.
func (s DashboardUserStatus) IsValid() bool {
	switch s {
	case DashboardUserStatusActive, DashboardUserStatusDisabled:
		return true
	}
	return false
}

// ─── Entities ─────────────────────────────────────────────────────────────────

// MerchantUser is the domain entity for a dashboard user account.
// It maps 1-to-1 to the merchant_users database table.
//
// Security invariants:
//   - PasswordHash is a salted Argon2id hash — the plaintext password is never stored.
//   - PasswordHash must NEVER be included in any API response DTO (json:"-" tag).
//   - Email is normalised (trimmed + lowercased) before storage and comparison.
type MerchantUser struct {
	ID           uuid.UUID           `db:"id"`
	MerchantID   uuid.UUID           `db:"merchant_id"`
	Email        string              `db:"email"`
	PasswordHash string              `json:"-" db:"password_hash"` // Argon2id hash — never serialised
	Role         DashboardUserRole   `db:"role"`
	Status       DashboardUserStatus `db:"status"`
	LastLoginAt  *time.Time          `db:"last_login_at"`
	CreatedAt    time.Time           `db:"created_at"`
	UpdatedAt    time.Time           `db:"updated_at"`
}

// IsActive returns true when the user may authenticate.
func (u *MerchantUser) IsActive() bool {
	return u.Status == DashboardUserStatusActive
}

// DashboardSession is the domain entity for a persistent dashboard session.
// It maps 1-to-1 to the dashboard_sessions database table.
//
// Security invariants:
//   - RefreshTokenHash is the SHA-256 hex of the opaque refresh token.
//   - The plaintext refresh token is sent to the client once (HttpOnly cookie) and never stored.
//   - RefreshTokenHash must NEVER appear in any API response.
type DashboardSession struct {
	ID               uuid.UUID  `db:"id"`
	MerchantUserID   uuid.UUID  `db:"merchant_user_id"`
	RefreshTokenHash string     `db:"refresh_token_hash"` // SHA-256 hex — never returned
	ExpiresAt        time.Time  `db:"expires_at"`
	CreatedAt        time.Time  `db:"created_at"`
	LastUsedAt       *time.Time `db:"last_used_at"`
}

// IsExpired returns true when the session has passed its expiry time.
func (s *DashboardSession) IsExpired() bool {
	return time.Now().UTC().After(s.ExpiresAt)
}

// ─── Request / Response DTOs ──────────────────────────────────────────────────

// CreateDashboardUserRequest is the input for the admin-bootstrap user creation endpoint.
// POST /api/v1/admin/merchants/:merchant_id/users
//
// Email format is validated after trim+lowercase normalisation in the service layer
// so that whitespace-padded addresses are accepted and stored canonically.
type CreateDashboardUserRequest struct {
	Email    string            `json:"email"    binding:"required,max=254"`
	Password string            `json:"password" binding:"required,min=8,max=128"`
	Role     DashboardUserRole `json:"role"     binding:"required,oneof=OWNER ADMIN VIEWER"`
}

// DashboardUserResponse is the safe DTO returned for all dashboard user endpoints.
// It never includes PasswordHash, and never includes merchant API keys or secrets.
type DashboardUserResponse struct {
	ID          uuid.UUID           `json:"id"`
	MerchantID  uuid.UUID           `json:"merchant_id"`
	Email       string              `json:"email"`
	Role        DashboardUserRole   `json:"role"`
	Status      DashboardUserStatus `json:"status"`
	LastLoginAt *time.Time          `json:"last_login_at,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// LoginRequest is the input for POST /api/v1/auth/login.
// Email format is validated after normalisation in AuthService.
type LoginRequest struct {
	Email    string `json:"email"    binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse is returned for a successful login.
// The refresh token is NOT in the JSON body — it is set as an HttpOnly cookie by the handler.
type LoginResponse struct {
	AccessToken string                `json:"access_token"`
	TokenType   string                `json:"token_type"`
	ExpiresIn   int                   `json:"expires_in"` // seconds until access token expires
	User        DashboardUserResponse `json:"user"`
}

// UpdateUserStatusRequest is the input for PATCH /api/v1/dashboard/users/:user_id/status.
type UpdateUserStatusRequest struct {
	Status DashboardUserStatus `json:"status" binding:"required,oneof=ACTIVE DISABLED"`
}

// UpdateUserRoleRequest is the input for PATCH /api/v1/dashboard/users/:user_id/role.
type UpdateUserRoleRequest struct {
	Role DashboardUserRole `json:"role" binding:"required,oneof=OWNER ADMIN VIEWER"`
}

// ChangePasswordRequest is the input for PATCH /api/v1/dashboard/me/password.
// The caller identity is always derived from the authenticated session — user_id
// is never accepted from the request body.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password"     binding:"required,min=8,max=128"`
}
