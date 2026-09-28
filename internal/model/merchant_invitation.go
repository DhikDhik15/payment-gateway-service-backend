package model

import (
	"time"

	"github.com/google/uuid"
)

// InvitationStatus represents the lifecycle state of a merchant team invitation.
type InvitationStatus string

const (
	InvitationStatusPending  InvitationStatus = "PENDING"
	InvitationStatusAccepted InvitationStatus = "ACCEPTED"
	InvitationStatusExpired  InvitationStatus = "EXPIRED"
	InvitationStatusRevoked  InvitationStatus = "REVOKED"
)

// IsValid returns true when the status is one of the recognised values.
func (s InvitationStatus) IsValid() bool {
	switch s {
	case InvitationStatusPending, InvitationStatusAccepted, InvitationStatusExpired, InvitationStatusRevoked:
		return true
	}
	return false
}

// MerchantInvitation is the domain entity for a Phase 8B team invitation.
// It maps 1-to-1 to the `merchant_user_invitations` database table.
//
// Security: TokenHash is the SHA-256 hex of the opaque invitation token.
// The plaintext token is NEVER stored and is returned only once, at creation.
type MerchantInvitation struct {
	ID         uuid.UUID         `db:"id"          json:"-"`
	MerchantID uuid.UUID         `db:"merchant_id" json:"merchant_id"`
	Email      string            `db:"email"       json:"email"`
	Role       DashboardUserRole `db:"role"        json:"role"`
	TokenHash  string            `db:"token_hash"  json:"-"` // never serialised
	Status     InvitationStatus  `db:"status"      json:"status"`
	ExpiresAt  time.Time         `db:"expires_at"  json:"expires_at"`
	AcceptedAt *time.Time        `db:"accepted_at" json:"accepted_at,omitempty"`
	CreatedAt  time.Time         `db:"created_at"  json:"created_at"`
	UpdatedAt  time.Time         `db:"updated_at"  json:"updated_at"`
}

// IsPending returns true when the invitation is still awaiting acceptance.
// Expiry is time-based and must be checked separately against ExpiresAt.
func (i *MerchantInvitation) IsPending() bool {
	return i.Status == InvitationStatusPending
}

// IsExpired returns true when the invitation's expiry time has passed.
func (i *MerchantInvitation) IsExpired(now time.Time) bool {
	return !i.ExpiresAt.After(now)
}

// ─── Request / Response DTOs ─────────────────────────────────────────────────

// CreateInvitationRequest is the input for POST /api/v1/dashboard/users/invite.
// No merchant_id is accepted — merchant identity comes from the caller.
type CreateInvitationRequest struct {
	Email string            `json:"email" binding:"required,max=254"`
	Role  DashboardUserRole `json:"role"  binding:"required,oneof=OWNER ADMIN VIEWER"`
}

// CreateInvitationResponse is returned ONCE when an invitation is created.
//
// Security:
//   - Token is the one-time disclosure of the plaintext invitation token so an
//     external system/frontend can construct the invitation link. It is NEVER
//     retrievable again — only its SHA-256 hash is stored.
//   - Password/hash fields are never present on any invitation DTO.
type CreateInvitationResponse struct {
	ID         uuid.UUID         `json:"id"`
	MerchantID uuid.UUID         `json:"merchant_id"`
	Email      string            `json:"email"`
	Role       DashboardUserRole `json:"role"`
	Status     InvitationStatus  `json:"status"`
	ExpiresAt  time.Time         `json:"expires_at"`
	CreatedAt  time.Time         `json:"created_at"`
	Token      string            `json:"token"` // ONE-TIME disclosure — never stored
}

// InvitationMetadataResponse is the public, unauthenticated metadata returned
// by GET /api/v1/invitations/:token so an invitee can preview the invitation
// before accepting. It never exposes the token hash or any user records.
type InvitationMetadataResponse struct {
	Email        string            `json:"email"`
	Role         DashboardUserRole `json:"role"`
	MerchantName string            `json:"merchant_name"`
	Status       InvitationStatus  `json:"status"` // always PENDING when returned
	ExpiresAt    time.Time         `json:"expires_at"`
}

// AcceptInvitationRequest is the input for
// POST /api/v1/invitations/:token/accept. The token comes from the URL path;
// only the new password is in the body.
type AcceptInvitationRequest struct {
	Password string `json:"password" binding:"required,min=8,max=128"`
}
