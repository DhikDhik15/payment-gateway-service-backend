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
	CodeInvalidRequest        ErrorCode = "INVALID_REQUEST"
	CodeValidationError       ErrorCode = "VALIDATION_ERROR"
	CodeInvalidAmount         ErrorCode = "INVALID_AMOUNT"
	CodeInvalidCurrency       ErrorCode = "INVALID_CURRENCY"
	CodeInvalidPaymentMethod  ErrorCode = "INVALID_PAYMENT_METHOD"
	CodeUnauthorized          ErrorCode = "UNAUTHORIZED"
	CodeInvalidAPIKey         ErrorCode = "INVALID_API_KEY"
	CodeMerchantInactive      ErrorCode = "MERCHANT_INACTIVE"
	CodeForbidden             ErrorCode = "FORBIDDEN"
	CodeResourceNotFound      ErrorCode = "RESOURCE_NOT_FOUND"
	CodeMerchantNotFound      ErrorCode = "MERCHANT_NOT_FOUND"
	CodeTransactionNotFound   ErrorCode = "TRANSACTION_NOT_FOUND"
	CodeDuplicateOrder          ErrorCode = "DUPLICATE_ORDER"
	CodeDuplicateMerchantCode   ErrorCode = "DUPLICATE_MERCHANT_CODE"
	CodeInvalidTransactionState ErrorCode = "INVALID_TRANSACTION_STATE"
	CodePaymentProviderError  ErrorCode = "PAYMENT_PROVIDER_ERROR"
	CodePaymentProviderTimeout ErrorCode = "PAYMENT_PROVIDER_TIMEOUT"
	CodeInternalError         ErrorCode = "INTERNAL_ERROR"
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
	Success bool   `json:"success"`
	Data    any    `json:"data"`
	Meta    Meta   `json:"meta"`
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

// UnprocessableEntity writes HTTP 422.
func UnprocessableEntity(c *gin.Context, code ErrorCode, message string, details any) {
	writeError(c, http.StatusUnprocessableEntity, code, message, details)
}

// TooManyRequests writes HTTP 429.
func TooManyRequests(c *gin.Context, message string) {
	writeError(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", message, nil)
}

// PaymentProviderError writes HTTP 502.
func PaymentProviderError(c *gin.Context, message string) {
	writeError(c, http.StatusBadGateway, CodePaymentProviderError, message, nil)
}

// PaymentProviderTimeout writes HTTP 504.
func PaymentProviderTimeout(c *gin.Context) {
	writeError(c, http.StatusGatewayTimeout, CodePaymentProviderTimeout, "Payment provider did not respond in time", nil)
}

// InternalServerError writes HTTP 500.
// The raw internal error is intentionally NOT forwarded to the client.
func InternalServerError(c *gin.Context) {
	writeError(c, http.StatusInternalServerError, CodeInternalError, "An unexpected error occurred", nil)
}
