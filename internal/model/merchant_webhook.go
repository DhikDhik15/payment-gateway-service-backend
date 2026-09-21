package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ─── Config status ────────────────────────────────────────────────────────────

// MerchantWebhookConfigStatus is the enable/disable state of a merchant webhook endpoint.
type MerchantWebhookConfigStatus string

const (
	MerchantWebhookConfigStatusActive   MerchantWebhookConfigStatus = "ACTIVE"
	MerchantWebhookConfigStatusDisabled MerchantWebhookConfigStatus = "DISABLED"
)

// ─── Delivery status ──────────────────────────────────────────────────────────

// MerchantWebhookDeliveryStatus is the outbox delivery state machine.
//
//	PENDING → PROCESSING → DELIVERED
//	PROCESSING → PENDING   (retryable failure)
//	PROCESSING → FAILED    (non-retryable 4xx)
//	PROCESSING → DEAD      (max attempts exhausted)
type MerchantWebhookDeliveryStatus string

const (
	MerchantWebhookDeliveryStatusPending    MerchantWebhookDeliveryStatus = "PENDING"
	MerchantWebhookDeliveryStatusProcessing MerchantWebhookDeliveryStatus = "PROCESSING"
	MerchantWebhookDeliveryStatusDelivered  MerchantWebhookDeliveryStatus = "DELIVERED"
	MerchantWebhookDeliveryStatusFailed     MerchantWebhookDeliveryStatus = "FAILED"
	MerchantWebhookDeliveryStatusDead       MerchantWebhookDeliveryStatus = "DEAD"
)

// ─── Event types ──────────────────────────────────────────────────────────────

// MerchantWebhookEventType is a canonical outbound payment lifecycle event.
type MerchantWebhookEventType string

const (
	MerchantWebhookEventPaymentCreated   MerchantWebhookEventType = "payment.created"
	MerchantWebhookEventPaymentPending   MerchantWebhookEventType = "payment.pending"
	MerchantWebhookEventPaymentPaid      MerchantWebhookEventType = "payment.paid"
	MerchantWebhookEventPaymentFailed    MerchantWebhookEventType = "payment.failed"
	MerchantWebhookEventPaymentExpired   MerchantWebhookEventType = "payment.expired"
	MerchantWebhookEventPaymentCancelled MerchantWebhookEventType = "payment.cancelled"

	MerchantWebhookEventRefundCreated    MerchantWebhookEventType = "refund.created"
	MerchantWebhookEventRefundProcessing MerchantWebhookEventType = "refund.processing"
	MerchantWebhookEventRefundSucceeded  MerchantWebhookEventType = "refund.succeeded"
	MerchantWebhookEventRefundFailed     MerchantWebhookEventType = "refund.failed"
)

// EventTypeForStatus maps a transaction status to its outbound webhook event type.
func EventTypeForStatus(status TransactionStatus) (MerchantWebhookEventType, bool) {
	switch status {
	case TransactionStatusCreated:
		return MerchantWebhookEventPaymentCreated, true
	case TransactionStatusPending:
		return MerchantWebhookEventPaymentPending, true
	case TransactionStatusPaid:
		return MerchantWebhookEventPaymentPaid, true
	case TransactionStatusFailed:
		return MerchantWebhookEventPaymentFailed, true
	case TransactionStatusExpired:
		return MerchantWebhookEventPaymentExpired, true
	case TransactionStatusCancelled:
		return MerchantWebhookEventPaymentCancelled, true
	default:
		return "", false
	}
}

// ─── Domain entities ──────────────────────────────────────────────────────────

// MerchantWebhookConfig maps to merchant_webhook_configs.
// EncryptedSecret must never be exposed via JSON APIs.
type MerchantWebhookConfig struct {
	ID              uuid.UUID                   `db:"id"`
	MerchantID      uuid.UUID                   `db:"merchant_id"`
	URL             string                      `db:"url"`
	EncryptedSecret string                      `db:"encrypted_secret" json:"-"`
	Status          MerchantWebhookConfigStatus `db:"status"`
	Description     *string                     `db:"description"`
	CreatedAt       time.Time                   `db:"created_at"`
	UpdatedAt       time.Time                   `db:"updated_at"`
}

