package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SimulatorHandler handles the simulated payment gateway creation and mock
// provider status triggers.
//
// Phase 8D.1 containment model:
//   - Routes are registered behind middleware.Auth (X-API-Key) in main.go.
//   - The simulator only serves the merchant configured via
//     SIMULATOR_MERCHANT_ID (startup-validated allowlist).
//   - Every read/write is scoped to the AUTHENTICATED merchant from the Gin
//     context — never to a client-supplied merchant_id — so the simulator can
//     never touch another tenant's transactions.
//   - Disabled by default (PAYMENT_SIMULATOR_ENABLED=false) and rejected at
//     startup when APP_ENV=production (validated in config.Load).
type SimulatorHandler struct {
	paymentSvc          service.PaymentService
	webhookSvc          service.WebhookService
	mockSecret          string
	simulatorEnabled    bool
	simulatorMerchantID uuid.UUID
}

// NewSimulatorHandler constructs a SimulatorHandler.
// simulatorMerchantID must be set when simulatorEnabled is true (validated at config load).
func NewSimulatorHandler(
	paymentSvc service.PaymentService,
	webhookSvc service.WebhookService,
	mockSecret string,
	simulatorEnabled bool,
	simulatorMerchantID uuid.UUID,
) *SimulatorHandler {
	return &SimulatorHandler{
		paymentSvc:          paymentSvc,
		webhookSvc:          webhookSvc,
		mockSecret:          mockSecret,
		simulatorEnabled:    simulatorEnabled,
		simulatorMerchantID: simulatorMerchantID,
	}
}

// checkEnabled is a helper to abort if the simulator is disabled.
func (h *SimulatorHandler) checkEnabled(c *gin.Context) bool {
	if !h.simulatorEnabled {
		response.Forbidden(c, response.CodeForbidden, "Simulator is disabled")
		return false
	}
	return true
}

// resolveSimulatedMerchant authorizes the request and returns the merchant ID
// that ALL simulator reads/writes must be scoped to. It fails closed:
//
//  1. Simulator disabled → 403 (checked first).
//  2. SIMULATOR_MERCHANT_ID unset → 500 (wiring bug: config guarantees it).
//  3. No authenticated merchant in context → 500 (wiring bug: the route must
//     be registered behind middleware.Auth, mirroring authorizeForMerchant).
//  4. Authenticated merchant ≠ configured simulator merchant → 403
//     (allowlist — another tenant's key can never drive the simulator).
func (h *SimulatorHandler) resolveSimulatedMerchant(c *gin.Context) (uuid.UUID, bool) {
	if !h.checkEnabled(c) {
		return uuid.Nil, false
	}
	if h.simulatorMerchantID == uuid.Nil {
		slog.Error("simulator handler: SIMULATOR_MERCHANT_ID is not configured",
			slog.String("request_id", c.GetString(response.ContextKey)),
		)
		response.InternalServerError(c)
		return uuid.Nil, false
	}
	m := middleware.MerchantFromContext(c)
	if m == nil {
		slog.Error("simulator handler: no authenticated merchant in context (route must use middleware.Auth)",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("path", c.Request.URL.Path),
		)
		response.InternalServerError(c)
		return uuid.Nil, false
	}
	if m.ID != h.simulatorMerchantID {
		response.Forbidden(c, response.CodeForbidden, "Simulator is not available for this merchant")
		return uuid.Nil, false
	}
	return m.ID, true
}

