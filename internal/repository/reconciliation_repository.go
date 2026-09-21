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
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReconciliationRepository persists reconciliation runs and results.
type ReconciliationRepository interface {
	CreateRun(ctx context.Context, run *model.ReconciliationRun) error
	CompleteRun(ctx context.Context, run *model.ReconciliationRun) error
	FailRun(ctx context.Context, runID uuid.UUID, errMsg string) error

	// UpsertResultsInTx replaces current results for items atomically with settlement status update.
	PersistReconciliation(
		ctx context.Context,
		settlementID uuid.UUID,
		finalStatus model.SettlementStatus,
		matched, mismatch, unmatched int,
		reconciledAt *time.Time,
		run *model.ReconciliationRun,
		results []*model.ReconciliationResult,
	) error

	GetResultByID(ctx context.Context, id uuid.UUID) (*model.ReconciliationResult, error)
	ListResults(ctx context.Context, filter model.ReconciliationListFilter) ([]*model.ReconciliationResult, error)
	CountResults(ctx context.Context, filter model.ReconciliationListFilter) (int64, error)
}

type pgReconciliationRepository struct {
	db *pgxpool.Pool
}

func NewReconciliationRepository(db *pgxpool.Pool) ReconciliationRepository {
	return &pgReconciliationRepository{db: db}
}

const reconResultCols = `id, settlement_id, settlement_item_id, reconciliation_run_id,
	transaction_id, refund_id, merchant_id, result_type,
	expected_amount, actual_amount, expected_currency, actual_currency, difference_amount,
	status, reason_code, details, created_at, updated_at`

func (r *pgReconciliationRepository) CreateRun(ctx context.Context, run *model.ReconciliationRun) error {
	const q = `
		INSERT INTO reconciliation_runs (
			id, settlement_id, status, total_items, matched_items, mismatch_items, unmatched_items,
			payment_matches, refund_matches, amount_mismatches, currency_mismatches,
			not_found_count, unsupported_count, started_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`
	_, err := r.db.Exec(ctx, q,
		run.ID, run.SettlementID, run.Status, run.TotalItems, run.MatchedItems, run.MismatchItems, run.UnmatchedItems,
		run.PaymentMatches, run.RefundMatches, run.AmountMismatches, run.CurrencyMismatches,
		run.NotFoundCount, run.UnsupportedCount, run.StartedAt, run.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create reconciliation run: %w", err)
	}
	return nil
}

func (r *pgReconciliationRepository) CompleteRun(ctx context.Context, run *model.ReconciliationRun) error {
	const q = `
		UPDATE reconciliation_runs SET
			status = $2, total_items = $3, matched_items = $4, mismatch_items = $5, unmatched_items = $6,
			payment_matches = $7, refund_matches = $8, amount_mismatches = $9, currency_mismatches = $10,
			not_found_count = $11, unsupported_count = $12, finished_at = $13
		WHERE id = $1`
	_, err := r.db.Exec(ctx, q,
		run.ID, run.Status, run.TotalItems, run.MatchedItems, run.MismatchItems, run.UnmatchedItems,
		run.PaymentMatches, run.RefundMatches, run.AmountMismatches, run.CurrencyMismatches,
		run.NotFoundCount, run.UnsupportedCount, run.FinishedAt,
	)
	return err
}

func (r *pgReconciliationRepository) FailRun(ctx context.Context, runID uuid.UUID, errMsg string) error {
	now := time.Now().UTC()
	const q = `UPDATE reconciliation_runs SET status = 'FAILED', error_message = $2, finished_at = $3 WHERE id = $1`
	_, err := r.db.Exec(ctx, q, runID, errMsg, now)
	return err
}

