package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AuditLog is the read model for an append-only security audit record.
// Metadata contains only the explicitly sanitized JSON object accepted by the
// audit service; it is never populated from request bodies or credential
// objects.
type AuditLog struct {
	ID          uuid.UUID       `db:"id"          json:"id"`
	MerchantID  *uuid.UUID      `db:"merchant_id"  json:"merchant_id,omitempty"`
	ActorUserID *uuid.UUID      `db:"actor_user_id" json:"actor_user_id,omitempty"`
	ActorType   string          `db:"actor_type"   json:"actor_type"`
	Action      string          `db:"action"      json:"action"`
	TargetType  *string         `db:"target_type"  json:"target_type,omitempty"`
	TargetID    *uuid.UUID      `db:"target_id"    json:"target_id,omitempty"`
	RequestID   *string         `db:"request_id"   json:"request_id,omitempty"`
	IP          *string         `db:"ip"           json:"ip,omitempty"`
	Metadata    json.RawMessage `db:"metadata"     json:"metadata"`
	CreatedAt   time.Time       `db:"created_at"   json:"created_at"`
}
