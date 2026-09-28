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
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrLegacyCredentialMerchantNotFound is returned when the target merchant
	// row does not exist.
	ErrLegacyCredentialMerchantNotFound = errors.New("merchant not found for legacy credential")

	// ErrLegacyCredentialAlreadyMigrated is returned when a migrate is requested
	// for a merchant that is no longer in the LEGACY state (already MIGRATED or
	// LEGACY_DISABLED). It is the concurrency loser's error too: only the
	// request that holds the row lock and sees LEGACY wins.
	ErrLegacyCredentialAlreadyMigrated = errors.New("legacy credential already migrated")

	// ErrLegacyCredentialMigrationRequired is returned when a disable is
	// requested for a merchant that still has an active legacy credential
	// (state LEGACY). Migration must happen first so the tenant never ends up
	// with no working credential at all.
	ErrLegacyCredentialMigrationRequired = errors.New("legacy credential migration required")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// LegacyCredentialStore performs the Phase 8D.3 migration-state transitions.
//
// Every method runs in a single PostgreSQL transaction that takes a row lock
// (SELECT ... FOR UPDATE) on the merchant before classifying the current state,
// so concurrent migrate/disable calls serialise deterministically:
//   - two concurrent Migrate calls → exactly one wins, the other gets
//     ErrLegacyCredentialAlreadyMigrated (409).
//   - Disable is idempotent: repeats are no-ops that never rewrite
//     legacy_credential_disabled_at.
//
// Production uses the pgx pool; tests inject an in-memory implementation.
type LegacyCredentialStore interface {
	// Migrate atomically: lock merchant → require state LEGACY → insert the
	// Phase 5C key using keyRepo → set state MIGRATED → commit.
	// key.MerchantID must equal merchantID.
	Migrate(ctx context.Context, merchantID uuid.UUID, key *model.MerchantAPIKey) error

	// Disable atomically: lock merchant → require state MIGRATED (or already
	// LEGACY_DISABLED) → set state LEGACY_DISABLED with disabled_at set once →
	// commit. Returns the resulting state and whether this call was a no-op.
	Disable(ctx context.Context, merchantID uuid.UUID) (*LegacyCredentialDisableResult, error)
}

