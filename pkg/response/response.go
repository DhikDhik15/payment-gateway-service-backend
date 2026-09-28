// Package response provides standardised JSON response helpers for Gin handlers.
//
// All responses follow a consistent envelope shape:
//
// Success:
//
//	{
//	  "success": true,
//	  "data": <payload>,
//	  "meta": { "request_id": "req_xxx" }
//	}
//
// List:
//
//	{
//	  "success": true,
//	  "data": [...],
//	  "meta": { "request_id": "req_xxx", "page": 1, "limit": 20, "total": 100, "total_pages": 5 }
//	}
//
// Error:
//
//	{
//	  "success": false,
//	  "error": { "code": "ERROR_CODE", "message": "...", "details": {} },
//	  "meta": { "request_id": "req_xxx" }
//	}
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ContextKey is used to store/retrieve the request ID from a Gin context.
const ContextKey = "request_id"

// ─── Error codes ────────────────────────────────────────────────────────────

// ErrorCode is a stable, machine-readable error identifier.
type ErrorCode string

const (
	CodeInvalidRequest          ErrorCode = "INVALID_REQUEST"
	CodeValidationError         ErrorCode = "VALIDATION_ERROR"
	CodeInvalidAmount           ErrorCode = "INVALID_AMOUNT"
	CodeInvalidCurrency         ErrorCode = "INVALID_CURRENCY"
	CodeInvalidPaymentMethod    ErrorCode = "INVALID_PAYMENT_METHOD"
	CodeUnauthorized            ErrorCode = "UNAUTHORIZED"
	CodeInvalidAPIKey           ErrorCode = "INVALID_API_KEY"
	CodeMerchantInactive        ErrorCode = "MERCHANT_INACTIVE"
	CodeForbidden               ErrorCode = "FORBIDDEN"
	CodeResourceNotFound        ErrorCode = "RESOURCE_NOT_FOUND"
	CodeMerchantNotFound        ErrorCode = "MERCHANT_NOT_FOUND"
	CodeTransactionNotFound     ErrorCode = "TRANSACTION_NOT_FOUND"
	CodeDuplicateOrder          ErrorCode = "DUPLICATE_ORDER"
	CodeDuplicateMerchantCode   ErrorCode = "DUPLICATE_MERCHANT_CODE"
	CodeInvalidTransactionState ErrorCode = "INVALID_TRANSACTION_STATE"
	CodePaymentProviderError    ErrorCode = "PAYMENT_PROVIDER_ERROR"
	CodePaymentProviderTimeout  ErrorCode = "PAYMENT_PROVIDER_TIMEOUT"
	CodeInternalError           ErrorCode = "INTERNAL_ERROR"
	CodeIdempotencyKeyReused    ErrorCode = "IDEMPOTENCY_KEY_REUSED"
	CodeIdempotencyInProgress   ErrorCode = "IDEMPOTENCY_REQUEST_IN_PROGRESS"

	// Refund error codes (Phase 7B).
	CodeRefundNotFound           ErrorCode = "REFUND_NOT_FOUND"
	CodeRefundNotAllowed         ErrorCode = "REFUND_NOT_ALLOWED"
	CodeRefundAmountExceeded     ErrorCode = "REFUND_AMOUNT_EXCEEDED"
	CodeRefundTransactionNotPaid ErrorCode = "REFUND_TRANSACTION_NOT_PAID"
	CodeRefundCurrencyMismatch   ErrorCode = "REFUND_CURRENCY_MISMATCH"
	CodeRefundProviderError      ErrorCode = "REFUND_PROVIDER_ERROR"
	CodeRefundProviderTimeout    ErrorCode = "REFUND_PROVIDER_TIMEOUT"

	// Settlement / reconciliation error codes (Phase 7C).
	CodeSettlementNotFound             ErrorCode = "SETTLEMENT_NOT_FOUND"
	CodeSettlementAlreadyExists        ErrorCode = "SETTLEMENT_ALREADY_EXISTS"
	CodeSettlementImportInvalid        ErrorCode = "SETTLEMENT_IMPORT_INVALID"
	CodeSettlementImportFailed         ErrorCode = "SETTLEMENT_IMPORT_FAILED"
	CodeSettlementImportConflict       ErrorCode = "SETTLEMENT_IMPORT_CONFLICT"
	CodeSettlementInvalidStatus        ErrorCode = "SETTLEMENT_INVALID_STATUS"
	CodeSettlementReconciliationFailed ErrorCode = "SETTLEMENT_RECONCILIATION_FAILED"
	CodeReconciliationNotFound         ErrorCode = "RECONCILIATION_NOT_FOUND"
	CodeReconciliationAlreadyRunning   ErrorCode = "RECONCILIATION_ALREADY_RUNNING"
	CodeReconciliationMismatch         ErrorCode = "RECONCILIATION_MISMATCH"
	CodeAdminUnauthorized              ErrorCode = "ADMIN_UNAUTHORIZED"
	CodeAdminNotConfigured             ErrorCode = "ADMIN_NOT_CONFIGURED"

	// API key lifecycle error codes (Phase 5C).
	CodeAPIKeyNotFound           ErrorCode = "API_KEY_NOT_FOUND"
	CodeAPIKeyAlreadyRevoked     ErrorCode = "API_KEY_ALREADY_REVOKED"
	CodeAPIKeyOwnershipViolation ErrorCode = "API_KEY_OWNERSHIP_VIOLATION"

	// Dashboard auth error codes (Phase 8).
	CodeInvalidCredentials    ErrorCode = "INVALID_CREDENTIALS"
	CodeUserDisabled          ErrorCode = "USER_DISABLED"
	CodeDashboardUserNotFound ErrorCode = "DASHBOARD_USER_NOT_FOUND"
	CodeEmailAlreadyExists    ErrorCode = "EMAIL_ALREADY_EXISTS"
	CodeInsufficientRole      ErrorCode = "INSUFFICIENT_ROLE"

	// Merchant lifecycle error codes (Phase 7).
	CodeInvalidStatusTransition ErrorCode = "INVALID_STATUS_TRANSITION"

	// Team management error codes (Phase 8A).
	CodeLastOwnerRequired      ErrorCode = "LAST_OWNER_REQUIRED"
	CodeInvalidCurrentPassword ErrorCode = "INVALID_CURRENT_PASSWORD"
	CodeInvalidRole            ErrorCode = "INVALID_ROLE"
	CodeInvalidPassword        ErrorCode = "INVALID_PASSWORD"

	// Team invitation error codes (Phase 8B).
	CodeInvitationNotFound        ErrorCode = "INVITATION_NOT_FOUND"
	CodeInvitationAlreadyPending  ErrorCode = "INVITATION_ALREADY_PENDING"
	CodeInvitationAlreadyAccepted ErrorCode = "INVITATION_ALREADY_ACCEPTED"

	// Outbound webhook destination policy (Phase 8D.2 — SSRF).
	CodeWebhookDestinationBlocked ErrorCode = "WEBHOOK_DESTINATION_BLOCKED"

	// Inbound HTTP request body limits (Phase 8D.2).
	CodeRequestTooLarge ErrorCode = "REQUEST_TOO_LARGE"

	// Legacy plaintext credential lifecycle (Phase 8D.3).
	//
	// CodeLegacyCredentialCreationDisabled (409) — creation of new row-level
	//   legacy credentials is permanently frozen (POST /api/v1/merchants).
	// CodeLegacyCredentialsNotEnabled (401) — a bare legacy key was presented
	//   while LEGACY_API_CREDENTIALS_ENABLED is false (fail-safe in production).
	//   Returned BEFORE any credential lookup, so it never confirms validity.
	// CodeLegacyCredentialMigrationRequired (409) — disable was called while
	//   the merchant is still in the LEGACY state; migrate first.
	// CodeLegacyCredentialAlreadyMigrated (409) — migrate was called for a
	//   merchant that is no longer LEGACY (including the concurrency loser).
	CodeLegacyCredentialCreationDisabled  ErrorCode = "LEGACY_CREDENTIAL_CREATION_DISABLED"
	CodeLegacyCredentialsNotEnabled       ErrorCode = "LEGACY_CREDENTIALS_NOT_ENABLED"
	CodeLegacyCredentialMigrationRequired ErrorCode = "LEGACY_CREDENTIAL_MIGRATION_REQUIRED"
	CodeLegacyCredentialAlreadyMigrated   ErrorCode = "LEGACY_CREDENTIAL_ALREADY_MIGRATED"
)

