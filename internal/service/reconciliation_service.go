package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ReconciliationService matches settlement items against gateway financial records.
// It never mutates payment/refund financial truth.
type ReconciliationService interface {
	ReconcileSettlement(ctx context.Context, settlementID uuid.UUID) (*model.ReconciliationSummary, error)
	ListResults(ctx context.Context, filter model.ReconciliationListFilter) (*ListReconResultsResult, error)
	GetResult(ctx context.Context, id uuid.UUID) (*model.ReconciliationResult, error)
}

type ListReconResultsResult struct {
	Results    []model.ReconciliationResult
	Total      int64
	TotalPages int
	Page       int
	Limit      int
}

type reconciliationService struct {
	settlementRepo repository.SettlementRepository
	reconRepo      repository.ReconciliationRepository
	txRepo         repository.TransactionRepository
	refundRepo     repository.RefundRepository
	staleAfter     time.Duration
}

func NewReconciliationService(
	settlementRepo repository.SettlementRepository,
	reconRepo repository.ReconciliationRepository,
	txRepo repository.TransactionRepository,
	refundRepo repository.RefundRepository,
	staleAfter time.Duration,
) ReconciliationService {
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	return &reconciliationService{
		settlementRepo: settlementRepo,
		reconRepo:      reconRepo,
		txRepo:         txRepo,
		refundRepo:     refundRepo,
		staleAfter:     staleAfter,
	}
}

func (s *reconciliationService) ReconcileSettlement(ctx context.Context, settlementID uuid.UUID) (*model.ReconciliationSummary, error) {
	start := time.Now()
	claimed, err := s.settlementRepo.ClaimForReconciliation(ctx, settlementID, s.staleAfter)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrSettlementNotFound):
			return nil, ErrSettlementNotFound
		case errors.Is(err, repository.ErrReconciliationAlreadyRunning):
			return nil, ErrReconciliationRunning
		case errors.Is(err, repository.ErrSettlementInvalidStatus):
			return nil, ErrSettlementInvalidStatus
		default:
			return nil, err
		}
	}

	now := time.Now().UTC()
	run := &model.ReconciliationRun{
		ID: uuid.New(), SettlementID: settlementID,
		Status: model.ReconciliationRunRunning, StartedAt: now, CreatedAt: now,
	}
	if err := s.reconRepo.CreateRun(ctx, run); err != nil {
		_ = s.settlementRepo.MarkFailed(ctx, settlementID)
		return nil, err
	}

	items, err := s.settlementRepo.ListItems(ctx, settlementID)
	if err != nil {
		_ = s.reconRepo.FailRun(ctx, run.ID, err.Error())
		_ = s.settlementRepo.MarkFailed(ctx, settlementID)
		return nil, err
	}

	results := make([]*model.ReconciliationResult, 0, len(items))
	var (
		matched, mismatch, unmatched         int
		paymentMatches, refundMatches        int
		amountMismatches, currencyMismatches int
		notFound, unsupported                int
		totalExpected, totalActual           int64
	)

	for _, item := range items {
		res := s.matchItem(ctx, claimed, item, run.ID)
		results = append(results, res)
		switch res.Status {
		case model.ReconciliationResultMatched:
			matched++
		case model.ReconciliationResultMismatch:
			mismatch++
		default:
			unmatched++
		}
		switch res.ResultType {
		case model.ReconResultPaymentMatch:
			paymentMatches++
		case model.ReconResultRefundMatch:
			refundMatches++
		case model.ReconResultPaymentAmountMismatch, model.ReconResultRefundAmountMismatch:
			amountMismatches++
		case model.ReconResultPaymentCurrencyMismatch, model.ReconResultRefundCurrencyMismatch:
			currencyMismatches++
		case model.ReconResultPaymentNotFound, model.ReconResultRefundNotFound:
			notFound++
		case model.ReconResultUnsupportedAdjustment, model.ReconResultUnexpectedItem, model.ReconResultDuplicateItem:
			unsupported++
		}
		if res.ExpectedAmount != nil {
			totalExpected += *res.ExpectedAmount
		}
		if res.ActualAmount != nil {
			totalActual += *res.ActualAmount
		}
	}

	finalStatus := model.SettlementStatusReconciled
	switch {
	case matched == len(items) && len(items) > 0:
		finalStatus = model.SettlementStatusReconciled
	case matched > 0 && (mismatch > 0 || unmatched > 0):
		finalStatus = model.SettlementStatusPartial
	case len(items) == 0:
		finalStatus = model.SettlementStatusFailed
	default:
		finalStatus = model.SettlementStatusMismatch
	}

	finished := time.Now().UTC()
	run.Status = model.ReconciliationRunCompleted
	run.TotalItems = len(items)
	run.MatchedItems = matched
	run.MismatchItems = mismatch
	run.UnmatchedItems = unmatched
	run.PaymentMatches = paymentMatches
	run.RefundMatches = refundMatches
	run.AmountMismatches = amountMismatches
	run.CurrencyMismatches = currencyMismatches
	run.NotFoundCount = notFound
	run.UnsupportedCount = unsupported
	run.FinishedAt = &finished

	if err := s.reconRepo.PersistReconciliation(ctx, settlementID, finalStatus, matched, mismatch, unmatched, &finished, run, results); err != nil {
		_ = s.reconRepo.FailRun(ctx, run.ID, err.Error())
		_ = s.settlementRepo.MarkFailed(ctx, settlementID)
		return nil, fmt.Errorf("persist reconciliation: %w", err)
	}

	slog.Info("settlement reconciled",
		slog.String("settlement_id", settlementID.String()),
		slog.String("provider", claimed.Provider),
		slog.String("settlement_ref", claimed.SettlementRef),
		slog.String("status", string(finalStatus)),
		slog.Int("matched", matched),
		slog.Int("mismatch", mismatch),
		slog.Int("unmatched", unmatched),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)

	diff := totalActual - totalExpected
	if diff < 0 {
		diff = -diff
	}
	return &model.ReconciliationSummary{
		SettlementID: settlementID, SettlementStatus: finalStatus, ReconciliationRunID: run.ID,
		TotalItems: len(items), MatchedItems: matched, MismatchItems: mismatch, UnmatchedItems: unmatched,
		PaymentMatches: paymentMatches, RefundMatches: refundMatches,
		AmountMismatches: amountMismatches, CurrencyMismatches: currencyMismatches,
		NotFound: notFound, Unsupported: unsupported,
		TotalExpectedAmount: totalExpected, TotalActualAmount: totalActual, TotalDifference: diff,
	}, nil
}

