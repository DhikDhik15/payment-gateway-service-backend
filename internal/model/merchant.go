package model

import (
	"time"

	"github.com/google/uuid"
)

// MerchantStatus represents the lifecycle state of a merchant account.
type MerchantStatus string

const (
	MerchantStatusActive    MerchantStatus = "ACTIVE"
	MerchantStatusInactive  MerchantStatus = "INACTIVE"
	MerchantStatusSuspended MerchantStatus = "SUSPENDED"
)

// IsValid returns true when the status is one of the recognised values.
func (s MerchantStatus) IsValid() bool {
	switch s {
	case MerchantStatusActive, MerchantStatusInactive, MerchantStatusSuspended:
		return true
	}
	return false
}

// CanTransitionTo reports whether a lifecycle transition is permitted by the
// merchant state machine. The same rule is used by the service pre-check and
// by the PostgreSQL row-locked mutation, so a concurrent request cannot bypass
// the transition guard with a stale read.
func (s MerchantStatus) CanTransitionTo(next MerchantStatus) bool {
	switch s {
	case MerchantStatusActive:
		return next == MerchantStatusSuspended || next == MerchantStatusInactive
	case MerchantStatusSuspended, MerchantStatusInactive:
		return next == MerchantStatusActive
	default:
		return false
	}
}

// Merchant is the core domain entity for a registered merchant.
// It maps 1-to-1 to the `merchants` database table.
//
// Phase 8D.3 security invariants:
//   - APIKey is the legacy plaintext credential (migration 000001). It is only
//     ever READ for authentication — creation of new values is frozen. An empty
//     value (NULL in the database) means no legacy credential exists.
//   - APISecret is a SHA-256 hash and is never serialised.
//   - Neither credential nor its migration state is ever serialised to JSON.
type Merchant struct {
	ID        uuid.UUID      `db:"id"         json:"id"`
	Name      string         `db:"name"       json:"name"`
	Code      string         `db:"code"       json:"code"`
	APIKey    string         `db:"api_key"    json:"-"` // legacy plaintext — never serialised
	APISecret string         `db:"api_secret" json:"-"` // hash — never serialised
	Status    MerchantStatus `db:"status"     json:"status"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt time.Time      `db:"updated_at" json:"updated_at"`
	// LegacyCredentialState is the Phase 8D.3 migration state of the row-level
	// legacy credential (LEGACY | MIGRATED | LEGACY_DISABLED).
	LegacyCredentialState LegacyCredentialState `db:"legacy_credential_state" json:"-"`
	// LegacyCredentialDisabledAt records when LEGACY_DISABLED was first reached.
	LegacyCredentialDisabledAt *time.Time `db:"legacy_credential_disabled_at" json:"-"`
}

// IsActive returns true when the merchant is allowed to process payments.
func (m *Merchant) IsActive() bool {
	return m.Status == MerchantStatusActive
}

// ─── Request / Response DTOs ─────────────────────────────────────────────────

// CreateMerchantRequest is the validated input for the create-merchant endpoint.
type CreateMerchantRequest struct {
	Name string `json:"name" binding:"required,min=2,max=150"`
	Code string `json:"code" binding:"required,min=2,max=50"`
}

// CreateMerchantResponse is what we return after a merchant is created.
//
// Phase 8D.3: creation of new legacy plaintext credentials is FROZEN, so this
// endpoint always fails with 409 LEGACY_CREDENTIAL_CREATION_DISABLED and this
// DTO is never produced in practice. No API key field exists here — legacy
// credentials are never issued again. Use the Phase 5C dashboard API keys or
// POST /api/v1/admin/onboarding/merchants instead.
type CreateMerchantResponse struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	Code      string         `json:"code"`
	Status    MerchantStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
}

// GetMerchantResponse is what we return when fetching a merchant by ID.
// api_key and api_secret are never exposed here.
type GetMerchantResponse struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	Code      string         `json:"code"`
	Status    MerchantStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	// LegacyCredentialState reports the Phase 8D.3 migration state so clients
	// can prompt migration; it is a state label, not a credential.
	LegacyCredentialState LegacyCredentialState `json:"legacy_credential_state"`
	// LegacyCredentialDisabledAt is set once the legacy credential is disabled.
	LegacyCredentialDisabledAt *time.Time `json:"legacy_credential_disabled_at,omitempty"`
}

// UpdateMerchantStatusRequest is the validated input for
// PATCH /api/v1/admin/merchants/:merchant_id/status.
type UpdateMerchantStatusRequest struct {
	Status MerchantStatus `json:"status" binding:"required"`
}