// CreatePayment godoc
//
//	@Summary		Create simulator payment
//	@Description	Creates a simulated payment for the AUTHENTICATED merchant (X-API-Key).
//	@Description	Requires PAYMENT_SIMULATOR_ENABLED=true, and the API key must belong to the
//	@Description	merchant configured via SIMULATOR_MERCHANT_ID. The client must not send
//	@Description	merchant_id — identity comes only from the authenticated context.
//	@Tags			Simulator
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			Idempotency-Key	header		string						true	"Idempotency key"
//	@Param			body			body		model.CreatePaymentRequest	true	"Payment payload"
//	@Success		201				{object}	response.successEnvelope{data=model.CreatePaymentResponse}
//	@Failure		400				{object}	response.errorEnvelope
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		403				{object}	response.errorEnvelope
//	@Failure		409				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments [post]
func (h *SimulatorHandler) CreatePayment(c *gin.Context) {
	merchantID, ok := h.resolveSimulatedMerchant(c)
	if !ok {
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

	resp, err := h.paymentSvc.CreatePaymentWithIdempotency(c.Request.Context(), merchantID, req, key)
	if err != nil {
		h.handleCreatePaymentError(c, err)
		return
	}
	response.Created(c, resp)
}

// GetPayment godoc
//
//	@Summary		Get simulator payment detail
//	@Description	Retrieves payment details scoped to the authenticated simulator merchant (X-API-Key).
//	@Description	A payment belonging to another merchant is reported as 404 (no existence leak).
//	@Tags			Simulator
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			payment_id	path		string	true	"Payment ID"
//	@Success		200			{object}	response.successEnvelope{data=model.PaymentResponse}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		403			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments/{payment_id} [get]
func (h *SimulatorHandler) GetPayment(c *gin.Context) {
	merchantID, ok := h.resolveSimulatedMerchant(c)
	if !ok {
		return
	}

	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid payment ID format")
		return
	}

	// Tenant-scoped read: the service adds a merchant_id predicate, so another
	// merchant's payment is indistinguishable from a missing one (404).
	p, err := h.paymentSvc.GetPayment(c.Request.Context(), merchantID, paymentID)
	if err != nil {
		if errors.Is(err, repository.ErrTransactionNotFound) {
			response.NotFound(c, response.CodeTransactionNotFound, "Payment not found")
			return
		}
		slog.Error("simulator handler: failed to get payment",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.OK(c, *p)
}

// SimulateSuccess godoc
//
//	@Summary		Simulate payment success
//	@Description	Triggers a payment success transition through the mock provider webhook process.
//	@Description	Scoped to the authenticated simulator merchant (X-API-Key); other tenants' payments are reported as 404.
//	@Tags			Simulator
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			payment_id	path		string	true	"Payment ID"
//	@Success		200			{object}	response.successEnvelope{data=map[string]string}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		403			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments/{payment_id}/success [post]
func (h *SimulatorHandler) SimulateSuccess(c *gin.Context) {
	h.simulateStatusTransition(c, model.WebhookEventTypePaymentPaid, "PAID")
}

// SimulateFailure godoc
//
//	@Summary		Simulate payment failure
//	@Description	Triggers a payment failure transition through the mock provider webhook process.
//	@Description	Scoped to the authenticated simulator merchant (X-API-Key); other tenants' payments are reported as 404.
//	@Tags			Simulator
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			payment_id	path		string	true	"Payment ID"
//	@Success		200			{object}	response.successEnvelope{data=map[string]string}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		403			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments/{payment_id}/fail [post]
func (h *SimulatorHandler) SimulateFailure(c *gin.Context) {
	h.simulateStatusTransition(c, model.WebhookEventTypePaymentFailed, "FAILED")
}

func (h *SimulatorHandler) simulateStatusTransition(c *gin.Context, eventType model.WebhookEventType, status string) {
	merchantID, ok := h.resolveSimulatedMerchant(c)
	if !ok {
		return
	}

	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid payment ID format")
		return
	}

	// Tenant-scoped read (same contract as GetPayment): a payment belonging
	// to another merchant yields ErrTransactionNotFound → 404, so the
	// transition below can never run against another tenant's transaction.
	p, err := h.paymentSvc.GetPayment(c.Request.Context(), merchantID, paymentID)
	if err != nil {
		if errors.Is(err, repository.ErrTransactionNotFound) {
			response.NotFound(c, response.CodeTransactionNotFound, "Payment not found")
			return
		}
		slog.Error("simulator handler: failed to get payment for transition",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	if p.Status.IsTerminal() {
		response.BadRequest(c, response.CodeInvalidTransactionState, fmt.Sprintf("Payment is already in a terminal state: %s", p.Status))
		return
	}

	providerTxID := ""
	if p.ProviderTransactionID != nil {
		providerTxID = *p.ProviderTransactionID
	}

	// Construct mock webhook payload
	payload := map[string]any{
		"event_id":                "evt_sim_" + uuid.New().String(),
		"event_type":              string(eventType),
		"provider_transaction_id": providerTxID,
		"merchant_order_id":       p.MerchantOrderID,
		"status":                  status,
		"amount":                  p.Amount,
		"currency":                p.Currency,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		response.InternalServerError(c)
		return
	}

	// Compute signature
	signature := service.SignMockWebhookPayload(payloadBytes, h.mockSecret)

	// Call WebhookService directly to execute standard transition and outbox logic.
	_, err = h.webhookSvc.ProcessWebhook(c.Request.Context(), "MOCK", payloadBytes, signature)
	if err != nil {
		slog.Error("simulator handler: failed to process simulated webhook",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("failure", "simulator_webhook_transition_failed"),
		)
		if errors.Is(err, repository.ErrTransactionNotFound) {
			response.NotFound(c, response.CodeTransactionNotFound, "Transaction not found for this event")
			return
		}
		// Never return provider/database diagnostics to the simulator caller.
		// The operational log retains the error for investigation; the HTTP
		// response remains stable and free of SQL/provider details.
		response.BadRequest(c, response.CodeInvalidRequest, "Simulator transition could not be processed")
		return
	}

	response.OK(c, gin.H{"status": status})
}

func (h *SimulatorHandler) handleCreatePaymentError(c *gin.Context, err error) {
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
		slog.Error("simulator payment create error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