func (s *reconciliationService) matchItem(ctx context.Context, settlement *model.Settlement, item *model.SettlementItem, runID uuid.UUID) *model.ReconciliationResult {
	now := time.Now().UTC()
	base := &model.ReconciliationResult{
		ID: uuid.New(), SettlementID: settlement.ID, SettlementItemID: item.ID,
		ReconciliationRunID: runID, ActualAmount: int64Ptr(item.GrossAmount),
		ActualCurrency: strPtr(item.Currency), CreatedAt: now, UpdatedAt: now,
	}

	switch item.ItemType {
	case model.SettlementItemTypePayment:
		return s.matchPayment(ctx, settlement, item, base)
	case model.SettlementItemTypeRefund:
		return s.matchRefund(ctx, settlement, item, base)
	case model.SettlementItemTypeAdjustment:
		reason := string(model.ReconResultUnsupportedAdjustment)
		base.ResultType = model.ReconResultUnsupportedAdjustment
		base.Status = model.ReconciliationResultUnmatched
		base.ReasonCode = &reason
		base.Details = mustJSON(map[string]string{"message": "ADJUSTMENT items are stored but not auto-matched in Phase 7C"})
		return base
	default:
		reason := string(model.ReconResultUnexpectedItem)
		base.ResultType = model.ReconResultUnexpectedItem
		base.Status = model.ReconciliationResultUnmatched
		base.ReasonCode = &reason
		return base
	}
}

func (s *reconciliationService) matchPayment(ctx context.Context, settlement *model.Settlement, item *model.SettlementItem, base *model.ReconciliationResult) *model.ReconciliationResult {
	if item.ProviderTransactionID == nil || *item.ProviderTransactionID == "" {
		reason := string(model.ReconResultPaymentNotFound)
		base.ResultType = model.ReconResultPaymentNotFound
		base.Status = model.ReconciliationResultUnmatched
		base.ReasonCode = &reason
		base.Details = mustJSON(map[string]string{"message": "missing provider_transaction_id"})
		return base
	}

	tx, err := s.txRepo.FindByProviderTransactionID(ctx, settlement.Provider, *item.ProviderTransactionID)
	if err != nil {
		reason := string(model.ReconResultPaymentNotFound)
		base.ResultType = model.ReconResultPaymentNotFound
		base.Status = model.ReconciliationResultUnmatched
		base.ReasonCode = &reason
		base.Details = mustJSON(map[string]string{
			"provider_transaction_id": *item.ProviderTransactionID,
			"message":                 "transaction not found in gateway",
		})
		return base
	}

	mid := tx.MerchantID
	base.MerchantID = &mid
	tid := tx.ID
	base.TransactionID = &tid
	base.ExpectedAmount = int64Ptr(tx.Amount)
	base.ExpectedCurrency = strPtr(tx.Currency)
	diff := item.GrossAmount - tx.Amount
	base.DifferenceAmount = int64Ptr(diff)

	if tx.Status != model.TransactionStatusPaid {
		reason := string(model.ReconResultUnexpectedItem)
		base.ResultType = model.ReconResultUnexpectedItem
		base.Status = model.ReconciliationResultMismatch
		base.ReasonCode = &reason
		base.Details = mustJSON(map[string]any{
			"message":            "settlement item references non-PAID transaction",
			"transaction_status": tx.Status,
		})
		return base
	}
	if tx.Currency != item.Currency {
		reason := string(model.ReconResultPaymentCurrencyMismatch)
		base.ResultType = model.ReconResultPaymentCurrencyMismatch
		base.Status = model.ReconciliationResultMismatch
		base.ReasonCode = &reason
		return base
	}
	if tx.Amount != item.GrossAmount {
		reason := string(model.ReconResultPaymentAmountMismatch)
		base.ResultType = model.ReconResultPaymentAmountMismatch
		base.Status = model.ReconciliationResultMismatch
		base.ReasonCode = &reason
		return base
	}

	reason := string(model.ReconResultPaymentMatch)
	base.ResultType = model.ReconResultPaymentMatch
	base.Status = model.ReconciliationResultMatched
	base.ReasonCode = &reason
	base.DifferenceAmount = int64Ptr(0)
	return base
}

