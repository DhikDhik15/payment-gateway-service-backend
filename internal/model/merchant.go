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

// Merchant is the core domain entity for a registered merchant.
// It maps 1-to-1 to the `merchants` database table.
type Merchant struct {
	ID        uuid.UUID      `db:"id"         json:"id"`
	Name      string         `db:"name"       json:"name"`
	Code      string         `db:"code"       json:"code"`
	APIKey    string         `db:"api_key"    json:"api_key"`
	APISecret string         `db:"api_secret" json:"-"` // never serialised to JSON
	Status    MerchantStatus `db:"status"     json:"status"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt time.Time      `db:"updated_at" json:"updated_at"`
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
// APISecret is intentionally excluded — it is never returned after creation.
type CreateMerchantResponse struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	Code      string         `json:"code"`
	APIKey    string         `json:"api_key"`
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
}
