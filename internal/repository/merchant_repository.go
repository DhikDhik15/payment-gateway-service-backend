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

// MerchantRepository defines the database operations for merchants.
// Using an interface allows the service to be tested without a real database.
type MerchantRepository interface {
	Create(ctx context.Context, m *model.Merchant) error
	GetByID(ctx context.Context, id uuid.UUID) (*model.Merchant, error)
	GetByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error)
	ExistsByCode(ctx context.Context, code string) (bool, error)
}

// pgMerchantRepository is the PostgreSQL implementation of MerchantRepository.
type pgMerchantRepository struct {
	db *pgxpool.Pool
}

// NewMerchantRepository returns a PostgreSQL-backed MerchantRepository.
func NewMerchantRepository(db *pgxpool.Pool) MerchantRepository {
	return &pgMerchantRepository{db: db}
}

// Create inserts a new merchant row. The caller must have populated all fields
// including ID, APIKey, APISecret, and Status before calling.
func (r *pgMerchantRepository) Create(ctx context.Context, m *model.Merchant) error {
	const q = `
		INSERT INTO merchants (id, name, code, api_key, api_secret, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.Exec(ctx, q,
		m.ID,
		m.Name,
		m.Code,
		m.APIKey,
		m.APISecret,
		m.Status,
		m.CreatedAt,
		m.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("merchant repository create: %w", err)
	}
	return nil
}

// GetByID retrieves a merchant by its primary key.
// Returns ErrMerchantNotFound when no row matches.
func (r *pgMerchantRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Merchant, error) {
	const q = `
		SELECT id, name, code, api_key, api_secret, status, created_at, updated_at
		FROM merchants
		WHERE id = $1
	`
	m, err := scanMerchant(r.db.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("merchant repository get by id: %w", err)
	}
	return m, nil
}

// GetByAPIKey retrieves a merchant by its API key.
// Returns ErrMerchantNotFound when no row matches.
func (r *pgMerchantRepository) GetByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error) {
	const q = `
		SELECT id, name, code, api_key, api_secret, status, created_at, updated_at
		FROM merchants
		WHERE api_key = $1
	`
	m, err := scanMerchant(r.db.QueryRow(ctx, q, apiKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("merchant repository get by api key: %w", err)
	}
	return m, nil
}

// ExistsByCode returns true when a merchant with the given code already exists.
func (r *pgMerchantRepository) ExistsByCode(ctx context.Context, code string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM merchants WHERE code = $1)`
	var exists bool
	if err := r.db.QueryRow(ctx, q, code).Scan(&exists); err != nil {
		return false, fmt.Errorf("merchant repository exists by code: %w", err)
	}
	return exists, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// scanMerchant reads all merchant columns from a single row.
func scanMerchant(row pgx.Row) (*model.Merchant, error) {
	m := &model.Merchant{}
	err := row.Scan(
		&m.ID,
		&m.Name,
		&m.Code,
		&m.APIKey,
		&m.APISecret,
		&m.Status,
		&m.CreatedAt,
		&m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return m, nil
}
