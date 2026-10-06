package model

import (
	"time"

	"github.com/google/uuid"
)

// Product is a merchant-scoped sellable item (Phase 7 POS domain).
// Price is integer minor units (IDR: rupiah) — never floating point.
type Product struct {
	ID          uuid.UUID  `json:"id"`
	MerchantID  uuid.UUID  `json:"merchant_id"`
	CategoryID  *uuid.UUID `json:"category_id"`
	Name        string     `json:"name"`
	SKU         string     `json:"sku,omitempty"`
	Description string     `json:"description,omitempty"`
	Price       int64      `json:"price"`
	Currency    string     `json:"currency"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CreateProductRequest is the POST /api/v1/dashboard/products payload.
// CategoryID is optional and must belong to the same merchant.
type CreateProductRequest struct {
	CategoryID  *uuid.UUID `json:"category_id" binding:"omitempty,uuid"`
	Name        string     `json:"name"        binding:"required,min=1,max=200"`
	SKU         string     `json:"sku"         binding:"omitempty,max=50"`
	Description string     `json:"description" binding:"omitempty,max=500"`
	Price       int64      `json:"price"       binding:"required,min=0"`
	Currency    string     `json:"currency"    binding:"required,len=3"`
	Active      *bool      `json:"active"`
}

// UpdateProductRequest is the PATCH /api/v1/dashboard/products/{id} payload.
// All fields are optional; only provided fields are updated.
type UpdateProductRequest struct {
	CategoryID  *uuid.UUID `json:"category_id" binding:"omitempty,uuid"`
	Name        *string    `json:"name"        binding:"omitempty,min=1,max=200"`
	SKU         *string    `json:"sku"         binding:"omitempty,max=50"`
	Description *string    `json:"description" binding:"omitempty,max=500"`
	Price       *int64     `json:"price"       binding:"omitempty,min=0"`
	Currency    *string    `json:"currency"    binding:"omitempty,len=3"`
	Active      *bool      `json:"active"`
}

// ListProductsFilter filters GET /api/v1/dashboard/products.
type ListProductsFilter struct {
	CategoryID *uuid.UUID
	Active     *bool
	Search     string
}
