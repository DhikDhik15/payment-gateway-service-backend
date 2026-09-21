package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── Service-level errors ─────────────────────────────────────────────────────

var (
	// ErrWebhookUnknownProvider is returned when no parser is registered for the given provider.
	ErrWebhookUnknownProvider = errors.New("unknown webhook provider")

	// ErrWebhookProviderTxMismatch is returned when the event's provider_transaction_id
	// does not match the transaction found in the database.
	ErrWebhookProviderTxMismatch = errors.New("provider transaction id mismatch")
)

// ─── Response type ────────────────────────────────────────────────────────────

// WebhookProcessResult carries the outcome of processing a single webhook event.
type WebhookProcessResult struct {
	// Status is the final status of the webhook_event row
	// (PROCESSED, IGNORED, or FAILED).
	Status model.WebhookEventStatus
}

// ─── Interface ────────────────────────────────────────────────────────────────

// WebhookService processes inbound payment provider events.
type WebhookService interface {
	// ProcessWebhook receives a raw payload + signature for a given provider,
	// verifies the signature, parses the event, updates transaction state, and
	// persists the webhook_event record.
	ProcessWebhook(ctx context.Context, providerName string, payload []byte, signature string) (*WebhookProcessResult, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type webhookService struct {
	parsers     map[string]PaymentWebhookParser // keyed by ProviderName()
	txRepo      repository.TransactionRepository
	webhookRepo repository.WebhookEventRepository
	refundRepo  repository.RefundRepository
	publisher   MerchantWebhookPublisher
}

// NewWebhookService constructs a WebhookService backed by the given parsers and
// repositories. Pass one PaymentWebhookParser per supported provider.
func NewWebhookService(
	txRepo repository.TransactionRepository,
	webhookRepo repository.WebhookEventRepository,
	parsers ...PaymentWebhookParser,
) WebhookService {
	pm := make(map[string]PaymentWebhookParser, len(parsers))
	for _, p := range parsers {
		pm[p.ProviderName()] = p
	}
	return &webhookService{
		parsers:     pm,
		txRepo:      txRepo,
		webhookRepo: webhookRepo,
	}
}

// ConfigureWebhookMerchantPublisher attaches outbound merchant webhook outbox enqueue.
func ConfigureWebhookMerchantPublisher(svc WebhookService, publisher MerchantWebhookPublisher) {
	if s, ok := svc.(*webhookService); ok {
		s.publisher = publisher
	}
}

// ConfigureWebhookRefundRepository attaches the refund repository for processing
// inbound refund webhook events (REFUND_SUCCEEDED, REFUND_FAILED).
func ConfigureWebhookRefundRepository(svc WebhookService, refundRepo repository.RefundRepository) {
	if s, ok := svc.(*webhookService); ok {
		s.refundRepo = refundRepo
	}
}

// ProcessWebhook is the main entry point for inbound webhook events.
//
// Flow:
//  1. Resolve parser for provider.
//  2. Verify signature.
//  3. Parse payload.
//  4. Attempt to INSERT webhook_event as RECEIVED (idempotency gate).
//     - On UNIQUE constraint violation → look up existing event and return its status.
//  5. Find transaction by provider + provider_transaction_id.
//  6. Apply state transition.
//  7. Update webhook_event status.
func (s *webhookService) ProcessWebhook(ctx context.Context, providerName string, payload []byte, signature string) (*WebhookProcessResult, error) {
	normalizedProvider := strings.ToUpper(providerName)

	// ── 1. Resolve parser ────────────────────────────────────────────────────
	parser, ok := s.parsers[normalizedProvider]
	if !ok {
		return nil, ErrWebhookUnknownProvider
	}

	// ── 2. Verify signature ──────────────────────────────────────────────────
	if err := parser.VerifySignature(payload, signature); err != nil {
		return nil, err // ErrWebhookInvalidSignature propagated as-is
	}

	// ── 3. Parse payload ─────────────────────────────────────────────────────
	event, err := parser.ParseEvent(payload)
	if err != nil {
		return nil, err // ErrWebhookMalformedPayload / ErrWebhookMissingFields
	}

	// Refund events have a separate state machine and must not enter the payment
	// transaction processor below.
	if isRefundEvent(event.EventType) {
		return s.processRefundWebhook(ctx, normalizedProvider, payload, signature, event)
	}

	// Signature verification and parsing happen before the database transaction.
	// The PostgreSQL repository then commits idempotency, state, and event status
	// together. In-memory repositories use the fallback below for unit tests.
	if atomicRepo, ok := s.webhookRepo.(repository.AtomicWebhookEventProcessor); ok {
		providerTxID := event.ProviderTransactionID
		we := &model.WebhookEvent{
			ID: uuid.New(), Provider: normalizedProvider, EventID: event.EventID,
			EventType: event.EventType, ProviderTransactionID: &providerTxID,
			Payload: payload, Signature: &signature,
			Status: model.WebhookEventStatusReceived, CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		var targetStatus model.TransactionStatus
		switch event.EventType {
		case string(model.WebhookEventTypePaymentPaid):
			targetStatus = model.TransactionStatusPaid
		case string(model.WebhookEventTypePaymentFailed):
			targetStatus = model.TransactionStatusFailed
		case string(model.WebhookEventTypePaymentExpired):
			targetStatus = model.TransactionStatusExpired
		}
		status, processErr := atomicRepo.ProcessAtomically(ctx, we, targetStatus, model.TransactionStatusPending, event.Amount, event.Currency, s.merchantOutboxHook())
		if processErr != nil {
			return nil, processErr
		}
		return &WebhookProcessResult{Status: status}, nil
	}

	// ── 4. Persist webhook_event as RECEIVED (idempotency gate) ─────────────
	// This INSERT uses UNIQUE(provider, event_id). If a concurrent or duplicate
	// request races here, exactly one will succeed and the other gets
	// ErrWebhookEventDuplicate — we then look up the existing row and return
	// its current status rather than re-processing.
	now := time.Now().UTC()
	providerTxID := event.ProviderTransactionID
	we := &model.WebhookEvent{
		ID:                    uuid.New(),
		Provider:              normalizedProvider,
		EventID:               event.EventID,
		EventType:             event.EventType,
		ProviderTransactionID: &providerTxID,
		Payload:               payload,
		Signature:             &signature,
		Status:                model.WebhookEventStatusReceived,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	insertErr := s.webhookRepo.Create(ctx, we)
	if insertErr != nil {
		if errors.Is(insertErr, repository.ErrWebhookEventDuplicate) {
			// Duplicate event — look up what happened last time and return it.
			_, findErr := s.webhookRepo.FindByProviderAndEventID(ctx, normalizedProvider, event.EventID)
			if findErr != nil {
				return nil, fmt.Errorf("webhook service: lookup existing event: %w", findErr)
			}
			slog.Info("webhook service: duplicate event ignored",
				slog.String("provider", normalizedProvider),
				slog.String("event_id", event.EventID),
			)
			// If previous processing succeeded or was ignored, return IGNORED.
			// If it previously failed, we still return IGNORED rather than retrying
			// (retry strategy is deferred to Phase 4).
			return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
		}
		return nil, fmt.Errorf("webhook service: persist event: %w", insertErr)
	}

	// ── 5. Find transaction by provider_transaction_id ───────────────────────
	tx, err := s.txRepo.FindByProviderTransactionID(ctx, normalizedProvider, event.ProviderTransactionID)
	if err != nil {
		if errors.Is(err, repository.ErrTransactionNotFound) {
			// Mark event as FAILED — transaction not found.
			errMsg := fmt.Sprintf("transaction not found for provider_tx_id=%s", event.ProviderTransactionID)
			_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusFailed, &errMsg)
			return nil, repository.ErrTransactionNotFound
		}
		errMsg := err.Error()
		_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusFailed, &errMsg)
		return nil, fmt.Errorf("webhook service: find transaction: %w", err)
	}

	// Link webhook_event → transaction (update in place — we already inserted)
	txID := tx.ID
	we.TransactionID = &txID
	if (event.Amount > 0 && event.Amount != tx.Amount) || (event.Currency != "" && event.Currency != tx.Currency) {
		errMsg := "webhook amount or currency does not match transaction"
		_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusIgnored, &errMsg)
		return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
	}

	// ── 6. Apply state transition ────────────────────────────────────────────
	result, transitionErr := s.applyTransition(ctx, we, tx, event)
	if transitionErr != nil {
		return nil, transitionErr
	}

	return result, nil
}

// applyTransition applies the event's state change to the transaction and
// updates the webhook_event status accordingly.
func (s *webhookService) applyTransition(
	ctx context.Context,
	we *model.WebhookEvent,
	tx *model.Transaction,
	event *ParsedWebhookEvent,
) (*WebhookProcessResult, error) {
	// Map provider event type → desired transaction status.
	var targetStatus model.TransactionStatus
	switch event.EventType {
	case string(model.WebhookEventTypePaymentPaid):
		targetStatus = model.TransactionStatusPaid
	case string(model.WebhookEventTypePaymentFailed):
		targetStatus = model.TransactionStatusFailed
	case string(model.WebhookEventTypePaymentExpired):
		targetStatus = model.TransactionStatusExpired
	default:
		// Unknown event type — mark as IGNORED and return.
		slog.Info("webhook service: unknown event type, ignoring",
			slog.String("event_type", event.EventType),
			slog.String("event_id", event.EventID),
		)
		_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusIgnored, nil)
		return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
	}

	// If the transaction is already in the desired terminal state, this is a
	// harmless duplicate — return IGNORED without re-processing.
	if tx.Status == targetStatus {
		slog.Info("webhook service: transaction already in target state, ignoring",
			slog.String("transaction_id", tx.ID.String()),
			slog.String("current_status", string(tx.Status)),
			slog.String("event_id", event.EventID),
		)
		_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusIgnored, nil)
		return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
	}

	// Check state machine validity.
	if !tx.Status.CanTransitionTo(targetStatus) {
		errMsg := fmt.Sprintf("cannot transition from %s to %s", tx.Status, targetStatus)
		slog.Warn("webhook service: invalid state transition",
			slog.String("transaction_id", tx.ID.String()),
			slog.String("from", string(tx.Status)),
			slog.String("to", string(targetStatus)),
			slog.String("event_id", event.EventID),
		)
		_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusIgnored, &errMsg)
		return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
	}

	// Execute the transition using an optimistic conditional UPDATE.
	// If RowsAffected == 0, a concurrent request already changed the status.
	var updateErr error
	if targetStatus == model.TransactionStatusPaid {
		updateErr = s.txRepo.UpdateStatusWithPaidAt(ctx, tx.ID, tx.Status)
	} else {
		updateErr = s.txRepo.UpdateStatus(ctx, tx.ID, tx.Status, targetStatus)
	}

	if updateErr != nil {
		if errors.Is(updateErr, repository.ErrTransactionNotFound) {
			// Concurrency: another goroutine already changed the status.
			// Re-read the transaction to decide what happened.
			current, refetchErr := s.txRepo.FindByID(ctx, tx.ID)
			if refetchErr != nil {
				errMsg := "concurrent update: refetch failed"
				_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusFailed, &errMsg)
				return nil, fmt.Errorf("webhook service: concurrent refetch: %w", refetchErr)
			}

			if current.Status == targetStatus {
				// Another goroutine beat us to it — treat as IGNORED.
				_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusIgnored, nil)
				return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
			}

			// A different terminal state won (e.g. EXPIRED beat PAID).
			errMsg := fmt.Sprintf("concurrent state change: current=%s, wanted=%s", current.Status, targetStatus)
			_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusIgnored, &errMsg)
			return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
		}
		errMsg := updateErr.Error()
		_ = s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusFailed, &errMsg)
		return nil, fmt.Errorf("webhook service: update transaction status: %w", updateErr)
	}

	// Transition succeeded — mark webhook_event PROCESSED.
	if updErr := s.webhookRepo.UpdateStatus(ctx, we.ID, model.WebhookEventStatusProcessed, nil); updErr != nil {
		// Non-fatal: transaction is already updated. Log and continue.
		slog.Error("webhook service: failed to mark event PROCESSED",
			slog.String("webhook_event_id", we.ID.String()),
			slog.String("error", updErr.Error()),
		)
	}

	slog.Info("webhook service: transaction status updated",
		slog.String("transaction_id", tx.ID.String()),
		slog.String("from", string(tx.Status)),
		slog.String("to", string(targetStatus)),
		slog.String("event_id", event.EventID),
	)

	// Copy before enqueue — avoid mutating shared *Transaction (race with concurrent expiry/webhook tests).
	forEnqueue := *tx
	forEnqueue.Status = targetStatus
	forEnqueue.UpdatedAt = time.Now().UTC()
	if targetStatus == model.TransactionStatusPaid && forEnqueue.PaidAt == nil {
		now := time.Now().UTC()
		forEnqueue.PaidAt = &now
	}
	EnqueueForTransaction(ctx, s.publisher, &forEnqueue, targetStatus)

	return &WebhookProcessResult{Status: model.WebhookEventStatusProcessed}, nil
}

