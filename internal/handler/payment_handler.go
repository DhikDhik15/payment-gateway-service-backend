package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// PaymentHandler handles HTTP requests for the payment lifecycle.
// It is intentionally thin: parse → validate format → call service → respond.
// No business logic lives here.
type PaymentHandler struct {
	paymentSvc service.PaymentService
}

// NewPaymentHandler constructs a PaymentHandler.
func NewPaymentHandler(paymentSvc service.PaymentService) *PaymentHandler {
	return &PaymentHandler{paymentSvc: paymentSvc}
}

// List godoc
//
//	@Summary		List payments
//	@Description	Returns a paginated, filtered list of transactions for the authenticated merchant.
//	@Description
//	@Description	**Merchant isolation**: results are always scoped to the authenticated merchant.
//	@Description	The merchant identity comes from the `X-API-Key` header — never from a query parameter.
//	@Description
//	@Description	**Ordering**: fixed `created_at DESC, id DESC` for deterministic pagination.
//	@Description
//	@Description	**Date range**: half-open interval `[created_from, created_to)`.
//	@Description	`created_at >= created_from` and `created_at < created_to`.
//	@Description	If only `created_from` is supplied, the lower bound applies with no upper bound.
//	@Description	If only `created_to` is supplied, the upper bound applies with no lower bound.
//	@Description	`created_from >= created_to` is a validation error.
//	@Description
//	@Description	**Page beyond last page**: returns an empty `data` array with accurate `total` and `total_pages`.
//	@Tags			payments
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			page				query		int		false	"Page number (default: 1, min: 1)"
//	@Param			limit				query		int		false	"Items per page (default: 20, min: 1, max: 100)"
//	@Param			status				query		string	false	"Filter by status: CREATED | PENDING | PAID | FAILED | EXPIRED | CANCELLED"
//	@Param			merchant_order_id	query		string	false	"Filter by exact merchant_order_id"
//	@Param			payment_method		query		string	false	"Filter by payment method (e.g. QRIS)"
//	@Param			created_from		query		string	false	"Lower bound on created_at, RFC3339 (inclusive): e.g. 2026-01-01T00:00:00Z"
//	@Param			created_to			query		string	false	"Upper bound on created_at, RFC3339 (exclusive): e.g. 2026-02-01T00:00:00Z"
//	@Success		200					{object}	response.successEnvelope{data=[]model.PaymentResponse}	"List of payments with pagination metadata"
//	@Failure		400					{object}	response.errorEnvelope	"INVALID_REQUEST — invalid pagination, status, payment method, or date range"
//	@Failure		401					{object}	response.errorEnvelope	"INVALID_API_KEY or MERCHANT_INACTIVE"
//	@Failure		500					{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/payments [get]
func (h *PaymentHandler) List(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}

	filter, validationErrs := parseListFilter(c)
	if len(validationErrs) > 0 {
		response.ValidationError(c, validationErrs)
		return
	}

	result, err := h.paymentSvc.ListPayments(c.Request.Context(), merchant.ID, filter)
	if err != nil {
		h.handleListError(c, err)
		return
	}

	// Return empty array (not null) when there are no results.
	data := result.Transactions
	if data == nil {
		data = []model.PaymentResponse{}
	}

	response.List(c, data, response.Pagination{
		Page:       result.Page,
		Limit:      result.Limit,
		Total:      result.Total,
		TotalPages: result.TotalPages,
	})
}

// ─── list query parsing ───────────────────────────────────────────────────────

