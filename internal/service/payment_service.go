package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Service-level errors ─────────────────────────────────────────────────────

var (
	// ErrDuplicateOrder is returned when merchant_order_id already exists.
	ErrDuplicateOrder = errors.New("duplicate merchant order id")

	// ErrInvalidCurrency is returned for unsupported currency codes.
	ErrInvalidCurrency = errors.New("unsupported currency")

	// ErrInvalidPaymentMethod is returned for unsupported payment methods.
	ErrInvalidPaymentMethod = errors.New("unsupported payment method")

	// ErrInvalidTransactionState is returned when a state transition is not allowed.
	ErrInvalidTransactionState = errors.New("invalid transaction state transition")
	ErrIdempotencyKeyReused    = errors.New("idempotency key reused")
	ErrIdempotencyInProgress   = errors.New("idempotency request in progress")

	// Listing validation errors.
	ErrInvalidPage      = errors.New("page must be >= 1")
	ErrInvalidLimit     = errors.New("limit must be between 1 and 100")
	ErrInvalidStatus    = errors.New("invalid transaction status")
	ErrInvalidDateRange = errors.New("created_from must be before created_to")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// PaymentService defines the business operations for the payment lifecycle.
type PaymentService interface {
	// CreatePayment validates the request, calls the provider, and persists
	// the transaction + first payment attempt.
	CreatePayment(ctx context.Context, merchantID uuid.UUID, req model.CreatePaymentRequest) (*model.CreatePaymentResponse, error)
	CreatePaymentWithIdempotency(ctx context.Context, merchantID uuid.UUID, req model.CreatePaymentRequest, key string) (*model.CreatePaymentResponse, error)

	// GetPayment returns a transaction that belongs to merchantID.
	// Returns ErrTransactionNotFound (from repository) if not found or
	// if the transaction belongs to a different merchant.
	GetPayment(ctx context.Context, merchantID, transactionID uuid.UUID) (*model.PaymentResponse, error)

	// CancelPayment cancels a CREATED or PENDING transaction.
	// For PENDING transactions the provider is asked to cancel first.
	CancelPayment(ctx context.Context, merchantID, transactionID uuid.UUID) (*model.PaymentResponse, error)

	// ListPayments returns a paginated, filtered list of transactions belonging
	// to merchantID.  Pagination defaults and validation are applied here.
	ListPayments(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (*model.ListPaymentsResult, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type paymentService struct {
	txRepo         repository.TransactionRepository
	attemptRepo    repository.PaymentAttemptRepository
	provider       PaymentProvider
	idempotency    repository.IdempotencyKeyRepository
	idempotencyTTL time.Duration
	publisher      MerchantWebhookPublisher
	outbox         *OutboxStatusUpdater
}

// NewPaymentService constructs a PaymentService.
func NewPaymentService(
	txRepo repository.TransactionRepository,
	attemptRepo repository.PaymentAttemptRepository,
	provider PaymentProvider,
) PaymentService {
	return &paymentService{
		txRepo:         txRepo,
		attemptRepo:    attemptRepo,
		provider:       provider,
		idempotencyTTL: 24 * time.Hour,
	}
}

func NewPaymentServiceWithIdempotency(
	txRepo repository.TransactionRepository,
	attemptRepo repository.PaymentAttemptRepository,
	provider PaymentProvider,
	idempotency repository.IdempotencyKeyRepository,
	ttl time.Duration,
) PaymentService {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &paymentService{txRepo: txRepo, attemptRepo: attemptRepo, provider: provider, idempotency: idempotency, idempotencyTTL: ttl}
}

// ConfigurePaymentMerchantWebhooks attaches outbound webhook outbox support to a PaymentService.
func ConfigurePaymentMerchantWebhooks(svc PaymentService, publisher MerchantWebhookPublisher, outbox *OutboxStatusUpdater) {
	if s, ok := svc.(*paymentService); ok {
		s.publisher = publisher
		s.outbox = outbox
	}
}

// WithMerchantWebhookPublisher attaches the outbound webhook outbox publisher.
// Deprecated: prefer ConfigurePaymentMerchantWebhooks.
func (s *paymentService) WithMerchantWebhookPublisher(publisher MerchantWebhookPublisher, outbox *OutboxStatusUpdater) PaymentService {
	s.publisher = publisher
	s.outbox = outbox
	return s
}

// ─── CreatePayment ────────────────────────────────────────────────────────────
//
// Atomicity design (external call cannot join a DB transaction):
//
//  1. Validate business rules.
//  2. Persist transaction as CREATED.
//  3. Call provider (outside DB tx — may be slow / fail).
//  4. Persist payment_attempt (success or failure).
//  5. Update transaction status (PENDING on success, FAILED on failure).
//
// If step 3 fails we still record the attempt so there is always an audit trail.
// If step 5 fails after a successful provider call we log a critical error but
// do NOT leave the response in an ambiguous state — the attempt is already saved.

func (s *paymentService) CreatePayment(ctx context.Context, merchantID uuid.UUID, req model.CreatePaymentRequest) (*model.CreatePaymentResponse, error) {
	return s.createPayment(ctx, merchantID, req)
}

func (s *paymentService) CreatePaymentWithIdempotency(ctx context.Context, merchantID uuid.UUID, req model.CreatePaymentRequest, key string) (*model.CreatePaymentResponse, error) {
	if s.idempotency == nil {
		return s.createPayment(ctx, merchantID, req)
	}

	now := time.Now().UTC()
	record := &model.IdempotencyKey{
		ID: uuid.New(), MerchantID: merchantID, Key: key,
		RequestHash: model.PaymentRequestHash(req), Status: model.IdempotencyStatusProcessing,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(s.idempotencyTTL),
	}
	owner, err := s.idempotency.Reserve(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("reserve idempotency key: %w", err)
	}
	if !owner {
		record, err = s.idempotency.GetByMerchantAndKey(ctx, merchantID, key)
		if err != nil {
			return nil, fmt.Errorf("load idempotency key after conflict: %w", err)
		}
		if record.RequestHash != model.PaymentRequestHash(req) {
			return nil, ErrIdempotencyKeyReused
		}
		switch record.Status {
		case model.IdempotencyStatusProcessing:
			return nil, ErrIdempotencyInProgress
		case model.IdempotencyStatusCompleted:
			var response model.CreatePaymentResponse
			if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
				return nil, fmt.Errorf("decode idempotency response: %w", err)
			}
			return &response, nil
		case model.IdempotencyStatusFailed:
			return nil, storedIdempotencyError(record.ResponseBody)
		default:
			return nil, fmt.Errorf("unknown idempotency status %q", record.Status)
		}
	}

	response, processErr := s.createPayment(ctx, merchantID, req)
	if processErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(processErr, ErrProviderTimeout) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(processErr, ErrProviderFailure) {
			status = http.StatusBadGateway
		}
		body, _ := json.Marshal(map[string]string{"error_code": idempotencyErrorCode(processErr)})
		if err := s.idempotency.Fail(ctx, record.ID, status, body, nil, nil); err != nil {
			return nil, fmt.Errorf("store failed idempotency result: %w", err)
		}
		return nil, processErr
	}

	body, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode idempotency response: %w", err)
	}
	tid := response.TransactionID
	if err := s.idempotency.Complete(ctx, record.ID, http.StatusCreated, body, &tid, nil); err != nil {
		return nil, fmt.Errorf("store completed idempotency result: %w", err)
	}
	return response, nil
}

func idempotencyErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrProviderTimeout):
		return "PAYMENT_PROVIDER_TIMEOUT"
	case errors.Is(err, ErrProviderFailure):
		return "PAYMENT_PROVIDER_ERROR"
	default:
		return "INTERNAL_ERROR"
	}
}

