package handler

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RefundHandler handles HTTP requests for the refund lifecycle.
type RefundHandler struct {
	refundSvc service.RefundService
}

// NewRefundHandler constructs a RefundHandler.
func NewRefundHandler(refundSvc service.RefundService) *RefundHandler {
	return &RefundHandler{refundSvc: refundSvc}
}

// CreateRefund godoc
//
//	@Summary		Create a refund
//	@Description	Creates a partial or full refund against a PAID payment transaction.
//	@Description
//	@Description	**Idempotency**
//	@Description
//	@Description	`Idempotency-Key` (1–255 chars, required) is scoped to the authenticated merchant.
//	@Description	The key and the request body are hashed together; the hash is compared on every replay.
//	@Description
//	@Description	- **Same key + same payload**: replays the stored result (HTTP 201). No second refund or provider call.
//	@Description	- **Same key + different payload**: `409 IDEMPOTENCY_KEY_REUSED`.
//	@Description	- **Concurrent requests with the same key**: one reserves the key; the other receives `409 IDEMPOTENCY_REQUEST_IN_PROGRESS`.
//	@Description
//	@Description	**Over-refund protection**
//	@Description
//	@Description	The gateway serializes concurrent refund creates using `SELECT … FOR UPDATE` on the transaction row.
//	@Description	Both PENDING and PROCESSING refunds count against the refundable balance, preventing double-spend.
//	@Description	The maximum total refundable amount is `transaction.amount`.
//	@Description
//	@Description	**Partial refunds**: send `amount` less than `transaction.amount`. Multiple partial refunds are supported.
//	@Description
//	@Description	**Provider timeout**: if the provider does not respond, the refund stays PENDING with its reservation held.
//	@Description	Replay the same `Idempotency-Key` to recover the stored result.
//	@Tags			refunds
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id				path		string						true	"Payment transaction UUID"
//	@Param			Idempotency-Key	header		string						true	"Merchant-scoped idempotency key (1–255 chars)"
//	@Param			body			body		model.CreateRefundRequest	true	"Refund request payload"
//	@Success		201				{object}	response.successEnvelope{data=model.RefundResponse}	"Refund created (or replayed)"
//	@Failure		400				{object}	response.errorEnvelope	"INVALID_REQUEST | VALIDATION_ERROR | REFUND_TRANSACTION_NOT_PAID | REFUND_AMOUNT_EXCEEDED | REFUND_CURRENCY_MISMATCH"
//	@Failure		401				{object}	response.errorEnvelope	"INVALID_API_KEY | MERCHANT_INACTIVE"
//	@Failure		404				{object}	response.errorEnvelope	"TRANSACTION_NOT_FOUND"
//	@Failure		409				{object}	response.errorEnvelope	"IDEMPOTENCY_KEY_REUSED | IDEMPOTENCY_REQUEST_IN_PROGRESS"
//	@Failure		502				{object}	response.errorEnvelope	"REFUND_PROVIDER_ERROR — provider rejected the refund"
//	@Failure		504				{object}	response.errorEnvelope	"REFUND_PROVIDER_TIMEOUT — provider timed out; result stored and replayable"
//	@Failure		500				{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/payments/{id}/refunds [post]
func (h *RefundHandler) CreateRefund(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}
	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid transaction ID")
		return
	}
	var req model.CreateRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		response.BadRequest(c, response.CodeInvalidRequest, "Idempotency-Key must be between 1 and 255 characters")
		return
	}
	resp, err := h.refundSvc.CreateRefundWithIdempotency(c.Request.Context(), merchant.ID, txID, req, key)
	if err != nil {
		h.handleRefundError(c, err)
		return
	}
	response.Created(c, resp)
}

// GetRefund godoc
//
//	@Summary		Get a refund
//	@Description	Returns the refund details. The refund must belong to the authenticated merchant.
//	@Description	Returns `404` for both non-existent refund IDs and refunds belonging to another merchant
//	@Description	(no information leakage via 403).
//	@Tags			refunds
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Refund UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.RefundResponse}	"Refund details including current status and financial context"
//	@Failure		400	{object}	response.errorEnvelope	"INVALID_REQUEST — invalid UUID format"
//	@Failure		401	{object}	response.errorEnvelope	"INVALID_API_KEY | MERCHANT_INACTIVE"
//	@Failure		404	{object}	response.errorEnvelope	"REFUND_NOT_FOUND — not found or belongs to another merchant"
//	@Failure		500	{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/refunds/{id} [get]
func (h *RefundHandler) GetRefund(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}
	refundID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid refund ID")
		return
	}
	resp, err := h.refundSvc.GetRefund(c.Request.Context(), merchant.ID, refundID)
	if err != nil {
		h.handleRefundError(c, err)
		return
	}
	response.OK(c, resp)
}

