package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// PaymentAttempt records a single communication attempt with a payment provider.
// It maps 1-to-1 to the `payment_attempts` database table.
type PaymentAttempt struct {
	ID                    uuid.UUID       `db:"id"`
	TransactionID         uuid.UUID       `db:"transaction_id"`
	Provider              string          `db:"provider"`
	ProviderTransactionID *string         `db:"provider_transaction_id"`
	RequestPayload        json.RawMessage `db:"request_payload"`
	ResponsePayload       json.RawMessage `db:"response_payload"`
	Status                string          `db:"status"`
	AttemptNumber         int             `db:"attempt_number"`
	CreatedAt             time.Time       `db:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at"`
}

// ─── Context key ─────────────────────────────────────────────────────────────

// ContextKeyMerchant is the Gin context key under which the authenticated
// Merchant is stored by the auth middleware.
const ContextKeyMerchant = "merchant"
