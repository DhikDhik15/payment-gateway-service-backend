package service

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrProductNotFound        = errors.New("product not found")
	ErrProductSKUUsed         = errors.New("product sku already exists")
	ErrProductInvalidName     = errors.New("product name is required")
	ErrProductInvalidPrice    = errors.New("product price must be zero or positive")
	ErrProductInvalidCurrency = errors.New("product currency must be IDR")
	ErrProductInvalidImage    = errors.New("product image URL must be a valid HTTPS URL")
	ErrProductInvalidPage     = errors.New("invalid product pagination")
)

// SupportedProductCurrencies is the initial-release currency allowlist,
// matching the Order and Payment currency contract.
const SupportedProductCurrency = "IDR"

// maxProductImageURLLength bounds stored image references.
const maxProductImageURLLength = 2048

type ProductService interface {
	Create(ctx context.Context, merchantID uuid.UUID, req model.CreateProductRequest) (*model.Product, error)
	List(ctx context.Context, merchantID uuid.UUID, filter model.ListProductsFilter, page, limit int) (model.ListProductsResult, error)
	Get(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) (*model.Product, error)
	Update(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID, req model.UpdateProductRequest) (*model.Product, error)
	Delete(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) error
}

type productService struct {
	repo         repository.ProductRepository
	categoryRepo repository.CategoryRepository
	// janitor is an optional best-effort cleanup hook for replaced/deleted
	// local image files. Nil disables cleanup (existing behavior preserved).
	janitor ProductImageJanitor
}

func NewProductService(repo repository.ProductRepository, categoryRepo repository.CategoryRepository) ProductService {
	return &productService{repo: repo, categoryRepo: categoryRepo}
}

// NewProductServiceWithImageJanitor behaves like NewProductService and
// additionally removes the backing file of a replaced or deleted LOCAL image
// reference when no other product references it. Cleanup is best-effort and
// never fails the product operation; external HTTPS URLs are never deleted.
func NewProductServiceWithImageJanitor(repo repository.ProductRepository, categoryRepo repository.CategoryRepository, janitor ProductImageJanitor) ProductService {
	return &productService{repo: repo, categoryRepo: categoryRepo, janitor: janitor}
}

func (s *productService) Create(ctx context.Context, merchantID uuid.UUID, req model.CreateProductRequest) (*model.Product, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, ErrProductInvalidName
	}
	if req.Price < 0 {
		return nil, ErrProductInvalidPrice
	}
	if strings.ToUpper(strings.TrimSpace(req.Currency)) != SupportedProductCurrency {
		return nil, ErrProductInvalidCurrency
	}
	imageURL, err := normalizeProductImageURL(req.ImageURL)
	if err != nil {
		return nil, err
	}
	if req.CategoryID != nil {
		if _, err := s.categoryRepo.GetByID(ctx, merchantID, *req.CategoryID); err != nil {
			if errors.Is(err, repository.ErrCategoryNotFound) {
				return nil, ErrCategoryNotFound
			}
			return nil, err
		}
	}
	product := &model.Product{
		ID:          uuid.New(),
		MerchantID:  merchantID,
		CategoryID:  req.CategoryID,
		Name:        strings.TrimSpace(req.Name),
		SKU:         strings.TrimSpace(req.SKU),
		Description: strings.TrimSpace(req.Description),
		Price:       req.Price,
		Currency:    strings.ToUpper(strings.TrimSpace(req.Currency)),
		ImageURL:    imageURL,
		Active:      true,
	}
	if req.Active != nil {
		product.Active = *req.Active
	}
	if err := s.repo.Create(ctx, product); err != nil {
		if errors.Is(err, repository.ErrProductSKUExists) {
			return nil, ErrProductSKUUsed
		}
		return nil, err
	}
	return product, nil
}

