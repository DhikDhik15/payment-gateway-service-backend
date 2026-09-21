package model

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type IdempotencyStatus string

const (
	IdempotencyStatusProcessing IdempotencyStatus = "PROCESSING"
	IdempotencyStatusCompleted  IdempotencyStatus = "COMPLETED"
	IdempotencyStatusFailed     IdempotencyStatus = "FAILED"
)

// IdempotencyKey is the persisted merchant-scoped payment-create reservation.
type IdempotencyKey struct {
	ID             uuid.UUID
	MerchantID     uuid.UUID
	Key            string
	RequestHash    string
	Status         IdempotencyStatus
	ResponseStatus *int
	ResponseBody   []byte
	TransactionID  *uuid.UUID
	RefundID       *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
}

// PaymentRequestHash uses explicit, length-delimited business fields, avoiding
// JSON ordering ambiguity and separator collisions.
func PaymentRequestHash(req CreatePaymentRequest) string {
	canonical := fmt.Sprintf("merchant_order_id=%d:%s|amount=%d|currency=%d:%s|payment_method=%d:%s", len(req.MerchantOrderID), req.MerchantOrderID, req.Amount, len(req.Currency), req.Currency, len(req.PaymentMethod), req.PaymentMethod)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
}