func (s *webhookService) merchantOutboxHook() func(ctx context.Context, dbTx pgx.Tx, payment *model.Transaction) error {
	if s.publisher == nil {
		return nil
	}
	return func(ctx context.Context, dbTx pgx.Tx, payment *model.Transaction) error {
		eventType, ok := model.EventTypeForStatus(payment.Status)
		if !ok {
			return nil
		}
		return s.publisher.EnqueueInTx(ctx, dbTx, payment, eventType)
	}
}

// ─── Refund webhook helpers ───────────────────────────────────────────────────

// isRefundEvent returns true for REFUND_SUCCEEDED and REFUND_FAILED event types.
func isRefundEvent(eventType string) bool {
	return eventType == string(model.WebhookEventTypeRefundSucceeded) ||
		eventType == string(model.WebhookEventTypeRefundFailed)
}

// processRefundWebhook handles inbound REFUND_SUCCEEDED / REFUND_FAILED events.
// It delegates to RefundRepository.ProcessRefundWebhookAtomically which handles
// all idempotency, financial counter updates, and outbox enqueueing in one DB tx.
func (s *webhookService) processRefundWebhook(
	ctx context.Context,
	normalizedProvider string,
	payload []byte,
	signature string,
	event *ParsedWebhookEvent,
) (*WebhookProcessResult, error) {
	if s.refundRepo == nil {
		slog.Warn("webhook service: received refund event but refund repository not configured",
			slog.String("provider", normalizedProvider),
			slog.String("event_id", event.EventID),
			slog.String("event_type", event.EventType),
		)
		return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
	}

	// Map event type → target refund status.
	var targetRefundStatus model.RefundStatus
	var outboxEventType model.MerchantWebhookEventType
	switch event.EventType {
	case string(model.WebhookEventTypeRefundSucceeded):
		targetRefundStatus = model.RefundStatusSucceeded
		outboxEventType = model.MerchantWebhookEventRefundSucceeded
	case string(model.WebhookEventTypeRefundFailed):
		targetRefundStatus = model.RefundStatusFailed
		outboxEventType = model.MerchantWebhookEventRefundFailed
	default:
		slog.Info("webhook service: unhandled refund event type, ignoring",
			slog.String("event_type", event.EventType),
		)
		return &WebhookProcessResult{Status: model.WebhookEventStatusIgnored}, nil
	}

	now := time.Now().UTC()
	providerTxID := event.ProviderTransactionID
	we := &model.WebhookEvent{
		ID:                    uuid.New(),
		Provider:              normalizedProvider,
		EventID:               event.EventID,
		EventType:             event.EventType,
		ProviderTransactionID: &providerTxID,
		Payload:               payload,
		Signature:             &signature,
		Status:                model.WebhookEventStatusReceived,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	// Build the outbox hook using the publisher if available.
	var outboxHook repository.RefundOutboxHook
	if s.publisher != nil {
		pub := s.publisher
		outboxHook = func(ctx context.Context, dbTx pgx.Tx, refund *model.Refund, tx *model.Transaction, et model.MerchantWebhookEventType) error {
			return pub.EnqueueRefundInTx(ctx, dbTx, refund, tx, et)
		}
	}

	status, err := s.refundRepo.ProcessRefundWebhookAtomically(
		ctx,
		we,
		normalizedProvider,
		event.ProviderRefundID,
		event.Amount,
		event.Currency,
		targetRefundStatus,
		outboxHook,
		outboxEventType,
	)
	if err != nil {
		slog.Error("webhook service: refund webhook processing failed",
			slog.String("provider", normalizedProvider),
			slog.String("event_id", event.EventID),
			slog.String("provider_refund_id", event.ProviderRefundID),
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	slog.Info("webhook service: refund webhook processed",
		slog.String("provider", normalizedProvider),
		slog.String("event_id", event.EventID),
		slog.String("provider_refund_id", event.ProviderRefundID),
		slog.String("target_status", string(targetRefundStatus)),
		slog.String("webhook_status", string(status)),
	)
	return &WebhookProcessResult{Status: status}, nil
}
