package model

import (
	"time"

	"github.com/google/uuid"
)

// MerchantAPIKeyStatus represents the lifecycle state of a merchant API key.
type MerchantAPIKeyStatus string

const (
	MerchantAPIKeyStatusActive  MerchantAPIKeyStatus = "ACTIVE"
	MerchantAPIKeyStatusRevoked MerchantAPIKeyStatus = "REVOKED"
)

// MerchantAPIKey is the domain entity for a named, revocable merchant API key.
// It maps 1-to-1 to the `merchant_api_keys` database table (migration 000008).
//
// Security invariants:
//   - SecretHash is a salted Argon2id hash — the plaintext secret is never stored.
//   - The plaintext secret is only returned once: at creation or rotation time.
//   - SecretHash must never be included in any API response DTO.
type MerchantAPIKey struct {
	ID         uuid.UUID            `db:"id"`
	MerchantID uuid.UUID            `db:"merchant_id"`
	KeyID      string               `db:"key_id"`      // public identifier: "pk_<hex>"
	SecretHash string               `db:"secret_hash"` // Argon2id hash — never returned in responses
	Name       string               `db:"name"`
	Status     MerchantAPIKeyStatus `db:"status"`
	LastUsedAt *time.Time           `db:"last_used_at"`
	ExpiresAt  *time.Time           `db:"expires_at"`
	RevokedAt  *time.Time           `db:"revoked_at"`
	CreatedAt  time.Time            `db:"created_at"`
	UpdatedAt  time.Time            `db:"updated_at"`
}

// IsActive returns true when the key may be used for authentication.
// A key is rejected when:
//   - Status is REVOKED
//   - ExpiresAt is set and is in the past
func (k *MerchantAPIKey) IsActive() bool {
	if k.Status != MerchantAPIKeyStatusActive {
		return false
	}
	if k.ExpiresAt != nil && time.Now().UTC().After(*k.ExpiresAt) {
		return false
	}
	return true
}

// ─── Request / Response DTOs ─────────────────────────────────────────────────

// CreateMerchantAPIKeyRequest is the validated input for key creation.
type CreateMerchantAPIKeyRequest struct {
	Name      string     `json:"name"       binding:"required,min=1,max=100"`
	ExpiresAt *time.Time `json:"expires_at"` // optional; must be in the future if supplied
}

// MerchantAPIKeyResponse is the safe DTO returned for list and individual
// key responses. It must NEVER include SecretHash or the plaintext secret.
type MerchantAPIKeyResponse struct {
	ID         uuid.UUID            `json:"id"`
	MerchantID uuid.UUID            `json:"merchant_id"`
	Name       string               `json:"name"`
	KeyID      string               `json:"key_id"`
	Status     MerchantAPIKeyStatus `json:"status"`
	ExpiresAt  *time.Time           `json:"expires_at,omitempty"`
	RevokedAt  *time.Time           `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time           `json:"last_used_at,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

// CreateMerchantAPIKeyResponse is returned only at key creation time.
// The Secret field contains the plaintext credential that the caller must
// store securely — it will never be returned again.
type CreateMerchantAPIKeyResponse struct {
	ID         uuid.UUID            `json:"id"`
	MerchantID uuid.UUID            `json:"merchant_id"`
	Name       string               `json:"name"`
	KeyID      string               `json:"key_id"`
	Secret     string               `json:"secret"` // plaintext — returned ONCE only
	Status     MerchantAPIKeyStatus `json:"status"`
	ExpiresAt  *time.Time           `json:"expires_at,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
}

// RotateMerchantAPIKeyResponse is returned only at rotation time.
// OldKeyID identifies what was revoked; the new Secret is the one-time credential.
type RotateMerchantAPIKeyResponse struct {
	ID         uuid.UUID            `json:"id"`
	MerchantID uuid.UUID            `json:"merchant_id"`
	Name       string               `json:"name"`
	KeyID      string               `json:"key_id"`
	Secret     string               `json:"secret"` // new plaintext — returned ONCE only
	Status     MerchantAPIKeyStatus `json:"status"`
	ExpiresAt  *time.Time           `json:"expires_at,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
}
