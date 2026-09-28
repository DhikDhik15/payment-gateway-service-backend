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

	// ErrSessionExpired is returned by the transactional rotation path when a
	// matching token exists but is no longer usable.
	ErrSessionExpired = errors.New("session expired")

	// ErrSessionUserDisabled and ErrSessionMerchantInactive let the service
	// preserve the existing authentication error semantics while the database
	// transaction re-checks lifecycle state under its locks.
	ErrSessionUserDisabled     = errors.New("session user is disabled")
	ErrSessionMerchantInactive = errors.New("session merchant is inactive")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// DashboardSessionRepository defines database operations for dashboard sessions.
type DashboardSessionRepository interface {
	// Create inserts a new dashboard_sessions row.
	Create(ctx context.Context, session *model.DashboardSession) error

	// GetByRefreshTokenHash looks up a session by the SHA-256 hash of the refresh token.
	// Returns ErrSessionNotFound when no row matches.
	GetByRefreshTokenHash(ctx context.Context, tokenHash string) (*model.DashboardSession, error)

	// ConsumeByRefreshTokenHash atomically DELETES the session identified by
	// the SHA-256 hash of the refresh token and returns the deleted row. It is
	// retained as a low-level compatibility operation; refresh-token rotation
	// uses RotateByRefreshTokenHash below so replacement state is committed
	// atomically with the claim.
	// Returns ErrSessionNotFound when no row matches (already consumed,
	// logged out, or never issued).
	ConsumeByRefreshTokenHash(ctx context.Context, tokenHash string) (*model.DashboardSession, error)

	// GetByUserID returns all active sessions for a given user.
	GetByUserID(ctx context.Context, userID uuid.UUID) ([]*model.DashboardSession, error)

	// Delete removes a session by its primary key.
	// Returns ErrSessionNotFound when no row matches.
	Delete(ctx context.Context, id uuid.UUID) error

	// DeleteByIDAndUser removes a session only when it belongs to the expected
	// user. This is the tenant/user-scoped logout primitive used with a signed
	// access-token session claim. It returns ErrSessionNotFound when the row is
	// absent or belongs to another user, preventing cross-user deletion.
	DeleteByIDAndUser(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// DeleteByUserID removes all sessions for a given user (logout-all).
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error

	// DeleteByMerchantID removes all dashboard sessions belonging to any user
	// of the given merchant. Used when a merchant is suspended or deactivated
	// to immediately invalidate all active sessions across the tenant.
	DeleteByMerchantID(ctx context.Context, merchantID uuid.UUID) error

	// DeleteExpired removes all sessions whose expires_at is in the past.
	DeleteExpired(ctx context.Context) error

	// UpdateLastUsedAt sets last_used_at. Best-effort.
	UpdateLastUsedAt(ctx context.Context, id uuid.UUID, t time.Time) error
}

// AtomicRefreshSessionRepository is the transactionally safe refresh rotation
// capability. It is separate from DashboardSessionRepository to preserve
// compatibility with existing test/fallback implementations; the PostgreSQL
// implementation is used by the production service.
//
// RotateByRefreshTokenHash claims the old row and replaces its token material
// in one transaction. A concurrent caller using the same old hash either waits
// and then receives ErrSessionNotFound, or wins the claim; no second caller
// can issue a replacement for that token.
type AtomicRefreshSessionRepository interface {
	RotateByRefreshTokenHash(
		ctx context.Context,
		oldTokenHash string,
		replacement *model.DashboardSession,
	) (*model.DashboardSession, error)
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgDashboardSessionRepository struct {
	db *pgxpool.Pool
}

var _ AtomicRefreshSessionRepository = (*pgDashboardSessionRepository)(nil)

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

func (r *pgDashboardSessionRepository) ConsumeByRefreshTokenHash(ctx context.Context, tokenHash string) (*model.DashboardSession, error) {
	// DELETE ... RETURNING is atomic: the row is deleted and returned by one
	// statement, so concurrent refreshes with the same token produce exactly
	// one winner (the rest get ErrNoRows → ErrSessionNotFound).
	const q = `
		DELETE FROM dashboard_sessions
		WHERE  refresh_token_hash = $1
		RETURNING id, merchant_user_id, refresh_token_hash, expires_at, created_at, last_used_at
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
		return nil, fmt.Errorf("dashboard session repository consume by token hash: %w", err)
	}
	return s, nil
}

// RotateByRefreshTokenHash atomically consumes one refresh session and replaces
// its token material. The old row is claimed with a row lock, while the
// merchant and user lifecycle rows are checked under the same transaction. The
// UPDATE uses the old hash as a compare-and-swap condition and happens before
// COMMIT, so a failed update/commit leaves the old token in its previous safe
// state. Keeping the same session row also makes concurrent logout, password
// change, disablement, and merchant invalidation serialize on the same row.
func (r *pgDashboardSessionRepository) RotateByRefreshTokenHash(
	ctx context.Context,
	oldTokenHash string,
	replacement *model.DashboardSession,
) (result *model.DashboardSession, err error) {
	if replacement == nil {
		return nil, errors.New("dashboard session replacement is nil")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("dashboard session rotation begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			if err == nil {
				err = fmt.Errorf("dashboard session rotation rollback: %w", rollbackErr)
			} else {
				err = fmt.Errorf("%w; rollback: %v", err, rollbackErr)
			}
		}
	}()

	// Resolve the subject from the server-side session first. This lookup is
	// deliberately not the claim; the row is re-read FOR UPDATE below after
	// the merchant lock so all refreshes use the same lock order.
	var candidateUserID uuid.UUID
	if err = tx.QueryRow(ctx, `
		SELECT merchant_user_id
		FROM dashboard_sessions
		WHERE refresh_token_hash = $1
	`, oldTokenHash).Scan(&candidateUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session rotation subject lookup: %w", err)
	}

	// Lock the tenant coordination row before locking the session/user. This
	// serializes lifecycle changes for this merchant without blocking other
	// merchants.
	var merchantID uuid.UUID
	if err = tx.QueryRow(ctx, `
		SELECT m.id
		FROM merchants AS m
		JOIN merchant_users AS u ON u.merchant_id = m.id
		WHERE u.id = $1
		FOR UPDATE OF m
	`, candidateUserID).Scan(&merchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session rotation lock merchant: %w", err)
	}

	// Lock the user row and capture its lifecycle state. A refresh must not
	// create a new session for a user disabled after the old session was read.
	var userMerchantID uuid.UUID
	var userStatus model.DashboardUserStatus
	if err = tx.QueryRow(ctx, `
		SELECT merchant_id, status
		FROM merchant_users
		WHERE id = $1
		FOR UPDATE
	`, candidateUserID).Scan(&userMerchantID, &userStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session rotation lock user: %w", err)
	}
	if userMerchantID != merchantID {
		return nil, ErrSessionNotFound
	}

	// Re-read and lock the actual token row. A competing rotation that already
	// won will make this query return no row after it commits.
	var valid bool
	old := &model.DashboardSession{}
	if err = tx.QueryRow(ctx, `
		SELECT id, merchant_user_id, refresh_token_hash, expires_at, created_at,
		       last_used_at, expires_at > NOW()
		FROM dashboard_sessions
		WHERE refresh_token_hash = $1 AND merchant_user_id = $2
		FOR UPDATE
	`, oldTokenHash, candidateUserID).Scan(
		&old.ID, &old.MerchantUserID, &old.RefreshTokenHash,
		&old.ExpiresAt, &old.CreatedAt, &old.LastUsedAt, &valid,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session rotation claim: %w", err)
	}

	if !valid {
		return nil, ErrSessionExpired
	}
	if userStatus != model.DashboardUserStatusActive {
		return nil, ErrSessionUserDisabled
	}

	var merchantStatus model.MerchantStatus
	if err = tx.QueryRow(ctx, `
		SELECT status
		FROM merchants
		WHERE id = $1
	`, merchantID).Scan(&merchantStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session rotation merchant status: %w", err)
	}
	if merchantStatus != model.MerchantStatusActive {
		return nil, ErrSessionMerchantInactive
	}

	// The replacement is derived from the server-side user, never from a
	// client-supplied tenant. Refuse an inconsistent pair before touching the
	// old row.
	if replacement.MerchantUserID != old.MerchantUserID {
		return nil, errors.New("dashboard session replacement user mismatch")
	}
	if replacement.RefreshTokenHash == "" || replacement.RefreshTokenHash == oldTokenHash {
		return nil, errors.New("dashboard session replacement token is invalid")
	}

	// Compare-and-swap the existing row. Keeping the session ID stable makes
	// concurrent DELETE-based invalidation paths target the same row: a logout
	// or disable that waits on this update can still revoke the replacement,
	// while a delete that wins first makes this update return no row.
	rotated := &model.DashboardSession{}
	if err = tx.QueryRow(ctx, `
		UPDATE dashboard_sessions
		SET refresh_token_hash = $1,
		    expires_at = $2,
		    created_at = $3,
		    last_used_at = $3
		WHERE id = $4
		  AND refresh_token_hash = $5
		  AND merchant_user_id = $6
		  AND expires_at > NOW()
		RETURNING id, merchant_user_id, refresh_token_hash, expires_at,
		          created_at, last_used_at
	`,
		replacement.RefreshTokenHash,
		replacement.ExpiresAt,
		replacement.CreatedAt,
		old.ID,
		oldTokenHash,
		old.MerchantUserID,
	).Scan(
		&rotated.ID, &rotated.MerchantUserID, &rotated.RefreshTokenHash,
		&rotated.ExpiresAt, &rotated.CreatedAt, &rotated.LastUsedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("dashboard session rotation update replacement: %w", err)
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("dashboard session rotation commit: %w", err)
	}
	committed = true
	return rotated, nil
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

func (r *pgDashboardSessionRepository) DeleteByIDAndUser(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	const q = `DELETE FROM dashboard_sessions WHERE id = $1 AND merchant_user_id = $2`
	tag, err := r.db.Exec(ctx, q, id, userID)
	if err != nil {
		return fmt.Errorf("dashboard session repository delete by id and user: %w", err)
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

func (r *pgDashboardSessionRepository) DeleteByMerchantID(ctx context.Context, merchantID uuid.UUID) error {
	// Delete all sessions whose owning user belongs to the given merchant.
	// Uses a subquery so we never store merchant_id redundantly in sessions.
	const q = `
		DELETE FROM dashboard_sessions
		WHERE merchant_user_id IN (
			SELECT id FROM merchant_users WHERE merchant_id = $1
		)
	`
	_, err := r.db.Exec(ctx, q, merchantID)
	if err != nil {
		return fmt.Errorf("dashboard session repository delete by merchant: %w", err)
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
