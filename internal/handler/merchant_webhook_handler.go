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

// MerchantWebhookHandler handles merchant outbound webhook configuration and delivery APIs.
type MerchantWebhookHandler struct {
	svc service.MerchantWebhookConfigService
}

// NewMerchantWebhookHandler constructs a MerchantWebhookHandler.
func NewMerchantWebhookHandler(svc service.MerchantWebhookConfigService) *MerchantWebhookHandler {
	return &MerchantWebhookHandler{svc: svc}
}

// UpsertWebhook godoc
//
//	@Summary		Configure merchant webhook
//	@Description	Creates or replaces the merchant's outbound webhook endpoint.
//	@Description	Returns the plaintext signing secret **once**. Store it securely.
//	@Description	One active endpoint per merchant. Secret is AES-256-GCM encrypted at rest.
//	@Tags			merchant-webhooks
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id		path		string								true	"Merchant UUID"
//	@Param			body	body		model.UpsertMerchantWebhookRequest	true	"Webhook URL"
//	@Success		201		{object}	response.successEnvelope{data=model.MerchantWebhookConfigWithSecretResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		403		{object}	response.errorEnvelope
//	@Failure		404		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook [post]
func (h *MerchantWebhookHandler) UpsertWebhook(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	var req model.UpsertMerchantWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	result, err := h.svc.Upsert(c.Request.Context(), merchantID, req)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.Created(c, result)
}

// GetWebhook godoc
//
//	@Summary		Get merchant webhook config
//	@Description	Returns the webhook configuration. Never includes the signing secret.
//	@Tags			merchant-webhooks
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Merchant UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookConfigResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook [get]
func (h *MerchantWebhookHandler) GetWebhook(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	result, err := h.svc.Get(c.Request.Context(), merchantID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// RotateWebhookSecret godoc
//
//	@Summary		Rotate webhook signing secret
//	@Description	Generates a new signing secret. Old secret stops being used for new deliveries immediately.
//	@Description	Plaintext secret returned once only.
//	@Tags			merchant-webhooks
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Merchant UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookConfigWithSecretResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook/rotate [post]
func (h *MerchantWebhookHandler) RotateWebhookSecret(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	result, err := h.svc.RotateSecret(c.Request.Context(), merchantID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// DisableWebhook godoc
//
//	@Summary		Disable merchant webhook
//	@Description	Soft-disables the webhook (ACTIVE → DISABLED). Historical deliveries remain.
//	@Description	No new deliveries are enqueued while disabled.
//	@Tags			merchant-webhooks
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Merchant UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.MerchantWebhookConfigResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook [delete]
func (h *MerchantWebhookHandler) DisableWebhook(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	result, err := h.svc.Disable(c.Request.Context(), merchantID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// ListDeliveries godoc
//
//	@Summary		List webhook deliveries
//	@Description	Lists outbound webhook delivery attempts for the merchant (audit / outbox).
//	@Tags			merchant-webhooks
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id		path		string	true	"Merchant UUID"
//	@Param			page	query		int		false	"Page (default 1)"
//	@Param			limit	query		int		false	"Limit (default 20, max 100)"
//	@Success		200		{object}	response.successEnvelope{data=[]model.MerchantWebhookDeliveryResponse}
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		403		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook/deliveries [get]
func (h *MerchantWebhookHandler) ListDeliveries(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	items, total, err := h.svc.ListDeliveries(c.Request.Context(), merchantID, page, limit)
	if err != nil {
		h.handleError(c, err)
		return
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
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
//	@Tags			merchant-webhooks
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id				path		string	true	"Merchant UUID"
//	@Param			delivery_id		path		string	true	"Delivery UUID"
//	@Success		200				{object}	response.successEnvelope{data=model.MerchantWebhookDeliveryResponse}
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		403				{object}	response.errorEnvelope
//	@Failure		404				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook/deliveries/{delivery_id} [get]
func (h *MerchantWebhookHandler) GetDelivery(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	deliveryID, err := uuid.Parse(c.Param("delivery_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid delivery ID format")
		return
	}
	result, err := h.svc.GetDelivery(c.Request.Context(), merchantID, deliveryID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

// RetryDelivery godoc
//
//	@Summary		Manually retry a webhook delivery
//	@Description	Re-queues FAILED, DEAD, or PENDING deliveries for immediate attempt.
//	@Description	DELIVERED and PROCESSING cannot be retried.
//	@Tags			merchant-webhooks
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id				path		string	true	"Merchant UUID"
//	@Param			delivery_id		path		string	true	"Delivery UUID"
//	@Success		200				{object}	response.successEnvelope{data=model.MerchantWebhookDeliveryResponse}
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		403				{object}	response.errorEnvelope
//	@Failure		404				{object}	response.errorEnvelope
//	@Failure		409				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id}/webhook/deliveries/{delivery_id}/retry [post]
func (h *MerchantWebhookHandler) RetryDelivery(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}
	deliveryID, err := uuid.Parse(c.Param("delivery_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid delivery ID format")
		return
	}
	result, err := h.svc.RetryDelivery(c.Request.Context(), merchantID, deliveryID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *MerchantWebhookHandler) handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrWebhookConfigNotFound),
		errors.Is(err, service.ErrWebhookDeliveryNotFound):
		response.NotFound(c, response.CodeResourceNotFound, err.Error())
	case errors.Is(err, service.ErrWebhookDestinationBlocked):
		response.BadRequest(c, response.CodeWebhookDestinationBlocked, "Webhook destination is not allowed")
	case errors.Is(err, service.ErrWebhookInvalidURL):
		response.ValidationError(c, map[string]string{"url": "must be a valid https URL"})
	case errors.Is(err, service.ErrWebhookDeliveryNotRetryable):
		response.Conflict(c, response.CodeInvalidRequest, "Delivery cannot be retried in its current status")
	case errors.Is(err, repository.ErrMerchantNotFound):
		response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
	default:
		slog.Error("merchant webhook handler error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
