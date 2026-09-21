package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookEventRepository defines the database operations for webhook events.
type WebhookEventRepository interface {
	// Create inserts a new webhook_event row.
	// Returns ErrWebhookEventDuplicate when the UNIQUE(provider, event_id) constraint fires,
	// indicating a duplicate event that should be handled idempotently.
	Create(ctx context.Context, event *model.WebhookEvent) error

	// FindByProviderAndEventID looks up an event by its provider + event_id pair.
	// Returns ErrWebhookEventNotFound when no row matches.
	FindByProviderAndEventID(ctx context.Context, provider, eventID string) (*model.WebhookEvent, error)

	// UpdateStatus updates the status (and optionally error message) of a webhook event.
	// If status is PROCESSED, processed_at is also set to NOW().
	UpdateStatus(ctx context.Context, id uuid.UUID, status model.WebhookEventStatus, errorMessage *string) error
}

// AtomicWebhookEventProcessor applies webhook idempotency, transaction state,
// and event status in one database transaction. When outbox is non-nil and the
// status transition succeeds, outbox is invoked inside the same DB transaction.
type AtomicWebhookEventProcessor interface {
	ProcessAtomically(
		ctx context.Context,
		event *model.WebhookEvent,
		targetStatus, from model.TransactionStatus,
		amount int64,
		currency string,
		outbox func(ctx context.Context, dbTx pgx.Tx, payment *model.Transaction) error,
	) (model.WebhookEventStatus, error)
}

// pgWebhookEventRepository is the PostgreSQL implementation.
type pgWebhookEventRepository struct {
	db *pgxpool.Pool
}

// NewWebhookEventRepository returns a PostgreSQL-backed WebhookEventRepository.
func NewWebhookEventRepository(db *pgxpool.Pool) WebhookEventRepository {
	return &pgWebhookEventRepository{db: db}
}

// Create inserts a new webhook_event row.
// On UNIQUE(provider, event_id) violation it returns ErrWebhookEventDuplicate.
func (r *pgWebhookEventRepository) Create(ctx context.Context, event *model.WebhookEvent) error {
	const q = `
		INSERT INTO webhook_events
		    (id, provider, event_id, event_type, transaction_id,
		     provider_transaction_id, payload, signature, status,
		     error_message, processed_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`
	_, err := r.db.Exec(ctx, q,
		event.ID,
		event.Provider,
		event.EventID,
		event.EventType,
		event.TransactionID,
		event.ProviderTransactionID,
		event.Payload,
		event.Signature,
		event.Status,
		event.ErrorMessage,
		event.ProcessedAt,
		event.CreatedAt,
		event.UpdatedAt,
	)
	if err != nil {
		// pgx v5: check for unique violation (23505)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrWebhookEventDuplicate
		}
		return fmt.Errorf("webhook event repository create: %w", err)
	}
	return nil
}

// FindByProviderAndEventID looks up an event by provider + event_id.
func (r *pgWebhookEventRepository) FindByProviderAndEventID(ctx context.Context, provider, eventID string) (*model.WebhookEvent, error) {
	const q = `
		SELECT id, provider, event_id, event_type, transaction_id,
		       provider_transaction_id, payload, signature, status,
		       error_message, processed_at, created_at, updated_at
		FROM   webhook_events
		WHERE  provider  = $1
		AND    event_id  = $2
	`
	ev, err := scanWebhookEvent(r.db.QueryRow(ctx, q, provider, eventID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWebhookEventNotFound
		}
		return nil, fmt.Errorf("webhook event repository find by provider and event id: %w", err)
	}
	return ev, nil
}

