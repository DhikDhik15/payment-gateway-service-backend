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
	// ErrInvitationNotFound is returned when no invitation matches the lookup.
	ErrInvitationNotFound = errors.New("invitation not found")

	// ErrInvitationAlreadyPending is returned when a PENDING invitation already
	// exists for the same merchant + email (partial unique index violation).
	ErrInvitationAlreadyPending = errors.New("invitation already pending for this email")

	// ErrInvitationAlreadyAccepted is returned when the single-use claim fails
	// because the invitation was already accepted (concurrent accept race).
	ErrInvitationAlreadyAccepted = errors.New("invitation already accepted")
)

// Invitation table index names — used to classify unique violations.
const (
	invPendingIndex   = "uq_merchant_user_invitations_pending"
	invTokenHashIndex = "uq_merchant_user_invitations_token_hash"
)

// ─── Interface ────────────────────────────────────────────────────────────────

// MerchantInvitationRepository defines the database operations for Phase 8B
// team invitations.
//
// Atomicity conventions (same shape as MerchantAPIKeyRepository.Rotate and
// RefundRepository.ReserveAndCreate): methods that write more than one logical
// unit own their transaction internally. CreateWithEmailOutbox is the Phase
// 8C.3A example — the invitation row and its email_outbox row commit or
// roll back together (invitation exists ⇔ queued email exists).
type MerchantInvitationRepository interface {
	// CreateWithEmailOutbox inserts a PENDING invitation and its rendered
	// invitation email into email_outbox in ONE transaction (Phase 8C.3A).
	// Returns ErrInvitationAlreadyPending when a PENDING invitation already
	// exists for the same merchant + email (DB-level race protection) — the
	// rollback removes any partially written outbox row as well.
	CreateWithEmailOutbox(ctx context.Context, inv *model.MerchantInvitation, entry *model.EmailOutbox) error

	// GetByTokenHash retrieves an invitation by the SHA-256 hash of its token.
	// Returns ErrInvitationNotFound when no row matches.
	GetByTokenHash(ctx context.Context, tokenHash string) (*model.MerchantInvitation, error)

	// GetPendingByMerchantEmail returns the active PENDING invitation for the
	// given merchant + email, or ErrInvitationNotFound when none exists.
	GetPendingByMerchantEmail(ctx context.Context, merchantID uuid.UUID, email string) (*model.MerchantInvitation, error)

	// ExpireStalePending transitions PENDING invitations that are past their
	// expiry to EXPIRED for the given merchant + email, freeing their slot in
	// the one-pending-per-email unique index. Returns the rows affected.
	// Best-effort — callers may ignore the count but must handle errors.
	ExpireStalePending(ctx context.Context, merchantID uuid.UUID, email string) (int64, error)

	// MarkExpired lazily transitions a single PENDING invitation to EXPIRED.
	// Best-effort — used when an expired invitation is encountered at read or
	// acceptance time. A conditional UPDATE: rows already ACCEPTED are left
	// untouched.
	MarkExpired(ctx context.Context, id uuid.UUID) error

	// Accept atomically claims the invitation (PENDING → ACCEPTED with
	// accepted_at set) and inserts the new merchant_users row in ONE
	// transaction. This provides the single-use guarantee under concurrency:
	//   - The claim UPDATE is conditional on status = 'PENDING' AND
	//     expires_at > NOW(), so two concurrent accepts cannot both win.
	//   - Claim failure classifies to ErrInvitationNotFound (missing/expired/
	//     revoked) or ErrInvitationAlreadyAccepted (already accepted).
	//   - A unique-email violation on insert maps to
	//     ErrMerchantUserEmailExists and rolls the transaction back, leaving
	//     the invitation PENDING.
	Accept(ctx context.Context, invitationID uuid.UUID, user *model.MerchantUser) error
}

// ─── PostgreSQL implementation ────────────────────────────────────────────────

type pgMerchantInvitationRepository struct {
	db      *pgxpool.Pool
	outbox  EmailOutboxRepository
	auditor audit.Recorder
}

// NewMerchantInvitationRepository returns a PostgreSQL-backed
// MerchantInvitationRepository. outbox participates in the invitation
// transaction (Phase 8C.3A atomic enqueue) — the caller owns no separate
// connection for it. The optional recorder makes security events part of that
// same transaction.
func NewMerchantInvitationRepository(db *pgxpool.Pool, outbox EmailOutboxRepository, recorders ...audit.Recorder) MerchantInvitationRepository {
	var recorder audit.Recorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &pgMerchantInvitationRepository{db: db, outbox: outbox, auditor: recorder}
}

