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

// TransactionRepository defines the database operations for transactions.
// Using an interface allows PaymentService to be tested without a real database.
type TransactionRepository interface {
	// Create inserts a new transaction row.
	Create(ctx context.Context, tx *model.Transaction) error

	// FindByID returns a transaction by primary key regardless of merchant.
	// Used internally; prefer FindByMerchantAndID for merchant-facing queries.
	FindByID(ctx context.Context, id uuid.UUID) (*model.Transaction, error)

	// FindByMerchantAndID returns a transaction only when it belongs to merchantID.
	// This is the correct method to use for all merchant-facing API queries —
	// it enforces merchant isolation at the SQL level (WHERE id=$1 AND merchant_id=$2).
	FindByMerchantAndID(ctx context.Context, merchantID, transactionID uuid.UUID) (*model.Transaction, error)

	// FindByMerchantOrderID returns a transaction by the merchant's own order ID.
	// Used for duplicate detection before creating a new transaction.
	FindByMerchantOrderID(ctx context.Context, merchantID uuid.UUID, merchantOrderID string) (*model.Transaction, error)

	// FindByProviderTransactionID looks up a transaction by the provider's own
	// reference. Used during webhook processing to match an inbound event.
	FindByProviderTransactionID(ctx context.Context, provider, providerTransactionID string) (*model.Transaction, error)

	// FindExpiredPendingTransactions returns up to `limit` PENDING transactions
	// whose expired_at is in the past. Used by the expiry worker.
	FindExpiredPendingTransactions(ctx context.Context, limit int) ([]*model.Transaction, error)

	// UpdateStatus performs a conditional status update:
	//   UPDATE transactions SET status=$1, updated_at=NOW()
	//   WHERE id=$2 AND status=$3
	// Returns ErrTransactionNotFound if no row matched (wrong ID or stale status).
	UpdateStatus(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus) error

	// UpdateStatusWithProvider updates status and sets the provider field atomically.
	// Used when transitioning CREATED->PENDING after a successful provider call.
	// expiredAt is persisted when non-nil so the expiry worker can find the row.
	UpdateStatusWithProvider(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus, provider, providerTransactionID, paymentURL string, expiredAt *time.Time) error

	// UpdateStatusWithPaidAt updates status to PAID and sets paid_at atomically.
	// Only transitions if current status matches `from`.
	UpdateStatusWithPaidAt(ctx context.Context, id uuid.UUID, from model.TransactionStatus) error

	// List returns a page of transactions for merchantID matching filter,
	// ordered by (created_at DESC, id DESC) for deterministic pagination.
	// Merchant isolation is enforced at the SQL level — merchantID is always
	// included in the WHERE clause.
	List(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) ([]*model.Transaction, error)

	// CountList returns the total number of transactions for merchantID matching
	// filter.  Uses the same WHERE conditions as List so pagination totals
	// are accurate.
	CountList(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (int64, error)
}

// pgTransactionRepository is the PostgreSQL implementation.
type pgTransactionRepository struct {
	db *pgxpool.Pool
}

// NewTransactionRepository returns a PostgreSQL-backed TransactionRepository.
func NewTransactionRepository(db *pgxpool.Pool) TransactionRepository {
	return &pgTransactionRepository{db: db}
}

// Create inserts a transaction row. All fields must be populated by the caller.
func (r *pgTransactionRepository) Create(ctx context.Context, tx *model.Transaction) error {
	const q = `
		INSERT INTO transactions
		    (id, merchant_id, merchant_order_id, amount, currency,
		     payment_method, provider, provider_transaction_id, payment_url,
		     status, expired_at, paid_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`
	_, err := r.db.Exec(ctx, q,
		tx.ID,
		tx.MerchantID,
		tx.MerchantOrderID,
		tx.Amount,
		tx.Currency,
		tx.PaymentMethod,
		tx.Provider,
		tx.ProviderTransactionID,
		tx.PaymentURL,
		tx.Status,
		tx.ExpiredAt,
		tx.PaidAt,
		tx.CreatedAt,
		tx.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("transaction repository create: %w", err)
	}
	return nil
}

// FindByID retrieves a transaction by primary key.
func (r *pgTransactionRepository) FindByID(ctx context.Context, id uuid.UUID) (*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE id = $1`, TransactionSelectCols)
	tx, err := scanTransaction(r.db.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("transaction repository find by id: %w", err)
	}
	return tx, nil
}

// FindByMerchantAndID retrieves a transaction only when it belongs to merchantID.
// Returns ErrTransactionNotFound when id exists but belongs to a different merchant
// — this prevents information leakage (the caller should never distinguish between
// "not found" and "belongs to someone else").
func (r *pgTransactionRepository) FindByMerchantAndID(ctx context.Context, merchantID, transactionID uuid.UUID) (*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE id = $1 AND merchant_id = $2`, TransactionSelectCols)
	tx, err := scanTransaction(r.db.QueryRow(ctx, q, transactionID, merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("transaction repository find by merchant and id: %w", err)
	}
	return tx, nil
}

// FindByMerchantOrderID retrieves a transaction by the merchant's own order ID.
func (r *pgTransactionRepository) FindByMerchantOrderID(ctx context.Context, merchantID uuid.UUID, merchantOrderID string) (*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE merchant_id = $1 AND merchant_order_id = $2`, TransactionSelectCols)
	tx, err := scanTransaction(r.db.QueryRow(ctx, q, merchantID, merchantOrderID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("transaction repository find by merchant order id: %w", err)
	}
	return tx, nil
}

// FindByProviderTransactionID looks up a transaction by the provider's own reference.
// Used during webhook processing to match an inbound event to a transaction.
func (r *pgTransactionRepository) FindByProviderTransactionID(ctx context.Context, provider, providerTransactionID string) (*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE provider = $1 AND provider_transaction_id = $2`, TransactionSelectCols)
	tx, err := scanTransaction(r.db.QueryRow(ctx, q, provider, providerTransactionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("transaction repository find by provider transaction id: %w", err)
	}
	return tx, nil
}

// FindExpiredPendingTransactions returns PENDING transactions past their expiry time.
// Results are ordered by expired_at ASC (oldest first) and capped at limit.
// The partial index idx_transactions_pending_expired (migration 000006) makes this fast.
func (r *pgTransactionRepository) FindExpiredPendingTransactions(ctx context.Context, limit int) ([]*model.Transaction, error) {
	q := fmt.Sprintf(`SELECT %s FROM transactions WHERE status = 'PENDING' AND expired_at IS NOT NULL AND expired_at <= NOW() ORDER BY expired_at ASC LIMIT $1`, TransactionSelectCols)
	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("transaction repository find expired pending: %w", err)
	}
	defer rows.Close()

	var txs []*model.Transaction
	for rows.Next() {
		tx, err := scanTransaction(rows)
		if err != nil {
			return nil, fmt.Errorf("transaction repository find expired pending scan: %w", err)
		}
		txs = append(txs, tx)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transaction repository find expired pending rows: %w", err)
	}
	return txs, nil
}

