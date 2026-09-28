package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrRefundNotFound            = errors.New("refund not found")
	ErrRefundNotAllowed          = errors.New("refund not allowed")
	ErrRefundAmountExceeded      = errors.New("refund amount exceeded")
	ErrRefundTransactionNotPaid  = errors.New("refund transaction not paid")
	ErrRefundCurrencyMismatch    = errors.New("refund currency mismatch")
	ErrRefundProviderUnsupported = errors.New("refund provider is not configured")
)

type RefundService interface {
	CreateRefundWithIdempotency(ctx context.Context, merchantID, transactionID uuid.UUID, req model.CreateRefundRequest, idempotencyKey string) (*model.RefundResponse, error)
	GetRefund(ctx context.Context, merchantID, refundID uuid.UUID) (*model.RefundResponse, error)
	ListRefunds(ctx context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) (*ListRefundsResult, error)
	// ListRefundsByMerchant returns a merchant-wide paginated refund list (dashboard).
	ListRefundsByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (*ListRefundsResult, error)
}

type ListRefundsResult struct {
	Refunds    []model.RefundResponse
	Total      int64
	TotalPages int
	Page       int
	Limit      int
}

type refundService struct {
	txRepo             repository.TransactionRepository
	refundRepo         repository.RefundRepository
	attemptRepo        repository.RefundAttemptRepository
	idempotency        repository.IdempotencyKeyRepository
	provider           RefundProvider
	publisher          MerchantWebhookPublisher
	idempotencyTTL     time.Duration
	configuredProvider string
}

func NewRefundService(
	txRepo repository.TransactionRepository,
	refundRepo repository.RefundRepository,
	attemptRepo repository.RefundAttemptRepository,
	idempotency repository.IdempotencyKeyRepository,
	provider RefundProvider,
	publisher MerchantWebhookPublisher,
	idempotencyTTL time.Duration,
	configuredProvider ...string,
) RefundService {
	providerName := ""
	if len(configuredProvider) > 0 {
		providerName = strings.ToLower(strings.TrimSpace(configuredProvider[0]))
	}
	return &refundService{
		txRepo: txRepo, refundRepo: refundRepo, attemptRepo: attemptRepo,
		idempotency: idempotency, provider: provider, publisher: publisher,
		idempotencyTTL: idempotencyTTL, configuredProvider: providerName,
	}
}

func (s *refundService) outboxHook() repository.RefundOutboxHook {
	if s.publisher == nil {
		return nil
	}
	return func(ctx context.Context, dbTx pgx.Tx, refund *model.Refund, tx *model.Transaction, eventType model.MerchantWebhookEventType) error {
		return s.publisher.EnqueueRefundInTx(ctx, dbTx, refund, tx, eventType)
	}
}