func storedIdempotencyError(body []byte) error {
	var stored struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		return fmt.Errorf("stored idempotency failure: %w", err)
	}
	switch stored.ErrorCode {
	case "PAYMENT_PROVIDER_TIMEOUT":
		return ErrProviderTimeout
	case "PAYMENT_PROVIDER_ERROR":
		return ErrProviderFailure
	default:
		return errors.New("stored idempotent payment failure")
	}
}

func (s *paymentService) createPayment(ctx context.Context, merchantID uuid.UUID, req model.CreatePaymentRequest) (*model.CreatePaymentResponse, error) {
	// ── 1. Business validation ────────────────────────────────────────────────

	if !model.SupportedCurrencies[req.Currency] {
		return nil, ErrInvalidCurrency
	}
	if !model.SupportedPaymentMethods[req.PaymentMethod] {
		return nil, ErrInvalidPaymentMethod
	}

	// ── 2. Duplicate order check ──────────────────────────────────────────────

	_, err := s.txRepo.FindByMerchantOrderID(ctx, merchantID, req.MerchantOrderID)
	if err == nil {
		// Row found → duplicate.
		return nil, ErrDuplicateOrder
	}
	if !errors.Is(err, repository.ErrTransactionNotFound) {
		return nil, fmt.Errorf("payment service create: check duplicate: %w", err)
	}

	// ── 3. Persist transaction as CREATED ─────────────────────────────────────

	now := time.Now().UTC()
	tx := &model.Transaction{
		ID:              uuid.New(),
		MerchantID:      merchantID,
		MerchantOrderID: req.MerchantOrderID,
		Amount:          req.Amount,
		Currency:        req.Currency,
		PaymentMethod:   req.PaymentMethod,
		Status:          model.TransactionStatusCreated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if s.outbox != nil {
		if err := s.outbox.CreateCreated(ctx, tx); err != nil {
			return nil, fmt.Errorf("payment service create: persist transaction: %w", err)
		}
	} else {
		if err := s.txRepo.Create(ctx, tx); err != nil {
			return nil, fmt.Errorf("payment service create: persist transaction: %w", err)
		}
		EnqueueForTransaction(ctx, s.publisher, tx, model.TransactionStatusCreated)
	}

	slog.Info("transaction created",
		slog.String("transaction_id", tx.ID.String()),
		slog.String("merchant_id", merchantID.String()),
		slog.String("merchant_order_id", req.MerchantOrderID),
		slog.Int64("amount", req.Amount),
		slog.String("currency", req.Currency),
	)

	// ── 4. Call payment provider ──────────────────────────────────────────────
	// This happens OUTSIDE any database transaction intentionally — provider
	// calls can be slow and holding a DB connection open would exhaust the pool.

	providerReq := ProviderCreateRequest{
		TransactionID:   tx.ID.String(),
		MerchantOrderID: req.MerchantOrderID,
		Amount:          req.Amount,
		Currency:        req.Currency,
		PaymentMethod:   req.PaymentMethod,
	}

	providerResp, providerErr := s.provider.CreatePayment(ctx, providerReq)

	// ── 5. Persist payment attempt (always, regardless of provider result) ────

	attemptStatus := "SUCCESS"
	if providerErr != nil {
		attemptStatus = "FAILED"
	}

	reqPayload, _ := json.Marshal(providerReq)
	var respPayload []byte
	if providerResp != nil {
		respPayload, _ = json.Marshal(providerResp)
	}

	count, _ := s.attemptRepo.CountByTransactionID(ctx, tx.ID)

	var providerTxID *string
	if providerResp != nil && providerResp.ProviderTransactionID != "" {
		id := providerResp.ProviderTransactionID
		providerTxID = &id
	}

	attemptNow := time.Now().UTC()
	attempt := &model.PaymentAttempt{
		ID:                    uuid.New(),
		TransactionID:         tx.ID,
		Provider:              s.provider.Name(),
		ProviderTransactionID: providerTxID,
		RequestPayload:        json.RawMessage(reqPayload),
		ResponsePayload:       json.RawMessage(respPayload),
		Status:                attemptStatus,
		AttemptNumber:         count + 1,
		CreatedAt:             attemptNow,
		UpdatedAt:             attemptNow,
	}

	if err := s.attemptRepo.Create(ctx, attempt); err != nil {
		// Non-fatal for the outer flow, but we log it loudly.
		slog.Error("payment service create: failed to persist payment attempt",
			slog.String("transaction_id", tx.ID.String()),
			slog.String("error", err.Error()),
		)
	}

	// ── 6. Handle provider failure ────────────────────────────────────────────

	if providerErr != nil {
		slog.Warn("payment service create: provider error, marking transaction FAILED",
			slog.String("transaction_id", tx.ID.String()),
			slog.String("error", providerErr.Error()),
		)

		// Best-effort: mark transaction as FAILED.
		if updateErr := s.transitionFailed(ctx, tx); updateErr != nil {
			slog.Error("payment service create: failed to mark transaction FAILED",
				slog.String("transaction_id", tx.ID.String()),
				slog.String("error", updateErr.Error()),
			)
		}

		if errors.Is(providerErr, ErrProviderTimeout) {
			return nil, ErrProviderTimeout
		}
		return nil, ErrProviderFailure
	}

	// ── 7. Update transaction to PENDING with provider info ───────────────────

	if err := s.transitionPending(ctx, tx, providerResp); err != nil {
		slog.Error("payment service create: failed to transition to PENDING",
			slog.String("transaction_id", tx.ID.String()),
			slog.String("error", err.Error()),
		)
		return nil, fmt.Errorf("payment service create: update status: %w", err)
	}

	slog.Info("transaction moved to PENDING",
		slog.String("transaction_id", tx.ID.String()),
		slog.String("provider", s.provider.Name()),
		slog.String("provider_tx_id", providerResp.ProviderTransactionID),
	)

	return &model.CreatePaymentResponse{
		TransactionID:         tx.ID,
		MerchantOrderID:       tx.MerchantOrderID,
		Amount:                tx.Amount,
		Currency:              tx.Currency,
		PaymentMethod:         tx.PaymentMethod,
		Provider:              s.provider.Name(),
		ProviderTransactionID: providerResp.ProviderTransactionID,
		Status:                model.TransactionStatusPending,
		PaymentURL:            providerResp.PaymentURL,
		ExpiredAt:             providerResp.ExpiredAt,
		CreatedAt:             tx.CreatedAt,
	}, nil
}

func (s *paymentService) transitionFailed(ctx context.Context, tx *model.Transaction) error {
	if s.outbox != nil {
		if err := s.outbox.UpdateStatus(ctx, tx.ID, model.TransactionStatusCreated, model.TransactionStatusFailed); err != nil {
			return err
		}
		tx.Status = model.TransactionStatusFailed
		return nil
	}
	if err := s.txRepo.UpdateStatus(ctx, tx.ID, model.TransactionStatusCreated, model.TransactionStatusFailed); err != nil {
		return err
	}
	tx.Status = model.TransactionStatusFailed
	EnqueueForTransaction(ctx, s.publisher, tx, model.TransactionStatusFailed)
	return nil
}

func (s *paymentService) transitionPending(ctx context.Context, tx *model.Transaction, providerResp *ProviderPaymentResponse) error {
	if s.outbox != nil {
		if err := s.outbox.UpdateStatusWithProvider(ctx, tx.ID,
			model.TransactionStatusCreated, model.TransactionStatusPending,
			s.provider.Name(), providerResp.ProviderTransactionID, providerResp.PaymentURL, providerResp.ExpiredAt,
		); err != nil {
			return err
		}
		tx.Status = model.TransactionStatusPending
		tx.ExpiredAt = providerResp.ExpiredAt
		return nil
	}
	if err := s.txRepo.UpdateStatusWithProvider(ctx, tx.ID,
		model.TransactionStatusCreated, model.TransactionStatusPending,
		s.provider.Name(), providerResp.ProviderTransactionID, providerResp.PaymentURL, providerResp.ExpiredAt,
	); err != nil {
		return err
	}
	tx.Status = model.TransactionStatusPending
	tx.ExpiredAt = providerResp.ExpiredAt
	prov := s.provider.Name()
	tx.Provider = &prov
	ptid := providerResp.ProviderTransactionID
	tx.ProviderTransactionID = &ptid
	purl := providerResp.PaymentURL
	tx.PaymentURL = &purl
	EnqueueForTransaction(ctx, s.publisher, tx, model.TransactionStatusPending)
	return nil
}

// ─── GetPayment ───────────────────────────────────────────────────────────────

func (s *paymentService) GetPayment(ctx context.Context, merchantID, transactionID uuid.UUID) (*model.PaymentResponse, error) {
	tx, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, transactionID)
	if err != nil {
		// ErrTransactionNotFound is returned for both "not found" and
		// "belongs to another merchant" — intentional (no info leakage).
		return nil, err
	}
	return txToPaymentResponse(tx), nil
}