// parseListFilter reads and validates all query parameters for GET /payments.
// Returns a populated filter and a map of field→message validation errors.
func parseListFilter(c *gin.Context) (model.TransactionListFilter, map[string]string) {
	errs := make(map[string]string)
	var filter model.TransactionListFilter

	// page
	if raw := c.Query("page"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			errs["page"] = "must be an integer >= 1"
		} else {
			filter.Page = v
		}
	}

	// limit
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > model.MaxLimit {
			errs["limit"] = fmt.Sprintf("must be an integer between 1 and %d", model.MaxLimit)
		} else {
			filter.Limit = v
		}
	}

	// status
	if raw := c.Query("status"); raw != "" {
		s := model.TransactionStatus(raw)
		switch s {
		case model.TransactionStatusCreated,
			model.TransactionStatusPending,
			model.TransactionStatusPaid,
			model.TransactionStatusFailed,
			model.TransactionStatusExpired,
			model.TransactionStatusCancelled:
			filter.Status = &s
		default:
			errs["status"] = "must be one of: CREATED, PENDING, PAID, FAILED, EXPIRED, CANCELLED"
		}
	}

	// merchant_order_id
	if raw := c.Query("merchant_order_id"); raw != "" {
		filter.MerchantOrderID = &raw
	}

	// payment_method
	if raw := c.Query("payment_method"); raw != "" {
		if !model.SupportedPaymentMethods[raw] {
			errs["payment_method"] = "unsupported payment method"
		} else {
			filter.PaymentMethod = &raw
		}
	}

	// created_from
	var createdFrom, createdTo *time.Time
	if raw := c.Query("created_from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs["created_from"] = "must be a valid RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)"
		} else {
			utc := t.UTC()
			createdFrom = &utc
		}
	}

	// created_to
	if raw := c.Query("created_to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs["created_to"] = "must be a valid RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)"
		} else {
			utc := t.UTC()
			createdTo = &utc
		}
	}

	// Validate date range coherence only when both are present and individually valid.
	if createdFrom != nil && createdTo != nil {
		if !createdFrom.Before(*createdTo) {
			errs["created_from"] = "created_from must be before created_to"
		}
	}

	// Only set on filter when no date errors.
	if _, hasFromErr := errs["created_from"]; !hasFromErr {
		filter.CreatedFrom = createdFrom
	}
	if _, hasToErr := errs["created_to"]; !hasToErr {
		filter.CreatedTo = createdTo
	}

	return filter, errs
}

// handleListError maps listing-specific service errors to HTTP responses.
func (h *PaymentHandler) handleListError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPage):
		response.ValidationError(c, map[string]string{"page": "must be >= 1"})
	case errors.Is(err, service.ErrInvalidLimit):
		response.ValidationError(c, map[string]string{"limit": fmt.Sprintf("must be between 1 and %d", model.MaxLimit)})
	case errors.Is(err, service.ErrInvalidStatus):
		response.ValidationError(c, map[string]string{"status": "invalid transaction status"})
	case errors.Is(err, service.ErrInvalidPaymentMethod):
		response.BadRequest(c, response.CodeInvalidPaymentMethod, "Payment method not supported. Supported: QRIS")
	case errors.Is(err, service.ErrInvalidDateRange):
		response.ValidationError(c, map[string]string{"created_from": "created_from must be before created_to"})
	default:
		slog.Error("payment handler list: unexpected error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}

// Create godoc
//
//	@Summary		Create a payment
//	@Description	Initiates a new payment for the authenticated merchant.
//	@Description
//	@Description	**Idempotency**
//	@Description
//	@Description	`Idempotency-Key` (1–255 chars, required) scopes the request to the authenticated merchant.
//	@Description	The key and the request body are hashed together; the hash is compared on every replay.
//	@Description
//	@Description	- **Same key + same payload**: replays the stored result (HTTP 201). No second transaction,
//	@Description	  provider call, or payment attempt is created.
//	@Description	- **Same key + different payload**: `409 IDEMPOTENCY_KEY_REUSED`. The original record is not modified.
//	@Description	- **Concurrent requests with the same key**: one request reserves the key (`PROCESSING`).
//	@Description	  The other receives `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` or the completed replay.
//	@Description	- **FAILED results** (including provider timeouts) are replayed. The gateway never
//	@Description	  automatically retries an ambiguous external provider call.
//	@Description	- Records expire after `IDEMPOTENCY_TTL` (default `24h`). Expired keys can be reserved again.
//	@Tags			payments
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			Idempotency-Key	header		string								true	"Merchant-scoped idempotency key (1–255 chars). Required for every POST /payments call."
//	@Param			body			body		model.CreatePaymentRequest				true	"Payment creation payload"
//	@Success		201				{object}	response.successEnvelope{data=model.CreatePaymentResponse}	"Payment created (or replayed)"
//	@Failure		400				{object}	response.errorEnvelope					"INVALID_REQUEST — missing / empty / oversized Idempotency-Key; or VALIDATION_ERROR — invalid body fields"
//	@Failure		401				{object}	response.errorEnvelope					"INVALID_API_KEY or MERCHANT_INACTIVE"
//	@Failure		409				{object}	response.errorEnvelope					"DUPLICATE_ORDER | IDEMPOTENCY_KEY_REUSED | IDEMPOTENCY_REQUEST_IN_PROGRESS"
//	@Failure		502				{object}	response.errorEnvelope					"PAYMENT_PROVIDER_ERROR — provider rejected the request"
//	@Failure		504				{object}	response.errorEnvelope					"PAYMENT_PROVIDER_TIMEOUT — provider did not respond in time (result stored; replay returns same 504)"
//	@Failure		500				{object}	response.errorEnvelope					"INTERNAL_ERROR"
//	@Router			/api/v1/payments [post]
func (h *PaymentHandler) Create(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}

	var req model.CreatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 255 {
		response.BadRequest(c, response.CodeInvalidRequest, "Idempotency-Key must be between 1 and 255 characters")
		return
	}

	result, err := h.paymentSvc.CreatePaymentWithIdempotency(c.Request.Context(), merchant.ID, req, idempotencyKey)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.Created(c, result)
}

