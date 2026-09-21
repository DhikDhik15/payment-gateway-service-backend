package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// WebhookEventStatus represents the processing state of an inbound webhook.
type WebhookEventStatus string

const (
	// WebhookEventStatusReceived means the event was persisted but not yet processed.
	WebhookEventStatusReceived WebhookEventStatus = "RECEIVED"

	// WebhookEventStatusProcessed means the event successfully caused a state transition.
	WebhookEventStatusProcessed WebhookEventStatus = "PROCESSED"

	// WebhookEventStatusFailed means processing failed and requires investigation.
	WebhookEventStatusFailed WebhookEventStatus = "FAILED"

	// WebhookEventStatusIgnored means the event was valid but no transition was needed
	// (e.g. duplicate PAID event when transaction is already PAID).
	WebhookEventStatusIgnored WebhookEventStatus = "IGNORED"
)

// WebhookEventType represents the type of event sent by a payment provider.
type WebhookEventType string

const (
	WebhookEventTypePaymentPaid    WebhookEventType = "PAYMENT_PAID"
	WebhookEventTypePaymentFailed  WebhookEventType = "PAYMENT_FAILED"
	WebhookEventTypePaymentExpired WebhookEventType = "PAYMENT_EXPIRED"

	// Refund events (Phase 7B).
	WebhookEventTypeRefundSucceeded WebhookEventType = "REFUND_SUCCEEDED"
	WebhookEventTypeRefundFailed    WebhookEventType = "REFUND_FAILED"
)

// WebhookEvent maps 1-to-1 to the `webhook_events` database table.
// It records every inbound event from payment providers for audit and
// idempotency purposes.
//
// webhook_events and payment_attempts are intentionally separate:
//   - payment_attempts: records outbound API calls made TO the provider
//   - webhook_events:   records inbound events received FROM the provider
type WebhookEvent struct {
	ID                    uuid.UUID          `db:"id"`
	Provider              string             `db:"provider"`
	EventID               string             `db:"event_id"`
	EventType             string             `db:"event_type"`
	TransactionID         *uuid.UUID         `db:"transaction_id"`
	ProviderTransactionID *string            `db:"provider_transaction_id"`
	Payload               json.RawMessage    `db:"payload"`
	Signature             *string            `db:"signature"`
	Status                WebhookEventStatus `db:"status"`
	ErrorMessage          *string            `db:"error_message"`
	ProcessedAt           *time.Time         `db:"processed_at"`
	CreatedAt             time.Time          `db:"created_at"`
	UpdatedAt             time.Time          `db:"updated_at"`
}
