package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MerchantRepository defines the database operations for merchants.
// Using an interface allows the service to be tested without a real database.
type MerchantRepository interface {
	Create(ctx context.Context, m *model.Merchant) error
	// CreateInTx inserts a merchant row using an existing transaction.
	CreateInTx(ctx context.Context, tx pgx.Tx, m *model.Merchant) error
	GetByID(ctx context.Context, id uuid.UUID) (*model.Merchant, error)
	GetByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error)
	ExistsByCode(ctx context.Context, code string) (bool, error)
	// UpdateStatus changes only the status and updated_at of a single merchant.
	// Returns ErrMerchantNotFound when no row matches the given id.
	// All other merchant fields (name, code, api_key, api_secret) are never touched.
	UpdateStatus(ctx context.Context, id uuid.UUID, status model.MerchantStatus) error
}

// pgMerchantRepository is the PostgreSQL implementation of MerchantRepository.
type pgMerchantRepository struct {
	db      *pgxpool.Pool
	auditor audit.Recorder
}

// NewMerchantRepository returns a PostgreSQL-backed MerchantRepository.
func NewMerchantRepository(db *pgxpool.Pool, recorders ...audit.Recorder) MerchantRepository {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgMerchantRepository{db: db, auditor: recorder}
}

// Create inserts a new merchant row. The caller must have populated all fields
// including ID, APIKey, APISecret, and Status before calling.
func (r *pgMerchantRepository) Create(ctx context.Context, m *model.Merchant) error {
	return insertMerchant(ctx, r.db, m)
}

// CreateInTx inserts a merchant row within an existing transaction.
func (r *pgMerchantRepository) CreateInTx(ctx context.Context, tx pgx.Tx, m *model.Merchant) error {
	return insertMerchant(ctx, tx, m)
}

type merchantExecuter interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func insertMerchant(ctx context.Context, db merchantExecuter, m *model.Merchant) error {
	// Phase 8D.3: legacy credentials are optional. An empty string means "no
	// legacy credential" and is stored as NULL (the columns are nullable since
	// migration 000017). The legacy state always has an explicit value.
	const q = `
		INSERT INTO merchants (id, name, code, api_key, api_secret, status,
		                       legacy_credential_state, legacy_credential_disabled_at,
		                       created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := db.Exec(ctx, q,
		m.ID,
		m.Name,
		m.Code,
		nullableString(m.APIKey),
		nullableString(m.APISecret),
		m.Status,
		legacyStateOrLegacy(m.LegacyCredentialState),
		m.LegacyCredentialDisabledAt,
		m.CreatedAt,
		m.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrMerchantCodeExists
		}
		return fmt.Errorf("merchant repository create: %w", err)
	}
	return nil
}

// nullableString maps an empty credential to SQL NULL: a merchant created
// after the Phase 8D.3 freeze holds no legacy credential.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// legacyStateOrLegacy defaults an unset state to LEGACY so rows written by
// pre-8D.3 callers (tests, fixtures) keep their previous semantics.
func legacyStateOrLegacy(s model.LegacyCredentialState) model.LegacyCredentialState {
	if s == "" {
		return model.LegacyCredentialStateLegacy
	}
	return s
}

// GetByID retrieves a merchant by its primary key.
// Returns ErrMerchantNotFound when no row matches.
func (r *pgMerchantRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Merchant, error) {
	const q = `
		SELECT id, name, code, COALESCE(api_key, ''), COALESCE(api_secret, ''), status,
		       legacy_credential_state, legacy_credential_disabled_at, created_at, updated_at
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
// NULL api_key rows can never match (PostgreSQL NULL != any value), so
// credential-less merchants are unreachable through this path.
func (r *pgMerchantRepository) GetByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error) {
	const q = `
		SELECT id, name, code, COALESCE(api_key, ''), COALESCE(api_secret, ''), status,
		       legacy_credential_state, legacy_credential_disabled_at, created_at, updated_at
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

// UpdateStatus changes a merchant's lifecycle status under a row lock.
// Only the status and updated_at columns are written; all other fields are
// unchanged. The lifecycle rule is re-evaluated after FOR UPDATE, so a stale
// service-side read cannot authorize a transition that is invalid after a
// concurrent mutation wins. When the merchant becomes non-ACTIVE, dashboard
// sessions are deleted in the same transaction; any failure rolls back both
// the status and session changes. When an audit event is attached, the status,
// session invalidation, and audit row commit together.
func (r *pgMerchantRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status model.MerchantStatus) error {
	if _, hasEvent := audit.EventFromContext(ctx); hasEvent {
		if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
			return err
		}
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant repository status update begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	var currentStatus model.MerchantStatus
	if err := tx.QueryRow(ctx, `
		SELECT status FROM merchants WHERE id = $1 FOR UPDATE
	`, id).Scan(&currentStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMerchantNotFound
		}
		return fmt.Errorf("merchant repository status lock: %w", err)
	}
	if currentStatus == status {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("merchant repository status no-op commit: %w", err)
		}
		committed = true
		return nil
	}
	if !currentStatus.CanTransitionTo(status) {
		return ErrMerchantStatusTransitionInvalid
	}

	const q = `
		UPDATE merchants
		SET    status     = $1,
		       updated_at = NOW()
		WHERE  id         = $2
	`
	tag, err := tx.Exec(ctx, q, status, id)
	if err != nil {
		return fmt.Errorf("merchant repository status update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantNotFound
	}
	if status != model.MerchantStatusActive {
		if _, err := tx.Exec(ctx, `
			DELETE FROM dashboard_sessions
			WHERE merchant_user_id IN (
				SELECT id FROM merchant_users WHERE merchant_id = $1
			)
		`, id); err != nil {
			return fmt.Errorf("merchant repository status session revocation: %w", err)
		}
	}
	if event, ok := audit.EventFromContext(ctx); ok {
		event, err = audit.MergeMetadata(event, map[string]any{
			"old_status": currentStatus,
			"new_status": status,
		})
		if err != nil {
			return err
		}
		if err := audit.RecordEventInTx(ctx, tx, r.auditor, event); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("merchant repository status update commit: %w", err)
	}
	committed = true
	return nil
}

// UpdateStatusWithSessionRevocation exposes the production transaction
// explicitly. UpdateStatus already performs the revocation for non-ACTIVE
// statuses; this named capability lets the service avoid a second, separate
// best-effort delete after the transaction has committed.
func (r *pgMerchantRepository) UpdateStatusWithSessionRevocation(ctx context.Context, id uuid.UUID, status model.MerchantStatus) error {
	return r.UpdateStatus(ctx, id, status)
}

// TransactionalMerchantStatusRepository is the production lifecycle capability
// that commits the merchant status and dashboard-session revocation together.
// It is optional so legacy/test repositories can use the service fallback.
type TransactionalMerchantStatusRepository interface {
	UpdateStatusWithSessionRevocation(ctx context.Context, id uuid.UUID, status model.MerchantStatus) error
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// scanMerchant reads all merchant columns from a single row.
// api_key / api_secret are COALESCEd to ” in SQL, so no NULL reaches Scan.
func scanMerchant(row pgx.Row) (*model.Merchant, error) {
	m := &model.Merchant{}
	err := row.Scan(
		&m.ID,
		&m.Name,
		&m.Code,
		&m.APIKey,
		&m.APISecret,
		&m.Status,
		&m.LegacyCredentialState,
		&m.LegacyCredentialDisabledAt,
		&m.CreatedAt,
		&m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return m, nil
}
