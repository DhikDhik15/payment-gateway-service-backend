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
	// ErrMerchantAPIKeyNotFound is returned when no matching key row exists.
	ErrMerchantAPIKeyNotFound = errors.New("merchant api key not found")

	// ErrMerchantAPIKeyAlreadyRevoked is returned when a key that is already
	// REVOKED is targeted by a revoke operation.
	ErrMerchantAPIKeyAlreadyRevoked = errors.New("merchant api key already revoked")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// MerchantAPIKeyRepository defines the database operations for merchant API keys.
type MerchantAPIKeyRepository interface {
	// Create inserts a new API key row. key.ID, key.KeyID, key.SecretHash,
	// key.MerchantID, and key.Name must be populated by the caller.
	Create(ctx context.Context, key *model.MerchantAPIKey) error

	// CreateInTx inserts a new API key row using an existing transaction.
	CreateInTx(ctx context.Context, tx pgx.Tx, key *model.MerchantAPIKey) error

	// GetByID retrieves a key by its primary key, enforcing merchant ownership.
	// Returns ErrMerchantAPIKeyNotFound if id does not exist or belongs to a
	// different merchant.
	GetByID(ctx context.Context, merchantID, id uuid.UUID) (*model.MerchantAPIKey, error)

	// GetByKeyID retrieves a key by its public key identifier (the "pk_…" string).
	// Does NOT filter by merchant — the caller must verify ownership after lookup
	// because the keyID alone is the routing token during authentication.
	GetByKeyID(ctx context.Context, keyID string) (*model.MerchantAPIKey, error)

	// ListByMerchant returns all keys belonging to merchantID, ordered by
	// created_at DESC.
	ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*model.MerchantAPIKey, error)

	// Revoke transitions a key from ACTIVE → REVOKED.
	// Returns ErrMerchantAPIKeyNotFound if the key does not exist or does not
	// belong to merchantID.
	// Returns ErrMerchantAPIKeyAlreadyRevoked if status is already REVOKED.
	Revoke(ctx context.Context, merchantID, id uuid.UUID) error

	// Rotate atomically revokes oldID and inserts newKey within a single
	// database transaction so no partial state is left on failure.
	// oldID must belong to merchantID; newKey.MerchantID must equal merchantID.
	Rotate(ctx context.Context, merchantID, oldID uuid.UUID, newKey *model.MerchantAPIKey) error

	// UpdateLastUsedAt sets last_used_at = now for the given key ID.
	// This is best-effort metadata — a failure here must not fail authentication.
	UpdateLastUsedAt(ctx context.Context, id uuid.UUID) error
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgMerchantAPIKeyRepository struct {
	db      *pgxpool.Pool
	auditor audit.Recorder
}

// NewMerchantAPIKeyRepository returns a PostgreSQL-backed MerchantAPIKeyRepository.
// The optional recorder enables atomic audit writes for credential mutations.
func NewMerchantAPIKeyRepository(db *pgxpool.Pool, recorders ...audit.Recorder) MerchantAPIKeyRepository {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgMerchantAPIKeyRepository{db: db, auditor: recorder}
}

// Create inserts a new merchant_api_keys row. If an audit event is attached,
// the credential and event are committed in one transaction.
func (r *pgMerchantAPIKeyRepository) Create(ctx context.Context, key *model.MerchantAPIKey) error {
	_, hasEvent := audit.EventFromContext(ctx)
	if !hasEvent {
		return insertMerchantAPIKey(ctx, r.db, key)
	}
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant api key repository create begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := insertMerchantAPIKey(ctx, tx, key); err != nil {
		return err
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant api key repository create commit: %w", err)
	}
	committed = true
	return nil
}

// CreateInTx inserts a new merchant_api_keys row within an existing transaction.
func (r *pgMerchantAPIKeyRepository) CreateInTx(ctx context.Context, tx pgx.Tx, key *model.MerchantAPIKey) error {
	return insertMerchantAPIKey(ctx, tx, key)
}