// ListRefunds godoc
//
//	@Summary		List refunds for a payment
//	@Description	Returns a paginated list of refunds for a specific PAID payment transaction.
//	@Description
//	@Description	**Merchant isolation**: results are always scoped to the authenticated merchant.
//	@Description	The transaction must belong to the authenticated merchant.
//	@Description
//	@Description	**Ordering**: fixed `created_at DESC, id DESC` for deterministic pagination.
//	@Description
//	@Description	**Page beyond last page**: returns an empty `data` array with accurate `total` and `total_pages`.
//	@Tags			refunds
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id		path		string	true	"Payment transaction UUID"
//	@Param			page	query		int		false	"Page number (default: 1, min: 1)"
//	@Param			limit	query		int		false	"Items per page (default: 20, min: 1, max: 100)"
//	@Param			status	query		string	false	"Filter by refund status: PENDING | PROCESSING | SUCCEEDED | FAILED"
//	@Success		200		{object}	response.successEnvelope{data=[]model.RefundResponse}	"Paginated list of refunds"
//	@Failure		400		{object}	response.errorEnvelope	"INVALID_REQUEST — invalid UUID format"
//	@Failure		401		{object}	response.errorEnvelope	"INVALID_API_KEY | MERCHANT_INACTIVE"
//	@Failure		404		{object}	response.errorEnvelope	"TRANSACTION_NOT_FOUND"
//	@Failure		500		{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/payments/{id}/refunds [get]
func (h *RefundHandler) ListRefunds(c *gin.Context) {
	merchant := middleware.MerchantFromContext(c)
	if merchant == nil {
		response.InternalServerError(c)
		return
	}
	txID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid transaction ID")
		return
	}
	filter := model.RefundListFilter{Page: model.DefaultPage, Limit: model.DefaultLimit}
	if p := c.Query("page"); p != "" {
		if v, e := strconv.Atoi(p); e == nil && v >= 1 {
			filter.Page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		if v, e := strconv.Atoi(l); e == nil && v >= 1 && v <= model.MaxLimit {
			filter.Limit = v
		}
	}
	if st := c.Query("status"); st != "" {
		s := model.RefundStatus(st)
		filter.Status = &s
	}
	result, err := h.refundSvc.ListRefunds(c.Request.Context(), merchant.ID, txID, filter)
	if err != nil {
		h.handleRefundError(c, err)
		return
	}
	data := result.Refunds
	if data == nil {
		data = []model.RefundResponse{}
	}
	response.List(c, data, response.Pagination{
		Page: result.Page, Limit: result.Limit, Total: result.Total, TotalPages: result.TotalPages,
	})
}

// ─── error mapping ─────────────────────────────────────────────────────────────

func (h *RefundHandler) handleRefundError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrTransactionNotFound):
		response.NotFound(c, response.CodeTransactionNotFound, "Transaction not found")
	case errors.Is(err, service.ErrRefundNotFound):
		response.NotFound(c, response.CodeRefundNotFound, "Refund not found")
	case errors.Is(err, service.ErrRefundTransactionNotPaid):
		response.BadRequest(c, response.CodeRefundTransactionNotPaid, "Refunds are only allowed for PAID transactions")
	case errors.Is(err, service.ErrRefundAmountExceeded):
		response.BadRequest(c, response.CodeRefundAmountExceeded, "Refund amount exceeds available refundable balance")
	case errors.Is(err, service.ErrRefundCurrencyMismatch):
		response.BadRequest(c, response.CodeRefundCurrencyMismatch, "Refund currency must match transaction currency")
	case errors.Is(err, service.ErrIdempotencyKeyReused):
		response.Conflict(c, response.CodeIdempotencyKeyReused, "Idempotency key reused with a different request payload")
	case errors.Is(err, service.ErrIdempotencyInProgress):
		response.Conflict(c, response.CodeIdempotencyInProgress, "A request with this idempotency key is already in progress")
	case errors.Is(err, service.ErrProviderFailure):
		response.BadGateway(c, response.CodeRefundProviderError, "Refund provider rejected the request")
	case errors.Is(err, service.ErrProviderTimeout):
		response.GatewayTimeout(c, response.CodeRefundProviderTimeout, "Refund provider did not respond in time")
	default:
		slog.Error("refund handler error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