// UpdateStatus performs a conditional (optimistic) status update.
// If the row does not exist OR its current status is not `from`, the update is
// a no-op and ErrTransactionNotFound is returned so the caller can handle it.
func (r *pgTransactionRepository) UpdateStatus(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus) error {
	const q = `
		UPDATE transactions
		SET    status     = $1,
		       updated_at = NOW()
		WHERE  id     = $2
		AND    status = $3
	`
	tag, err := r.db.Exec(ctx, q, to, id, from)
	if err != nil {
		return fmt.Errorf("transaction repository update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTransactionNotFound
	}
	return nil
}

// UpdateStatusWithProvider updates status and sets the provider atomically.
func (r *pgTransactionRepository) UpdateStatusWithProvider(ctx context.Context, id uuid.UUID, from, to model.TransactionStatus, provider, providerTransactionID, paymentURL string, expiredAt *time.Time) error {
	const q = `
		UPDATE transactions
		SET    status                  = $1,
		       provider               = $2,
		       provider_transaction_id = $3,
		       payment_url            = $4,
		       expired_at             = COALESCE($5, expired_at),
		       updated_at             = NOW()
		WHERE  id     = $6
		AND    status = $7
	`
	tag, err := r.db.Exec(ctx, q, to, provider, providerTransactionID, paymentURL, expiredAt, id, from)
	if err != nil {
		return fmt.Errorf("transaction repository update status with provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTransactionNotFound
	}
	return nil
}

// UpdateStatusWithPaidAt transitions a transaction to PAID and sets paid_at = NOW().
// The update is conditional on the current status being `from` (optimistic lock).
// paid_at is only set when it is currently NULL to avoid overwriting an existing value.
func (r *pgTransactionRepository) UpdateStatusWithPaidAt(ctx context.Context, id uuid.UUID, from model.TransactionStatus) error {
	const q = `
		UPDATE transactions
		SET    status     = 'PAID',
		       paid_at    = CASE WHEN paid_at IS NULL THEN NOW() ELSE paid_at END,
		       updated_at = NOW()
		WHERE  id     = $1
		AND    status = $2
	`
	tag, err := r.db.Exec(ctx, q, id, from)
	if err != nil {
		return fmt.Errorf("transaction repository update status with paid_at: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTransactionNotFound
	}
	return nil
}

// ─── List / CountList ────────────────────────────────────────────────────────

// List returns a merchant-scoped, filtered, paginated slice of transactions.
//
// Safety notes:
//   - merchantID is always bound as a parameter — never trusted from input.
//   - Additional filter conditions are appended with positional parameters
//     ($2, $3 …) — no string interpolation of user values.
//   - ORDER BY is fixed to (created_at DESC, id DESC) — not client-controlled.
func (r *pgTransactionRepository) List(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) ([]*model.Transaction, error) {
	query, args := buildListQuery(false, merchantID, filter)
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("transaction repository list: %w", err)
	}
	defer rows.Close()

	var txs []*model.Transaction
	for rows.Next() {
		tx, err := scanTransaction(rows)
		if err != nil {
			return nil, fmt.Errorf("transaction repository list scan: %w", err)
		}
		txs = append(txs, tx)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transaction repository list rows: %w", err)
	}
	return txs, nil
}

// CountList returns the total row count for the same filter used by List.
func (r *pgTransactionRepository) CountList(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (int64, error) {
	query, args := buildListQuery(true, merchantID, filter)
	var total int64
	if err := r.db.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("transaction repository count list: %w", err)
	}
	return total, nil
}

// buildListQuery constructs the parameterised SELECT or COUNT query for the
// listing endpoint.  countOnly=true emits "SELECT COUNT(*)" and omits the
// ORDER BY / LIMIT / OFFSET clauses (not meaningful for counting).
//
// Parameter positions:
//
//	$1  merchantID          — always present
//	$2… optional filters    — status, merchant_order_id, payment_method,
//	                           created_from, created_to (in that order when set)
//	last two (data only)    — LIMIT, OFFSET
func buildListQuery(countOnly bool, merchantID uuid.UUID, filter model.TransactionListFilter) (string, []any) {
	args := []any{merchantID}
	pos := 1 // $1 = merchantID

	where := "WHERE merchant_id = $1"

	if filter.Status != nil {
		pos++
		where += fmt.Sprintf(" AND status = $%d", pos)
		args = append(args, string(*filter.Status))
	}
	if filter.MerchantOrderID != nil {
		pos++
		where += fmt.Sprintf(" AND merchant_order_id = $%d", pos)
		args = append(args, *filter.MerchantOrderID)
	}
	if filter.PaymentMethod != nil {
		pos++
		where += fmt.Sprintf(" AND payment_method = $%d", pos)
		args = append(args, *filter.PaymentMethod)
	}
	if filter.CreatedFrom != nil {
		pos++
		where += fmt.Sprintf(" AND created_at >= $%d", pos)
		args = append(args, *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		pos++
		where += fmt.Sprintf(" AND created_at < $%d", pos)
		args = append(args, *filter.CreatedTo)
	}
	if filter.Search != nil && *filter.Search != "" {
		pos++
		searchPos := pos
		args = append(args, *filter.Search)
		where += fmt.Sprintf(
			` AND (
				id::text = $%d
				OR merchant_order_id ILIKE '%%' || $%d || '%%'
				OR COALESCE(provider_transaction_id, '') ILIKE '%%' || $%d || '%%'
			)`,
			searchPos, searchPos, searchPos,
		)
	}

	const cols = TransactionSelectCols

	if countOnly {
		return fmt.Sprintf("SELECT COUNT(*) FROM transactions %s", where), args
	}

	offset := (filter.Page - 1) * filter.Limit
	pos++
	limitPos := pos
	pos++
	offsetPos := pos
	args = append(args, filter.Limit, offset)

	query := fmt.Sprintf(
		`SELECT %s FROM transactions %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		cols, where, limitPos, offsetPos,
	)
	return query, args
}

// TransactionSelectCols is the standard SELECT column list for transactions.
const TransactionSelectCols = `id, merchant_id, merchant_order_id, amount, currency,
		       payment_method, provider, provider_transaction_id, payment_url,
		       status, expired_at, paid_at, refunded_amount, reserved_refund_amount,
		       created_at, updated_at`

// ─── helpers ─────────────────────────────────────────────────────────────────

func scanTransaction(row pgx.Row) (*model.Transaction, error) {
	t := &model.Transaction{}
	err := row.Scan(
		&t.ID,
		&t.MerchantID,
		&t.MerchantOrderID,
		&t.Amount,
		&t.Currency,
		&t.PaymentMethod,
		&t.Provider,
		&t.ProviderTransactionID,
		&t.PaymentURL,
		&t.Status,
		&t.ExpiredAt,
		&t.PaidAt,
		&t.RefundedAmount,
		&t.ReservedRefundAmount,
		&t.CreatedAt,
		&t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}