func (r *pgReconciliationRepository) PersistReconciliation(
	ctx context.Context,
	settlementID uuid.UUID,
	finalStatus model.SettlementStatus,
	matched, mismatch, unmatched int,
	reconciledAt *time.Time,
	run *model.ReconciliationRun,
	results []*model.ReconciliationResult,
) error {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("recon persist begin: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	const upsert = `
		INSERT INTO reconciliation_results (
			id, settlement_id, settlement_item_id, reconciliation_run_id,
			transaction_id, refund_id, merchant_id, result_type,
			expected_amount, actual_amount, expected_currency, actual_currency, difference_amount,
			status, reason_code, details, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
		)
		ON CONFLICT (settlement_item_id) DO UPDATE SET
			reconciliation_run_id = EXCLUDED.reconciliation_run_id,
			transaction_id = EXCLUDED.transaction_id,
			refund_id = EXCLUDED.refund_id,
			merchant_id = EXCLUDED.merchant_id,
			result_type = EXCLUDED.result_type,
			expected_amount = EXCLUDED.expected_amount,
			actual_amount = EXCLUDED.actual_amount,
			expected_currency = EXCLUDED.expected_currency,
			actual_currency = EXCLUDED.actual_currency,
			difference_amount = EXCLUDED.difference_amount,
			status = EXCLUDED.status,
			reason_code = EXCLUDED.reason_code,
			details = EXCLUDED.details,
			updated_at = EXCLUDED.updated_at`
	for _, res := range results {
		if _, err := dbTx.Exec(ctx, upsert,
			res.ID, res.SettlementID, res.SettlementItemID, res.ReconciliationRunID,
			res.TransactionID, res.RefundID, res.MerchantID, res.ResultType,
			res.ExpectedAmount, res.ActualAmount, res.ExpectedCurrency, res.ActualCurrency, res.DifferenceAmount,
			res.Status, res.ReasonCode, res.Details, res.CreatedAt, res.UpdatedAt,
		); err != nil {
			return fmt.Errorf("upsert recon result: %w", err)
		}
	}

	const updSettlement = `
		UPDATE settlements
		SET status = $2, matched_count = $3, mismatch_count = $4, unmatched_count = $5,
			reconciled_at = $6, updated_at = NOW()
		WHERE id = $1 AND status = 'RECONCILING'`
	ct, err := dbTx.Exec(ctx, updSettlement, settlementID, finalStatus, matched, mismatch, unmatched, reconciledAt)
	if err != nil {
		return fmt.Errorf("update settlement status: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrSettlementInvalidStatus
	}

	const updRun = `
		UPDATE reconciliation_runs SET
			status = $2, total_items = $3, matched_items = $4, mismatch_items = $5, unmatched_items = $6,
			payment_matches = $7, refund_matches = $8, amount_mismatches = $9, currency_mismatches = $10,
			not_found_count = $11, unsupported_count = $12, finished_at = $13
		WHERE id = $1`
	if _, err := dbTx.Exec(ctx, updRun,
		run.ID, run.Status, run.TotalItems, run.MatchedItems, run.MismatchItems, run.UnmatchedItems,
		run.PaymentMatches, run.RefundMatches, run.AmountMismatches, run.CurrencyMismatches,
		run.NotFoundCount, run.UnsupportedCount, run.FinishedAt,
	); err != nil {
		return fmt.Errorf("complete recon run: %w", err)
	}

	if err := dbTx.Commit(ctx); err != nil {
		return fmt.Errorf("recon persist commit: %w", err)
	}
	return nil
}

func (r *pgReconciliationRepository) GetResultByID(ctx context.Context, id uuid.UUID) (*model.ReconciliationResult, error) {
	q := fmt.Sprintf(`SELECT %s FROM reconciliation_results WHERE id = $1`, reconResultCols)
	res, err := scanReconResult(r.db.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReconciliationNotFound
	}
	return res, err
}

func (r *pgReconciliationRepository) ListResults(ctx context.Context, filter model.ReconciliationListFilter) ([]*model.ReconciliationResult, error) {
	where, args := reconWhere(filter)
	offset := (filter.Page - 1) * filter.Limit
	q := fmt.Sprintf(`SELECT %s FROM reconciliation_results %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		reconResultCols, where, len(args)+1, len(args)+2)
	args = append(args, filter.Limit, offset)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list recon results: %w", err)
	}
	defer rows.Close()
	var out []*model.ReconciliationResult
	for rows.Next() {
		res, err := scanReconResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *pgReconciliationRepository) CountResults(ctx context.Context, filter model.ReconciliationListFilter) (int64, error) {
	where, args := reconWhere(filter)
	q := `SELECT COUNT(*) FROM reconciliation_results ` + where
	var n int64
	if err := r.db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func reconWhere(filter model.ReconciliationListFilter) (string, []any) {
	var parts []string
	var args []any
	if filter.SettlementID != nil {
		args = append(args, *filter.SettlementID)
		parts = append(parts, fmt.Sprintf("settlement_id = $%d", len(args)))
	}
	if filter.MerchantID != nil {
		args = append(args, *filter.MerchantID)
		parts = append(parts, fmt.Sprintf("merchant_id = $%d", len(args)))
	}
	if filter.ResultType != nil {
		args = append(args, string(*filter.ResultType))
		parts = append(parts, fmt.Sprintf("result_type = $%d", len(args)))
	}
	if filter.Status != nil {
		args = append(args, string(*filter.Status))
		parts = append(parts, fmt.Sprintf("status = $%d", len(args)))
	}
	if filter.ReasonCode != nil && *filter.ReasonCode != "" {
		args = append(args, *filter.ReasonCode)
		parts = append(parts, fmt.Sprintf("reason_code = $%d", len(args)))
	}
	if filter.DateFrom != nil {
		args = append(args, *filter.DateFrom)
		parts = append(parts, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if filter.DateTo != nil {
		args = append(args, *filter.DateTo)
		parts = append(parts, fmt.Sprintf("created_at < $%d", len(args)))
	}
	if filter.MismatchesOnly {
		parts = append(parts, "status IN ('MISMATCH', 'UNMATCHED')")
	}
	if filter.Provider != nil && *filter.Provider != "" {
		// Join via settlement_id subquery to avoid changing FROM.
		args = append(args, *filter.Provider)
		parts = append(parts, fmt.Sprintf(
			"settlement_id IN (SELECT id FROM settlements WHERE provider = $%d)", len(args)))
	}
	if len(parts) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(parts, " AND "), args
}

func scanReconResult(row scannable) (*model.ReconciliationResult, error) {
	var res model.ReconciliationResult
	err := row.Scan(
		&res.ID, &res.SettlementID, &res.SettlementItemID, &res.ReconciliationRunID,
		&res.TransactionID, &res.RefundID, &res.MerchantID, &res.ResultType,
		&res.ExpectedAmount, &res.ActualAmount, &res.ExpectedCurrency, &res.ActualCurrency, &res.DifferenceAmount,
		&res.Status, &res.ReasonCode, &res.Details, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
