package model

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// SettlementStatus is the lifecycle of an imported settlement batch.
type SettlementStatus string

const (
	SettlementStatusImported    SettlementStatus = "IMPORTED"
	SettlementStatusReconciling SettlementStatus = "RECONCILING"
	SettlementStatusReconciled  SettlementStatus = "RECONCILED"
	SettlementStatusPartial     SettlementStatus = "PARTIAL"
	SettlementStatusMismatch    SettlementStatus = "MISMATCH"
	SettlementStatusFailed      SettlementStatus = "FAILED"
)

// SettlementItemType classifies a settlement line.
type SettlementItemType string

const (
	SettlementItemTypePayment    SettlementItemType = "PAYMENT"
	SettlementItemTypeRefund     SettlementItemType = "REFUND"
	SettlementItemTypeAdjustment SettlementItemType = "ADJUSTMENT"
)

// Settlement is a provider settlement batch (external accounting evidence).
// It does NOT replace transactions/refunds as financial truth.
type Settlement struct {
	ID             uuid.UUID        `db:"id" json:"id"`
	Provider       string           `db:"provider" json:"provider"`
	SettlementRef  string           `db:"settlement_ref" json:"settlement_ref"`
	SettlementDate time.Time        `db:"settlement_date" json:"settlement_date"`
	Currency       string           `db:"currency" json:"currency"`
	GrossAmount    int64            `db:"gross_amount" json:"gross_amount"`
	FeeAmount      int64            `db:"fee_amount" json:"fee_amount"`
	NetAmount      int64            `db:"net_amount" json:"net_amount"`
	Status         SettlementStatus `db:"status" json:"status"`
	Source         string           `db:"source" json:"source"`
	PayloadHash    string           `db:"payload_hash" json:"payload_hash"`
	RawPayload     []byte           `db:"raw_payload" json:"-"` // never expose by default
	ItemCount      int              `db:"item_count" json:"item_count"`
	MatchedCount   int              `db:"matched_count" json:"matched_count"`
	MismatchCount  int              `db:"mismatch_count" json:"mismatch_count"`
	UnmatchedCount int              `db:"unmatched_count" json:"unmatched_count"`
	ImportedAt     time.Time        `db:"imported_at" json:"imported_at"`
	ReconciledAt   *time.Time       `db:"reconciled_at" json:"reconciled_at,omitempty"`
	CreatedAt      time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at" json:"updated_at"`
}

// SettlementItem is one line within a settlement batch.
type SettlementItem struct {
	ID                    uuid.UUID          `db:"id" json:"id"`
	SettlementID          uuid.UUID          `db:"settlement_id" json:"settlement_id"`
	Provider              string             `db:"provider" json:"provider"`
	ItemRef               string             `db:"item_ref" json:"item_ref"`
	ItemType              SettlementItemType `db:"item_type" json:"item_type"`
	ProviderTransactionID *string            `db:"provider_transaction_id" json:"provider_transaction_id,omitempty"`
	ProviderRefundID      *string            `db:"provider_refund_id" json:"provider_refund_id,omitempty"`
	OrderID               *string            `db:"order_id" json:"order_id,omitempty"`
	Currency              string             `db:"currency" json:"currency"`
	GrossAmount           int64              `db:"gross_amount" json:"gross_amount"`
	FeeAmount             int64              `db:"fee_amount" json:"fee_amount"`
	NetAmount             int64              `db:"net_amount" json:"net_amount"`
	SettledAt             *time.Time         `db:"settled_at" json:"settled_at,omitempty"`
	RawPayload            []byte             `db:"raw_payload" json:"-"`
	CreatedAt             time.Time          `db:"created_at" json:"created_at"`
}

// SettlementListFilter for admin listing.
type SettlementListFilter struct {
	Page          int
	Limit         int
	Provider      *string
	Status        *SettlementStatus
	Currency      *string
	SettlementRef *string
	DateFrom      *time.Time
	DateTo        *time.Time
}

// ImportSettlementRequest is the admin import body.
type ImportSettlementRequest struct {
	Provider      string          `json:"provider" binding:"required"`
	SettlementRef string          `json:"settlement_ref" binding:"required,min=1,max=150"`
	Payload       json.RawMessage `json:"payload" binding:"required"`
}

// SettlementDetailResponse includes items for get-by-id.
type SettlementDetailResponse struct {
	Settlement
	Items []*SettlementItem `json:"items,omitempty"`
}

// SettlementImportPayload is the parsed/normalized import from a SettlementImporter.
type SettlementImportPayload struct {
	Provider       string
	SettlementRef  string
	SettlementDate time.Time
	Currency       string
	GrossAmount    int64
	FeeAmount      int64
	NetAmount      int64
	Source         string
	Items          []SettlementImportItem
	RawPayload     []byte
	PayloadHash    string
}

// SettlementImportItem is one normalized line from an importer.
type SettlementImportItem struct {
	ItemRef               string
	ItemType              SettlementItemType
	ProviderTransactionID *string
	ProviderRefundID      *string
	OrderID               *string
	Currency              string
	GrossAmount           int64
	FeeAmount             int64
	NetAmount             int64
	SettledAt             *time.Time
	RawPayload            []byte
}

// CanonicalSettlementPayloadHash builds a deterministic SHA-256 over a
// canonicalized JSON object (sorted keys) so field ordering cannot affect the hash.
func CanonicalSettlementPayloadHash(payload json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return "", fmt.Errorf("invalid settlement payload JSON: %w", err)
	}
	canonical, err := canonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", sum), nil
}

func canonicalJSON(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf := []byte("{")
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kb, _ := json.Marshal(k)
			vb, err := canonicalJSON(t[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, kb...)
			buf = append(buf, ':')
			buf = append(buf, vb...)
		}
		buf = append(buf, '}')
		return buf, nil
	case []any:
		buf := []byte("[")
		for i, el := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			eb, err := canonicalJSON(el)
			if err != nil {
				return nil, err
			}
			buf = append(buf, eb...)
		}
		buf = append(buf, ']')
		return buf, nil
	default:
		return json.Marshal(t)
	}
}

// DeterministicItemRef builds a stable item identity when the provider does not supply one.
func DeterministicItemRef(itemType SettlementItemType, providerTxID, providerRefundID, orderID string, amount int64, currency string) string {
	canonical := fmt.Sprintf(
		"type=%s|ptx=%d:%s|prx=%d:%s|oid=%d:%s|amount=%d|currency=%d:%s",
		itemType,
		len(providerTxID), providerTxID,
		len(providerRefundID), providerRefundID,
		len(orderID), orderID,
		amount,
		len(currency), currency,
	)
	sum := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("fp_%x", sum[:16])
}
