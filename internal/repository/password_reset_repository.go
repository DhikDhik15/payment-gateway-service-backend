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

// ─── Sentinel errors ─────────────────────────────────────────────────────────

var (
	// ErrPasswordResetNotFound is returned when no token row matches the hash.
	ErrPasswordResetNotFound = errors.New("password reset token not found")

	// ErrPasswordResetExpired is returned when the matched token is past its
	// expiry (or was already classified expired).
	ErrPasswordResetExpired = errors.New("password reset token expired")

	// ErrPasswordResetConsumed is returned when the single-use claim fails
	// because the token was already consumed or revoked (concurrent race).
	ErrPasswordResetConsumed = errors.New("password reset token already used")

	// ErrPasswordResetAlreadyPending is returned when a PENDING token already
	// exists for the user (partial unique index backstop for concurrent
	// requests). Callers treat this as a benign outcome.
	ErrPasswordResetAlreadyPending = errors.New("password reset already pending for this user")
)

// Password-reset table index names — used to classify unique violations.
const (
	resetPendingIndex   = "uq_password_reset_tokens_user_pending"
	resetTokenHashIndex = "uq_password_reset_tokens_token_hash"
)

// ─── Interface ───────────────────────────────────────────────────────────────

// PasswordResetRepository persists forgot-password tokens and applies resets
// atomically. All multi-step mutations run in a single transaction so a token
// can never be consumed twice and a password update never lands without its
// claim (and session revocation) committing together.
type PasswordResetRepository interface {
	// CreateWithEmailOutbox revokes prior PENDING tokens for the user, inserts
	// the new token, and enqueues the reset email — atomically. A concurrent
	// request winning the pending race surfaces
	// ErrPasswordResetAlreadyPending (benign: the winner's email is on its way).
	CreateWithEmailOutbox(ctx context.Context, token *model.PasswordResetToken, entry *model.EmailOutbox) error

	// ConsumeWithPasswordUpdate claims the token (PENDING and unexpired),
	// updates the user's password hash, revokes the user's other PENDING
	// tokens, and deletes the user's dashboard refresh sessions — atomically.
	// Returns the owning user ID. Unknown tokens map to
	// ErrPasswordResetNotFound, past-expiry tokens to ErrPasswordResetExpired,
	// and lost single-use races to ErrPasswordResetConsumed.
	ConsumeWithPasswordUpdate(ctx context.Context, tokenHash, passwordHash string) (uuid.UUID, error)

	// DeleteExpired removes consumed/revoked/expired rows older than the
	// cutoff. Safe periodic cleanup; never required for correctness.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// ─── PostgreSQL implementation ───────────────────────────────────────────────

type pgPasswordResetRepository struct {
	db     *pgxpool.Pool
	outbox EmailOutboxRepository
}

var _ PasswordResetRepository = (*pgPasswordResetRepository)(nil)

// NewPasswordResetRepository returns a PostgreSQL-backed PasswordResetRepository.
func NewPasswordResetRepository(db *pgxpool.Pool, outbox EmailOutboxRepository) PasswordResetRepository {
	return &pgPasswordResetRepository{db: db, outbox: outbox}
}

func (r *pgPasswordResetRepository) CreateWithEmailOutbox(
	ctx context.Context,
	token *model.PasswordResetToken,
	entry *model.EmailOutbox,
) error {
	// NOTE: no audit events are recorded here; the public reset flow runs
	// outside an audited session, so no recorder is required.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("password reset repository create begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op after commit

	// Supersede prior requests first: at most one active token per user.
	if _, err := tx.Exec(ctx, `
		UPDATE password_reset_tokens
		SET status = 'REVOKED', updated_at = NOW()
		WHERE user_id = $1 AND status = 'PENDING'`,
		token.UserID,
	); err != nil {
		return fmt.Errorf("password reset repository revoke prior: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO password_reset_tokens (id, user_id, token_hash, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'PENDING', $4, NOW(), NOW())`,
		token.ID, token.UserID, token.TokenHash, token.ExpiresAt,
	); err != nil {
		if isUniqueViolation(err) {
			if uniqueViolationOn(err, resetPendingIndex) {
				return ErrPasswordResetAlreadyPending
			}
			if uniqueViolationOn(err, resetTokenHashIndex) {
				return fmt.Errorf("password reset repository create: token hash collision: %w", err)
			}
			return ErrPasswordResetAlreadyPending
		}
		return fmt.Errorf("password reset repository create: %w", err)
	}

	// Same tx: a failure here rolls the token back too — no orphan email rows.
	if err := r.outbox.EnqueueInTx(ctx, tx, entry); err != nil {
		return fmt.Errorf("password reset repository enqueue email: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("password reset repository create commit: %w", err)
	}
	return nil
}

func (r *pgPasswordResetRepository) ConsumeWithPasswordUpdate(
	ctx context.Context,
	tokenHash, passwordHash string,
) (uuid.UUID, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("password reset repository consume begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op after commit

	// Single-use claim: exactly one concurrent transaction can flip a given
	// PENDING, unexpired row to CONSUMED. Everyone else sees zero rows.
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE password_reset_tokens
		SET status = 'CONSUMED', consumed_at = NOW(), updated_at = NOW()
		WHERE token_hash = $1 AND status = 'PENDING' AND expires_at > NOW()
		RETURNING user_id`,
		tokenHash,
	).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, r.classifyMiss(ctx, tx, tokenHash)
		}
		return uuid.Nil, fmt.Errorf("password reset repository claim: %w", err)
	}

	// Password update lands only under the won claim.
	if _, err := tx.Exec(ctx, `
		UPDATE merchant_users
		SET password_hash = $2, updated_at = NOW()
		WHERE id = $1`,
		userID, passwordHash,
	); err != nil {
		return uuid.Nil, fmt.Errorf("password reset repository update password: %w", err)
	}

	// Defense in depth: clear any other PENDING rows for the user.
	if _, err := tx.Exec(ctx, `
		UPDATE password_reset_tokens
		SET status = 'REVOKED', updated_at = NOW()
		WHERE user_id = $1 AND status = 'PENDING'`,
		userID,
	); err != nil {
		return uuid.Nil, fmt.Errorf("password reset repository revoke others: %w", err)
	}

	// Previously established refresh sessions must not survive the reset.
	// (Already-issued stateless access JWTs still expire naturally; the
	// architecture has no JWT blacklist by design.)
	if _, err := tx.Exec(ctx, `
		DELETE FROM dashboard_sessions
		WHERE merchant_user_id = $1`,
		userID,
	); err != nil {
		return uuid.Nil, fmt.Errorf("password reset repository revoke sessions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("password reset repository consume commit: %w", err)
	}
	return userID, nil
}

// classifyMiss distinguishes unknown tokens from expired vs consumed/revoked
// ones with a single read inside the same transaction.
func (r *pgPasswordResetRepository) classifyMiss(ctx context.Context, tx pgx.Tx, tokenHash string) error {
	var status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT status, expires_at
		FROM password_reset_tokens
		WHERE token_hash = $1`,
		tokenHash,
	).Scan(&status, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPasswordResetNotFound
		}
		return fmt.Errorf("password reset repository classify: %w", err)
	}
	if !expiresAt.After(time.Now()) {
		return ErrPasswordResetExpired
	}
	if status != string(model.PasswordResetTokenPending) {
		return ErrPasswordResetConsumed
	}
	// PENDING and unexpired but the claim missed: a concurrent transaction won
	// the race between our statements.
	return ErrPasswordResetConsumed
}

func (r *pgPasswordResetRepository) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.Exec(ctx, `
		DELETE FROM password_reset_tokens
		WHERE (status IN ('CONSUMED', 'REVOKED') OR expires_at <= NOW())
		  AND updated_at < $1`,
		before,
	)
	if err != nil {
		return 0, fmt.Errorf("password reset repository delete expired: %w", err)
	}
	return res.RowsAffected(), nil
}
