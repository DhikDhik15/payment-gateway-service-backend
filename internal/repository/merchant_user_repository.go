package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
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

	// CreateInTx inserts a merchant_users row using an existing transaction.
	// Returns ErrMerchantUserEmailExists on unique email violation.
	CreateInTx(ctx context.Context, tx pgx.Tx, user *model.MerchantUser) error

	// GetByID retrieves a user by primary key.
	// Returns ErrMerchantUserNotFound when no row matches.
	GetByID(ctx context.Context, id uuid.UUID) (*model.MerchantUser, error)

	// GetByEmail retrieves a user by their normalised email address.
	// Returns ErrMerchantUserNotFound when no row matches.
	GetByEmail(ctx context.Context, email string) (*model.MerchantUser, error)

	// ListByMerchant returns all users belonging to merchantID, newest first.
	ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*model.MerchantUser, error)

	// UpdateStatus changes the user status (ACTIVE ↔ DISABLED). PostgreSQL
	// implementations route owner-affecting changes through the merchant
	// coordination transaction.
	// Returns ErrMerchantUserNotFound when no row matches.
	UpdateStatus(ctx context.Context, id uuid.UUID, status model.DashboardUserStatus) error

	// UpdateRole changes the user's role. PostgreSQL implementations route
	// owner-affecting changes through the merchant coordination transaction.
	// Returns ErrMerchantUserNotFound when no row matches.
	UpdateRole(ctx context.Context, id uuid.UUID, role model.DashboardUserRole) error

	// CountActiveOwners returns the number of ACTIVE users with role OWNER
	// belonging to merchantID. Used to enforce the last-OWNER invariant.
	CountActiveOwners(ctx context.Context, merchantID uuid.UUID) (int, error)

	// UpdatePasswordHash replaces the Argon2id password hash for a user.
	// Returns ErrMerchantUserNotFound when no row matches.
	UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error

	// UpdateLastLoginAt sets last_login_at. Best-effort — a failure here must
	// not fail authentication.
	UpdateLastLoginAt(ctx context.Context, id uuid.UUID, t time.Time) error
}

// OwnerInvariantRepository is the database-safe owner-mutation capability used
// by the dashboard team service. It is intentionally separate from
// MerchantUserRepository so existing non-team repository fakes remain source
// compatible; the PostgreSQL implementation below is the production provider.
//
// Both operations acquire the merchant coordination row with FOR UPDATE before
// re-reading the target and counting ACTIVE OWNERs. Consequently, owner
// mutations serialize only within the affected merchant and the count and
// mutation commit atomically.
type OwnerInvariantRepository interface {
	UpdateStatusWithOwnerLock(
		ctx context.Context,
		merchantID uuid.UUID,
		targetUserID uuid.UUID,
		status model.DashboardUserStatus,
	) (*model.MerchantUser, error)

	UpdateRoleWithOwnerLock(
		ctx context.Context,
		merchantID uuid.UUID,
		targetUserID uuid.UUID,
		role model.DashboardUserRole,
	) (*model.MerchantUser, error)
}

// TransactionalUserStatusRepository is the production status-mutation
// capability that commits user status, session revocation, and audit together.
// It is optional so older in-memory/test repositories remain source-compatible.
type TransactionalUserStatusRepository interface {
	UpdateStatusWithOwnerLockAndRevokeSessions(
		ctx context.Context,
		merchantID uuid.UUID,
		targetUserID uuid.UUID,
		status model.DashboardUserStatus,
	) (*model.MerchantUser, error)
}