// ─── Envelope types ──────────────────────────────────────────────────────────

// Meta holds per-request metadata included in every response.
type Meta struct {
	RequestID  string `json:"request_id"`
	Page       *int   `json:"page,omitempty"`
	Limit      *int   `json:"limit,omitempty"`
	Total      *int64 `json:"total,omitempty"`
	TotalPages *int   `json:"total_pages,omitempty"`
}

// successEnvelope is the JSON shape for all successful single-resource responses.
type successEnvelope struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
	Meta    Meta `json:"meta"`
}

// errorDetail is the machine- and human-readable error payload.
type errorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Details any       `json:"details,omitempty"`
}

// errorEnvelope is the JSON shape for all error responses.
type errorEnvelope struct {
	Success bool        `json:"success"`
	Error   errorDetail `json:"error"`
	Meta    Meta        `json:"meta"`
}

// Pagination bundles pagination parameters for list responses.
type Pagination struct {
	Page       int
	Limit      int
	Total      int64
	TotalPages int
}

// ─── Context helpers ─────────────────────────────────────────────────────────

// requestID extracts the request ID stored by the requestid middleware.
// Falls back to an empty string if not set.
func requestID(c *gin.Context) string {
	if id, ok := c.Get(ContextKey); ok {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}

func baseMeta(c *gin.Context) Meta {
	return Meta{RequestID: requestID(c)}
}

// ─── Success helpers ─────────────────────────────────────────────────────────

// OK writes HTTP 200 with a success envelope.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, successEnvelope{
		Success: true,
		Data:    data,
		Meta:    baseMeta(c),
	})
}

