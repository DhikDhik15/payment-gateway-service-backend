package model

import (
	"time"

	"github.com/google/uuid"
)

// OnboardMerchantRequest is the input for secure tenant onboarding.
// POST /api/v1/admin/onboarding/merchants
//
// merchant_id is never accepted from the client — it is generated inside the
// onboarding transaction so OWNER and API credential cannot be cross-associated.
type OnboardMerchantRequest struct {
	Name          string `json:"name"           binding:"required,min=2,max=150"`
	Code          string `json:"code"           binding:"required,min=2,max=50"`
	OwnerEmail    string `json:"owner_email"    binding:"required,max=254"`
	OwnerPassword string `json:"owner_password" binding:"required,min=8,max=128"`
}

// OnboardMerchantResponse is returned after a successful atomic tenant provision.
//
// Security:
//   - owner_password / password_hash are NEVER returned.
//   - api_credential.secret is the Phase 5C plaintext secret, returned ONCE.
//   - No legacy credential is generated at all since Phase 8D.3: the merchant
//     row carries NULL api_key/api_secret in the MIGRATED state, so there is no
//     legacy material to disclose or discard.
type OnboardMerchantResponse struct {
	Merchant      OnboardedMerchant      `json:"merchant"`
	Owner         OnboardedOwner         `json:"owner"`
	APICredential OnboardedAPICredential `json:"api_credential"`
}

// OnboardedMerchant is the merchant portion of an onboarding response.
type OnboardedMerchant struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	Code      string         `json:"code"`
	Status    MerchantStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
}

// OnboardedOwner is the OWNER dashboard user portion of an onboarding response.
type OnboardedOwner struct {
	ID    uuid.UUID         `json:"id"`
	Email string            `json:"email"`
	Role  DashboardUserRole `json:"role"`
}

// OnboardedAPICredential is the initial Phase 5C merchant API key.
// Secret is a one-time disclosure — store it securely; it cannot be retrieved again.
type OnboardedAPICredential struct {
	ID        uuid.UUID            `json:"id"`
	KeyID     string               `json:"key_id"`
	Secret    string               `json:"secret"` // plaintext — returned ONCE only
	Name      string               `json:"name"`
	Status    MerchantAPIKeyStatus `json:"status"`
	CreatedAt time.Time            `json:"created_at"`
}
