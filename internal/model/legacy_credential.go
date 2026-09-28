package model

import (
	"time"

	"github.com/google/uuid"
)

// LegacyCredentialState is the Phase 8D.3 migration state of a merchant's
// row-level legacy plaintext credential (merchants.api_key / api_secret).
//
// It is stored as merchants.legacy_credential_state (migration 000017) with a
// CHECK constraint over exactly these three values, so the state machine is
// deterministic:
//
//	LEGACY ──migrate──► MIGRATED ──disable──► LEGACY_DISABLED
//	   ▲                    │
//	   └──── new legacy credentials are FROZEN (creation is never allowed).
//
// Semantics:
//   - LEGACY          — the plaintext credential still exists and authenticates
//     (only while LEGACY_API_CREDENTIALS_ENABLED allows the migration window).
//   - MIGRATED        — a canonical Phase 5C credential exists. The legacy key,
//     if present, STILL authenticates until it is explicitly disabled.
//   - LEGACY_DISABLED — the legacy credential never authenticates again.
//     Only reachable from MIGRATED.
//
// A NULL api_key independently means "no legacy credential exists"; auth can
// never match it regardless of state.
type LegacyCredentialState string

const (
	// LegacyCredentialStateLegacy marks an active row-level plaintext credential.
	LegacyCredentialStateLegacy LegacyCredentialState = "LEGACY"
	// LegacyCredentialStateMigrated marks migration to Phase 5C as complete.
	LegacyCredentialStateMigrated LegacyCredentialState = "MIGRATED"
	// LegacyCredentialStateLegacyDisabled marks the legacy credential as
	// permanently unusable for authentication.
	LegacyCredentialStateLegacyDisabled LegacyCredentialState = "LEGACY_DISABLED"
)

// IsValid returns true when the state is one of the recognised values.
func (s LegacyCredentialState) IsValid() bool {
	switch s {
	case LegacyCredentialStateLegacy, LegacyCredentialStateMigrated, LegacyCredentialStateLegacyDisabled:
		return true
	}
	return false
}

// AllowsLegacyAuth returns true when a stored legacy API key may still
// authenticate. It is the single authoritative gate used by the Auth
// middleware's legacy stage, on top of the LEGACY_API_CREDENTIALS_ENABLED
// flag. Any unknown value fails closed.
func (s LegacyCredentialState) AllowsLegacyAuth() bool {
	return s == LegacyCredentialStateLegacy || s == LegacyCredentialStateMigrated
}

// ─── Phase 8D.3 DTOs ─────────────────────────────────────────────────────────

// MigrateLegacyCredentialResponse is returned ONCE by
// POST /api/v1/dashboard/legacy-credential/migrate.
//
// Field conventions reuse the Phase 5C CreateMerchantAPIKeyResponse
// (key_id + secret). Security:
//   - Secret is the Phase 5C plaintext secret, Argon2id-hashed at rest; it is
//     returned only in this response, is never persisted in plaintext, and is
//     never logged.
//   - Calling migrate again returns 409 LEGACY_CREDENTIAL_ALREADY_MIGRATED, so
//     the secret can never be re-read.
type MigrateLegacyCredentialResponse struct {
	ID         uuid.UUID            `json:"id"`
	MerchantID uuid.UUID            `json:"merchant_id"`
	Name       string               `json:"name"`
	KeyID      string               `json:"key_id"` // public "pk_…" identifier
	Secret     string               `json:"secret"` // plaintext — returned ONCE only
	Status     MerchantAPIKeyStatus `json:"status"`
	CreatedAt  time.Time            `json:"created_at"`
	// LegacyCredentialState is MIGRATED after a successful migration.
	LegacyCredentialState LegacyCredentialState `json:"legacy_credential_state"`
	// LegacyDisabled is always false here: the legacy key (if any) keeps
	// working until POST /legacy-credential/disable is called.
	LegacyDisabled bool `json:"legacy_disabled"`
}

// LegacyCredentialStatusResponse is returned by
// POST /api/v1/dashboard/legacy-credential/disable.
// It carries no credentials — only the resulting migration state.
type LegacyCredentialStatusResponse struct {
	LegacyCredentialState LegacyCredentialState `json:"legacy_credential_state"`
	// LegacyCredentialDisabledAt is the first (and only) disable timestamp;
	// repeats never overwrite it.
	LegacyCredentialDisabledAt *time.Time `json:"legacy_credential_disabled_at,omitempty"`
	// AlreadyDisabled reports whether this call was a no-op because the
	// credential was already disabled — the operation is idempotent.
	AlreadyDisabled bool `json:"already_disabled"`
}