func (s *refundService) CreateRefundWithIdempotency(ctx context.Context, merchantID, transactionID uuid.UUID, req model.CreateRefundRequest, key string) (*model.RefundResponse, error) {
	// The current application has a real payment adapter selection but only a
	// mock refund adapter. Never reserve a local refund or call the mock for a
	// non-mock production provider; a real provider adapter must be wired before
	// this capability is enabled.
	if s.configuredProvider != "" && !strings.EqualFold(s.configuredProvider, mockProviderName) {
		return nil, ErrRefundProviderUnsupported
	}
	if s.idempotency == nil {
		return s.createRefund(ctx, merchantID, transactionID, req, nil)
	}

	hash := model.RefundRequestHash(transactionID, req)
	now := time.Now().UTC()
	record := &model.IdempotencyKey{
		ID: uuid.New(), MerchantID: merchantID, Key: key, RequestHash: hash,
		Status: model.IdempotencyStatusProcessing, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(s.idempotencyTTL),
	}
	owner, err := s.idempotency.Reserve(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("reserve idempotency: %w", err)
	}
	if !owner {
		record, err = s.idempotency.GetByMerchantAndKey(ctx, merchantID, key)
		if err != nil {
			return nil, err
		}
		if record.RequestHash != hash {
			return nil, ErrIdempotencyKeyReused
		}
		switch record.Status {
		case model.IdempotencyStatusProcessing:
			if record.RefundID != nil {
				// A provider timeout may leave the refund uncertain. Reuse the
				// existing refund identity instead of issuing a second provider call.
				return s.GetRefund(ctx, merchantID, *record.RefundID)
			}
			return nil, ErrIdempotencyInProgress
		case model.IdempotencyStatusCompleted:
			var resp model.RefundResponse
			if err := json.Unmarshal(record.ResponseBody, &resp); err != nil {
				return nil, err
			}
			return &resp, nil
		case model.IdempotencyStatusFailed:
			return nil, storedRefundIdempotencyError(record.ResponseBody)
		default:
			return nil, fmt.Errorf("unknown idempotency status %q", record.Status)
		}
	}

	resp, processErr := s.createRefund(ctx, merchantID, transactionID, req, &record.ID)
	if processErr != nil {
		if errors.Is(processErr, ErrProviderTimeout) {
			// Keep the idempotency record PROCESSING. The refund remains the
			// durable source of truth and can be completed by a later webhook.
			return resp, processErr
		}
		status, body := refundIdempotencyFailure(processErr)
		rid := respRefundID(resp)
		if err := s.idempotency.Fail(ctx, record.ID, status, body, &transactionID, rid); err != nil {
			return nil, err
		}
		return resp, processErr
	}

	body, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	rid := resp.RefundID
	if err := s.idempotency.Complete(ctx, record.ID, http.StatusCreated, body, &transactionID, &rid); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *refundService) createRefund(ctx context.Context, merchantID, transactionID uuid.UUID, req model.CreateRefundRequest, idempotencyKeyID *uuid.UUID) (*model.RefundResponse, error) {
	if req.Amount <= 0 {
		return nil, ErrRefundAmountExceeded
	}
	if !model.SupportedCurrencies[req.Currency] {
		return nil, ErrRefundCurrencyMismatch
	}

	txPreview, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, transactionID)
	if err != nil {
		return nil, err
	}
	if txPreview.Status != model.TransactionStatusPaid {
		return nil, ErrRefundTransactionNotPaid
	}
	if req.Currency != txPreview.Currency {
		return nil, ErrRefundCurrencyMismatch
	}
	providerName := mockProviderName
	if txPreview.Provider != nil && *txPreview.Provider != "" {
		providerName = *txPreview.Provider
	}

	now := time.Now().UTC()
	refund := &model.Refund{
		ID: uuid.New(), MerchantID: merchantID, TransactionID: transactionID,
		Amount: req.Amount, Currency: req.Currency, Status: model.RefundStatusPending,
		Provider: providerName, Reason: req.Reason, IdempotencyKeyID: idempotencyKeyID,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}

	tx, err := s.refundRepo.ReserveAndCreate(ctx, merchantID, transactionID, refund, s.outboxHook(), model.MerchantWebhookEventRefundCreated)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrRefundAmountExceeded):
			return nil, ErrRefundAmountExceeded
		case errors.Is(err, repository.ErrRefundNotAllowed):
			return nil, ErrRefundTransactionNotPaid
		case errors.Is(err, repository.ErrRefundCurrencyMismatch):
			return nil, ErrRefundCurrencyMismatch
		default:
			return nil, err
		}
	}
	if idempotencyKeyID != nil && s.idempotency != nil {
		if err := s.idempotency.AttachRefund(ctx, *idempotencyKeyID, refund.ID); err != nil {
			return nil, fmt.Errorf("attach refund to idempotency key: %w", err)
		}
	}

	ptx := ""
	if txPreview.ProviderTransactionID != nil {
		ptx = *txPreview.ProviderTransactionID
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	providerReq := ProviderRefundCreateRequest{
		GatewayRefundID: refund.ID.String(), GatewayTransactionID: transactionID.String(),
		ProviderTransactionID: ptx, Amount: req.Amount, Currency: req.Currency, Reason: reason,
	}

	providerResp, providerErr := s.provider.CreateRefund(ctx, providerReq)
	s.recordAttempt(ctx, refund, providerReq, providerResp, providerErr)

	if providerErr != nil {
		if errors.Is(providerErr, ErrProviderTimeout) {
			// Uncertain — keep PENDING with reservation; do not release.
			return refund.ToRefundResponse(tx), ErrProviderTimeout
		}
		code := "REFUND_PROVIDER_ERROR"
		msg := providerErr.Error()
		_, _, _ = s.refundRepo.FinalizeAfterProvider(ctx, refund.ID, model.RefundStatusFailed, nil, &code, &msg, s.outboxHook(), model.MerchantWebhookEventRefundFailed)
		tx, _ = s.txRepo.FindByMerchantAndID(ctx, merchantID, transactionID)
		refund, _ = s.refundRepo.FindByID(ctx, refund.ID)
		return refund.ToRefundResponse(tx), ErrProviderFailure
	}

	return s.applyProviderResult(ctx, refund.ID, providerResp, tx)
}

