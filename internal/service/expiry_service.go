package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
)

// ExpiryService processes PENDING transactions that have passed their expiry time.
type ExpiryService interface {
	// ProcessExpired finds PENDING transactions whose expired_at <= NOW() and
	// transitions them to EXPIRED. Returns the number of transactions processed.
	// Errors from individual transitions are logged but do not abort the batch.
	ProcessExpired(ctx context.Context, batchSize int) (int, error)
}

type expiryService struct {
	txRepo    repository.TransactionRepository
	publisher MerchantWebhookPublisher
	outbox    *OutboxStatusUpdater
}

// NewExpiryService constructs an ExpiryService.
func NewExpiryService(txRepo repository.TransactionRepository) ExpiryService {
	return &expiryService{txRepo: txRepo}
}

// ConfigureExpiryMerchantWebhooks attaches outbound webhook outbox support.
func ConfigureExpiryMerchantWebhooks(svc ExpiryService, publisher MerchantWebhookPublisher, outbox *OutboxStatusUpdater) {
	if s, ok := svc.(*expiryService); ok {
		s.publisher = publisher
		s.outbox = outbox
	}
}

// ProcessExpired fetches up to batchSize expired PENDING transactions and
// transitions each one to EXPIRED using the optimistic conditional UPDATE.
// If a concurrent webhook already moved the transaction to a terminal state,
// the update is a no-op and is not counted as processed.
func (s *expiryService) ProcessExpired(ctx context.Context, batchSize int) (int, error) {
	txs, err := s.txRepo.FindExpiredPendingTransactions(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("expiry service: find expired: %w", err)
	}

	processed := 0
	for _, tx := range txs {
		if !tx.Status.CanTransitionTo(model.TransactionStatusExpired) {
			slog.Warn("expiry service: unexpected non-PENDING status in batch",
				slog.String("transaction_id", tx.ID.String()),
				slog.String("status", string(tx.Status)),
			)
			continue
		}

		var updateErr error
		if s.outbox != nil {
			updateErr = s.outbox.UpdateStatus(ctx, tx.ID, model.TransactionStatusPending, model.TransactionStatusExpired)
		} else {
			updateErr = s.txRepo.UpdateStatus(ctx, tx.ID, model.TransactionStatusPending, model.TransactionStatusExpired)
		}
		if updateErr != nil {
			if errors.Is(updateErr, repository.ErrTransactionNotFound) {
				slog.Info("expiry service: transaction already changed (concurrent), skipping",
					slog.String("transaction_id", tx.ID.String()),
				)
				continue
			}
			slog.Error("expiry service: failed to expire transaction",
				slog.String("transaction_id", tx.ID.String()),
				slog.String("error", updateErr.Error()),
			)
			continue
		}

		if s.outbox == nil {
			forEnqueue := *tx
			forEnqueue.Status = model.TransactionStatusExpired
			EnqueueForTransaction(ctx, s.publisher, &forEnqueue, model.TransactionStatusExpired)
		}

		slog.Info("expiry service: transaction expired",
			slog.String("transaction_id", tx.ID.String()),
		)
		processed++
	}

	return processed, nil
}
