package handler

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DashboardWebhookHandler serves webhook configuration and delivery APIs for the dashboard.
type DashboardWebhookHandler struct {
	svc service.MerchantWebhookConfigService
}

// NewDashboardWebhookHandler constructs a DashboardWebhookHandler.
func NewDashboardWebhookHandler(svc service.MerchantWebhookConfigService) *DashboardWebhookHandler {
	return &DashboardWebhookHandler{svc: svc}
}

// GetWebhookConfig godoc
//
//	@Summary		Get webhook configuration
//	@Description	Returns the merchant outbound webhook config. Never includes signing secret.
//	@Tags			Dashboard Webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookConfigResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhook-config [get]
func (h *DashboardWebhookHandler) GetWebhookConfig(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	result, err := h.svc.Get(c.Request.Context(), caller.MerchantID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// UpsertWebhookConfig godoc
//
//	@Summary		Create or replace webhook configuration
//	@Description	Upserts the outbound webhook endpoint. Returns plaintext signing secret **once**.
//	@Description	Requires OWNER or ADMIN. VIEWER receives 403.
//	@Tags			Dashboard Webhooks
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		model.UpsertMerchantWebhookRequest	true	"Webhook URL"
//	@Success		201		{object}	response.successEnvelope{data=model.MerchantWebhookConfigWithSecretResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		403		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhook-config [put]
func (h *DashboardWebhookHandler) UpsertWebhookConfig(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	var req model.UpsertMerchantWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	result, err := h.svc.Upsert(c.Request.Context(), caller.MerchantID, req)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.Created(c, result)
}

// RotateWebhookSecret godoc
//
//	@Summary		Rotate webhook signing secret
//	@Description	Generates a new signing secret (returned once). Requires OWNER or ADMIN.
//	@Tags			Dashboard Webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookConfigWithSecretResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhook-config/rotate [post]
func (h *DashboardWebhookHandler) RotateWebhookSecret(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	result, err := h.svc.RotateSecret(c.Request.Context(), caller.MerchantID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// DisableWebhookConfig godoc
//
//	@Summary		Disable webhook configuration
//	@Description	Soft-disables outbound webhooks. Requires OWNER or ADMIN.
//	@Tags			Dashboard Webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookConfigResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhook-config [delete]
func (h *DashboardWebhookHandler) DisableWebhookConfig(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	result, err := h.svc.Disable(c.Request.Context(), caller.MerchantID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// ListDeliveries godoc
//
//	@Summary		List webhook deliveries
//	@Description	Lists outbound webhook delivery / outbox records for the authenticated merchant.
//	@Tags			Dashboard Webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page	query		int	false	"Page (default 1)"
//	@Param			limit	query		int	false	"Limit (default 20, max 100)"
//	@Success		200		{object}	response.successEnvelope{data=[]model.MerchantWebhookDeliveryResponse}
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhooks [get]
func (h *DashboardWebhookHandler) ListDeliveries(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > model.MaxLimit {
		limit = model.DefaultLimit
	}
	items, total, err := h.svc.ListDeliveries(c.Request.Context(), caller.MerchantID, page, limit)
	if err != nil {
		h.handleError(c, err)
		return
	}
	if items == nil {
		items = []model.MerchantWebhookDeliveryResponse{}
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}
	response.List(c, items, response.Pagination{Page: page, Limit: limit, Total: total, TotalPages: totalPages})
}

// GetDelivery godoc
//
//	@Summary		Get webhook delivery
//	@Description	Returns one delivery belonging to the authenticated merchant. Never exposes signing secrets.
//	@Tags			Dashboard Webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Delivery UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookDeliveryResponse}
//	@Failure		400	{object}	response.errorEnvelope
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhooks/{id} [get]
func (h *DashboardWebhookHandler) GetDelivery(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	deliveryID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid delivery ID format")
		return
	}
	result, err := h.svc.GetDelivery(c.Request.Context(), caller.MerchantID, deliveryID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// RetryDelivery godoc
//
//	@Summary		Retry webhook delivery
//	@Description	Re-queues a FAILED/DEAD/PENDING delivery. Requires OWNER or ADMIN.
//	@Tags			Dashboard Webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Delivery UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookDeliveryResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		409	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/webhooks/{id}/retry [post]
func (h *DashboardWebhookHandler) RetryDelivery(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	deliveryID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid delivery ID format")
		return
	}
	result, err := h.svc.RetryDelivery(c.Request.Context(), caller.MerchantID, deliveryID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *DashboardWebhookHandler) handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrWebhookConfigNotFound),
		errors.Is(err, service.ErrWebhookDeliveryNotFound):
		response.NotFound(c, response.CodeResourceNotFound, "Resource not found")
	case errors.Is(err, service.ErrWebhookDestinationBlocked):
		response.BadRequest(c, response.CodeWebhookDestinationBlocked, "Webhook destination is not allowed")
	case errors.Is(err, service.ErrWebhookInvalidURL):
		response.ValidationError(c, map[string]string{"url": "must be a valid https URL"})
	case errors.Is(err, service.ErrWebhookDeliveryNotRetryable):
		response.Conflict(c, response.CodeInvalidRequest, "Delivery cannot be retried in its current status")
	case errors.Is(err, repository.ErrMerchantNotFound):
		response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
	default:
		slog.Error("dashboard webhook error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