// TransactionalPasswordUpdateRepository combines the password mutation and
// refresh-session revocation in one PostgreSQL transaction. The dashboard
// service uses this capability in production; legacy/test repositories may
// implement only UpdatePasswordHash and retain the compatibility fallback.
type TransactionalPasswordUpdateRepository interface {
	UpdatePasswordHashAndRevokeSessions(ctx context.Context, id uuid.UUID, passwordHash string) error
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgMerchantUserRepository struct {
	db      *pgxpool.Pool
	auditor audit.Recorder
}

var _ OwnerInvariantRepository = (*pgMerchantUserRepository)(nil)

// NewMerchantUserRepository returns a PostgreSQL-backed MerchantUserRepository.
// The optional recorder enables same-transaction audit events; existing test
// doubles and callers that do not need auditing remain source-compatible.
func NewMerchantUserRepository(db *pgxpool.Pool, recorders ...audit.Recorder) MerchantUserRepository {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgMerchantUserRepository{db: db, auditor: recorder}
}

// Create inserts a new merchant_users row. When an audit event is attached,
// creation and its audit record commit together.
func (r *pgMerchantUserRepository) Create(ctx context.Context, user *model.MerchantUser) error {
	hasEvent := false
	if _, hasEvent = audit.EventFromContext(ctx); hasEvent {
		if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
			return err
		}
	} else {
		return insertMerchantUser(ctx, r.db, user)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant user repository create begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := insertMerchantUser(ctx, tx, user); err != nil {
		return err
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant user repository create commit: %w", err)
	}
	committed = true
	return nil
}

// CreateInTx inserts a merchant_users row within an existing transaction.
func (r *pgMerchantUserRepository) CreateInTx(ctx context.Context, tx pgx.Tx, user *model.MerchantUser) error {
	return insertMerchantUser(ctx, tx, user)
}

type merchantUserExecuter interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func insertMerchantUser(ctx context.Context, db merchantUserExecuter, user *model.MerchantUser) error {
	const q = `
		INSERT INTO merchant_users
		    (id, merchant_id, email, password_hash, role, status, last_login_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := db.Exec(ctx, q,
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

// UpdateStatus changes the user's status. It resolves the user's merchant and
// delegates to the same owner-lock transaction used by the team service, so
// this legacy interface method cannot bypass the ACTIVE OWNER invariant.
func (r *pgMerchantUserRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status model.DashboardUserStatus) error {
	merchantID, err := r.merchantIDForUser(ctx, id)
	if err != nil {
		return err
	}
	_, err = r.updateWithOwnerLock(ctx, merchantID, id, &status, nil)
	return err
}

// UpdateRole changes a user's role through the owner-lock transaction for the
// same reason as UpdateStatus.
func (r *pgMerchantUserRepository) UpdateRole(ctx context.Context, id uuid.UUID, role model.DashboardUserRole) error {
	merchantID, err := r.merchantIDForUser(ctx, id)
	if err != nil {
		return err
	}
	_, err = r.updateWithOwnerLock(ctx, merchantID, id, nil, &role)
	return err
}

func (r *pgMerchantUserRepository) merchantIDForUser(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var merchantID uuid.UUID
	if err := r.db.QueryRow(ctx, `
		SELECT merchant_id
		FROM merchant_users
		WHERE id = $1
	`, userID).Scan(&merchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrMerchantUserNotFound
		}
		return uuid.Nil, fmt.Errorf("merchant user repository resolve merchant: %w", err)
	}
	return merchantID, nil
}

// UpdateStatusWithOwnerLock changes a user's status while preserving the
// merchant's ACTIVE OWNER invariant. The merchant row is the per-tenant
// coordination lock; it is acquired before the target row and owner count are
// read. The returned row is the committed post-update state.
func (r *pgMerchantUserRepository) UpdateStatusWithOwnerLock(
	ctx context.Context,
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	status model.DashboardUserStatus,
) (*model.MerchantUser, error) {
	return r.updateWithOwnerLock(ctx, merchantID, targetUserID, &status, nil)
}

// UpdateStatusWithOwnerLockAndRevokeSessions is the explicit production
// capability used by the dashboard service. The common transaction already
// revokes sessions whenever the resulting status is DISABLED.
func (r *pgMerchantUserRepository) UpdateStatusWithOwnerLockAndRevokeSessions(
	ctx context.Context,
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	status model.DashboardUserStatus,
) (*model.MerchantUser, error) {
	return r.updateWithOwnerLock(ctx, merchantID, targetUserID, &status, nil)
}

// UpdateRoleWithOwnerLock changes a user's role while preserving the
// merchant's ACTIVE OWNER invariant. It uses the same merchant-row lock and
// post-lock count as UpdateStatusWithOwnerLock.
func (r *pgMerchantUserRepository) UpdateRoleWithOwnerLock(
	ctx context.Context,
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	role model.DashboardUserRole,
) (*model.MerchantUser, error) {
	return r.updateWithOwnerLock(ctx, merchantID, targetUserID, nil, &role)
}

// updateWithOwnerLock is the single transaction used by both owner-affecting
// mutations. The lock order is deliberately merchant -> target user. Every
// operation that can remove ACTIVE OWNER status uses this same order, so two
// mutations for one merchant serialize while unrelated merchants remain
// independent.
func (r *pgMerchantUserRepository) updateWithOwnerLock(
	ctx context.Context,
	merchantID uuid.UUID,
	targetUserID uuid.UUID,
	status *model.DashboardUserStatus,
	role *model.DashboardUserRole,
) (updated *model.MerchantUser, err error) {
	if (status == nil) == (role == nil) {
		return nil, errors.New("merchant user owner mutation requires exactly one field")
	}
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return nil, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("merchant user repository owner mutation begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			if err == nil {
				err = fmt.Errorf("merchant user repository owner mutation rollback: %w", rollbackErr)
			} else {
				err = fmt.Errorf("%w; rollback: %v", err, rollbackErr)
			}
		}
	}()

	// Serialize all owner-affecting changes for this merchant. Merchants use
	// independent rows, so this does not create a global bottleneck.
	var lockedMerchantID uuid.UUID
	var merchantStatus model.MerchantStatus
	if err = tx.QueryRow(ctx, `
		SELECT id, status
		FROM merchants
		WHERE id = $1
		FOR UPDATE
	`, merchantID).Scan(&lockedMerchantID, &merchantStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("merchant user repository lock merchant: %w", err)
	}
	if merchantStatus != model.MerchantStatusActive {
		return nil, ErrMerchantInactive
	}

	// Resolve tenant ownership without taking a row lock. If the target is
	// foreign, return before locking that user's row (cross-tenant requests
	// therefore cannot deadlock one another). The actual target state is
	// re-read and locked below after this check.
	var targetMerchantID uuid.UUID
	if err = tx.QueryRow(ctx, `
		SELECT merchant_id
		FROM merchant_users
		WHERE id = $1
	`, targetUserID).Scan(&targetMerchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantUserNotFound
		}
		return nil, fmt.Errorf("merchant user repository resolve target tenant: %w", err)
	}
	if targetMerchantID != lockedMerchantID {
		return nil, ErrMerchantUserCrossTenant
	}

	// Re-read the target only after the coordination lock is held. The
	// merchant predicate remains explicit on the locking query as defense in
	// depth.
	target, err := scanMerchantUser(tx.QueryRow(ctx, `
		SELECT id, merchant_id, email, password_hash, role, status,
		       last_login_at, created_at, updated_at
		FROM merchant_users
		WHERE id = $1 AND merchant_id = $2
		FOR UPDATE
	`, targetUserID, lockedMerchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantUserNotFound
		}
		return nil, fmt.Errorf("merchant user repository lock target: %w", err)
	}
	if target.MerchantID != lockedMerchantID {
		return nil, ErrMerchantUserCrossTenant
	}

	removesActiveOwner := target.Role == model.DashboardUserRoleOwner &&
		target.Status == model.DashboardUserStatusActive &&
		((status != nil && *status == model.DashboardUserStatusDisabled) ||
			(role != nil && *role != model.DashboardUserRoleOwner))
	if removesActiveOwner {
		var activeOwners int
		if err = tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM merchant_users
			WHERE merchant_id = $1
			  AND role = 'OWNER'
			  AND status = 'ACTIVE'
		`, lockedMerchantID).Scan(&activeOwners); err != nil {
			return nil, fmt.Errorf("merchant user repository count active owners: %w", err)
		}
		if activeOwners <= 1 {
			return nil, ErrLastActiveOwnerRequired
		}
	}

	if status != nil {
		updated, err = scanMerchantUser(tx.QueryRow(ctx, `
			UPDATE merchant_users
			SET status = $1, updated_at = NOW()
			WHERE id = $2 AND merchant_id = $3
			RETURNING id, merchant_id, email, password_hash, role, status,
			          last_login_at, created_at, updated_at
		`, *status, targetUserID, lockedMerchantID))
	} else {
		updated, err = scanMerchantUser(tx.QueryRow(ctx, `
			UPDATE merchant_users
			SET role = $1, updated_at = NOW()
			WHERE id = $2 AND merchant_id = $3
			RETURNING id, merchant_id, email, password_hash, role, status,
			          last_login_at, created_at, updated_at
		`, *role, targetUserID, lockedMerchantID))
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantUserNotFound
		}
		return nil, fmt.Errorf("merchant user repository owner mutation update: %w", err)
	}
	if status != nil && *status == model.DashboardUserStatusDisabled {
		if _, err := tx.Exec(ctx, `
			DELETE FROM dashboard_sessions WHERE merchant_user_id = $1
		`, targetUserID); err != nil {
			return nil, fmt.Errorf("merchant user repository status session revocation: %w", err)
		}
	}

	if event, ok := audit.EventFromContext(ctx); ok {
		values := map[string]any{}
		if status != nil {
			values["old_status"] = target.Status
			values["new_status"] = *status
		} else {
			values["old_role"] = target.Role
			values["new_role"] = *role
		}
		event, err = audit.MergeMetadata(event, values)
		if err != nil {
			return nil, err
		}
		if err := audit.RecordEventInTx(ctx, tx, r.auditor, event); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("merchant user repository owner mutation commit: %w", err)
	}
	committed = true
	return updated, nil
}