// LegacyCredentialDisableResult describes the outcome of a disable call.
type LegacyCredentialDisableResult struct {
	State      model.LegacyCredentialState
	DisabledAt *time.Time
	// AlreadyDisabled is true when the credential was already LEGACY_DISABLED
	// before this call (idempotent no-op).
	AlreadyDisabled bool
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgLegacyCredentialStore struct {
	pool    *pgxpool.Pool
	keyRepo MerchantAPIKeyRepository
	auditor audit.Recorder
}

// NewPGLegacyCredentialStore returns a PostgreSQL-backed LegacyCredentialStore.
// keyRepo is used inside the migrate transaction so the Phase 5C key insert and
// the state flip commit or roll back together (mirrors OnboardingProvisioner).
// The optional recorder couples security events to the same transaction.
func NewPGLegacyCredentialStore(pool *pgxpool.Pool, keyRepo MerchantAPIKeyRepository, recorders ...audit.Recorder) LegacyCredentialStore {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgLegacyCredentialStore{pool: pool, keyRepo: keyRepo, auditor: recorder}
}

// Migrate runs the LEGACY → MIGRATED transition under a row lock.
func (s *pgLegacyCredentialStore) Migrate(ctx context.Context, merchantID uuid.UUID, key *model.MerchantAPIKey) error {
	if err := audit.RequireRecorder(ctx, s.auditor); err != nil {
		return err
	}
	if key.MerchantID != merchantID {
		return fmt.Errorf("legacy credential migrate: key tenant mismatch")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("legacy credential migrate begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // rolled back on any error path

	state, err := lockMerchantState(ctx, tx, merchantID)
	if err != nil {
		return err
	}
	if state != model.LegacyCredentialStateLegacy {
		return ErrLegacyCredentialAlreadyMigrated
	}

	if err := s.keyRepo.CreateInTx(ctx, tx, key); err != nil {
		return fmt.Errorf("legacy credential migrate insert key: %w", err)
	}

	const q = `
		UPDATE merchants
		SET    legacy_credential_state       = 'MIGRATED',
		       legacy_credential_disabled_at = NULL,
		       updated_at                    = NOW()
		WHERE  id = $1
	`
	if _, err := tx.Exec(ctx, q, merchantID); err != nil {
		return fmt.Errorf("legacy credential migrate update state: %w", err)
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, s.auditor); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("legacy credential migrate commit: %w", err)
	}
	return nil
}

// Disable runs the MIGRATED → LEGACY_DISABLED transition under a row lock.
// Repeating the call is a no-op: disabled_at keeps its first value.
func (s *pgLegacyCredentialStore) Disable(ctx context.Context, merchantID uuid.UUID) (*LegacyCredentialDisableResult, error) {
	if err := audit.RequireRecorder(ctx, s.auditor); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("legacy credential disable begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // rolled back on any error path

	state, err := lockMerchantState(ctx, tx, merchantID)
	if err != nil {
		return nil, err
	}

	switch state {
	case model.LegacyCredentialStateLegacy:
		// The tenant still relies on the legacy credential: force migration first
		// so it is never left without a working credential.
		return nil, ErrLegacyCredentialMigrationRequired
	case model.LegacyCredentialStateLegacyDisabled:
		disabledAt, err := readDisabledAt(ctx, tx, selectDisabledAtQuery, merchantID)
		if err != nil {
			return nil, err
		}
		return &LegacyCredentialDisableResult{
			State:           state,
			DisabledAt:      disabledAt,
			AlreadyDisabled: true,
		}, nil
	case model.LegacyCredentialStateMigrated:
		// fall through to the update below
	default:
		return nil, fmt.Errorf("legacy credential disable: unexpected state")
	}

	const disableQ = `
		UPDATE merchants
		SET    legacy_credential_state       = 'LEGACY_DISABLED',
		       legacy_credential_disabled_at = COALESCE(legacy_credential_disabled_at, NOW()),
		       updated_at                    = NOW()
		WHERE  id = $1
		RETURNING legacy_credential_disabled_at
	`
	disabledAt, err := readDisabledAt(ctx, tx, disableQ, merchantID)
	if err != nil {
		return nil, fmt.Errorf("legacy credential disable update state: %w", err)
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, s.auditor); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("legacy credential disable commit: %w", err)
	}
	return &LegacyCredentialDisableResult{
		State:           model.LegacyCredentialStateLegacyDisabled,
		DisabledAt:      disabledAt,
		AlreadyDisabled: false,
	}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

const lockMerchantStateQuery = `
	SELECT legacy_credential_state
	FROM   merchants
	WHERE  id = $1
	FOR UPDATE
`

// lockMerchantState reads legacy_credential_state under a row lock.
// Returns ErrLegacyCredentialMerchantNotFound when no row matches.
func lockMerchantState(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) (model.LegacyCredentialState, error) {
	var state model.LegacyCredentialState
	err := tx.QueryRow(ctx, lockMerchantStateQuery, merchantID).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrLegacyCredentialMerchantNotFound
		}
		return "", fmt.Errorf("legacy credential lock state: %w", err)
	}
	return state, nil
}

// selectDisabledAtQuery reads the current disable timestamp (no write).
const selectDisabledAtQuery = `SELECT legacy_credential_disabled_at FROM merchants WHERE id = $1`

// readDisabledAt executes query (a SELECT or an UPDATE ... RETURNING) inside
// the transaction and returns legacy_credential_disabled_at.
func readDisabledAt(ctx context.Context, tx pgx.Tx, query string, merchantID uuid.UUID) (*time.Time, error) {
	var disabledAt *time.Time
	if err := tx.QueryRow(ctx, query, merchantID).Scan(&disabledAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLegacyCredentialMerchantNotFound
		}
		return nil, fmt.Errorf("legacy credential read disabled_at: %w", err)
	}
	return disabledAt, nil
}
