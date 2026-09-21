package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SettlementRepository persists settlement batches and items.
type SettlementRepository interface {
	CreateWithItems(ctx context.Context, settlement *model.Settlement, items []*model.SettlementItem) error
	GetByID(ctx context.Context, id uuid.UUID) (*model.Settlement, error)
	GetByProviderAndRef(ctx context.Context, provider, settlementRef string) (*model.Settlement, error)
	List(ctx context.Context, filter model.SettlementListFilter) ([]*model.Settlement, error)
	Count(ctx context.Context, filter model.SettlementListFilter) (int64, error)
	ListItems(ctx context.Context, settlementID uuid.UUID) ([]*model.SettlementItem, error)
	GetItemByID(ctx context.Context, id uuid.UUID) (*model.SettlementItem, error)

	// ClaimForReconciliation atomically moves settlement to RECONCILING.
	// Stale RECONCILING rows older than staleAfter may be reclaimed.
	ClaimForReconciliation(ctx context.Context, id uuid.UUID, staleAfter time.Duration) (*model.Settlement, error)

	UpdateAfterReconciliation(ctx context.Context, id uuid.UUID, status model.SettlementStatus, matched, mismatch, unmatched int, reconciledAt *time.Time) error
	MarkFailed(ctx context.Context, id uuid.UUID) error
}

type pgSettlementRepository struct {
	db *pgxpool.Pool
}

func NewSettlementRepository(db *pgxpool.Pool) SettlementRepository {
	return &pgSettlementRepository{db: db}
}

const settlementSelectCols = `id, provider, settlement_ref, settlement_date, currency,
	gross_amount, fee_amount, net_amount, status, source, payload_hash, raw_payload,
	item_count, matched_count, mismatch_count, unmatched_count,
	imported_at, reconciled_at, created_at, updated_at`

func (r *pgSettlementRepository) CreateWithItems(ctx context.Context, settlement *model.Settlement, items []*model.SettlementItem) error {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settlement begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	const q = `
		INSERT INTO settlements (
			id, provider, settlement_ref, settlement_date, currency,
			gross_amount, fee_amount, net_amount, status, source, payload_hash, raw_payload,
			item_count, matched_count, mismatch_count, unmatched_count,
			imported_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,0,0,0,$14,$15,$16
		)`
	_, err = dbTx.Exec(ctx, q,
		settlement.ID, settlement.Provider, settlement.SettlementRef, settlement.SettlementDate,
		settlement.Currency, settlement.GrossAmount, settlement.FeeAmount, settlement.NetAmount,
		settlement.Status, settlement.Source, settlement.PayloadHash, settlement.RawPayload,
		settlement.ItemCount, settlement.ImportedAt, settlement.CreatedAt, settlement.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrSettlementAlreadyExists
		}
		return fmt.Errorf("insert settlement: %w", err)
	}

	const iq = `
		INSERT INTO settlement_items (
			id, settlement_id, provider, item_ref, item_type,
			provider_transaction_id, provider_refund_id, order_id,
			currency, gross_amount, fee_amount, net_amount, settled_at, raw_payload, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`
	for _, it := range items {
		if _, err := dbTx.Exec(ctx, iq,
			it.ID, settlement.ID, it.Provider, it.ItemRef, it.ItemType,
			it.ProviderTransactionID, it.ProviderRefundID, it.OrderID,
			it.Currency, it.GrossAmount, it.FeeAmount, it.NetAmount, it.SettledAt, it.RawPayload, it.CreatedAt,
		); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrSettlementAlreadyExists
			}
			return fmt.Errorf("insert settlement item: %w", err)
		}
	}

	if err := dbTx.Commit(ctx); err != nil {
		return fmt.Errorf("settlement commit: %w", err)
	}
	return nil
}

func (r *pgSettlementRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Settlement, error) {
	q := fmt.Sprintf(`SELECT %s FROM settlements WHERE id = $1`, settlementSelectCols)
	s, err := scanSettlement(r.db.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSettlementNotFound
	}
	return s, err
}

