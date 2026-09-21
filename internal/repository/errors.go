package repository

import "errors"

// Sentinel errors returned by repository implementations.
// Service layer checks these with errors.Is to decide HTTP status codes.
var (
	ErrMerchantNotFound       = errors.New("merchant not found")
	ErrTransactionNotFound    = errors.New("transaction not found")
	ErrDuplicateOrder         = errors.New("duplicate merchant order id")
	ErrWebhookEventNotFound   = errors.New("webhook event not found")
	ErrWebhookEventDuplicate  = errors.New("webhook event already exists")
	ErrRefundNotFound         = errors.New("refund not found")
	ErrRefundAmountExceeded   = errors.New("refund amount exceeds refundable balance")
	ErrRefundNotAllowed       = errors.New("refund not allowed for transaction state")
	ErrRefundCurrencyMismatch = errors.New("refund currency mismatch")

	ErrSettlementNotFound           = errors.New("settlement not found")
	ErrSettlementAlreadyExists      = errors.New("settlement already exists")
	ErrSettlementImportConflict     = errors.New("settlement import conflict")
	ErrSettlementInvalidStatus      = errors.New("settlement invalid status")
	ErrReconciliationNotFound       = errors.New("reconciliation result not found")
	ErrReconciliationAlreadyRunning = errors.New("reconciliation already running")
)