func (s *reconciliationService) matchRefund(ctx context.Context, settlement *model.Settlement, item *model.SettlementItem, base *model.ReconciliationResult) *model.ReconciliationResult {
	if item.ProviderRefundID == nil || *item.ProviderRefundID == "" {
		reason := string(model.ReconResultRefundNotFound)
		base.ResultType = model.ReconResultRefundNotFound
		base.Status = model.ReconciliationResultUnmatched
		base.ReasonCode = &reason
		return base
	}

	ref, err := s.refundRepo.FindByProviderRefundID(ctx, settlement.Provider, *item.ProviderRefundID)
	if err != nil {
		reason := string(model.ReconResultRefundNotFound)
		base.ResultType = model.ReconResultRefundNotFound
		base.Status = model.ReconciliationResultUnmatched
		base.ReasonCode = &reason
		base.Details = mustJSON(map[string]string{
			"provider_refund_id": *item.ProviderRefundID,
			"message":            "refund not found in gateway",
		})
		return base
	}

	mid := ref.MerchantID
	base.MerchantID = &mid
	rid := ref.ID
	base.RefundID = &rid
	tid := ref.TransactionID
	base.TransactionID = &tid
	base.ExpectedAmount = int64Ptr(ref.Amount)
	base.ExpectedCurrency = strPtr(ref.Currency)
	diff := item.GrossAmount - ref.Amount
	base.DifferenceAmount = int64Ptr(diff)

	if ref.Status != model.RefundStatusSucceeded {
		reason := string(model.ReconResultUnexpectedItem)
		base.ResultType = model.ReconResultUnexpectedItem
		base.Status = model.ReconciliationResultMismatch
		base.ReasonCode = &reason
		base.Details = mustJSON(map[string]any{
			"message":       "settlement item references non-SUCCEEDED refund",
			"refund_status": ref.Status,
		})
		return base
	}
	if ref.Currency != item.Currency {
		reason := string(model.ReconResultRefundCurrencyMismatch)
		base.ResultType = model.ReconResultRefundCurrencyMismatch
		base.Status = model.ReconciliationResultMismatch
		base.ReasonCode = &reason
		return base
	}
	if ref.Amount != item.GrossAmount {
		reason := string(model.ReconResultRefundAmountMismatch)
		base.ResultType = model.ReconResultRefundAmountMismatch
		base.Status = model.ReconciliationResultMismatch
		base.ReasonCode = &reason
		return base
	}

	reason := string(model.ReconResultRefundMatch)
	base.ResultType = model.ReconResultRefundMatch
	base.Status = model.ReconciliationResultMatched
	base.ReasonCode = &reason
	base.DifferenceAmount = int64Ptr(0)
	return base
}

func (s *reconciliationService) ListResults(ctx context.Context, filter model.ReconciliationListFilter) (*ListReconResultsResult, error) {
	if filter.Page <= 0 {
		filter.Page = model.DefaultPage
	}
	if filter.Limit <= 0 {
		filter.Limit = model.DefaultLimit
	}
	if filter.Limit > model.MaxLimit {
		filter.Limit = model.MaxLimit
	}
	total, err := s.reconRepo.CountResults(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows, err := s.reconRepo.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]model.ReconciliationResult, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	pages := int(total) / filter.Limit
	if int(total)%filter.Limit != 0 {
		pages++
	}
	return &ListReconResultsResult{Results: out, Total: total, TotalPages: pages, Page: filter.Page, Limit: filter.Limit}, nil
}

func (s *reconciliationService) GetResult(ctx context.Context, id uuid.UUID) (*model.ReconciliationResult, error) {
	res, err := s.reconRepo.GetResultByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrReconciliationNotFound) {
			return nil, ErrReconciliationNotFound
		}
		return nil, err
	}
	return res, nil
}

func int64Ptr(v int64) *int64 { return &v }
func strPtr(v string) *string { return &v }

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
