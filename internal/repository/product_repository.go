package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProductRepository interface {
	Create(ctx context.Context, product *model.Product) error
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.ListProductsFilter, page, limit int) (model.ListProductsResult, error)
	GetByID(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) (*model.Product, error)
	Update(ctx context.Context, product *model.Product) error
	Delete(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) error
}

// ProductImageCounter counts products referencing an image URL across all
// merchants. It is a separate narrow interface (implemented by
// pgProductRepository) so existing ProductRepository doubles keep compiling.
type ProductImageCounter interface {
	CountByImageURL(ctx context.Context, imageURL string) (int64, error)
}

type pgProductRepository struct {
	db *pgxpool.Pool
}

func NewProductRepository(db *pgxpool.Pool) ProductRepository {
	return &pgProductRepository{db: db}
}

func (r *pgProductRepository) Create(ctx context.Context, product *model.Product) error {
	const q = `
		INSERT INTO products (id, merchant_id, category_id, name, sku, description, price, currency, image_url, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		RETURNING id, merchant_id, category_id, name, sku, description, price, currency, image_url, active, created_at, updated_at
	`
	row := r.db.QueryRow(ctx, q,
		product.ID,
		product.MerchantID,
		product.CategoryID,
		product.Name,
		product.SKU,
		product.Description,
		product.Price,
		product.Currency,
		product.ImageURL,
		product.Active,
	)
	if err := scanProductRow(row, product); err != nil {
		if isUniqueViolation(err) {
			return ErrProductSKUExists
		}
		return fmt.Errorf("product repository create: %w", err)
	}
	return nil
}

func (r *pgProductRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.ListProductsFilter, page, limit int) (model.ListProductsResult, error) {
	result := model.ListProductsResult{Products: make([]model.Product, 0), Page: page, Limit: limit}
	args := []any{merchantID}
	conditions := []string{"merchant_id = $1"}
	idx := 2

	if filter.CategoryID != nil {
		conditions = append(conditions, fmt.Sprintf("category_id = $%d", idx))
		args = append(args, *filter.CategoryID)
		idx++
	}
	if filter.Active != nil {
		conditions = append(conditions, fmt.Sprintf("active = $%d", idx))
		args = append(args, *filter.Active)
		idx++
	}
	if filter.Search != "" {
		search := "%" + strings.ToLower(filter.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(LOWER(name) LIKE $%d OR LOWER(sku) LIKE $%d OR LOWER(description) LIKE $%d)", idx, idx, idx))
		args = append(args, search)
		idx++
	}
	where := strings.Join(conditions, " AND ")

	// The count and the page use identical merchant/filter predicates so the
	// reported total always matches the returned rows.
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM products WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("product repository count: %w", err)
	}
	result.TotalPages = int((result.Total + int64(limit) - 1) / int64(limit))

	q := `
		SELECT id, merchant_id, category_id, name, sku, description, price, currency, image_url, active, created_at, updated_at
		FROM products
		WHERE ` + where + `
		ORDER BY active DESC, updated_at DESC, name ASC, id DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1) + `
	`
	args = append(args, limit, (page-1)*limit)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return result, fmt.Errorf("product repository list: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var product model.Product
		if err := rows.Scan(
			&product.ID,
			&product.MerchantID,
			&product.CategoryID,
			&product.Name,
			&product.SKU,
			&product.Description,
			&product.Price,
			&product.Currency,
			&product.ImageURL,
			&product.Active,
			&product.CreatedAt,
			&product.UpdatedAt,
		); err != nil {
			return result, fmt.Errorf("product repository scan: %w", err)
		}
		result.Products = append(result.Products, product)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("product repository list rows: %w", err)
	}
	return result, nil
}

func (r *pgProductRepository) GetByID(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) (*model.Product, error) {
	const q = `
		SELECT id, merchant_id, category_id, name, sku, description, price, currency, image_url, active, created_at, updated_at
		FROM products
		WHERE merchant_id = $1 AND id = $2
	`
	row := r.db.QueryRow(ctx, q, merchantID, productID)
	var product model.Product
	if err := row.Scan(
		&product.ID,
		&product.MerchantID,
		&product.CategoryID,
		&product.Name,
		&product.SKU,
		&product.Description,
		&product.Price,
		&product.Currency,
		&product.ImageURL,
		&product.Active,
		&product.CreatedAt,
		&product.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProductNotFound
		}
		return nil, fmt.Errorf("product repository get: %w", err)
	}
	return &product, nil
}

func (r *pgProductRepository) Update(ctx context.Context, product *model.Product) error {
	const q = `
		UPDATE products
		SET category_id = $1,
		    name = $2,
		    sku = $3,
		    description = $4,
		    price = $5,
		    currency = $6,
		    image_url = $7,
		    active = $8,
		    updated_at = NOW()
		WHERE id = $9 AND merchant_id = $10
		RETURNING id, merchant_id, category_id, name, sku, description, price, currency, image_url, active, created_at, updated_at
	`
	row := r.db.QueryRow(ctx, q,
		product.CategoryID,
		product.Name,
		product.SKU,
		product.Description,
		product.Price,
		product.Currency,
		product.ImageURL,
		product.Active,
		product.ID,
		product.MerchantID,
	)
	if err := scanProductRow(row, product); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProductNotFound
		}
		if isUniqueViolation(err) {
			return ErrProductSKUExists
		}
		return fmt.Errorf("product repository update: %w", err)
	}
	return nil
}

func (r *pgProductRepository) Delete(ctx context.Context, merchantID uuid.UUID, productID uuid.UUID) error {
	const q = `DELETE FROM products WHERE merchant_id = $1 AND id = $2`
	cmd, err := r.db.Exec(ctx, q, merchantID, productID)
	if err != nil {
		return fmt.Errorf("product repository delete: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrProductNotFound
	}
	return nil
}

// CountByImageURL counts products referencing imageURL across ALL merchants.
// It backs product-image orphan cleanup: a file is deleted only when the
// count reaches zero, so an image pasted into another merchant's product (or
// shared by two products of the same merchant) is never removed while still
// referenced. Intentionally not part of the ProductRepository interface so
// existing in-memory doubles keep compiling; callers use it via
// service.ProductImageReferenceCounter wired in cmd/server.
func (r *pgProductRepository) CountByImageURL(ctx context.Context, imageURL string) (int64, error) {
	const q = `SELECT count(*) FROM products WHERE image_url = $1`
	var count int64
	if err := r.db.QueryRow(ctx, q, imageURL).Scan(&count); err != nil {
		return 0, fmt.Errorf("product repository count by image url: %w", err)
	}
	return count, nil
}

func scanProductRow(row pgx.Row, product *model.Product) error {
	return row.Scan(
		&product.ID,
		&product.MerchantID,
		&product.CategoryID,
		&product.Name,
		&product.SKU,
		&product.Description,
		&product.Price,
		&product.Currency,
		&product.ImageURL,
		&product.Active,
		&product.CreatedAt,
		&product.UpdatedAt,
	)
}
