package model

import (
	"time"

	"github.com/google/uuid"
)

// DashboardOverviewResponse is the GET /api/v1/dashboard/overview payload.
// All counters and amounts are derived from live DB aggregates — never hardcoded.
type DashboardOverviewResponse struct {
	Period         DashboardPeriod       `json:"period"`
	Payments       DashboardPaymentStats `json:"payments"`
	Revenue        DashboardRevenueStats `json:"revenue"`
	Refunds        DashboardRefundStats  `json:"refunds"`
	RecentPayments []PaymentResponse     `json:"recent_payments"`
}

// DashboardPeriod is the UTC reporting window used for overview aggregates.
// Interval is half-open: [From, To).
type DashboardPeriod struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// DashboardPaymentStats counts transactions by status within the period
// (matched on created_at).
type DashboardPaymentStats struct {
	Total     int64 `json:"total"`
	Created   int64 `json:"created"`
	Pending   int64 `json:"pending"`
	Paid      int64 `json:"paid"`
	Failed    int64 `json:"failed"`
	Expired   int64 `json:"expired"`
	Cancelled int64 `json:"cancelled"`
}

// DashboardRevenueStats is financial truth for the period:
// SUM(amount) WHERE status = PAID AND paid_at ∈ [from, to).
// Amount is integer minor units (same as transactions.amount).
type DashboardRevenueStats struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// DashboardRefundStats aggregates SUCCEEDED refunds in the period
// (matched on succeeded_at, falling back to created_at when succeeded_at is null).
type DashboardRefundStats struct {
	Count    int64  `json:"count"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// DashboardPaymentDetailResponse enriches PaymentResponse with a real-timestamp timeline.
type DashboardPaymentDetailResponse struct {
	PaymentResponse
	Timeline []DashboardTimelineEvent `json:"timeline"`
}

// DashboardTimelineEvent is derived only from persisted timestamps — never fabricated.
type DashboardTimelineEvent struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
}

// DashboardMerchantSettingsResponse is the safe merchant profile for the dashboard.
// Never includes API keys, secrets, or credentials.
type DashboardMerchantSettingsResponse struct {
	ID        uuid.UUID      `json:"id"`
	Name      string         `json:"name"`
	Code      string         `json:"code"`
	Status    MerchantStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	// LegacyCredentialState (Phase 8D.3) lets the dashboard prompt for
	// migration; it is a state label, not a credential.
	LegacyCredentialState LegacyCredentialState `json:"legacy_credential_state"`
	// LegacyCredentialDisabledAt is set once the legacy credential is disabled.
	LegacyCredentialDisabledAt *time.Time `json:"legacy_credential_disabled_at,omitempty"`
}
