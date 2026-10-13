package model

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Product is a merchant-scoped sellable item (Phase 7 POS domain).
// Price is integer minor units (IDR: rupiah) — never floating point.
// ImageURL is an optional HTTPS reference for storefront display; the
// backend validates it syntactically but never fetches or proxies it.
type Product struct {
	ID          uuid.UUID  `json:"id"`
	MerchantID  uuid.UUID  `json:"merchant_id"`
	CategoryID  *uuid.UUID `json:"category_id"`
	Name        string     `json:"name"`
	SKU         string     `json:"sku,omitempty"`
	Description string     `json:"description,omitempty"`
	Price       int64      `json:"price"`
	Currency    string     `json:"currency"`
	ImageURL    *string    `json:"image_url,omitempty"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CreateProductRequest is the POST /api/v1/dashboard/products payload.
// CategoryID is optional and must belong to the same merchant.
// ImageURL is optional; when provided it must be an HTTPS URL.
type CreateProductRequest struct {
	CategoryID  *uuid.UUID `json:"category_id" binding:"omitempty,uuid"`
	Name        string     `json:"name"        binding:"required,min=1,max=200"`
	SKU         string     `json:"sku"         binding:"omitempty,max=50"`
	Description string     `json:"description" binding:"omitempty,max=500"`
	Price       int64      `json:"price"       binding:"required,min=0"`
	Currency    string     `json:"currency"    binding:"required,len=3"`
	ImageURL    *string    `json:"image_url"   binding:"omitempty,max=2048"`
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
	ImageURL    *string    `json:"image_url"   binding:"omitempty,max=2048"`
	Active      *bool      `json:"active"`
}

// ListProductsFilter filters GET /api/v1/dashboard/products.
type ListProductsFilter struct {
	CategoryID *uuid.UUID
	Active     *bool
	Search     string
}

// ListProductsResult is a paginated product listing.
type ListProductsResult struct {
	Products   []Product
	Page       int
	Limit      int
	Total      int64
	TotalPages int
}

// ProductImageURLPrefix is the stable public path prefix for product images
// uploaded through POST /api/v1/dashboard/product-images and served publicly
// via GET /api/v1/product-images/{merchant_id}/{file}.
const ProductImageURLPrefix = "/api/v1/product-images/"

// AllowedProductImageExtensions are the filename extensions permitted in
// local product image references. The upload validator may accept a subset
// (decodable formats only); the reference grammar stays forward-compatible.
var AllowedProductImageExtensions = map[string]bool{
	"jpg":  true,
	"jpeg": true,
	"png":  true,
	"webp": true,
}

// ParseLocalProductImageRef validates the strict grammar of a backend-stored
// product image reference:
//
//	/api/v1/product-images/{merchant_uuid}/{file_uuid}.{jpg|jpeg|png|webp}
//
// It returns the merchant UUID and filename on success. The grammar is
// deliberately narrow so references can never encode path traversal (".."),
// query strings, or absolute filesystem paths. Matching is lowercase-only,
// consistent with uuid.NewString output used at upload time.
func ParseLocalProductImageRef(raw string) (merchantID uuid.UUID, filename string, ok bool) {
	if !strings.HasPrefix(raw, ProductImageURLPrefix) {
		return uuid.Nil, "", false
	}
	rest := strings.TrimPrefix(raw, ProductImageURLPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return uuid.Nil, "", false
	}
	merchantID, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, "", false
	}
	name := parts[1]
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\?#") {
		return uuid.Nil, "", false
	}
	dot := strings.LastIndex(name, ".")
	if dot <= 0 || dot == len(name)-1 {
		return uuid.Nil, "", false
	}
	stem, ext := name[:dot], name[dot+1:]
	// Extensions are lowercase-only (uuid.NewString output is lowercase and
	// the upload path always generates lowercase names).
	if ext != strings.ToLower(ext) {
		return uuid.Nil, "", false
	}
	if !AllowedProductImageExtensions[ext] {
		return uuid.Nil, "", false
	}
	if _, err := uuid.Parse(stem); err != nil {
		return uuid.Nil, "", false
	}
	return merchantID, name, true
}

// IsLocalProductImageRef reports whether raw is a backend-stored image
// reference (as opposed to an external HTTPS URL).
func IsLocalProductImageRef(raw string) bool {
	_, _, ok := ParseLocalProductImageRef(raw)
	return ok
}