func (s *refundService) applyProviderResult(ctx context.Context, refundID uuid.UUID, providerResp *ProviderRefundResponse, tx *model.Transaction) (*model.RefundResponse, error) {
	prid := providerResp.ProviderRefundID
	switch providerResp.Status {
	case "SUCCEEDED":
		ref, tx2, err := s.refundRepo.FinalizeAfterProvider(ctx, refundID, model.RefundStatusSucceeded, &prid, nil, nil, s.outboxHook(), model.MerchantWebhookEventRefundSucceeded)
		if err != nil {
			return nil, err
		}
		return ref.ToRefundResponse(tx2), nil
	case "PROCESSING":
		ref, tx2, err := s.refundRepo.FinalizeAfterProvider(ctx, refundID, model.RefundStatusProcessing, &prid, nil, nil, s.outboxHook(), model.MerchantWebhookEventRefundProcessing)
		if err != nil {
			return nil, err
		}
		return ref.ToRefundResponse(tx2), nil
	default:
		code := "REFUND_PROVIDER_ERROR"
		msg := "provider rejected refund"
		ref, tx2, err := s.refundRepo.FinalizeAfterProvider(ctx, refundID, model.RefundStatusFailed, &prid, &code, &msg, s.outboxHook(), model.MerchantWebhookEventRefundFailed)
		if err != nil {
			return nil, err
		}
		return ref.ToRefundResponse(tx2), ErrProviderFailure
	}
}

func (s *refundService) recordAttempt(ctx context.Context, refund *model.Refund, req ProviderRefundCreateRequest, resp *ProviderRefundResponse, err error) {
	n, _ := s.attemptRepo.CountByRefundID(ctx, refund.ID)
	now := time.Now().UTC()
	status := "SUCCESS"
	var respPayload []byte
	var prid *string
	if err != nil {
		status = "FAILED"
		if errors.Is(err, ErrProviderTimeout) {
			status = "TIMEOUT"
		}
		respPayload = []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
	} else if resp != nil {
		respPayload, _ = json.Marshal(resp)
		prid = &resp.ProviderRefundID
	}
	reqPayload, _ := json.Marshal(req)
	_ = s.attemptRepo.Create(ctx, &model.RefundAttempt{
		ID: uuid.New(), RefundID: refund.ID, Provider: refund.Provider,
		ProviderRefundID: prid, RequestPayload: reqPayload, ResponsePayload: respPayload,
		Status: status, AttemptNumber: n + 1, CreatedAt: now, UpdatedAt: now,
	})
}

func (s *refundService) GetRefund(ctx context.Context, merchantID, refundID uuid.UUID) (*model.RefundResponse, error) {
	ref, err := s.refundRepo.FindByMerchantAndID(ctx, merchantID, refundID)
	if err != nil {
		if errors.Is(err, repository.ErrRefundNotFound) {
			return nil, ErrRefundNotFound
		}
		return nil, err
	}
	tx, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, ref.TransactionID)
	if err != nil {
		return nil, err
	}
	return ref.ToRefundResponse(tx), nil
}

func (s *refundService) ListRefunds(ctx context.Context, merchantID, transactionID uuid.UUID, filter model.RefundListFilter) (*ListRefundsResult, error) {
	if _, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, transactionID); err != nil {
		return nil, err
	}
	if filter.Page <= 0 {
		filter.Page = model.DefaultPage
	}
	if filter.Limit <= 0 {
		filter.Limit = model.DefaultLimit
	}
	total, err := s.refundRepo.CountByTransaction(ctx, merchantID, transactionID, filter)
	if err != nil {
		return nil, err
	}
	rows, err := s.refundRepo.ListByTransaction(ctx, merchantID, transactionID, filter)
	if err != nil {
		return nil, err
	}
	tx, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, transactionID)
	if err != nil {
		return nil, err
	}
	out := make([]model.RefundResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r.ToRefundResponse(tx))
	}
	totalPages := int(total) / filter.Limit
	if int(total)%filter.Limit != 0 {
		totalPages++
	}
	return &ListRefundsResult{Refunds: out, Total: total, TotalPages: totalPages, Page: filter.Page, Limit: filter.Limit}, nil
}

