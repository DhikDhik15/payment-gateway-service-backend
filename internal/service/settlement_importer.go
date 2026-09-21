package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
)

// SettlementImporter parses provider-specific settlement payloads into a
// normalized SettlementImportPayload. Provider credentials must never appear.
type SettlementImporter interface {
	ProviderName() string
	Parse(ctx context.Context, settlementRef string, payload []byte) (*model.SettlementImportPayload, error)
}

// MockSettlementPayload is the JSON shape accepted by MockSettlementImporter.
type MockSettlementPayload struct {
	SettlementDate string               `json:"settlement_date"` // YYYY-MM-DD
	Currency       string               `json:"currency"`
	GrossAmount    int64                `json:"gross_amount"`
	FeeAmount      int64                `json:"fee_amount"`
	NetAmount      int64                `json:"net_amount"`
	Items          []MockSettlementItem `json:"items"`
}

// MockSettlementItem is one mock settlement line.
type MockSettlementItem struct {
	ItemRef               string  `json:"item_ref,omitempty"`
	ItemType              string  `json:"item_type"` // PAYMENT | REFUND | ADJUSTMENT
	ProviderTransactionID string  `json:"provider_transaction_id,omitempty"`
	ProviderRefundID      string  `json:"provider_refund_id,omitempty"`
	OrderID               string  `json:"order_id,omitempty"`
	Currency              string  `json:"currency"`
	GrossAmount           int64   `json:"gross_amount"`
	FeeAmount             int64   `json:"fee_amount"`
	NetAmount             int64   `json:"net_amount"`
	SettledAt             *string `json:"settled_at,omitempty"` // RFC3339
}

// MockSettlementImporter parses the mock settlement JSON format.
type MockSettlementImporter struct{}

func NewMockSettlementImporter() *MockSettlementImporter {
	return &MockSettlementImporter{}
}

func (m *MockSettlementImporter) ProviderName() string {
	return "MOCK"
}

func (m *MockSettlementImporter) Parse(_ context.Context, settlementRef string, payload []byte) (*model.SettlementImportPayload, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrSettlementImportInvalid)
	}
	if len(payload) > maxSettlementPayloadBytes {
		return nil, fmt.Errorf("%w: payload exceeds %d bytes", ErrSettlementImportInvalid, maxSettlementPayloadBytes)
	}

	var raw MockSettlementPayload
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSettlementImportInvalid, err)
	}
	if strings.TrimSpace(raw.Currency) == "" {
		return nil, fmt.Errorf("%w: currency required", ErrSettlementImportInvalid)
	}
	if !model.SupportedCurrencies[raw.Currency] {
		return nil, fmt.Errorf("%w: unsupported currency", ErrSettlementImportInvalid)
	}
	if raw.GrossAmount < 0 || raw.FeeAmount < 0 || raw.NetAmount < 0 {
		return nil, fmt.Errorf("%w: amounts must be non-negative", ErrSettlementImportInvalid)
	}
	date, err := time.Parse("2006-01-02", raw.SettlementDate)
	if err != nil {
		return nil, fmt.Errorf("%w: settlement_date must be YYYY-MM-DD", ErrSettlementImportInvalid)
	}
	if len(raw.Items) == 0 {
		return nil, fmt.Errorf("%w: at least one item required", ErrSettlementImportInvalid)
	}

	hash, err := model.CanonicalSettlementPayloadHash(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSettlementImportInvalid, err)
	}

	out := &model.SettlementImportPayload{
		Provider:       m.ProviderName(),
		SettlementRef:  settlementRef,
		SettlementDate: date.UTC(),
		Currency:       raw.Currency,
		GrossAmount:    raw.GrossAmount,
		FeeAmount:      raw.FeeAmount,
		NetAmount:      raw.NetAmount,
		Source:         "API",
		RawPayload:     append([]byte(nil), payload...),
		PayloadHash:    hash,
		Items:          make([]model.SettlementImportItem, 0, len(raw.Items)),
	}

	seenRefs := make(map[string]struct{})
	for i, it := range raw.Items {
		itemType := model.SettlementItemType(strings.ToUpper(strings.TrimSpace(it.ItemType)))
		switch itemType {
		case model.SettlementItemTypePayment, model.SettlementItemTypeRefund, model.SettlementItemTypeAdjustment:
		default:
			return nil, fmt.Errorf("%w: item[%d] invalid item_type", ErrSettlementImportInvalid, i)
		}
		cur := it.Currency
		if cur == "" {
			cur = raw.Currency
		}
		if !model.SupportedCurrencies[cur] {
			return nil, fmt.Errorf("%w: item[%d] unsupported currency", ErrSettlementImportInvalid, i)
		}
		if it.GrossAmount < 0 || it.FeeAmount < 0 || it.NetAmount < 0 {
			return nil, fmt.Errorf("%w: item[%d] amounts must be non-negative", ErrSettlementImportInvalid, i)
		}
		if itemType == model.SettlementItemTypePayment && it.ProviderTransactionID == "" {
			return nil, fmt.Errorf("%w: item[%d] PAYMENT requires provider_transaction_id", ErrSettlementImportInvalid, i)
		}
		if itemType == model.SettlementItemTypeRefund && it.ProviderRefundID == "" {
			return nil, fmt.Errorf("%w: item[%d] REFUND requires provider_refund_id", ErrSettlementImportInvalid, i)
		}

		ref := strings.TrimSpace(it.ItemRef)
		if ref == "" {
			ref = model.DeterministicItemRef(itemType, it.ProviderTransactionID, it.ProviderRefundID, it.OrderID, it.GrossAmount, cur)
		}
		if _, dup := seenRefs[ref]; dup {
			return nil, fmt.Errorf("%w: duplicate item_ref %q", ErrSettlementImportInvalid, ref)
		}
		seenRefs[ref] = struct{}{}

		var settledAt *time.Time
		if it.SettledAt != nil && *it.SettledAt != "" {
			t, err := time.Parse(time.RFC3339, *it.SettledAt)
			if err != nil {
				return nil, fmt.Errorf("%w: item[%d] settled_at must be RFC3339", ErrSettlementImportInvalid, i)
			}
			utc := t.UTC()
			settledAt = &utc
		}

		item := model.SettlementImportItem{
			ItemRef:     ref,
			ItemType:    itemType,
			Currency:    cur,
			GrossAmount: it.GrossAmount,
			FeeAmount:   it.FeeAmount,
			NetAmount:   it.NetAmount,
			SettledAt:   settledAt,
		}
		if it.ProviderTransactionID != "" {
			v := it.ProviderTransactionID
			item.ProviderTransactionID = &v
		}
		if it.ProviderRefundID != "" {
			v := it.ProviderRefundID
			item.ProviderRefundID = &v
		}
		if it.OrderID != "" {
			v := it.OrderID
			item.OrderID = &v
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

const maxSettlementPayloadBytes = 1 << 20 // 1 MiB
