package model

import (
	"time"

	"github.com/google/uuid"
)

// Password-reset token lifecycle (forgot-password flow, Phase 8E).
//
// Only the SHA-256 hex of the opaque bearer token is persisted. Status
// transitions: PENDING → CONSUMED (successful reset), PENDING → REVOKED
// (superseded by a newer request or cleared after a successful reset).
// EXPIRED is a read-time classification (expires_at in the past); rows are
// removed by periodic cleanup, never required for correctness.
type PasswordResetTokenStatus string

const (
	PasswordResetTokenPending  PasswordResetTokenStatus = "PENDING"
	PasswordResetTokenConsumed PasswordResetTokenStatus = "CONSUMED"
	PasswordResetTokenExpired  PasswordResetTokenStatus = "EXPIRED"
	PasswordResetTokenRevoked  PasswordResetTokenStatus = "REVOKED"
)

// PasswordResetToken maps to password_reset_tokens.
type PasswordResetToken struct {
	ID         uuid.UUID                `db:"id"`
	UserID     uuid.UUID                `db:"user_id"`
	TokenHash  string                   `db:"token_hash"`
	Status     PasswordResetTokenStatus `db:"status"`
	ExpiresAt  time.Time                `db:"expires_at"`
	CreatedAt  time.Time                `db:"created_at"`
	UpdatedAt  time.Time                `db:"updated_at"`
	ConsumedAt *time.Time               `db:"consumed_at"`
}

// RequestPasswordResetRequest is the input for the public reset-request
// endpoint. The response is identical whether or not the email maps to an
// eligible account, so this is the only field.
type RequestPasswordResetRequest struct {
	Email string `json:"email" binding:"required,max=254"`
}

// RequestPasswordResetResponse is the generic public response. It never
// reveals whether the email belongs to an account.
type RequestPasswordResetResponse struct {
	Message string `json:"message"`
}

// ConfirmPasswordResetRequest consumes a reset token and sets a new password.
// The old password is intentionally not required.
type ConfirmPasswordResetRequest struct {
	Token    string `json:"token"    binding:"required,min=16,max=256"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// ConfirmPasswordResetResponse confirms the reset without establishing a
// session. The caller must sign in with the new password.
type ConfirmPasswordResetResponse struct {
	Message string `json:"message"`
}