func (s *refundService) ListRefundsByMerchant(ctx context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (*ListRefundsResult, error) {
	if filter.Page <= 0 {
		filter.Page = model.DefaultPage
	}
	if filter.Limit <= 0 {
		filter.Limit = model.DefaultLimit
	}
	if filter.Limit > model.MaxLimit {
		filter.Limit = model.MaxLimit
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedFrom.Before(*filter.CreatedTo) {
		return nil, ErrInvalidDateRange
	}
	if filter.TransactionID != nil {
		if _, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, *filter.TransactionID); err != nil {
			return nil, err
		}
	}

	total, err := s.refundRepo.CountByMerchant(ctx, merchantID, filter)
	if err != nil {
		return nil, err
	}
	rows, err := s.refundRepo.ListByMerchant(ctx, merchantID, filter)
	if err != nil {
		return nil, err
	}

	txCache := make(map[uuid.UUID]*model.Transaction)
	out := make([]model.RefundResponse, 0, len(rows))
	for _, r := range rows {
		tx, ok := txCache[r.TransactionID]
		if !ok {
			tx, err = s.txRepo.FindByMerchantAndID(ctx, merchantID, r.TransactionID)
			if err != nil {
				return nil, err
			}
			txCache[r.TransactionID] = tx
		}
		out = append(out, *r.ToRefundResponse(tx))
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(filter.Limit) - 1) / int64(filter.Limit))
	}
	return &ListRefundsResult{Refunds: out, Total: total, TotalPages: totalPages, Page: filter.Page, Limit: filter.Limit}, nil
}

func refundIdempotencyFailure(err error) (int, []byte) {
	switch {
	case errors.Is(err, ErrProviderTimeout):
		body, _ := json.Marshal(map[string]string{"error_code": string("REFUND_PROVIDER_TIMEOUT")})
		return http.StatusGatewayTimeout, body
	case errors.Is(err, ErrProviderFailure):
		body, _ := json.Marshal(map[string]string{"error_code": string("REFUND_PROVIDER_ERROR")})
		return http.StatusBadGateway, body
	case errors.Is(err, ErrRefundAmountExceeded):
		body, _ := json.Marshal(map[string]string{"error_code": string("REFUND_AMOUNT_EXCEEDED")})
		return http.StatusBadRequest, body
	case errors.Is(err, ErrRefundTransactionNotPaid):
		body, _ := json.Marshal(map[string]string{"error_code": string("REFUND_TRANSACTION_NOT_PAID")})
		return http.StatusBadRequest, body
	case errors.Is(err, ErrRefundCurrencyMismatch):
		body, _ := json.Marshal(map[string]string{"error_code": string("REFUND_CURRENCY_MISMATCH")})
		return http.StatusBadRequest, body
	default:
		body, _ := json.Marshal(map[string]string{"error_code": string("INTERNAL_ERROR")})
		return http.StatusInternalServerError, body
	}
}

func storedRefundIdempotencyError(body []byte) error {
	var m map[string]string
	if json.Unmarshal(body, &m) == nil {
		switch m["error_code"] {
		case "REFUND_PROVIDER_TIMEOUT":
			return ErrProviderTimeout
		case "REFUND_PROVIDER_ERROR":
			return ErrProviderFailure
		case "REFUND_AMOUNT_EXCEEDED":
			return ErrRefundAmountExceeded
		case "REFUND_TRANSACTION_NOT_PAID":
			return ErrRefundTransactionNotPaid
		case "REFUND_CURRENCY_MISMATCH":
			return ErrRefundCurrencyMismatch
		}
	}
	return errors.New("idempotency failed")
}

func respRefundID(resp *model.RefundResponse) *uuid.UUID {
	if resp == nil {
		return nil
	}
	id := resp.RefundID
	return &id
}