// UpdateStatus updates the processing status of a webhook event.
// When status == PROCESSED, processed_at is set to NOW().
func (r *pgWebhookEventRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status model.WebhookEventStatus, errorMessage *string) error {
	const q = `
		UPDATE webhook_events
		SET    status        = $1,
		       error_message = $2,
		       processed_at  = CASE WHEN $1 = 'PROCESSED' THEN $3 ELSE processed_at END,
		       updated_at    = NOW()
		WHERE  id = $4
	`
	now := time.Now().UTC()
	tag, err := r.db.Exec(ctx, q, status, errorMessage, now, id)
	if err != nil {
		return fmt.Errorf("webhook event repository update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrWebhookEventNotFound
	}
	return nil
}

// ProcessAtomically inserts the event, conditionally transitions its payment,
// and records the final event status in one transaction. A conflicting event
// is an idempotent duplicate and does not touch the transaction.
// When the transition succeeds and outbox is non-nil, outbox runs in the same TX.
func (r *pgWebhookEventRepository) ProcessAtomically(
	ctx context.Context,
	event *model.WebhookEvent,
	targetStatus, from model.TransactionStatus,
	amount int64,
	currency string,
	outbox func(ctx context.Context, dbTx pgx.Tx, payment *model.Transaction) error,
) (model.WebhookEventStatus, error) {
	dbTx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("webhook event repository begin transaction: %w", err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()

	const insertEvent = `
		INSERT INTO webhook_events
		    (id, provider, event_id, event_type, transaction_id,
		     provider_transaction_id, payload, signature, status,
		     error_message, processed_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (provider, event_id) DO NOTHING
		RETURNING id`
	var insertedID uuid.UUID
	insertErr := dbTx.QueryRow(ctx, insertEvent,
		event.ID, event.Provider, event.EventID, event.EventType,
		event.TransactionID, event.ProviderTransactionID, event.Payload,
		event.Signature, event.Status, event.ErrorMessage, event.ProcessedAt,
		event.CreatedAt, event.UpdatedAt,
	).Scan(&insertedID)
	if errors.Is(insertErr, pgx.ErrNoRows) {
		if err := dbTx.Commit(ctx); err != nil {
			return "", fmt.Errorf("webhook event repository commit duplicate: %w", err)
		}
		return model.WebhookEventStatusIgnored, nil
	}
	if insertErr != nil {
		return "", fmt.Errorf("webhook event repository insert: %w", insertErr)
	}

	findTransaction := fmt.Sprintf(`
		SELECT %s
		FROM transactions
		WHERE provider = $1 AND provider_transaction_id = $2
		FOR UPDATE`, TransactionSelectCols)
	payment, err := scanTransaction(dbTx.QueryRow(ctx, findTransaction, event.Provider, *event.ProviderTransactionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			errMsg := fmt.Sprintf("transaction not found for provider_tx_id=%s", *event.ProviderTransactionID)
			if updateErr := updateWebhookEventTx(ctx, dbTx, insertedID, nil, model.WebhookEventStatusFailed, &errMsg); updateErr != nil {
				return "", updateErr
			}
			if commitErr := dbTx.Commit(ctx); commitErr != nil {
				return "", fmt.Errorf("webhook event repository commit missing transaction: %w", commitErr)
			}
			return model.WebhookEventStatusFailed, ErrTransactionNotFound
		}
		return "", fmt.Errorf("webhook event repository find transaction: %w", err)
	}
	if (amount > 0 && amount != payment.Amount) || (currency != "" && currency != payment.Currency) {
		errMsg := "webhook amount or currency does not match transaction"
		if err := updateWebhookEventTx(ctx, dbTx, insertedID, &payment.ID, model.WebhookEventStatusIgnored, &errMsg); err != nil {
			return "", err
		}
		if err := dbTx.Commit(ctx); err != nil {
			return "", fmt.Errorf("webhook event repository commit mismatch: %w", err)
		}
		return model.WebhookEventStatusIgnored, nil
	}

	finalStatus := model.WebhookEventStatusProcessed
	transitioned := false
	if targetStatus == "" {
		finalStatus = model.WebhookEventStatusIgnored
	} else {
		updateTransaction := fmt.Sprintf(`
			UPDATE transactions
			SET status = $1,
			    paid_at = CASE WHEN $4 AND paid_at IS NULL THEN NOW() ELSE paid_at END,
			    updated_at = NOW()
			WHERE id = $2 AND status = $3
			RETURNING %s`, TransactionSelectCols)
		setPaidAt := targetStatus == model.TransactionStatusPaid
		updated, updateErr := scanTransaction(dbTx.QueryRow(ctx, updateTransaction, targetStatus, payment.ID, from, setPaidAt))
		if updateErr != nil {
			if errors.Is(updateErr, pgx.ErrNoRows) {
				finalStatus = model.WebhookEventStatusIgnored
			} else {
				return "", fmt.Errorf("webhook event repository update transaction: %w", updateErr)
			}
		} else {
			payment = updated
			transitioned = true
		}
	}

	if err := updateWebhookEventTx(ctx, dbTx, insertedID, &payment.ID, finalStatus, nil); err != nil {
		return "", err
	}

	if transitioned && outbox != nil {
		if err := outbox(ctx, dbTx, payment); err != nil {
			return "", fmt.Errorf("webhook event repository outbox: %w", err)
		}
	}

	if err := dbTx.Commit(ctx); err != nil {
		return "", fmt.Errorf("webhook event repository commit: %w", err)
	}
	return finalStatus, nil
}

func updateWebhookEventTx(ctx context.Context, dbTx pgx.Tx, id uuid.UUID, transactionID *uuid.UUID, status model.WebhookEventStatus, errorMessage *string) error {
	const q = `
		UPDATE webhook_events
		SET transaction_id = $1,
		    status = $2,
		    error_message = $3,
		    processed_at = CASE WHEN $5 THEN NOW() ELSE processed_at END,
		    updated_at = NOW()
		WHERE id = $4`
	setProcessedAt := status == model.WebhookEventStatusProcessed ||
		status == model.WebhookEventStatusFailed ||
		status == model.WebhookEventStatusIgnored
	if _, err := dbTx.Exec(ctx, q, transactionID, status, errorMessage, id, setProcessedAt); err != nil {
		return fmt.Errorf("webhook event repository update event: %w", err)
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func scanWebhookEvent(row pgx.Row) (*model.WebhookEvent, error) {
	ev := &model.WebhookEvent{}
	err := row.Scan(
		&ev.ID,
		&ev.Provider,
		&ev.EventID,
		&ev.EventType,
		&ev.TransactionID,
		&ev.ProviderTransactionID,
		&ev.Payload,
		&ev.Signature,
		&ev.Status,
		&ev.ErrorMessage,
		&ev.ProcessedAt,
		&ev.CreatedAt,
		&ev.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return ev, nil
}
