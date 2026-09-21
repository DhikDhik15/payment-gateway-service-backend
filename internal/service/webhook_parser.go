package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
)

// ─── Webhook parser errors ────────────────────────────────────────────────────

// ErrWebhookInvalidSignature is returned when HMAC verification fails.
var ErrWebhookInvalidSignature = errors.New("webhook signature invalid")

// ErrWebhookMalformedPayload is returned when the JSON payload cannot be parsed.
var ErrWebhookMalformedPayload = errors.New("webhook payload malformed")

// ErrWebhookMissingFields is returned when required event fields are absent.
var ErrWebhookMissingFields = errors.New("webhook payload missing required fields")

// ─── Generic event type ───────────────────────────────────────────────────────

// ParsedWebhookEvent is the provider-agnostic representation of an inbound event
// after parsing and signature verification. The WebhookService works exclusively
// with this type, keeping it decoupled from provider-specific payload formats.
type ParsedWebhookEvent struct {
	// EventID is the provider's own unique identifier for this event.
	EventID string

	// EventType is a normalised event type string (e.g. "PAYMENT_PAID").
	EventType string

	// ProviderTransactionID is the provider's own transaction reference.
	// Used to match payment events to a local transaction row.
	ProviderTransactionID string

	// ProviderRefundID is the provider's own refund reference.
	// Populated for REFUND_* events; empty for payment events.
	ProviderRefundID string

	// MerchantOrderID is the merchant's order reference from the event payload.
	// Informational only — transaction matching uses ProviderTransactionID.
	MerchantOrderID string

	// Status is the payment status reported by the provider.
	Status string

	// Amount in the smallest currency unit.
	Amount int64

	// Currency is the ISO 4217 currency code.
	Currency string
}

// ─── Interface ────────────────────────────────────────────────────────────────

// PaymentWebhookParser abstracts the provider-specific logic for verifying and
// parsing inbound webhook payloads. One implementation exists per provider.
//
// Adding a new provider requires implementing this interface and registering
// it in the WebhookService parser registry — no other changes needed.
type PaymentWebhookParser interface {
	// ProviderName returns the canonical provider identifier (e.g. "MOCK").
	// Must match the :provider path parameter in the webhook URL.
	ProviderName() string

	// VerifySignature checks the cryptographic signature of the raw payload.
	// Uses constant-time comparison to prevent timing attacks.
	// Returns ErrWebhookInvalidSignature on mismatch.
	VerifySignature(payload []byte, signature string) error

	// ParseEvent extracts a ParsedWebhookEvent from the raw JSON payload.
	// Returns ErrWebhookMalformedPayload or ErrWebhookMissingFields on failure.
	ParseEvent(payload []byte) (*ParsedWebhookEvent, error)
}

// ─── Mock implementation ──────────────────────────────────────────────────────

// mockWebhookPayload is the expected JSON structure from the MOCK provider.
// It covers both payment events and refund events.
type mockWebhookPayload struct {
	EventID               string `json:"event_id"`
	EventType             string `json:"event_type"`
	ProviderTransactionID string `json:"provider_transaction_id"`
	ProviderRefundID      string `json:"provider_refund_id,omitempty"`
	MerchantOrderID       string `json:"merchant_order_id,omitempty"`
	Status                string `json:"status,omitempty"`
	Amount                int64  `json:"amount,omitempty"`
	Currency              string `json:"currency,omitempty"`
}

// MockWebhookParser implements PaymentWebhookParser for the MOCK provider.
// Signature = HMAC-SHA256(payload, secret), hex-encoded.
type MockWebhookParser struct {
	// secret is the HMAC key used to verify webhook signatures.
	// Injected at construction — never hardcoded.
	secret string
}

// NewMockWebhookParser constructs a MockWebhookParser with the given HMAC secret.
func NewMockWebhookParser(secret string) *MockWebhookParser {
	return &MockWebhookParser{secret: secret}
}

// ProviderName returns "MOCK".
func (p *MockWebhookParser) ProviderName() string {
	return strings.ToUpper("MOCK")
}

// VerifySignature computes HMAC-SHA256(payload, secret) and compares it with the
// provided signature using hmac.Equal (constant-time) to resist timing attacks.
func (p *MockWebhookParser) VerifySignature(payload []byte, signature string) error {
	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	// Decode provided signature — accept it as raw hex.
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return ErrWebhookInvalidSignature
	}
	expectedBytes, _ := hex.DecodeString(expected)

	if !hmac.Equal(sigBytes, expectedBytes) {
		return ErrWebhookInvalidSignature
	}
	return nil
}

// ParseEvent deserialises the MOCK provider JSON payload into a ParsedWebhookEvent.
// Handles both payment events (PAYMENT_PAID, PAYMENT_FAILED, PAYMENT_EXPIRED) and
// refund events (REFUND_SUCCEEDED, REFUND_FAILED).
//
// For payment events: provider_transaction_id is required.
// For refund events: both provider_transaction_id and provider_refund_id are required.
func (p *MockWebhookParser) ParseEvent(payload []byte) (*ParsedWebhookEvent, error) {
	var raw mockWebhookPayload
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWebhookMalformedPayload, err.Error())
	}

	// Validate always-required fields.
	if raw.EventID == "" || raw.EventType == "" {
		return nil, ErrWebhookMissingFields
	}

	// For refund events provider_refund_id is required; provider_transaction_id optional.
	isRefundEvent := raw.EventType == string(model.WebhookEventTypeRefundSucceeded) ||
		raw.EventType == string(model.WebhookEventTypeRefundFailed)

	if isRefundEvent {
		if raw.ProviderRefundID == "" {
			return nil, ErrWebhookMissingFields
		}
	} else {
		// Payment events require provider_transaction_id.
		if raw.ProviderTransactionID == "" {
			return nil, ErrWebhookMissingFields
		}
	}

	return &ParsedWebhookEvent{
		EventID:               raw.EventID,
		EventType:             raw.EventType,
		ProviderTransactionID: raw.ProviderTransactionID,
		ProviderRefundID:      raw.ProviderRefundID,
		MerchantOrderID:       raw.MerchantOrderID,
		Status:                raw.Status,
		Amount:                raw.Amount,
		Currency:              raw.Currency,
	}, nil
}

// ─── Signing helper (for tests and manual verification) ───────────────────────

// SignMockWebhookPayload generates a valid HMAC-SHA256 signature for the given
// payload bytes using the provided secret. Used in tests and manual curl scripts.
func SignMockWebhookPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
