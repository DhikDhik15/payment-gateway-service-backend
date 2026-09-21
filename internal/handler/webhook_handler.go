package handler

import (
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

const headerWebhookSignature = "X-Webhook-Signature"

// WebhookHandler handles inbound payment provider webhook events.
// It does NOT require merchant API key authentication — webhooks originate
// from payment providers, not from merchants.
type WebhookHandler struct {
	webhookSvc service.WebhookService
}

// NewWebhookHandler constructs a WebhookHandler.
func NewWebhookHandler(webhookSvc service.WebhookService) *WebhookHandler {
	return &WebhookHandler{webhookSvc: webhookSvc}
}

// Receive godoc
//
//	@Summary		Receive a payment provider webhook
//	@Description	Receives an inbound event from a payment provider. No merchant API key required. Signature must be a valid HMAC-SHA256 hex digest of the request body, signed with the provider-specific secret.
//	@Tags			webhooks
//	@Accept			json
//	@Produce		json
//	@Param			provider				path		string	true	"Provider name (e.g. mock)"
//	@Param			X-Webhook-Signature		header		string	true	"HMAC-SHA256 signature of the raw request body"
//	@Param			body					body		object	true	"Provider-specific event payload"
//	@Success		200						{object}	response.successEnvelope{data=webhookResponse}
//	@Failure		400						{object}	response.errorEnvelope	"Unknown provider or malformed payload"
//	@Failure		401						{object}	response.errorEnvelope	"Invalid or missing signature"
//	@Failure		404						{object}	response.errorEnvelope	"Transaction not found"
//	@Failure		500						{object}	response.errorEnvelope	"Internal error"
//	@Router			/api/v1/webhooks/providers/{provider} [post]
func (h *WebhookHandler) Receive(c *gin.Context) {
	providerName := strings.ToUpper(c.Param("provider"))
	if providerName == "" {
		response.BadRequest(c, response.CodeInvalidRequest, "Provider name is required")
		return
	}

	// Read raw body so we can pass the exact bytes to signature verification.
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		slog.Error("webhook handler: failed to read request body",
			slog.String("provider", providerName),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	// MOCK uses this header; Midtrans carries its documented signature_key in
	// the notification body. Each parser decides whether an empty header is valid.
	signature := c.GetHeader(headerWebhookSignature)

	result, err := h.webhookSvc.ProcessWebhook(c.Request.Context(), providerName, rawBody, signature)
	if err != nil {
		h.handleServiceError(c, providerName, err)
		return
	}

	response.OK(c, webhookResponse{Status: string(result.Status)})
}

// webhookResponse is the data payload returned on webhook receipt.
type webhookResponse struct {
	Status string `json:"status" example:"PROCESSED"`
}

// handleServiceError maps WebhookService errors to HTTP responses.
func (h *WebhookHandler) handleServiceError(c *gin.Context, provider string, err error) {
	switch {
	case errors.Is(err, service.ErrWebhookUnknownProvider):
		response.BadRequest(c, response.CodeInvalidRequest, "Unknown payment provider: "+provider)

	case errors.Is(err, service.ErrWebhookInvalidSignature):
		response.Unauthorized(c, response.CodeUnauthorized, "Invalid webhook signature")

	case errors.Is(err, service.ErrWebhookMalformedPayload):
		response.BadRequest(c, response.CodeInvalidRequest, "Malformed webhook payload")

	case errors.Is(err, service.ErrWebhookMissingFields):
		response.ValidationError(c, map[string]string{
			"payload": "Missing required fields: event_id, event_type, provider_transaction_id",
		})

	case errors.Is(err, repository.ErrTransactionNotFound):
		response.NotFound(c, response.CodeTransactionNotFound, "Transaction not found for this event")

	case errors.Is(err, repository.ErrRefundNotFound), errors.Is(err, service.ErrRefundNotFound):
		response.NotFound(c, response.CodeRefundNotFound, "Refund not found for this event")

	case errors.Is(err, service.ErrWebhookProviderTxMismatch):
		response.Conflict(c, response.CodeInvalidRequest, "Provider transaction ID mismatch")

	default:
		slog.Error("webhook handler: unexpected error",
			slog.String("provider", provider),
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
