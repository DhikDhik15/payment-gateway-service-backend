package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CategoryRepository manages merchant-scoped POS categories.
type CategoryRepository interface {
	Create(ctx context.Context, category *model.Category) error
	ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]model.Category, error)
	GetByID(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) (*model.Category, error)
	Update(ctx context.Context, category *model.Category) error
	Delete(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) error
}

type pgCategoryRepository struct {
	db *pgxpool.Pool
}

func NewCategoryRepository(db *pgxpool.Pool) CategoryRepository {
	return &pgCategoryRepository{db: db}
}

func (r *pgCategoryRepository) Create(ctx context.Context, category *model.Category) error {
	const q = `
		INSERT INTO categories (id, merchant_id, name, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		RETURNING id, merchant_id, name, description, created_at, updated_at
	`
	row := r.db.QueryRow(ctx, q,
		category.ID,
		category.MerchantID,
		category.Name,
		category.Description,
	)
	if err := scanCategoryRow(row, category); err != nil {
		if isUniqueViolation(err) {
			return ErrCategoryNameExists
		}
		return fmt.Errorf("category repository create: %w", err)
	}
	return nil
}

func (r *pgCategoryRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]model.Category, error) {
	const q = `
		SELECT id, merchant_id, name, description, created_at, updated_at
		FROM categories
		WHERE merchant_id = $1
		ORDER BY lower(name) ASC, created_at DESC
	`
	rows, err := r.db.Query(ctx, q, merchantID)
	if err != nil {
		return nil, fmt.Errorf("category repository list: %w", err)
	}
	defer rows.Close()

	out := make([]model.Category, 0)
	for rows.Next() {
		var category model.Category
		if err := rows.Scan(
			&category.ID,
			&category.MerchantID,
			&category.Name,
			&category.Description,
			&category.CreatedAt,
			&category.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("category repository scan: %w", err)
		}
		out = append(out, category)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("category repository list rows: %w", err)
	}
	return out, nil
}

func (r *pgCategoryRepository) GetByID(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) (*model.Category, error) {
	const q = `
		SELECT id, merchant_id, name, description, created_at, updated_at
		FROM categories
		WHERE merchant_id = $1 AND id = $2
	`
	row := r.db.QueryRow(ctx, q, merchantID, categoryID)
	var category model.Category
	if err := row.Scan(
		&category.ID,
		&category.MerchantID,
		&category.Name,
		&category.Description,
		&category.CreatedAt,
		&category.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCategoryNotFound
		}
		return nil, fmt.Errorf("category repository get: %w", err)
	}
	return &category, nil
}

func (r *pgCategoryRepository) Update(ctx context.Context, category *model.Category) error {
	const q = `
		UPDATE categories
		SET name = $1,
		    description = $2,
		    updated_at = NOW()
		WHERE id = $3 AND merchant_id = $4
		RETURNING id, merchant_id, name, description, created_at, updated_at
	`
	row := r.db.QueryRow(ctx, q,
		category.Name,
		category.Description,
		category.ID,
		category.MerchantID,
	)
	if err := scanCategoryRow(row, category); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		if isUniqueViolation(err) {
			return ErrCategoryNameExists
		}
		return fmt.Errorf("category repository update: %w", err)
	}
	return nil
}

func (r *pgCategoryRepository) Delete(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) error {
	const q = `DELETE FROM categories WHERE merchant_id = $1 AND id = $2`
	cmd, err := r.db.Exec(ctx, q, merchantID, categoryID)
	if err != nil {
		return fmt.Errorf("category repository delete: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

func scanCategoryRow(row pgx.Row, category *model.Category) error {
	return row.Scan(
		&category.ID,
		&category.MerchantID,
		&category.Name,
		&category.Description,
		&category.CreatedAt,
		&category.UpdatedAt,
	)
}
