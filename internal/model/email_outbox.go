package model

import (
	"time"

	"github.com/google/uuid"
)

// ─── Outbox status ────────────────────────────────────────────────────────────

// EmailOutboxStatus is the outbox delivery state machine (Phase 8C.3A),
// mirroring MerchantWebhookDeliveryStatus:
//
//	PENDING → PROCESSING → SENT
//	PROCESSING → PENDING   (retryable failure — Phase 8C.3B)
//	PROCESSING → FAILED    (non-retryable failure — Phase 8C.3B)
//	PROCESSING → DEAD      (max attempts exhausted — Phase 8C.3B)
//
// Phase 8C.3A only ever writes PENDING; the worker transitions arrive in 8C.3B.
type EmailOutboxStatus string

const (
	EmailOutboxStatusPending    EmailOutboxStatus = "PENDING"
	EmailOutboxStatusProcessing EmailOutboxStatus = "PROCESSING"
	EmailOutboxStatusSent       EmailOutboxStatus = "SENT"
	EmailOutboxStatusFailed     EmailOutboxStatus = "FAILED"
	EmailOutboxStatusDead       EmailOutboxStatus = "DEAD"
)

// ─── Outbox types ─────────────────────────────────────────────────────────────

// EmailOutboxType is the canonical category of a queued email, mirroring
// MerchantWebhookEventType. Extend the CHECK constraint in a migration when
// adding a new type.
type EmailOutboxType string

const (
	EmailOutboxTypeInvitation EmailOutboxType = "INVITATION"
)

// ─── Domain entity ────────────────────────────────────────────────────────────

// EmailOutbox maps to email_outbox (transactional email outbox, Phase 8C.3A).
//
// Security: Recipient/TextBody/HTMLBody for an INVITATION row contain the
// plaintext invitation bearer token (the rendered email IS the payload —
// audited). token_hash must NEVER be written here, and no JSON API may expose
// this entity. Emails are rendered at enqueue time so retries send exactly
// what was committed (Phase 8C.3B).
type EmailOutbox struct {
	ID            uuid.UUID         `db:"id"`
	MerchantID    uuid.UUID         `db:"merchant_id"`
	ReferenceID   *uuid.UUID        `db:"reference_id"` // correlated row (invitation id); type-generic, no FK
	Type          EmailOutboxType   `db:"type"`
	Recipient     string            `db:"recipient"`
	Subject       string            `db:"subject"`
	TextBody      string            `db:"text_body"`
	HTMLBody      string            `db:"html_body"`
	Status        EmailOutboxStatus `db:"status"`
	AttemptCount  int               `db:"attempt_count"`
	NextAttemptAt time.Time         `db:"next_attempt_at"`
	ProcessingAt  *time.Time        `db:"processing_at"`
	LastAttemptAt *time.Time        `db:"last_attempt_at"`
	SentAt        *time.Time        `db:"sent_at"`
	LastError     *string           `db:"last_error"`
	CreatedAt     time.Time         `db:"created_at"`
	UpdatedAt     time.Time         `db:"updated_at"`
}