// CreateWithEmailOutbox inserts the invitation and its queued email in ONE
// transaction:
//
//	BEGIN
//	  INSERT merchant_user_invitations   (unique-violation classified here)
//	  INSERT email_outbox                (via EmailOutboxRepository.EnqueueInTx)
//	COMMIT                              -- any error → ROLLBACK of both rows
//
// Both INSERTs run on the same pgx.Tx — never on r.db directly.
func (r *pgMerchantInvitationRepository) CreateWithEmailOutbox(
	ctx context.Context,
	inv *model.MerchantInvitation,
	entry *model.EmailOutbox,
) error {
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("merchant invitation create begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op after commit
	if err := lockActiveInvitationMerchant(ctx, tx, inv.MerchantID); err != nil {
		return err
	}

	const q = `
		INSERT INTO merchant_user_invitations
		    (id, merchant_id, email, role, token_hash, status,
		     expires_at, accepted_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = tx.Exec(ctx, q,
		inv.ID,
		inv.MerchantID,
		inv.Email,
		inv.Role,
		inv.TokenHash,
		inv.Status,
		inv.ExpiresAt,
		inv.AcceptedAt,
		inv.CreatedAt,
		inv.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			// Mapping preserved byte-for-byte from Phase 8B Create so the
			// duplicate-pending rule stays 409 — never a generic 500.
			if uniqueViolationOn(err, invPendingIndex) {
				return ErrInvitationAlreadyPending
			}
			if uniqueViolationOn(err, invTokenHashIndex) {
				// Token hash collision is astronomically unlikely (256-bit
				// random tokens) — surface as a plain error, not a conflict.
				return fmt.Errorf("merchant invitation repository create: token hash collision: %w", err)
			}
			// Unknown unique index — treat like a pending-duplicate conflict.
			return ErrInvitationAlreadyPending
		}
		return fmt.Errorf("merchant invitation repository create: %w", err)
	}

	// Same tx, second INSERT: a failure here rolls the invitation back too.
	if err := r.outbox.EnqueueInTx(ctx, tx, entry); err != nil {
		return err
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetByTokenHash retrieves an invitation by token hash.
func (r *pgMerchantInvitationRepository) GetByTokenHash(ctx context.Context, tokenHash string) (*model.MerchantInvitation, error) {
	const q = `
		SELECT id, merchant_id, email, role, token_hash, status,
		       expires_at, accepted_at, created_at, updated_at
		FROM   merchant_user_invitations
		WHERE  token_hash = $1
	`
	inv, err := scanMerchantInvitation(r.db.QueryRow(ctx, q, tokenHash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvitationNotFound
		}
		return nil, fmt.Errorf("merchant invitation repository get by token hash: %w", err)
	}
	return inv, nil
}

// GetPendingByMerchantEmail returns the active PENDING invitation for a
// merchant + email.
func (r *pgMerchantInvitationRepository) GetPendingByMerchantEmail(ctx context.Context, merchantID uuid.UUID, email string) (*model.MerchantInvitation, error) {
	const q = `
		SELECT id, merchant_id, email, role, token_hash, status,
		       expires_at, accepted_at, created_at, updated_at
		FROM   merchant_user_invitations
		WHERE  merchant_id = $1
		  AND  email       = $2
		  AND  status      = 'PENDING'
	`
	inv, err := scanMerchantInvitation(r.db.QueryRow(ctx, q, merchantID, email))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvitationNotFound
		}
		return nil, fmt.Errorf("merchant invitation repository get pending: %w", err)
	}
	return inv, nil
}

// ExpireStalePending marks PENDING invitations past their expiry as EXPIRED.
func (r *pgMerchantInvitationRepository) ExpireStalePending(ctx context.Context, merchantID uuid.UUID, email string) (int64, error) {
	const q = `
		UPDATE merchant_user_invitations
		SET    status     = 'EXPIRED',
		       updated_at = $1
		WHERE  merchant_id = $2
		  AND  email       = $3
		  AND  status      = 'PENDING'
		  AND  expires_at <= $1
	`
	tag, err := r.db.Exec(ctx, q, time.Now().UTC(), merchantID, email)
	if err != nil {
		return 0, fmt.Errorf("merchant invitation repository expire stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MarkExpired lazily transitions a single PENDING invitation to EXPIRED.
func (r *pgMerchantInvitationRepository) MarkExpired(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE merchant_user_invitations
		SET    status     = 'EXPIRED',
		       updated_at = $1
		WHERE  id     = $2
		  AND  status = 'PENDING'
	`
	_, err := r.db.Exec(ctx, q, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("merchant invitation repository mark expired: %w", err)
	}
	return nil // zero rows affected is fine — best-effort transition
}

// Accept claims the invitation and inserts the new user in one transaction.
func (r *pgMerchantInvitationRepository) Accept(ctx context.Context, invitationID uuid.UUID, user *model.MerchantUser) error {
	if err := audit.RequireRecorder(ctx, r.auditor); err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("invitation accept begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op after commit

	// Lock the merchant before claiming the invitation. This preserves the
	// lifecycle decision at commit time and prevents an acceptance audit row
	// from surviving a concurrent merchant suspension.
	var merchantID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT merchant_id FROM merchant_user_invitations WHERE id = $1
	`, invitationID).Scan(&merchantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotFound
		}
		return fmt.Errorf("invitation accept resolve merchant: %w", err)
	}
	if err := lockActiveInvitationMerchant(ctx, tx, merchantID); err != nil {
		return err
	}

	now := time.Now().UTC()

	// 1. Atomic single-use claim — conditional on PENDING and not expired.
	const claimQ = `
		UPDATE merchant_user_invitations
		SET    status      = 'ACCEPTED',
		       accepted_at = $1,
		       updated_at  = $1
		WHERE  id          = $2
		  AND  status      = 'PENDING'
		  AND  expires_at  > $1
	`
	tag, err := tx.Exec(ctx, claimQ, now, invitationID)
	if err != nil {
		return fmt.Errorf("invitation accept claim: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Classify why the claim failed (audit R5: concurrent accept → conflict).
		return classifyClaimFailure(ctx, tx, invitationID)
	}

	// 2. Insert the new dashboard user (shared helper with merchant_users).
	// A unique-email violation aborts the transaction, leaving the invitation
	// PENDING — the caller can retry after resolving the email conflict.
	if err := insertMerchantUser(ctx, tx, user); err != nil {
		return err // already classified (ErrMerchantUserEmailExists) or wrapped
	}
	if err := audit.RecordInTxIfPresent(ctx, tx, r.auditor); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("invitation accept commit: %w", err)
	}
	return nil
}

func lockActiveInvitationMerchant(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) error {
	var status model.MerchantStatus
	if err := tx.QueryRow(ctx, `
		SELECT status FROM merchants WHERE id = $1 FOR UPDATE
	`, merchantID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMerchantNotFound
		}
		return fmt.Errorf("invitation merchant lock: %w", err)
	}
	if status != model.MerchantStatusActive {
		return ErrMerchantInactive
	}
	return nil
}

// classifyClaimFailure maps a zero-row claim to the correct sentinel error by
// reading the invitation's current state inside the same transaction.
func classifyClaimFailure(ctx context.Context, tx pgx.Tx, invitationID uuid.UUID) error {
	const q = `
		SELECT status, expires_at
		FROM   merchant_user_invitations
		WHERE  id = $1
	`
	var status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, q, invitationID).Scan(&status, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotFound
		}
		return fmt.Errorf("invitation accept classify: %w", err)
	}

	switch status {
	case string(model.InvitationStatusAccepted):
		return ErrInvitationAlreadyAccepted
	case string(model.InvitationStatusPending):
		// PENDING but the conditional UPDATE rejected it → must be expired
		// (checked by the same predicate with the same timestamp).
		return ErrInvitationNotFound
	default:
		// EXPIRED / REVOKED — treat as no longer valid (never confirm which).
		return ErrInvitationNotFound
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func scanMerchantInvitation(row pgx.Row) (*model.MerchantInvitation, error) {
	inv := &model.MerchantInvitation{}
	err := row.Scan(
		&inv.ID,
		&inv.MerchantID,
		&inv.Email,
		&inv.Role,
		&inv.TokenHash,
		&inv.Status,
		&inv.ExpiresAt,
		&inv.AcceptedAt,
		&inv.CreatedAt,
		&inv.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// uniqueViolationOn reports whether the unique violation happened on the
// named constraint/index.
func uniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == constraint
}
