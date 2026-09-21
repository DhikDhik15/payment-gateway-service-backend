package model

import (
	"time"

	"github.com/google/uuid"
)

// ReconciliationRunStatus tracks one reconciliation execution.
type ReconciliationRunStatus string

const (
	ReconciliationRunRunning   ReconciliationRunStatus = "RUNNING"
	ReconciliationRunCompleted ReconciliationRunStatus = "COMPLETED"
	ReconciliationRunFailed    ReconciliationRunStatus = "FAILED"
)

// ReconciliationResultStatus is MATCHED / MISMATCH / UNMATCHED.
type ReconciliationResultStatus string

const (
	ReconciliationResultMatched   ReconciliationResultStatus = "MATCHED"
	ReconciliationResultMismatch  ReconciliationResultStatus = "MISMATCH"
	ReconciliationResultUnmatched ReconciliationResultStatus = "UNMATCHED"
)

// ReconciliationResultType enumerates match/mismatch outcomes.
type ReconciliationResultType string

const (
	ReconResultPaymentMatch            ReconciliationResultType = "PAYMENT_MATCH"
	ReconResultRefundMatch             ReconciliationResultType = "REFUND_MATCH"
	ReconResultPaymentNotFound         ReconciliationResultType = "PAYMENT_NOT_FOUND"
	ReconResultRefundNotFound          ReconciliationResultType = "REFUND_NOT_FOUND"
	ReconResultPaymentAmountMismatch   ReconciliationResultType = "PAYMENT_AMOUNT_MISMATCH"
	ReconResultRefundAmountMismatch    ReconciliationResultType = "REFUND_AMOUNT_MISMATCH"
	ReconResultPaymentCurrencyMismatch ReconciliationResultType = "PAYMENT_CURRENCY_MISMATCH"
	ReconResultRefundCurrencyMismatch  ReconciliationResultType = "REFUND_CURRENCY_MISMATCH"
	ReconResultUnexpectedItem          ReconciliationResultType = "UNEXPECTED_SETTLEMENT_ITEM"
	ReconResultUnsupportedAdjustment   ReconciliationResultType = "UNSUPPORTED_ADJUSTMENT"
	ReconResultDuplicateItem           ReconciliationResultType = "DUPLICATE_SETTLEMENT_ITEM"
)

// ReconciliationRun is one audit execution of reconcile against a settlement.
type ReconciliationRun struct {
	ID                 uuid.UUID               `db:"id" json:"id"`
	SettlementID       uuid.UUID               `db:"settlement_id" json:"settlement_id"`
	Status             ReconciliationRunStatus `db:"status" json:"status"`
	TotalItems         int                     `db:"total_items" json:"total_items"`
	MatchedItems       int                     `db:"matched_items" json:"matched_items"`
	MismatchItems      int                     `db:"mismatch_items" json:"mismatch_items"`
	UnmatchedItems     int                     `db:"unmatched_items" json:"unmatched_items"`
	PaymentMatches     int                     `db:"payment_matches" json:"payment_matches"`
	RefundMatches      int                     `db:"refund_matches" json:"refund_matches"`
	AmountMismatches   int                     `db:"amount_mismatches" json:"amount_mismatches"`
	CurrencyMismatches int                     `db:"currency_mismatches" json:"currency_mismatches"`
	NotFoundCount      int                     `db:"not_found_count" json:"not_found_count"`
	UnsupportedCount   int                     `db:"unsupported_count" json:"unsupported_count"`
	ErrorMessage       *string                 `db:"error_message" json:"error_message,omitempty"`
	StartedAt          time.Time               `db:"started_at" json:"started_at"`
	FinishedAt         *time.Time              `db:"finished_at" json:"finished_at,omitempty"`
	CreatedAt          time.Time               `db:"created_at" json:"created_at"`
}

// ReconciliationResult is the audit-friendly outcome for one settlement item.
type ReconciliationResult struct {
	ID                  uuid.UUID                  `db:"id" json:"id"`
	SettlementID        uuid.UUID                  `db:"settlement_id" json:"settlement_id"`
	SettlementItemID    uuid.UUID                  `db:"settlement_item_id" json:"settlement_item_id"`
	ReconciliationRunID uuid.UUID                  `db:"reconciliation_run_id" json:"reconciliation_run_id"`
	TransactionID       *uuid.UUID                 `db:"transaction_id" json:"transaction_id,omitempty"`
	RefundID            *uuid.UUID                 `db:"refund_id" json:"refund_id,omitempty"`
	MerchantID          *uuid.UUID                 `db:"merchant_id" json:"merchant_id,omitempty"`
	ResultType          ReconciliationResultType   `db:"result_type" json:"result_type"`
	ExpectedAmount      *int64                     `db:"expected_amount" json:"expected_amount,omitempty"`
	ActualAmount        *int64                     `db:"actual_amount" json:"actual_amount,omitempty"`
	ExpectedCurrency    *string                    `db:"expected_currency" json:"expected_currency,omitempty"`
	ActualCurrency      *string                    `db:"actual_currency" json:"actual_currency,omitempty"`
	DifferenceAmount    *int64                     `db:"difference_amount" json:"difference_amount,omitempty"`
	Status              ReconciliationResultStatus `db:"status" json:"status"`
	ReasonCode          *string                    `db:"reason_code" json:"reason_code,omitempty"`
	Details             []byte                     `db:"details" json:"-"`
	CreatedAt           time.Time                  `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time                  `db:"updated_at" json:"updated_at"`
}

// ReconciliationSummary is returned by the reconcile API.
type ReconciliationSummary struct {
	SettlementID        uuid.UUID        `json:"settlement_id"`
	SettlementStatus    SettlementStatus `json:"settlement_status"`
	ReconciliationRunID uuid.UUID        `json:"reconciliation_run_id"`
	TotalItems          int              `json:"total_items"`
	MatchedItems        int              `json:"matched_items"`
	MismatchItems       int              `json:"mismatch_items"`
	UnmatchedItems      int              `json:"unmatched_items"`
	PaymentMatches      int              `json:"payment_matches"`
	RefundMatches       int              `json:"refund_matches"`
	AmountMismatches    int              `json:"amount_mismatches"`
	CurrencyMismatches  int              `json:"currency_mismatches"`
	NotFound            int              `json:"not_found"`
	Unsupported         int              `json:"unsupported"`
	TotalExpectedAmount int64            `json:"total_expected_amount"`
	TotalActualAmount   int64            `json:"total_actual_amount"`
	TotalDifference     int64            `json:"total_difference"`
}

// ReconciliationListFilter for listing results / mismatches.
type ReconciliationListFilter struct {
	Page           int
	Limit          int
	SettlementID   *uuid.UUID
	Provider       *string
	MerchantID     *uuid.UUID
	ResultType     *ReconciliationResultType
	Status         *ReconciliationResultStatus
	ReasonCode     *string
	DateFrom       *time.Time
	DateTo         *time.Time
	MismatchesOnly bool
}