// normalizeProductImageURL validates the image contract and returns the
// canonical stored form. A nil input stays nil (absent for create, unchanged
// for PATCH updates — PATCH cannot clear an image, only replace it; use the
// dedicated DELETE .../image endpoint to clear). An explicit blank string is
// rejected.
//
// Two reference forms are accepted:
//  1. external absolute HTTPS URLs (syntactic validation only — the backend
//     never fetches them, so this says nothing about destination trust);
//  2. backend-stored local references (/api/v1/product-images/...) issued by
//     the authenticated upload endpoint (strict grammar, see
//     model.ParseLocalProductImageRef).
func normalizeProductImageURL(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" || len(trimmed) > maxProductImageURLLength {
		return nil, ErrProductInvalidImage
	}
	if model.IsLocalProductImageRef(trimmed) {
		out := trimmed
		return &out, nil
	}
	// Exact lowercase prefix keeps application validation consistent with
	// the chk_products_image_url database CHECK (LIKE is case-sensitive).
	if !strings.HasPrefix(trimmed, "https://") {
		return nil, ErrProductInvalidImage
	}
	u, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return nil, ErrProductInvalidImage
	}
	if u.Host == "" || u.User != nil {
		return nil, ErrProductInvalidImage
	}
	out := trimmed
	return &out, nil
}

func (s *productService) List(ctx context.Context, merchantID uuid.UUID, filter model.ListProductsFilter, page, limit int) (model.ListProductsResult, error) {
	if page < 1 || limit < 1 || limit > model.MaxLimit {
		return model.ListProductsResult{}, ErrProductInvalidPage
	}
	return s.repo.ListByMerchant(ctx, merchantID, filter, page, limit)
}

func (s *productService) Get(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) (*model.Product, error) {
	product, err := s.repo.GetByID(ctx, merchantID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	return product, nil
}

func (s *productService) Update(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID, req model.UpdateProductRequest) (*model.Product, error) {
	current, err := s.repo.GetByID(ctx, merchantID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	// Snapshot the pre-update image reference so a replacement can release
	// the old backing file after the update commits.
	var oldImageURL *string
	if current.ImageURL != nil {
		cp := *current.ImageURL
		oldImageURL = &cp
	}
	if req.CategoryID != nil {
		if _, err := s.categoryRepo.GetByID(ctx, merchantID, *req.CategoryID); err != nil {
			if errors.Is(err, repository.ErrCategoryNotFound) {
				return nil, ErrCategoryNotFound
			}
			return nil, err
		}
		current.CategoryID = req.CategoryID
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			return nil, ErrProductInvalidName
		}
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.SKU != nil {
		current.SKU = strings.TrimSpace(*req.SKU)
	}
	if req.Description != nil {
		current.Description = strings.TrimSpace(*req.Description)
	}
	if req.Price != nil {
		if *req.Price < 0 {
			return nil, ErrProductInvalidPrice
		}
		current.Price = *req.Price
	}
	if req.Currency != nil {
		if strings.ToUpper(strings.TrimSpace(*req.Currency)) != SupportedProductCurrency {
			return nil, ErrProductInvalidCurrency
		}
		current.Currency = strings.ToUpper(strings.TrimSpace(*req.Currency))
	}
	if req.ImageURL != nil {
		imageURL, err := normalizeProductImageURL(req.ImageURL)
		if err != nil {
			return nil, err
		}
		current.ImageURL = imageURL
	}
	if req.Active != nil {
		current.Active = *req.Active
	}
	if err := s.repo.Update(ctx, current); err != nil {
		if errors.Is(err, repository.ErrProductSKUExists) {
			return nil, ErrProductSKUUsed
		}
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	s.cleanupReplacedImage(ctx, oldImageURL, current.ImageURL)
	return current, nil
}

func (s *productService) Delete(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) error {
	// Load the row first only when cleanup is configured, so the backing
	// file of a deleted product's local image can be released afterwards.
	var oldImageURL *string
	if s.janitor != nil {
		if product, err := s.repo.GetByID(ctx, merchantID, productID); err == nil && product.ImageURL != nil {
			cp := *product.ImageURL
			oldImageURL = &cp
		}
	}
	if err := s.repo.Delete(ctx, merchantID, productID); err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return ErrProductNotFound
		}
		return err
	}
	s.cleanupReplacedImage(ctx, oldImageURL, nil)
	return nil
}

// cleanupReplacedImage releases the backing file of oldURL when the update
// replaced or removed it. It is strictly best-effort: janitor failures and
// still-referenced files never affect the product operation that already
// committed.
func (s *productService) cleanupReplacedImage(ctx context.Context, oldURL, newURL *string) {
	if s.janitor == nil || oldURL == nil || !model.IsLocalProductImageRef(*oldURL) {
		return
	}
	if newURL != nil && *newURL == *oldURL {
		return
	}
	_ = s.janitor.RemoveIfUnreferenced(ctx, *oldURL)
}