// Created writes HTTP 201 with a success envelope.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, successEnvelope{
		Success: true,
		Data:    data,
		Meta:    baseMeta(c),
	})
}

// List writes HTTP 200 with a paginated success envelope.
func List(c *gin.Context, data any, p Pagination) {
	meta := baseMeta(c)
	meta.Page = &p.Page
	meta.Limit = &p.Limit
	meta.Total = &p.Total
	meta.TotalPages = &p.TotalPages

	c.JSON(http.StatusOK, successEnvelope{
		Success: true,
		Data:    data,
		Meta:    meta,
	})
}

// ─── Error helpers ───────────────────────────────────────────────────────────

// writeError is the shared implementation for all error responses.
func writeError(c *gin.Context, status int, code ErrorCode, message string, details any) {
	c.JSON(status, errorEnvelope{
		Success: false,
		Error: errorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
		Meta: baseMeta(c),
	})
}

// BadRequest writes HTTP 400.
func BadRequest(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusBadRequest, code, message, nil)
}

// ValidationError writes HTTP 400 with per-field validation details.
// details should be a map[string]string of field → message.
func ValidationError(c *gin.Context, details any) {
	writeError(c, http.StatusBadRequest, CodeValidationError, "Validation failed", details)
}

// Unauthorized writes HTTP 401.
func Unauthorized(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusUnauthorized, code, message, nil)
}

// Forbidden writes HTTP 403.
func Forbidden(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusForbidden, code, message, nil)
}

// NotFound writes HTTP 404.
func NotFound(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusNotFound, code, message, nil)
}

// Conflict writes HTTP 409.
func Conflict(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusConflict, code, message, nil)
}

// ConflictWithDetails writes HTTP 409 with a details payload.
func ConflictWithDetails(c *gin.Context, code ErrorCode, message string, details any) {
	writeError(c, http.StatusConflict, code, message, details)
}

// UnprocessableEntity writes HTTP 422.
func UnprocessableEntity(c *gin.Context, code ErrorCode, message string, details any) {
	writeError(c, http.StatusUnprocessableEntity, code, message, details)
}

// PayloadTooLarge writes HTTP 413 (request body exceeds the configured
// HTTP_MAX_BODY_BYTES limit).
func PayloadTooLarge(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusRequestEntityTooLarge, code, message, nil)
}

// TooManyRequests writes HTTP 429.
func TooManyRequests(c *gin.Context, message string) {
	writeError(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", message, nil)
}

// PaymentProviderError writes HTTP 502.
func PaymentProviderError(c *gin.Context, message string) {
	writeError(c, http.StatusBadGateway, CodePaymentProviderError, message, nil)
}

// BadGateway writes HTTP 502 with the given error code and message.
func BadGateway(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusBadGateway, code, message, nil)
}

// PaymentProviderTimeout writes HTTP 504.
func PaymentProviderTimeout(c *gin.Context) {
	writeError(c, http.StatusGatewayTimeout, CodePaymentProviderTimeout, "Payment provider did not respond in time", nil)
}

// GatewayTimeout writes HTTP 504 with the given error code and message.
func GatewayTimeout(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusGatewayTimeout, code, message, nil)
}

// InternalServerError writes HTTP 500.
// The raw internal error is intentionally NOT forwarded to the client.
func InternalServerError(c *gin.Context) {
	writeError(c, http.StatusInternalServerError, CodeInternalError, "An unexpected error occurred", nil)
}

// ServiceUnavailable writes HTTP 503.
func ServiceUnavailable(c *gin.Context, code ErrorCode, message string) {
	writeError(c, http.StatusServiceUnavailable, code, message, nil)
}