func (r *pgSettlementRepository) GetByProviderAndRef(ctx context.Context, provider, settlementRef string) (*model.Settlement, error) {
	q := fmt.Sprintf(`SELECT %s FROM settlements WHERE provider = $1 AND settlement_ref = $2`, settlementSelectCols)
	s, err := scanSettlement(r.db.QueryRow(ctx, q, provider, settlementRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSettlementNotFound
	}
	return s, err
}

func (r *pgSettlementRepository) List(ctx context.Context, filter model.SettlementListFilter) ([]*model.Settlement, error) {
	where, args := settlementWhere(filter)
	offset := (filter.Page - 1) * filter.Limit
	q := fmt.Sprintf(`SELECT %s FROM settlements %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		settlementSelectCols, where, len(args)+1, len(args)+2)
	args = append(args, filter.Limit, offset)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list settlements: %w", err)
	}
	defer rows.Close()
	var out []*model.Settlement
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *pgSettlementRepository) Count(ctx context.Context, filter model.SettlementListFilter) (int64, error) {
	where, args := settlementWhere(filter)
	q := `SELECT COUNT(*) FROM settlements ` + where
	var n int64
	if err := r.db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count settlements: %w", err)
	}
	return n, nil
}

func settlementWhere(filter model.SettlementListFilter) (string, []any) {
	var parts []string
	var args []any
	if filter.Provider != nil && *filter.Provider != "" {
		args = append(args, *filter.Provider)
		parts = append(parts, fmt.Sprintf("provider = $%d", len(args)))
	}
	if filter.Status != nil {
		args = append(args, string(*filter.Status))
		parts = append(parts, fmt.Sprintf("status = $%d", len(args)))
	}
	if filter.Currency != nil && *filter.Currency != "" {
		args = append(args, *filter.Currency)
		parts = append(parts, fmt.Sprintf("currency = $%d", len(args)))
	}
	if filter.SettlementRef != nil && *filter.SettlementRef != "" {
		args = append(args, *filter.SettlementRef)
		parts = append(parts, fmt.Sprintf("settlement_ref = $%d", len(args)))
	}
	if filter.DateFrom != nil {
		args = append(args, *filter.DateFrom)
		parts = append(parts, fmt.Sprintf("settlement_date >= $%d", len(args)))
	}
	if filter.DateTo != nil {
		args = append(args, *filter.DateTo)
		parts = append(parts, fmt.Sprintf("settlement_date < $%d", len(args)))
	}
	if len(parts) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(parts, " AND "), args
}

func (r *pgSettlementRepository) ListItems(ctx context.Context, settlementID uuid.UUID) ([]*model.SettlementItem, error) {
	const q = `
		SELECT id, settlement_id, provider, item_ref, item_type,
			provider_transaction_id, provider_refund_id, order_id,
			currency, gross_amount, fee_amount, net_amount, settled_at, raw_payload, created_at
		FROM settlement_items
		WHERE settlement_id = $1
		ORDER BY created_at ASC, id ASC`
	rows, err := r.db.Query(ctx, q, settlementID)
	if err != nil {
		return nil, fmt.Errorf("list settlement items: %w", err)
	}
	defer rows.Close()
	var out []*model.SettlementItem
	for rows.Next() {
		it, err := scanSettlementItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *pgSettlementRepository) GetItemByID(ctx context.Context, id uuid.UUID) (*model.SettlementItem, error) {
	const q = `
		SELECT id, settlement_id, provider, item_ref, item_type,
			provider_transaction_id, provider_refund_id, order_id,
			currency, gross_amount, fee_amount, net_amount, settled_at, raw_payload, created_at
		FROM settlement_items WHERE id = $1`
	it, err := scanSettlementItem(r.db.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSettlementNotFound
	}
	return it, err
}

func (r *pgSettlementRepository) ClaimForReconciliation(ctx context.Context, id uuid.UUID, staleAfter time.Duration) (*model.Settlement, error) {
	staleCutoff := time.Now().UTC().Add(-staleAfter)
	q := fmt.Sprintf(`
		UPDATE settlements
		SET status = 'RECONCILING', updated_at = NOW()
		WHERE id = $1
		  AND (
			status IN ('IMPORTED', 'PARTIAL', 'MISMATCH', 'FAILED', 'RECONCILED')
			OR (status = 'RECONCILING' AND updated_at < $2)
		  )
		RETURNING %s`, settlementSelectCols)
	s, err := scanSettlement(r.db.QueryRow(ctx, q, id, staleCutoff))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := r.GetByID(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		if existing.Status == model.SettlementStatusReconciling {
			return nil, ErrReconciliationAlreadyRunning
		}
		return nil, ErrSettlementInvalidStatus
	}
	return s, err
}

func (r *pgSettlementRepository) UpdateAfterReconciliation(ctx context.Context, id uuid.UUID, status model.SettlementStatus, matched, mismatch, unmatched int, reconciledAt *time.Time) error {
	const q = `
		UPDATE settlements
		SET status = $2,
			matched_count = $3,
			mismatch_count = $4,
			unmatched_count = $5,
			reconciled_at = $6,
			updated_at = NOW()
		WHERE id = $1`
	ct, err := r.db.Exec(ctx, q, id, status, matched, mismatch, unmatched, reconciledAt)
	if err != nil {
		return fmt.Errorf("update settlement after recon: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrSettlementNotFound
	}
	return nil
}

func (r *pgSettlementRepository) MarkFailed(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE settlements SET status = 'FAILED', updated_at = NOW() WHERE id = $1`
	_, err := r.db.Exec(ctx, q, id)
	return err
}

type scannable interface {
	Scan(dest ...any) error
}

func scanSettlement(row scannable) (*model.Settlement, error) {
	var s model.Settlement
	err := row.Scan(
		&s.ID, &s.Provider, &s.SettlementRef, &s.SettlementDate, &s.Currency,
		&s.GrossAmount, &s.FeeAmount, &s.NetAmount, &s.Status, &s.Source, &s.PayloadHash, &s.RawPayload,
		&s.ItemCount, &s.MatchedCount, &s.MismatchCount, &s.UnmatchedCount,
		&s.ImportedAt, &s.ReconciledAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func scanSettlementItem(row scannable) (*model.SettlementItem, error) {
	var it model.SettlementItem
	err := row.Scan(
		&it.ID, &it.SettlementID, &it.Provider, &it.ItemRef, &it.ItemType,
		&it.ProviderTransactionID, &it.ProviderRefundID, &it.OrderID,
		&it.Currency, &it.GrossAmount, &it.FeeAmount, &it.NetAmount, &it.SettledAt, &it.RawPayload, &it.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &it, nil
}