// MerchantWebhookDelivery maps to merchant_webhook_deliveries (transactional outbox).
type MerchantWebhookDelivery struct {
	ID             uuid.UUID                     `db:"id"`
	MerchantID     uuid.UUID                     `db:"merchant_id"`
	ConfigID       *uuid.UUID                    `db:"config_id"`
	EventID        string                        `db:"event_id"`
	EventType      MerchantWebhookEventType      `db:"event_type"`
	TransactionID  uuid.UUID                     `db:"transaction_id"`
	EndpointURL    string                        `db:"endpoint_url"`
	Payload        json.RawMessage               `db:"payload"`
	AttemptCount   int                           `db:"attempt_count"`
	Status         MerchantWebhookDeliveryStatus `db:"status"`
	NextAttemptAt  time.Time                     `db:"next_attempt_at"`
	LastAttemptAt  *time.Time                    `db:"last_attempt_at"`
	ProcessingAt   *time.Time                    `db:"processing_at"`
	DeliveredAt    *time.Time                    `db:"delivered_at"`
	LastHTTPStatus *int                          `db:"last_http_status"`
	LastError      *string                       `db:"last_error"`
	CreatedAt      time.Time                     `db:"created_at"`
	UpdatedAt      time.Time                     `db:"updated_at"`
}

// ─── Outbound payload (versioned) ─────────────────────────────────────────────

const MerchantWebhookPayloadVersion = "1"

// MerchantWebhookEventPayload is the stable JSON body POSTed to merchants.
type MerchantWebhookEventPayload struct {
	ID        string                     `json:"id"`
	Type      MerchantWebhookEventType   `json:"type"`
	Version   string                     `json:"version"`
	CreatedAt time.Time                  `json:"created_at"`
	Data      MerchantWebhookPaymentData `json:"data"`
}

