package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type DashboardOrderHandler struct{ orders service.OrderService }

func NewDashboardOrderHandler(orders service.OrderService) *DashboardOrderHandler {
	return &DashboardOrderHandler{orders: orders}
}

// CreateOrder godoc
//
// @Summary Create an order from product snapshots
// @Description Merchant comes from the Bearer JWT. Duplicate product IDs are rejected. Prices come from active merchant products, never the body. Identical retries return the original order (200); a different body with the same key returns 409. No payment is created.
// @Tags Dashboard Orders
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param Idempotency-Key header string true "Durable merchant-scoped idempotency key (1-255 printable ASCII characters)"
// @Param request body model.CreateOrderRequest true "Product IDs and quantities only; unknown fields rejected"
// @Success 201 {object} response.successEnvelope{data=model.Order}
// @Success 200 {object} response.successEnvelope{data=model.Order}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 403 {object} response.errorEnvelope
// @Failure 409 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/orders [post]
func (h *DashboardOrderHandler) CreateOrder(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	// Unlike Gin's default JSON binder, reject unknown monetary and merchant
	// fields at every nesting level (and reject a second JSON document).
	var req model.CreateOrderRequest
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
	order, replay, err := h.orders.CreateOrder(c.Request.Context(), caller.MerchantID, c.GetHeader("Idempotency-Key"), req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrOrderKey):
			response.ValidationError(c, map[string]string{"idempotency_key": "Must contain 1-255 printable ASCII characters without surrounding whitespace"})
		case errors.Is(err, service.ErrInvalidOrder):
			response.ValidationError(c, map[string]string{"items": "Provide 1-100 distinct product IDs with quantities between 1 and 1000"})
		case errors.Is(err, service.ErrOrderProductMissing):
			response.BadRequest(c, response.CodeInvalidRequest, "Product not found or inactive")
		case errors.Is(err, service.ErrOrderCurrency):
			response.BadRequest(c, response.CodeInvalidCurrency, "Order products must have the same currency")
		case errors.Is(err, service.ErrOrderOverflow):
			response.BadRequest(c, response.CodeInvalidAmount, "Order amount exceeds supported range")
		case errors.Is(err, service.ErrOrderKeyConflict):
			response.Conflict(c, response.CodeIdempotencyKeyReused, "Idempotency-Key was used with a different request")
		default:
			slog.Error("dashboard order create error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("request_id", c.GetString(response.ContextKey)), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	if replay {
		response.OK(c, order)
	} else {
		response.Created(c, order)
	}
}

// GetOrder godoc
//
// @Summary Get a merchant order
// @Description Other merchants' orders are indistinguishable from missing orders (404).
// @Tags Dashboard Orders
// @Produce json
// @Security BearerAuth
// @Param order_id path string true "Order UUID"
// @Success 200 {object} response.successEnvelope{data=model.Order}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/orders/{order_id} [get]
func (h *DashboardOrderHandler) GetOrder(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("order_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid order id")
		return
	}
	order, err := h.orders.GetOrder(c.Request.Context(), caller.MerchantID, id)
	if errors.Is(err, repository.ErrOrderNotFound) {
		response.NotFound(c, response.CodeResourceNotFound, "Order not found")
	} else if err != nil {
		slog.Error("dashboard order get error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
		response.InternalServerError(c)
	} else {
		response.OK(c, order)
	}
}

// ListOrders godoc
//
// @Summary List merchant orders
// @Description Newest first (created_at and ID descending), scoped to the authenticated merchant.
// @Tags Dashboard Orders
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page (default 1)"
// @Param limit query int false "Page size (default 20, max 100)"
// @Success 200 {object} response.successEnvelope{data=[]model.Order}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/orders [get]
func (h *DashboardOrderHandler) ListOrders(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	page, errPage := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, errLimit := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if errPage != nil || errLimit != nil || page < 1 || limit < 1 || limit > model.MaxLimit {
		response.ValidationError(c, map[string]string{"page": "Must be a positive integer", "limit": "Must be between 1 and 100"})
		return
	}
	result, err := h.orders.ListOrders(c.Request.Context(), caller.MerchantID, page, limit)
	if errors.Is(err, service.ErrInvalidOrder) {
		response.ValidationError(c, map[string]string{"page": "Page is too large"})
	} else if err != nil {
		slog.Error("dashboard order list error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
		response.InternalServerError(c)
	} else {
		response.List(c, result.Orders, response.Pagination{Page: result.Page, Limit: result.Limit, Total: result.Total, TotalPages: result.TotalPages})
	}
}
