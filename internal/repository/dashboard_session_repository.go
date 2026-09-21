package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrSessionNotFound is returned when no matching session row exists.
	ErrSessionNotFound = errors.New("session not found")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// DashboardSessionRepository defines database operations for dashboard sessions.
type DashboardSessionRepository interface {
	// Create inserts a new dashboard_sessions row.
	Create(ctx context.Context, session *model.DashboardSession) error

	// GetByRefreshTokenHash looks up a session by the SHA-256 hash of the refresh token.
	// Returns ErrSessionNotFound when no row matches.
	GetByRefreshTokenHash(ctx context.Context, tokenHash string) (*model.DashboardSession, error)

	// GetByUserID returns all active sessions for a given user.
	GetByUserID(ctx context.Context, userID uuid.UUID) ([]*model.DashboardSession, error)

	// Delete removes a session by its primary key.
	// Returns ErrSessionNotFound when no row matches.
	Delete(ctx context.Context, id uuid.UUID) error

	// DeleteByUserID removes all sessions for a given user (logout-all).
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error

	// DeleteExpired removes all sessions whose expires_at is in the past.
	DeleteExpired(ctx context.Context) error

	// UpdateLastUsedAt sets last_used_at. Best-effort.
	UpdateLastUsedAt(ctx context.Context, id uuid.UUID, t time.Time) error
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgDashboardSessionRepository struct {
	db *pgxpool.Pool
}

// NewDashboardSessionRepository returns a PostgreSQL-backed DashboardSessionRepository.
func NewDashboardSessionRepository(db *pgxpool.Pool) DashboardSessionRepository {
	return &pgDashboardSessionRepository{db: db}
}

func (r *pgDashboardSessionRepository) Create(ctx context.Context, session *model.DashboardSession) error {
	const q = `
		INSERT INTO dashboard_sessions
		    (id, merchant_user_id, refresh_token_hash, expires_at, created_at, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := r.db.Exec(ctx, q,
		session.ID,
		session.MerchantUserID,
		session.RefreshTokenHash,
		session.ExpiresAt,
		session.CreatedAt,
		session.LastUsedAt,
	)
	if err != nil {
		return fmt.Errorf("dashboard session repository create: %w", err)
	}
	return nil
}

func (r *pgDashboardSessionRepository) GetByRefreshTokenHash(ctx context.Context, tokenHash string) (*model.DashboardSession, error) {
	const q = `
		SELECT id, merchant_user_id, refresh_token_hash, expires_at, created_at, last_used_at
		FROM   dashboard_sessions
		WHERE  refresh_token_hash = $1
	`
	s := &model.DashboardSession{}
	err := r.db.QueryRow(ctx, q, tokenHash).Scan(
		&s.ID, &s.MerchantUserID, &s.RefreshTokenHash,
		&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session repository get by token hash: %w", err)
	}
	return s, nil
}

func (r *pgDashboardSessionRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]*model.DashboardSession, error) {
	const q = `
		SELECT id, merchant_user_id, refresh_token_hash, expires_at, created_at, last_used_at
		FROM   dashboard_sessions
		WHERE  merchant_user_id = $1
		ORDER  BY created_at DESC
	`
	rows, err := r.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("dashboard session repository get by user: %w", err)
	}
	defer rows.Close()

	var sessions []*model.DashboardSession
	for rows.Next() {
		s := &model.DashboardSession{}
		if err := rows.Scan(
			&s.ID, &s.MerchantUserID, &s.RefreshTokenHash,
			&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt,
		); err != nil {
			return nil, fmt.Errorf("dashboard session repository scan: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dashboard session repository rows: %w", err)
	}
	return sessions, nil
}

func (r *pgDashboardSessionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM dashboard_sessions WHERE id = $1`
	tag, err := r.db.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("dashboard session repository delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *pgDashboardSessionRepository) DeleteByUserID(ctx context.Context, userID uuid.UUID) error {
	const q = `DELETE FROM dashboard_sessions WHERE merchant_user_id = $1`
	_, err := r.db.Exec(ctx, q, userID)
	if err != nil {
		return fmt.Errorf("dashboard session repository delete by user: %w", err)
	}
	return nil
}

func (r *pgDashboardSessionRepository) DeleteExpired(ctx context.Context) error {
	const q = `DELETE FROM dashboard_sessions WHERE expires_at < NOW()`
	_, err := r.db.Exec(ctx, q)
	if err != nil {
		return fmt.Errorf("dashboard session repository delete expired: %w", err)
	}
	return nil
}

func (r *pgDashboardSessionRepository) UpdateLastUsedAt(ctx context.Context, id uuid.UUID, t time.Time) error {
	const q = `UPDATE dashboard_sessions SET last_used_at = $1 WHERE id = $2`
	_, err := r.db.Exec(ctx, q, t, id)
	if err != nil {
		return fmt.Errorf("dashboard session repository update last used: %w", err)
	}
	return nil
}