// GetByID godoc
//
//	@Summary		Get a payment
//	@Description	Returns the payment details. The transaction must belong to the authenticated merchant.
//	@Tags			payments
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Transaction UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.PaymentResponse}
//	@Failure		400	{object}	response.errorEnvelope	"Invalid UUID"
//	@Failure		401	{object}	response.errorEnvelope	"Missing or invalid API key"
//	@Failure		404	{object}	response.errorEnvelope	"Transaction not found"
//	@Failure		500	{object}	response.errorEnvelope	"Internal error"
//	@Router			/api/v1/payments/{id} [get]
func (h *PaymentHandler) GetByID(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}

	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid transaction ID format")
		return
	}

	result, err := h.paymentSvc.GetPayment(c.Request.Context(), merchant.ID, txID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.OK(c, result)
}

// Cancel godoc
//
//	@Summary		Cancel a payment
//	@Description	Cancels a CREATED or PENDING transaction. PAID, FAILED, EXPIRED, and CANCELLED transactions cannot be cancelled.
//	@Tags			payments
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Transaction UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.PaymentResponse}
//	@Failure		400	{object}	response.errorEnvelope	"Invalid UUID"
//	@Failure		401	{object}	response.errorEnvelope	"Missing or invalid API key"
//	@Failure		404	{object}	response.errorEnvelope	"Transaction not found"
//	@Failure		409	{object}	response.errorEnvelope	"Invalid state transition"
//	@Failure		502	{object}	response.errorEnvelope	"Payment provider error"
//	@Failure		504	{object}	response.errorEnvelope	"Payment provider timeout"
//	@Failure		500	{object}	response.errorEnvelope	"Internal error"
//	@Router			/api/v1/payments/{id}/cancel [post]
func (h *PaymentHandler) Cancel(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}

	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid transaction ID format")
		return
	}

	result, err := h.paymentSvc.CancelPayment(c.Request.Context(), merchant.ID, txID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	response.OK(c, result)
}

// ─── error mapping ────────────────────────────────────────────────────────────

// handleServiceError maps service/repository sentinel errors to the correct
// HTTP response. Internal errors are never exposed to the client.
func (h *PaymentHandler) handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrTransactionNotFound):
		response.NotFound(c, response.CodeTransactionNotFound, "Transaction not found")

	case errors.Is(err, service.ErrDuplicateOrder):
		response.Conflict(c, response.CodeDuplicateOrder, "An order with this merchant_order_id already exists")

	case errors.Is(err, service.ErrIdempotencyKeyReused):
		response.Conflict(c, response.CodeIdempotencyKeyReused, "Idempotency-Key was already used with a different request")

	case errors.Is(err, service.ErrIdempotencyInProgress):
		response.Conflict(c, response.CodeIdempotencyInProgress, "A request with this Idempotency-Key is already in progress")

	case errors.Is(err, service.ErrInvalidCurrency):
		response.BadRequest(c, response.CodeInvalidCurrency, "Currency not supported. Supported: IDR")

	case errors.Is(err, service.ErrInvalidPaymentMethod):
		response.BadRequest(c, response.CodeInvalidPaymentMethod, "Payment method not supported. Supported: QRIS")

	case errors.Is(err, service.ErrInvalidTransactionState):
		response.ConflictWithDetails(c, response.CodeInvalidTransactionState,
			"Transaction cannot be cancelled in its current state",
			map[string]string{
				"requested_action": "CANCEL",
			},
		)

	case errors.Is(err, service.ErrProviderTimeout):
		response.PaymentProviderTimeout(c)

	case errors.Is(err, service.ErrProviderFailure):
		response.PaymentProviderError(c, "Payment provider failed to process the request")

	default:
		slog.Error("payment handler: unexpected error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
