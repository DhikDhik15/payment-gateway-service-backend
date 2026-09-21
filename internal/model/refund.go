package model

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RefundStatus is the refund lifecycle state.
type RefundStatus string

const (
	RefundStatusPending    RefundStatus = "PENDING"
	RefundStatusProcessing RefundStatus = "PROCESSING"
	RefundStatusSucceeded  RefundStatus = "SUCCEEDED"
	RefundStatusFailed     RefundStatus = "FAILED"
)

// Refund maps to the refunds table.
type Refund struct {
	ID               uuid.UUID    `db:"id"`
	MerchantID       uuid.UUID    `db:"merchant_id"`
	TransactionID    uuid.UUID    `db:"transaction_id"`
	Amount           int64        `db:"amount"`
	Currency         string       `db:"currency"`
	Status           RefundStatus `db:"status"`
	Provider         string       `db:"provider"`
	ProviderRefundID *string      `db:"provider_refund_id"`
	Reason           *string      `db:"reason"`
	FailureCode      *string      `db:"failure_code"`
	FailureMessage   *string      `db:"failure_message"`
	IdempotencyKeyID *uuid.UUID   `db:"idempotency_key_id"`
	RequestedAt      time.Time    `db:"requested_at"`
	SucceededAt      *time.Time   `db:"succeeded_at"`
	FailedAt         *time.Time   `db:"failed_at"`
	CreatedAt        time.Time    `db:"created_at"`
	UpdatedAt        time.Time    `db:"updated_at"`
}

// RefundAttempt maps to refund_attempts.
type RefundAttempt struct {
	ID               uuid.UUID `db:"id"`
	RefundID         uuid.UUID `db:"refund_id"`
	Provider         string    `db:"provider"`
	ProviderRefundID *string   `db:"provider_refund_id"`
	RequestPayload   []byte    `db:"request_payload"`
	ResponsePayload  []byte    `db:"response_payload"`
	Status           string    `db:"status"`
	AttemptNumber    int       `db:"attempt_number"`
	CreatedAt        time.Time `db:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"`
}

// CreateRefundRequest is the HTTP body for POST .../payments/:id/refunds.
type CreateRefundRequest struct {
	Amount   int64   `json:"amount" binding:"required,min=1"`
	Currency string  `json:"currency" binding:"required,len=3"`
	Reason   *string `json:"reason,omitempty" binding:"omitempty,max=500"`
}

// RefundResponse is returned for refund APIs.
type RefundResponse struct {
	RefundID          uuid.UUID    `json:"refund_id"`
	TransactionID     uuid.UUID    `json:"transaction_id"`
	Amount            int64        `json:"amount"`
	Currency          string       `json:"currency"`
	Status            RefundStatus `json:"status"`
	Provider          string       `json:"provider"`
	ProviderRefundID  *string      `json:"provider_refund_id,omitempty"`
	Reason            *string      `json:"reason,omitempty"`
	RefundedAmount    int64        `json:"refunded_amount"`
	ReservedAmount    int64        `json:"reserved_refund_amount"`
	RefundableAmount  int64        `json:"refundable_amount"`
	TransactionAmount int64        `json:"transaction_amount"`
	RequestedAt       time.Time    `json:"requested_at"`
	SucceededAt       *time.Time   `json:"succeeded_at,omitempty"`
	FailedAt          *time.Time   `json:"failed_at,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

// RefundListFilter for listing refunds on a payment or merchant-wide (dashboard).
type RefundListFilter struct {
	Page        int
	Limit       int
	Status      *RefundStatus
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	// TransactionID optionally scopes the list to one payment (dashboard /payments/:id/refunds).
	TransactionID *uuid.UUID
}

// RefundRequestHash canonical hash for idempotency (transaction + business fields).
func RefundRequestHash(transactionID uuid.UUID, req CreateRefundRequest) string {
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	canonical := fmt.Sprintf(
		"transaction_id=%s|amount=%d|currency=%d:%s|reason=%d:%s",
		transactionID.String(), req.Amount, len(req.Currency), req.Currency, len(reason), reason,
	)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
}

// ToRefundResponse builds API DTO with transaction financial context.
func (r *Refund) ToRefundResponse(tx *Transaction) *RefundResponse {
	refundable := tx.Amount - tx.RefundedAmount - tx.ReservedRefundAmount
	if refundable < 0 {
		refundable = 0
	}
	return &RefundResponse{
		RefundID:          r.ID,
		TransactionID:     r.TransactionID,
		Amount:            r.Amount,
		Currency:          r.Currency,
		Status:            r.Status,
		Provider:          r.Provider,
		ProviderRefundID:  r.ProviderRefundID,
		Reason:            r.Reason,
		RefundedAmount:    tx.RefundedAmount,
		ReservedAmount:    tx.ReservedRefundAmount,
		RefundableAmount:  refundable,
		TransactionAmount: tx.Amount,
		RequestedAt:       r.RequestedAt,
		SucceededAt:       r.SucceededAt,
		FailedAt:          r.FailedAt,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
}
