package repository

import "errors"

// Sentinel errors returned by repository implementations.
// Service layer checks these with errors.Is to decide HTTP status codes.
var (
	ErrMerchantNotFound       = errors.New("merchant not found")
	ErrMerchantCodeExists     = errors.New("merchant code already exists")
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

	// ErrMerchantUserCrossTenant is returned internally when a user ID belongs
	// to a different merchant than the one supplied by the authenticated
	// request. The service maps it to the existing non-leaking cross-tenant
	// business error.
	ErrMerchantUserCrossTenant = errors.New("merchant user belongs to another merchant")

	// ErrLastActiveOwnerRequired is returned by the transactional owner
	// mutation path when a change would leave a merchant with zero ACTIVE
	// OWNERs. The service maps it to LAST_OWNER_REQUIRED.
	ErrLastActiveOwnerRequired = errors.New("merchant must retain at least one active owner")

	// ErrMerchantInactive is returned when an owner mutation reaches the
	// database after the merchant has been suspended or deactivated.
	ErrMerchantInactive = errors.New("merchant is inactive")

	// ErrMerchantStatusTransitionInvalid is returned when the row-locked
	// lifecycle mutation observes a current state that does not permit the
	// requested transition. The service maps it to the public stable error.
	ErrMerchantStatusTransitionInvalid = errors.New("invalid merchant status transition")
)