type merchantAPIKeyExecuter interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func insertMerchantAPIKey(ctx context.Context, db merchantAPIKeyExecuter, key *model.MerchantAPIKey) error {
	const q = `
		INSERT INTO merchant_api_keys
		    (id, merchant_id, key_id, secret_hash, name, status,
		     last_used_at, expires_at, revoked_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`
	_, err := db.Exec(ctx, q,
		key.ID,
		key.MerchantID,
		key.KeyID,
		key.SecretHash,
		key.Name,
		key.Status,
		key.LastUsedAt,
		key.ExpiresAt,
		key.RevokedAt,
		key.CreatedAt,
		key.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("merchant api key repository create: %w", err)
	}
	return nil
}

// GetByID retrieves a key by primary key, scoped to merchantID.
func (r *pgMerchantAPIKeyRepository) GetByID(ctx context.Context, merchantID, id uuid.UUID) (*model.MerchantAPIKey, error) {
	const q = `
		SELECT id, merchant_id, key_id, secret_hash, name, status,
		       last_used_at, expires_at, revoked_at, created_at, updated_at
		FROM   merchant_api_keys
		WHERE  id = $1
		AND    merchant_id = $2
	`
	key, err := scanMerchantAPIKey(r.db.QueryRow(ctx, q, id, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantAPIKeyNotFound
		}
		return nil, fmt.Errorf("merchant api key repository get by id: %w", err)
	}
	return key, nil
}

// GetByKeyID retrieves a key by its public key_id string.
// Used during authentication — no merchant scope here because we derive the
// merchant from the key itself.
func (r *pgMerchantAPIKeyRepository) GetByKeyID(ctx context.Context, keyID string) (*model.MerchantAPIKey, error) {
	const q = `
		SELECT id, merchant_id, key_id, secret_hash, name, status,
		       last_used_at, expires_at, revoked_at, created_at, updated_at
		FROM   merchant_api_keys
		WHERE  key_id = $1
	`
	key, err := scanMerchantAPIKey(r.db.QueryRow(ctx, q, keyID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantAPIKeyNotFound
		}
		return nil, fmt.Errorf("merchant api key repository get by key id: %w", err)
	}
	return key, nil
}