// MerchantWebhookPaymentData mirrors the safe PaymentResponse fields.
type MerchantWebhookPaymentData struct {
	TransactionID         uuid.UUID         `json:"transaction_id"`
	MerchantOrderID       string            `json:"merchant_order_id"`
	Amount                int64             `json:"amount"`
	Currency              string            `json:"currency"`
	PaymentMethod         string            `json:"payment_method"`
	Status                TransactionStatus `json:"status"`
	PaymentURL            *string           `json:"payment_url,omitempty"`
	ProviderTransactionID *string           `json:"provider_transaction_id,omitempty"`
	ExpiredAt             *time.Time        `json:"expired_at,omitempty"`
	PaidAt                *time.Time        `json:"paid_at,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

// ─── API DTOs ─────────────────────────────────────────────────────────────────

// UpsertMerchantWebhookRequest is the body for POST .../webhook.
type UpsertMerchantWebhookRequest struct {
	URL         string  `json:"url" binding:"required,url,max=2048"`
	Description *string `json:"description,omitempty" binding:"omitempty,max=100"`
}

// MerchantWebhookConfigResponse is returned by GET (never includes secret).
type MerchantWebhookConfigResponse struct {
	ID          uuid.UUID                   `json:"id"`
	MerchantID  uuid.UUID                   `json:"merchant_id"`
	URL         string                      `json:"url"`
	Status      MerchantWebhookConfigStatus `json:"status"`
	Description *string                     `json:"description,omitempty"`
	CreatedAt   time.Time                   `json:"created_at"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

// MerchantWebhookConfigWithSecretResponse is returned once on create/rotate.
type MerchantWebhookConfigWithSecretResponse struct {
	ID          uuid.UUID                   `json:"id"`
	MerchantID  uuid.UUID                   `json:"merchant_id"`
	URL         string                      `json:"url"`
	Status      MerchantWebhookConfigStatus `json:"status"`
	Description *string                     `json:"description,omitempty"`
	Secret      string                      `json:"secret"`
	CreatedAt   time.Time                   `json:"created_at"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

// MerchantWebhookDeliveryResponse is a safe delivery audit DTO (no secrets).
type MerchantWebhookDeliveryResponse struct {
	ID             uuid.UUID                     `json:"id"`
	MerchantID     uuid.UUID                     `json:"merchant_id"`
	EventID        string                        `json:"event_id"`
	EventType      MerchantWebhookEventType      `json:"event_type"`
	TransactionID  uuid.UUID                     `json:"transaction_id"`
	EndpointURL    string                        `json:"endpoint_url"`
	AttemptCount   int                           `json:"attempt_count"`
	Status         MerchantWebhookDeliveryStatus `json:"status"`
	NextAttemptAt  time.Time                     `json:"next_attempt_at"`
	LastAttemptAt  *time.Time                    `json:"last_attempt_at,omitempty"`
	DeliveredAt    *time.Time                    `json:"delivered_at,omitempty"`
	LastHTTPStatus *int                          `json:"last_http_status,omitempty"`
	LastError      *string                       `json:"last_error,omitempty"`
	CreatedAt      time.Time                     `json:"created_at"`
	UpdatedAt      time.Time                     `json:"updated_at"`
}

// ToConfigResponse maps a config entity to the public DTO (no secret).
func (c *MerchantWebhookConfig) ToConfigResponse() *MerchantWebhookConfigResponse {
	return &MerchantWebhookConfigResponse{
		ID:          c.ID,
		MerchantID:  c.MerchantID,
		URL:         c.URL,
		Status:      c.Status,
		Description: c.Description,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

// ToDeliveryResponse maps a delivery entity to the public DTO.
func (d *MerchantWebhookDelivery) ToDeliveryResponse() *MerchantWebhookDeliveryResponse {
	return &MerchantWebhookDeliveryResponse{
		ID:             d.ID,
		MerchantID:     d.MerchantID,
		EventID:        d.EventID,
		EventType:      d.EventType,
		TransactionID:  d.TransactionID,
		EndpointURL:    d.EndpointURL,
		AttemptCount:   d.AttemptCount,
		Status:         d.Status,
		NextAttemptAt:  d.NextAttemptAt,
		LastAttemptAt:  d.LastAttemptAt,
		DeliveredAt:    d.DeliveredAt,
		LastHTTPStatus: d.LastHTTPStatus,
		LastError:      d.LastError,
		CreatedAt:      d.CreatedAt,
		UpdatedAt:      d.UpdatedAt,
	}
}

// MerchantWebhookRefundData is the refund section of outbound webhook payloads.
type MerchantWebhookRefundData struct {
	RefundID          uuid.UUID    `json:"refund_id"`
	TransactionID     uuid.UUID    `json:"transaction_id"`
	Amount            int64        `json:"amount"`
	Currency          string       `json:"currency"`
	Status            RefundStatus `json:"status"`
	Provider          string       `json:"provider"`
	ProviderRefundID  *string      `json:"provider_refund_id,omitempty"`
	Reason            *string      `json:"reason,omitempty"`
	TransactionAmount int64        `json:"transaction_amount"`
	RefundedAmount    int64        `json:"refunded_amount"`
	ReservedAmount    int64        `json:"reserved_refund_amount"`
	RefundableAmount  int64        `json:"refundable_amount"`
	RequestedAt       time.Time    `json:"requested_at"`
	SucceededAt       *time.Time   `json:"succeeded_at,omitempty"`
	FailedAt          *time.Time   `json:"failed_at,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

// RefundDataFromRefund builds refund webhook payload data.
func RefundDataFromRefund(r *Refund, tx *Transaction) MerchantWebhookRefundData {
	refundable := tx.Amount - tx.RefundedAmount - tx.ReservedRefundAmount
	if refundable < 0 {
		refundable = 0
	}
	return MerchantWebhookRefundData{
		RefundID:          r.ID,
		TransactionID:     r.TransactionID,
		Amount:            r.Amount,
		Currency:          r.Currency,
		Status:            r.Status,
		Provider:          r.Provider,
		ProviderRefundID:  r.ProviderRefundID,
		Reason:            r.Reason,
		TransactionAmount: tx.Amount,
		RefundedAmount:    tx.RefundedAmount,
		ReservedAmount:    tx.ReservedRefundAmount,
		RefundableAmount:  refundable,
		RequestedAt:       r.RequestedAt,
		SucceededAt:       r.SucceededAt,
		FailedAt:          r.FailedAt,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
}

// PaymentDataFromTransaction builds the safe webhook data section.
func PaymentDataFromTransaction(tx *Transaction) MerchantWebhookPaymentData {
	return MerchantWebhookPaymentData{
		TransactionID:         tx.ID,
		MerchantOrderID:       tx.MerchantOrderID,
		Amount:                tx.Amount,
		Currency:              tx.Currency,
		PaymentMethod:         tx.PaymentMethod,
		Status:                tx.Status,
		PaymentURL:            tx.PaymentURL,
		ProviderTransactionID: tx.ProviderTransactionID,
		ExpiredAt:             tx.ExpiredAt,
		PaidAt:                tx.PaidAt,
		CreatedAt:             tx.CreatedAt,
		UpdatedAt:             tx.UpdatedAt,
	}
}
