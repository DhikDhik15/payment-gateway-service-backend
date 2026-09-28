package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DashboardPaymentHandler serves dashboard payment list/detail endpoints.
type DashboardPaymentHandler struct {
	paymentSvc service.PaymentService
	refundSvc  service.RefundService
}

// NewDashboardPaymentHandler constructs a DashboardPaymentHandler.
func NewDashboardPaymentHandler(paymentSvc service.PaymentService, refundSvc service.RefundService) *DashboardPaymentHandler {
	return &DashboardPaymentHandler{paymentSvc: paymentSvc, refundSvc: refundSvc}
}

// ListPayments godoc
//
//	@Summary		List dashboard payments
//	@Description	Paginated payment list for the authenticated merchant.
//	@Description	Merchant identity comes from the JWT — never from query/body/path.
//	@Tags			Dashboard Payments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page				query		int		false	"Page (default 1)"
//	@Param			limit				query		int		false	"Page size (default 20, max 100)"
//	@Param			status				query		string	false	"CREATED|PENDING|PAID|FAILED|EXPIRED|CANCELLED"
//	@Param			merchant_order_id	query		string	false	"Exact merchant order ID"
//	@Param			payment_method		query		string	false	"Payment method (e.g. QRIS)"
//	@Param			search				query		string	false	"Search order ID, provider tx ID, or exact payment UUID"
//	@Param			created_from		query		string	false	"RFC3339 lower bound on created_at (inclusive)"
//	@Param			created_to			query		string	false	"RFC3339 upper bound on created_at (exclusive)"
//	@Success		200					{object}	response.successEnvelope{data=[]model.PaymentResponse}
//	@Failure		400					{object}	response.errorEnvelope
//	@Failure		401					{object}	response.errorEnvelope
//	@Failure		500					{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/payments [get]
func (h *DashboardPaymentHandler) ListPayments(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	filter, verrs := parseDashboardPaymentListFilter(c)
	if len(verrs) > 0 {
		response.ValidationError(c, verrs)
		return
	}

	result, err := h.paymentSvc.ListPayments(c.Request.Context(), caller.MerchantID, filter)
	if err != nil {
		h.handlePaymentListError(c, err)
		return
	}

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

// GetPayment godoc
//
//	@Summary		Get dashboard payment detail
//	@Description	Returns a payment belonging to the authenticated merchant, plus a timeline
//	@Description	derived only from persisted timestamps (CREATED, PAID).
//	@Tags			Dashboard Payments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			payment_id	path		string	true	"Payment / transaction UUID"
//	@Success		200			{object}	response.successEnvelope{data=model.DashboardPaymentDetailResponse}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/payments/{payment_id} [get]
func (h *DashboardPaymentHandler) GetPayment(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid payment ID format")
		return
	}

	payment, err := h.paymentSvc.GetPayment(c.Request.Context(), caller.MerchantID, paymentID)
	if err != nil {
		h.handlePaymentError(c, err)
		return
	}

	response.OK(c, model.DashboardPaymentDetailResponse{
		PaymentResponse: *payment,
		Timeline:        buildPaymentTimeline(payment),
	})
}

// CreateRefund godoc
//
//	@Summary		Create a refund from the dashboard
//	@Description	Creates a refund for a PAID payment owned by the authenticated merchant.
//	@Description	Reuses the existing RefundService (over-refund protection, idempotency, provider call).
//	@Description	Requires OWNER or ADMIN role. VIEWER receives 403.
//	@Description	`Idempotency-Key` header is required (1–255 chars).
//	@Tags			Dashboard Refunds
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			payment_id		path		string						true	"Payment UUID"
//	@Param			Idempotency-Key	header		string						true	"Idempotency key"
//	@Param			body			body		model.CreateRefundRequest	true	"Refund payload"
//	@Success		201				{object}	response.successEnvelope{data=model.RefundResponse}
//	@Failure		400				{object}	response.errorEnvelope
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		403				{object}	response.errorEnvelope
//	@Failure		404				{object}	response.errorEnvelope
//	@Failure		409				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Failure		503				{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/payments/{payment_id}/refunds [post]
func (h *DashboardPaymentHandler) CreateRefund(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid payment ID format")
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

	resp, err := h.refundSvc.CreateRefundWithIdempotency(c.Request.Context(), caller.MerchantID, paymentID, req, key)
	if err != nil {
		h.handleRefundError(c, err)
		return
	}
	response.Created(c, resp)
}

// CreatePayment godoc
//
//	@Summary		Create dashboard payment
//	@Description	Initiates a new payment for the authenticated merchant from the dashboard.
//	@Description	`Idempotency-Key` header is required (1–255 chars).
//	@Tags			Dashboard Payments
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			Idempotency-Key	header		string						true	"Idempotency key"
//	@Param			body			body		model.CreatePaymentRequest	true	"Payment payload"
//	@Success		201				{object}	response.successEnvelope{data=model.CreatePaymentResponse}
//	@Failure		400				{object}	response.errorEnvelope
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		403				{object}	response.errorEnvelope
//	@Failure		409				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/payments [post]
func (h *DashboardPaymentHandler) CreatePayment(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	var req model.CreatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		response.BadRequest(c, response.CodeInvalidRequest, "Idempotency-Key must be between 1 and 255 characters")
		return
	}

	resp, err := h.paymentSvc.CreatePaymentWithIdempotency(c.Request.Context(), caller.MerchantID, req, key)
	if err != nil {
		h.handleCreatePaymentError(c, err)
		return
	}
	response.Created(c, resp)
}

