package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type DashboardOrderPaymentHandler struct {
	payments service.OrderPaymentService
}

func NewDashboardOrderPaymentHandler(payments service.OrderPaymentService) *DashboardOrderPaymentHandler {
	return &DashboardOrderPaymentHandler{payments: payments}
}

// CreatePayment godoc
//
// @Summary Initiate payment for an existing Order
// @Description Amount, currency, merchant identity and merchant_order_id are derived from the authenticated Order. The body accepts only payment_method.
// @Tags Dashboard Orders
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param order_id path string true "Order UUID"
// @Param Idempotency-Key header string true "Merchant-scoped payment initiation key"
// @Param request body model.CreateOrderPaymentRequest true "Payment method only"
// @Success 201 {object} response.successEnvelope{data=model.CreatePaymentResponse}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 403 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 409 {object} response.errorEnvelope
// @Failure 502 {object} response.errorEnvelope
// @Failure 504 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/orders/{order_id}/payments [post]
func (h *DashboardOrderPaymentHandler) CreatePayment(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	orderID, err := uuid.Parse(c.Param("order_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid order ID format")
		return
	}

	var req model.CreateOrderPaymentRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		response.ValidationError(c, map[string]string{"_": "Expected a single JSON object"})
		return
	}

	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		response.BadRequest(c, response.CodeInvalidRequest, "Idempotency-Key must be between 1 and 255 characters")
		return
	}

	payment, err := h.payments.CreatePaymentForOrder(c.Request.Context(), caller.MerchantID, orderID, req, key)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrOrderNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Order not found")
		case errors.Is(err, service.ErrOrderPaymentNotAvailable), errors.Is(err, service.ErrOrderPaymentConflict), errors.Is(err, repository.ErrOrderPaymentConflict):
			response.Conflict(c, response.CodeInvalidTransactionState, "Order already has an active or paid payment, or is not payable")
		case errors.Is(err, service.ErrInvalidPaymentMethod):
			response.BadRequest(c, response.CodeInvalidPaymentMethod, "Payment method not supported. Supported: QRIS")
		case errors.Is(err, service.ErrInvalidCurrency):
			response.BadRequest(c, response.CodeInvalidCurrency, "Order currency is not supported for payment. Supported: IDR")
		case errors.Is(err, service.ErrIdempotencyKeyReused):
			response.Conflict(c, response.CodeIdempotencyKeyReused, "Idempotency-Key was used with a different Order payment request")
		case errors.Is(err, service.ErrIdempotencyInProgress):
			response.Conflict(c, response.CodeIdempotencyInProgress, "A request with this Idempotency-Key is already in progress")
		case errors.Is(err, service.ErrProviderTimeout):
			response.PaymentProviderTimeout(c)
		case errors.Is(err, service.ErrProviderFailure):
			response.PaymentProviderError(c, "Payment provider failed to process the request")
		default:
			slog.Error("dashboard Order payment creation failed",
				slog.String("merchant_id", caller.MerchantID.String()),
				slog.String("order_id", orderID.String()),
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}
	response.Created(c, payment)
}

// ListPayments godoc
//
// @Summary List payment attempts for an existing Order
// @Description Read-only payment history for one merchant-scoped Order, newest first. Never creates, mutates, reconciles, simulates, or cancels anything.
// @Tags Dashboard Orders
// @Produce json
// @Security BearerAuth
// @Param order_id path string true "Order UUID"
// @Success 200 {object} response.successEnvelope{data=[]model.PaymentResponse}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/orders/{order_id}/payments [get]
func (h *DashboardOrderPaymentHandler) ListPayments(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	orderID, err := uuid.Parse(c.Param("order_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid order ID format")
		return
	}

	payments, err := h.payments.ListPaymentsForOrder(c.Request.Context(), caller.MerchantID, orderID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrOrderNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Order not found")
		default:
			slog.Error("dashboard Order payment history failed",
				slog.String("merchant_id", caller.MerchantID.String()),
				slog.String("order_id", orderID.String()),
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, payments)
}