// ─── CancelPayment ────────────────────────────────────────────────────────────
//
// Flow:
//  1. Fetch transaction (merchant-scoped).
//  2. Validate current state is cancellable.
//  3. If PENDING: call provider.CancelPayment() first.
//     - provider error → return provider error, do NOT update DB.
//  4. Update transaction to CANCELLED (conditional on current status).

func (s *paymentService) CancelPayment(ctx context.Context, merchantID, transactionID uuid.UUID) (*model.PaymentResponse, error) {
	// ── 1. Fetch (merchant-scoped) ────────────────────────────────────────────

	tx, err := s.txRepo.FindByMerchantAndID(ctx, merchantID, transactionID)
	if err != nil {
		return nil, err
	}

	// ── 2. State machine check ────────────────────────────────────────────────

	if !tx.Status.IsCancellable() {
		return nil, ErrInvalidTransactionState
	}

	// ── 3. Cancel at provider when PENDING ────────────────────────────────────

	if tx.Status == model.TransactionStatusPending {
		// Determine the provider transaction ID from the most recent attempt.
		attempts, err := s.attemptRepo.FindByTransactionID(ctx, tx.ID)
		if err != nil {
			return nil, fmt.Errorf("payment service cancel: load attempts: %w", err)
		}

		providerTxID := resolveProviderTxID(attempts)

		slog.Info("payment service cancel: calling provider",
			slog.String("transaction_id", tx.ID.String()),
			slog.String("provider_tx_id", providerTxID),
		)

		if err := s.provider.CancelPayment(ctx, providerTxID); err != nil {
			slog.Warn("payment service cancel: provider refused",
				slog.String("transaction_id", tx.ID.String()),
				slog.String("error", err.Error()),
			)
			if errors.Is(err, ErrProviderTimeout) {
				return nil, ErrProviderTimeout
			}
			return nil, ErrProviderFailure
		}
	}

	// ── 4. Update transaction status ──────────────────────────────────────────

	fromStatus := tx.Status
	if s.outbox != nil {
		if err := s.outbox.UpdateStatus(ctx, tx.ID, fromStatus, model.TransactionStatusCancelled); err != nil {
			if errors.Is(err, repository.ErrTransactionNotFound) {
				return nil, ErrInvalidTransactionState
			}
			return nil, fmt.Errorf("payment service cancel: update status: %w", err)
		}
	} else {
		if err := s.txRepo.UpdateStatus(ctx, tx.ID, fromStatus, model.TransactionStatusCancelled); err != nil {
			if errors.Is(err, repository.ErrTransactionNotFound) {
				return nil, ErrInvalidTransactionState
			}
			return nil, fmt.Errorf("payment service cancel: update status: %w", err)
		}
	}

	slog.Info("transaction cancelled",
		slog.String("transaction_id", tx.ID.String()),
		slog.String("previous_status", string(fromStatus)),
	)

	tx.Status = model.TransactionStatusCancelled
	tx.UpdatedAt = time.Now().UTC()
	if s.outbox == nil {
		EnqueueForTransaction(ctx, s.publisher, tx, model.TransactionStatusCancelled)
	}
	return txToPaymentResponse(tx), nil
}