// buildPaymentTimeline returns timeline events from real timestamps only.
// CREATED always comes from created_at. PAID is included only when paid_at is set.
// Other terminal statuses are not fabricated — no event history table exists yet.
func buildPaymentTimeline(p *model.PaymentResponse) []model.DashboardTimelineEvent {
	events := []model.DashboardTimelineEvent{
		{Type: "CREATED", At: p.CreatedAt},
	}
	if p.PaidAt != nil {
		events = append(events, model.DashboardTimelineEvent{Type: "PAID", At: *p.PaidAt})
	}
	return events
}

func parseDashboardPaymentListFilter(c *gin.Context) (model.TransactionListFilter, map[string]string) {
	errs := make(map[string]string)
	var filter model.TransactionListFilter

	if raw := c.Query("page"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			errs["page"] = "must be an integer >= 1"
		} else {
			filter.Page = v
		}
	}
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > model.MaxLimit {
			errs["limit"] = fmt.Sprintf("must be an integer between 1 and %d", model.MaxLimit)
		} else {
			filter.Limit = v
		}
	}
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
	if raw := c.Query("merchant_order_id"); raw != "" {
		filter.MerchantOrderID = &raw
	}
	if raw := c.Query("payment_method"); raw != "" {
		if !model.SupportedPaymentMethods[raw] {
			errs["payment_method"] = "unsupported payment method"
		} else {
			filter.PaymentMethod = &raw
		}
	}
	if raw := strings.TrimSpace(c.Query("search")); raw != "" {
		filter.Search = &raw
	}

	var createdFrom, createdTo *time.Time
	if raw := c.Query("created_from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs["created_from"] = "must be a valid RFC3339 timestamp"
		} else {
			utc := t.UTC()
			createdFrom = &utc
		}
	}
	if raw := c.Query("created_to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs["created_to"] = "must be a valid RFC3339 timestamp"
		} else {
			utc := t.UTC()
			createdTo = &utc
		}
	}
	if createdFrom != nil && createdTo != nil && !createdFrom.Before(*createdTo) {
		errs["created_from"] = "created_from must be before created_to"
	}
	if _, ok := errs["created_from"]; !ok {
		filter.CreatedFrom = createdFrom
	}
	if _, ok := errs["created_to"]; !ok {
		filter.CreatedTo = createdTo
	}

	return filter, errs
}

func (h *DashboardPaymentHandler) handlePaymentListError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPage):
		response.ValidationError(c, map[string]string{"page": "must be >= 1"})
	case errors.Is(err, service.ErrInvalidLimit):
		response.ValidationError(c, map[string]string{"limit": fmt.Sprintf("must be between 1 and %d", model.MaxLimit)})
	case errors.Is(err, service.ErrInvalidStatus):
		response.ValidationError(c, map[string]string{"status": "invalid transaction status"})
	case errors.Is(err, service.ErrInvalidPaymentMethod):
		response.BadRequest(c, response.CodeInvalidPaymentMethod, "Payment method not supported")
	case errors.Is(err, service.ErrInvalidDateRange):
		response.ValidationError(c, map[string]string{"created_from": "created_from must be before created_to"})
	default:
		slog.Error("dashboard payment list error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}

func (h *DashboardPaymentHandler) handlePaymentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrTransactionNotFound):
		response.NotFound(c, response.CodeTransactionNotFound, "Transaction not found")
	default:
		slog.Error("dashboard payment error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}

func (h *DashboardPaymentHandler) handleCreatePaymentError(c *gin.Context, err error) {
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

	case errors.Is(err, service.ErrProviderTimeout):
		response.PaymentProviderTimeout(c)

	case errors.Is(err, service.ErrProviderFailure):
		response.PaymentProviderError(c, "Payment provider failed to process the request")

	default:
		slog.Error("dashboard payment create error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}

func (h *DashboardPaymentHandler) handleRefundError(c *gin.Context, err error) {
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
	case errors.Is(err, service.ErrRefundProviderUnsupported):
		response.ServiceUnavailable(c, response.CodeRefundProviderError, "Refund provider is not configured")
	case errors.Is(err, service.ErrProviderFailure):
		response.BadGateway(c, response.CodeRefundProviderError, "Refund provider rejected the request")
	case errors.Is(err, service.ErrProviderTimeout):
		response.GatewayTimeout(c, response.CodeRefundProviderTimeout, "Refund provider did not respond in time")
	default:
		slog.Error("dashboard refund error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