// ListByMerchant returns all keys for a merchant, newest first.
func (r *pgMerchantAPIKeyRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*model.MerchantAPIKey, error) {
	const q = `
		SELECT id, merchant_id, key_id, secret_hash, name, status,
		       last_used_at, expires_at, revoked_at, created_at, updated_at
		FROM   merchant_api_keys
		WHERE  merchant_id = $1
		ORDER  BY created_at DESC
	`
	rows, err := r.db.Query(ctx, q, merchantID)
	if err != nil {
		return nil, fmt.Errorf("merchant api key repository list: %w", err)
	}
	defer rows.Close()

	var keys []*model.MerchantAPIKey
	for rows.Next() {
		key, err := scanMerchantAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("merchant api key repository list scan: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("merchant api key repository list rows: %w", err)
	}
	return keys, nil
}

// Revoke transitions a key from ACTIVE → REVOKED. When an audit event is
// attached, the row is locked and the mutation plus event commit atomically.
func (r *pgMerchantAPIKeyRepository) Revoke(ctx context.Context, merchantID, id uuid.UUID) error {
	_, hasEvent := audit.EventFromContext(ctx)
	if hasEvent {
		if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
			return err
		}
		return r.revokeAudited(ctx, merchantID, id)
	}

	// First verify the key exists and belongs to the merchant.
	key, err := r.GetByID(ctx, merchantID, id)
	if err != nil {
		return err // ErrMerchantAPIKeyNotFound already
	}
	if key.Status == model.MerchantAPIKeyStatusRevoked {
		return ErrMerchantAPIKeyAlreadyRevoked
	}

	now := time.Now().UTC()
	const q = `
		UPDATE merchant_api_keys
		SET    status     = 'REVOKED',
		       revoked_at = $1,
		       updated_at = $2
		WHERE  id          = $3
		AND    merchant_id = $4
		AND    status      = 'ACTIVE'
	`
	tag, err := r.db.Exec(ctx, q, now, now, id, merchantID)
	if err != nil {
		return fmt.Errorf("merchant api key repository revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Concurrent revoke by another request beat us to it.
		return ErrMerchantAPIKeyAlreadyRevoked
	}
	return nil
}

func (r *pgMerchantAPIKeyRepository) revokeAudited(ctx context.Context, merchantID, id uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant api key repository revoke begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	const selectQ = `
		SELECT status
		FROM merchant_api_keys
		WHERE id = $1 AND merchant_id = $2
		FOR UPDATE
	`
	var status model.MerchantAPIKeyStatus
	if err := tx.QueryRow(ctx, selectQ, id, merchantID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMerchantAPIKeyNotFound
		}
		return fmt.Errorf("merchant api key repository revoke select: %w", err)
	}
	if status == model.MerchantAPIKeyStatusRevoked {
		return ErrMerchantAPIKeyAlreadyRevoked
	}

	now := time.Now().UTC()
	const updateQ = `
		UPDATE merchant_api_keys
		SET status = 'REVOKED', revoked_at = $1, updated_at = $2
		WHERE id = $3 AND merchant_id = $4 AND status = 'ACTIVE'
	`
	tag, err := tx.Exec(ctx, updateQ, now, now, id, merchantID)
	if err != nil {
		return fmt.Errorf("merchant api key repository revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantAPIKeyAlreadyRevoked
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant api key repository revoke commit: %w", err)
	}
	committed = true
	return nil
}

// Rotate atomically revokes oldID and inserts newKey within a transaction.
func (r *pgMerchantAPIKeyRepository) Rotate(ctx context.Context, merchantID, oldID uuid.UUID, newKey *model.MerchantAPIKey) error {
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant api key repository rotate begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rolled back on any error path

	now := time.Now().UTC()

	// 1. Revoke old key — conditional on ACTIVE so concurrent rotates are safe.
	const revokeQ = `
		UPDATE merchant_api_keys
		SET    status     = 'REVOKED',
		       revoked_at = $1,
		       updated_at = $2
		WHERE  id          = $3
		AND    merchant_id = $4
		AND    status      = 'ACTIVE'
	`
	tag, err := tx.Exec(ctx, revokeQ, now, now, oldID, merchantID)
	if err != nil {
		return fmt.Errorf("merchant api key repository rotate revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Key not found or not ACTIVE (already revoked or wrong merchant).
		return ErrMerchantAPIKeyNotFound
	}

	// 2. Insert new key.
	const insertQ = `
		INSERT INTO merchant_api_keys
		    (id, merchant_id, key_id, secret_hash, name, status,
		     last_used_at, expires_at, revoked_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`
	_, err = tx.Exec(ctx, insertQ,
		newKey.ID,
		newKey.MerchantID,
		newKey.KeyID,
		newKey.SecretHash,
		newKey.Name,
		newKey.Status,
		newKey.LastUsedAt,
		newKey.ExpiresAt,
		newKey.RevokedAt,
		newKey.CreatedAt,
		newKey.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("merchant api key repository rotate insert: %w", err)
	}

	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant api key repository rotate commit: %w", err)
	}
	return nil
}

// UpdateLastUsedAt updates the last_used_at timestamp for a key.
// Best-effort — the caller should not fail authentication if this errors.
func (r *pgMerchantAPIKeyRepository) UpdateLastUsedAt(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE merchant_api_keys
		SET    last_used_at = NOW(),
		       updated_at   = NOW()
		WHERE  id = $1
	`
	_, err := r.db.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("merchant api key repository update last used at: %w", err)
	}
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func scanMerchantAPIKey(row pgx.Row) (*model.MerchantAPIKey, error) {
	k := &model.MerchantAPIKey{}
	err := row.Scan(
		&k.ID,
		&k.MerchantID,
		&k.KeyID,
		&k.SecretHash,
		&k.Name,
		&k.Status,
		&k.LastUsedAt,
		&k.ExpiresAt,
		&k.RevokedAt,
		&k.CreatedAt,
		&k.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return k, nil
}