// ─── ListPayments ─────────────────────────────────────────────────────────────

// ListPayments validates the filter, applies defaults, delegates to the
// repository, and assembles pagination metadata.
//
// Validation rules:
//   - page >= 1  (default 1)
//   - 1 <= limit <= 100  (default 20)
//   - status must be a known TransactionStatus when supplied
//   - created_from < created_to when both are supplied
func (s *paymentService) ListPayments(ctx context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (*model.ListPaymentsResult, error) {
	// Apply defaults.
	if filter.Page == 0 {
		filter.Page = model.DefaultPage
	}
	if filter.Limit == 0 {
		filter.Limit = model.DefaultLimit
	}

	// Validate pagination.
	if filter.Page < 1 {
		return nil, ErrInvalidPage
	}
	if filter.Limit < 1 || filter.Limit > model.MaxLimit {
		return nil, ErrInvalidLimit
	}

	// Validate status.
	if filter.Status != nil {
		switch *filter.Status {
		case model.TransactionStatusCreated,
			model.TransactionStatusPending,
			model.TransactionStatusPaid,
			model.TransactionStatusFailed,
			model.TransactionStatusExpired,
			model.TransactionStatusCancelled:
			// valid
		default:
			return nil, ErrInvalidStatus
		}
	}

	// Validate payment method.
	if filter.PaymentMethod != nil && !model.SupportedPaymentMethods[*filter.PaymentMethod] {
		return nil, ErrInvalidPaymentMethod
	}

	// Validate date range.
	if filter.CreatedFrom != nil && filter.CreatedTo != nil {
		if !filter.CreatedFrom.Before(*filter.CreatedTo) {
			return nil, ErrInvalidDateRange
		}
	}

	// Count (uses same WHERE as List — consistent pagination total).
	total, err := s.txRepo.CountList(ctx, merchantID, filter)
	if err != nil {
		return nil, fmt.Errorf("list payments count: %w", err)
	}

	// Data.
	txs, err := s.txRepo.List(ctx, merchantID, filter)
	if err != nil {
		return nil, fmt.Errorf("list payments list: %w", err)
	}

	// Convert domain objects to response DTOs.
	items := make([]model.PaymentResponse, 0, len(txs))
	for _, tx := range txs {
		items = append(items, *txToPaymentResponse(tx))
	}

	// Compute total pages (ceil).
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(filter.Limit) - 1) / int64(filter.Limit))
	}

	return &model.ListPaymentsResult{
		Transactions: items,
		Total:        total,
		TotalPages:   totalPages,
		Page:         filter.Page,
		Limit:        filter.Limit,
	}, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// txToPaymentResponse converts a Transaction domain object to the API DTO.
func txToPaymentResponse(tx *model.Transaction) *model.PaymentResponse {
	return &model.PaymentResponse{
		TransactionID:         tx.ID,
		MerchantOrderID:       tx.MerchantOrderID,
		Amount:                tx.Amount,
		Currency:              tx.Currency,
		PaymentMethod:         tx.PaymentMethod,
		Provider:              tx.Provider,
		ProviderTransactionID: tx.ProviderTransactionID,
		PaymentURL:            tx.PaymentURL,
		Status:                tx.Status,
		ExpiredAt:             tx.ExpiredAt,
		PaidAt:                tx.PaidAt,
		CreatedAt:             tx.CreatedAt,
		UpdatedAt:             tx.UpdatedAt,
	}
}

// resolveProviderTxID returns the ProviderTransactionID from the most recent
// successful attempt, or an empty string if none exists.
func resolveProviderTxID(attempts []model.PaymentAttempt) string {
	// Attempts are ordered ASC — iterate in reverse to find the latest.
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].ProviderTransactionID != nil {
			return *attempts[i].ProviderTransactionID
		}
	}
	return ""
}