// CountActiveOwners returns the number of ACTIVE OWNERs for a merchant.
func (r *pgMerchantUserRepository) CountActiveOwners(ctx context.Context, merchantID uuid.UUID) (int, error) {
	const q = `
		SELECT COUNT(*)
		FROM   merchant_users
		WHERE  merchant_id = $1
		  AND  role   = 'OWNER'
		  AND  status = 'ACTIVE'
	`
	var count int
	if err := r.db.QueryRow(ctx, q, merchantID).Scan(&count); err != nil {
		return 0, fmt.Errorf("merchant user repository count active owners: %w", err)
	}
	return count, nil
}

// UpdatePasswordHash replaces the password hash for a user. When an audit
// event is attached, the password mutation and event commit atomically.
func (r *pgMerchantUserRepository) UpdatePasswordHash(ctx context.Context, id uuid.UUID, passwordHash string) error {
	_, hasEvent := audit.EventFromContext(ctx)
	if !hasEvent {
		const q = `
			UPDATE merchant_users
			SET    password_hash = $1, updated_at = $2
			WHERE  id = $3
		`
		tag, err := r.db.Exec(ctx, q, passwordHash, time.Now().UTC(), id)
		if err != nil {
			return fmt.Errorf("merchant user repository update password hash: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrMerchantUserNotFound
		}
		return nil
	}
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant user repository password update begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	const q = `
		UPDATE merchant_users
		SET    password_hash = $1, updated_at = $2
		WHERE  id = $3
	`
	tag, err := tx.Exec(ctx, q, passwordHash, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("merchant user repository password hash update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantUserNotFound
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant user repository password update commit: %w", err)
	}
	committed = true
	return nil
}

// UpdatePasswordHashAndRevokeSessions changes a user's password and deletes
// every refresh session in the same transaction. The merchant coordination row
// is locked first, matching refresh rotation's lock order, so a concurrent
// rotation cannot install a replacement after revocation. When an audit event
// is attached, all three effects commit atomically.
func (r *pgMerchantUserRepository) UpdatePasswordHashAndRevokeSessions(
	ctx context.Context,
	id uuid.UUID,
	passwordHash string,
) (err error) {
	if _, hasEvent := audit.EventFromContext(ctx); hasEvent {
		if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
			return err
		}
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant user repository password/session transaction begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			if err == nil {
				err = fmt.Errorf("merchant user repository password/session rollback: %w", rollbackErr)
			} else {
				err = fmt.Errorf("%w; rollback: %v", err, rollbackErr)
			}
		}
	}()

	var merchantID uuid.UUID
	if err = tx.QueryRow(ctx, `
		SELECT merchant_id FROM merchant_users WHERE id = $1
	`, id).Scan(&merchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMerchantUserNotFound
		}
		return fmt.Errorf("merchant user repository password merchant lookup: %w", err)
	}
	if err = tx.QueryRow(ctx, `
		SELECT id FROM merchants WHERE id = $1 FOR UPDATE
	`, merchantID).Scan(&merchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMerchantNotFound
		}
		return fmt.Errorf("merchant user repository password merchant lock: %w", err)
	}
	var lockedMerchantID uuid.UUID
	if err = tx.QueryRow(ctx, `
		SELECT merchant_id FROM merchant_users
		WHERE id = $1 AND merchant_id = $2
		FOR UPDATE
	`, id, merchantID).Scan(&lockedMerchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMerchantUserNotFound
		}
		return fmt.Errorf("merchant user repository password user lock: %w", err)
	}
	if lockedMerchantID != merchantID {
		return ErrMerchantUserCrossTenant
	}

	tag, err := tx.Exec(ctx, `
		UPDATE merchant_users
		SET password_hash = $1, updated_at = $2
		WHERE id = $3 AND merchant_id = $4
	`, passwordHash, time.Now().UTC(), id, merchantID)
	if err != nil {
		return fmt.Errorf("merchant user repository transactional password update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantUserNotFound
	}
	if _, err = tx.Exec(ctx, `DELETE FROM dashboard_sessions WHERE merchant_user_id = $1`, id); err != nil {
		return fmt.Errorf("merchant user repository transactional session revocation: %w", err)
	}
	if err = audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant user repository transactional password commit: %w", err)
	}
	committed = true
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
