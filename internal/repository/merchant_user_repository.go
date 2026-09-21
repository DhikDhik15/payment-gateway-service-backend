package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrMerchantUserNotFound is returned when no matching user row exists.
	ErrMerchantUserNotFound = errors.New("merchant user not found")

	// ErrMerchantUserEmailExists is returned when the email is already registered.
	ErrMerchantUserEmailExists = errors.New("email already registered")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// MerchantUserRepository defines the database operations for dashboard user accounts.
type MerchantUserRepository interface {
	// Create inserts a new merchant_users row. Returns ErrMerchantUserEmailExists
	// if the email is already taken (unique constraint violation).
	Create(ctx context.Context, user *model.MerchantUser) error

	// GetByID retrieves a user by primary key.
	// Returns ErrMerchantUserNotFound when no row matches.
	GetByID(ctx context.Context, id uuid.UUID) (*model.MerchantUser, error)

	// GetByEmail retrieves a user by their normalised email address.
	// Returns ErrMerchantUserNotFound when no row matches.
	GetByEmail(ctx context.Context, email string) (*model.MerchantUser, error)

	// ListByMerchant returns all users belonging to merchantID, newest first.
	ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*model.MerchantUser, error)

	// UpdateStatus changes the user status (ACTIVE ↔ DISABLED).
	// Returns ErrMerchantUserNotFound when no row matches.
	UpdateStatus(ctx context.Context, id uuid.UUID, status model.DashboardUserStatus) error

	// UpdateLastLoginAt sets last_login_at. Best-effort — a failure here must
	// not fail authentication.
	UpdateLastLoginAt(ctx context.Context, id uuid.UUID, t time.Time) error
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgMerchantUserRepository struct {
	db *pgxpool.Pool
}

// NewMerchantUserRepository returns a PostgreSQL-backed MerchantUserRepository.
func NewMerchantUserRepository(db *pgxpool.Pool) MerchantUserRepository {
	return &pgMerchantUserRepository{db: db}
}

// Create inserts a new merchant_users row.
func (r *pgMerchantUserRepository) Create(ctx context.Context, user *model.MerchantUser) error {
	const q = `
		INSERT INTO merchant_users
		    (id, merchant_id, email, password_hash, role, status, last_login_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.Exec(ctx, q,
		user.ID,
		user.MerchantID,
		user.Email,
		user.PasswordHash,
		user.Role,
		user.Status,
		user.LastLoginAt,
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrMerchantUserEmailExists
		}
		return fmt.Errorf("merchant user repository create: %w", err)
	}
	return nil
}

// GetByID retrieves a user by primary key.
func (r *pgMerchantUserRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.MerchantUser, error) {
	const q = `
		SELECT id, merchant_id, email, password_hash, role, status,
		       last_login_at, created_at, updated_at
		FROM   merchant_users
		WHERE  id = $1
	`
	user, err := scanMerchantUser(r.db.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantUserNotFound
		}
		return nil, fmt.Errorf("merchant user repository get by id: %w", err)
	}
	return user, nil
}

// GetByEmail retrieves a user by normalised email.
func (r *pgMerchantUserRepository) GetByEmail(ctx context.Context, email string) (*model.MerchantUser, error) {
	const q = `
		SELECT id, merchant_id, email, password_hash, role, status,
		       last_login_at, created_at, updated_at
		FROM   merchant_users
		WHERE  email = $1
	`
	user, err := scanMerchantUser(r.db.QueryRow(ctx, q, email))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantUserNotFound
		}
		return nil, fmt.Errorf("merchant user repository get by email: %w", err)
	}
	return user, nil
}

// ListByMerchant returns all users for a merchant, newest first.
func (r *pgMerchantUserRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*model.MerchantUser, error) {
	const q = `
		SELECT id, merchant_id, email, password_hash, role, status,
		       last_login_at, created_at, updated_at
		FROM   merchant_users
		WHERE  merchant_id = $1
		ORDER  BY created_at DESC
	`
	rows, err := r.db.Query(ctx, q, merchantID)
	if err != nil {
		return nil, fmt.Errorf("merchant user repository list: %w", err)
	}
	defer rows.Close()

	var users []*model.MerchantUser
	for rows.Next() {
		user, err := scanMerchantUser(rows)
		if err != nil {
			return nil, fmt.Errorf("merchant user repository list scan: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("merchant user repository list rows: %w", err)
	}
	return users, nil
}

// UpdateStatus changes the user's status.
func (r *pgMerchantUserRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status model.DashboardUserStatus) error {
	const q = `
		UPDATE merchant_users
		SET    status = $1, updated_at = $2
		WHERE  id = $3
	`
	tag, err := r.db.Exec(ctx, q, status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("merchant user repository update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantUserNotFound
	}
	return nil
}

// UpdateLastLoginAt sets last_login_at. Best-effort — caller should not fail auth on error.
func (r *pgMerchantUserRepository) UpdateLastLoginAt(ctx context.Context, id uuid.UUID, t time.Time) error {
	const q = `
		UPDATE merchant_users
		SET    last_login_at = $1, updated_at = $2
		WHERE  id = $3
	`
	_, err := r.db.Exec(ctx, q, t, t, id)
	if err != nil {
		return fmt.Errorf("merchant user repository update last login: %w", err)
	}
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func scanMerchantUser(row pgx.Row) (*model.MerchantUser, error) {
	u := &model.MerchantUser{}
	err := row.Scan(
		&u.ID,
		&u.MerchantID,
		&u.Email,
		&u.PasswordHash,
		&u.Role,
		&u.Status,
		&u.LastLoginAt,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// isUniqueViolation checks if a pgx error is a PostgreSQL unique_violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
