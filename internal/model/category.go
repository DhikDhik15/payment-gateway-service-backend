package model

import (
	"time"

	"github.com/google/uuid"
)

// Category is a merchant-scoped product grouping (Phase 7 POS domain).
type Category struct {
	ID          uuid.UUID `json:"id"`
	MerchantID  uuid.UUID `json:"merchant_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateCategoryRequest is the POST /api/v1/dashboard/categories payload.
type CreateCategoryRequest struct {
	Name        string `json:"name"        binding:"required,min=1,max=100"`
	Description string `json:"description" binding:"omitempty,max=500"`
}

// UpdateCategoryRequest is the PATCH /api/v1/dashboard/categories/{id} payload.
// All fields are optional; only provided fields are updated.
type UpdateCategoryRequest struct {
	Name        *string `json:"name"        binding:"omitempty,min=1,max=100"`
	Description *string `json:"description" binding:"omitempty,max=500"`
}
