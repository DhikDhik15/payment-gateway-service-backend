package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SimulatorHandler handles the simulated payment gateway creation and mock provider status triggers.
type SimulatorHandler struct {
	db               *pgxpool.Pool
	paymentSvc       service.PaymentService
	webhookSvc       service.WebhookService
	mockSecret       string
	simulatorEnabled bool
}

// NewSimulatorHandler constructs a SimulatorHandler.
func NewSimulatorHandler(
	db *pgxpool.Pool,
	paymentSvc service.PaymentService,
	webhookSvc service.WebhookService,
	mockSecret string,
	simulatorEnabled bool,
) *SimulatorHandler {
	return &SimulatorHandler{
		db:               db,
		paymentSvc:       paymentSvc,
		webhookSvc:       webhookSvc,
		mockSecret:       mockSecret,
		simulatorEnabled: simulatorEnabled,
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

// CreatePayment godoc
//
//	@Summary		Create simulator payment
//	@Description	Creates a simulated payment using the server-side determined active merchant.
//	@Tags			Simulator
//	@Accept			json
//	@Produce		json
//	@Param			Idempotency-Key	header		string						true	"Idempotency key"
//	@Param			body			body		model.CreatePaymentRequest	true	"Payment payload"
//	@Success		201				{object}	response.successEnvelope{data=model.CreatePaymentResponse}
//	@Failure		400				{object}	response.errorEnvelope
//	@Failure		403				{object}	response.errorEnvelope
//	@Failure		409				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments [post]
func (h *SimulatorHandler) CreatePayment(c *gin.Context) {
	if !h.checkEnabled(c) {
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

	// Server-side determined merchant context: get the first active merchant
	var merchantID uuid.UUID
	err := h.db.QueryRow(c.Request.Context(), "SELECT id FROM merchants WHERE status = 'ACTIVE' LIMIT 1").Scan(&merchantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.BadRequest(c, response.CodeMerchantNotFound, "No active merchant found in database. Please create a merchant first.")
			return
		}
		slog.Error("simulator handler: failed to query active merchant", slog.String("error", err.Error()))
		response.InternalServerError(c)
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
//	@Description	Retrieves payment details for the simulator. Public and read-only.
//	@Tags			Simulator
//	@Produce		json
//	@Param			payment_id	path		string	true	"Payment ID"
//	@Success		200			{object}	response.successEnvelope{data=model.PaymentResponse}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments/{payment_id} [get]
func (h *SimulatorHandler) GetPayment(c *gin.Context) {
	if !h.checkEnabled(c) {
		return
	}

	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid payment ID format")
		return
	}

	var p model.PaymentResponse
	var providerNull, providerTxNull, paymentUrlNull *string
	var expiredAtNull, paidAtNull *time.Time

	const q = `
		SELECT id, merchant_order_id, amount, currency, payment_method, provider, provider_transaction_id, payment_url, status, expired_at, paid_at, created_at, updated_at
		FROM transactions
		WHERE id = $1
	`
	err = h.db.QueryRow(c.Request.Context(), q, paymentID).Scan(
		&p.TransactionID,
		&p.MerchantOrderID,
		&p.Amount,
		&p.Currency,
		&p.PaymentMethod,
		&providerNull,
		&providerTxNull,
		&paymentUrlNull,
		&p.Status,
		&expiredAtNull,
		&paidAtNull,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.NotFound(c, response.CodeTransactionNotFound, "Payment not found")
			return
		}
		slog.Error("simulator handler: failed to get payment", slog.String("error", err.Error()))
		response.InternalServerError(c)
		return
	}

	p.Provider = providerNull
	p.ProviderTransactionID = providerTxNull
	p.PaymentURL = paymentUrlNull
	p.ExpiredAt = expiredAtNull
	p.PaidAt = paidAtNull

	response.OK(c, p)
}

// SimulateSuccess godoc
//
//	@Summary		Simulate payment success
//	@Description	Triggers a payment success transition through the mock provider webhook process.
//	@Tags			Simulator
//	@Produce		json
//	@Param			payment_id	path		string	true	"Payment ID"
//	@Success		200			{object}	response.successEnvelope{data=map[string]string}
//	@Failure		400			{object}	response.errorEnvelope
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
//	@Tags			Simulator
//	@Produce		json
//	@Param			payment_id	path		string	true	"Payment ID"
//	@Success		200			{object}	response.successEnvelope{data=map[string]string}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/simulator/payments/{payment_id}/fail [post]
func (h *SimulatorHandler) SimulateFailure(c *gin.Context) {
	h.simulateStatusTransition(c, model.WebhookEventTypePaymentFailed, "FAILED")
}

func (h *SimulatorHandler) simulateStatusTransition(c *gin.Context, eventType model.WebhookEventType, status string) {
	if !h.checkEnabled(c) {
		return
	}

	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid payment ID format")
		return
	}

	var tx model.Transaction
	var providerNull, providerTxNull, paymentUrlNull *string
	var expiredAtNull, paidAtNull *time.Time

	const q = `
		SELECT id, merchant_order_id, amount, currency, payment_method, provider, provider_transaction_id, payment_url, status, expired_at, paid_at, created_at, updated_at
		FROM transactions
		WHERE id = $1
	`
	err = h.db.QueryRow(c.Request.Context(), q, paymentID).Scan(
		&tx.ID,
		&tx.MerchantOrderID,
		&tx.Amount,
		&tx.Currency,
		&tx.PaymentMethod,
		&providerNull,
		&providerTxNull,
		&paymentUrlNull,
		&tx.Status,
		&expiredAtNull,
		&paidAtNull,
		&tx.CreatedAt,
		&tx.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.NotFound(c, response.CodeTransactionNotFound, "Payment not found")
			return
		}
		slog.Error("simulator handler: failed to get payment for transition", slog.String("error", err.Error()))
		response.InternalServerError(c)
		return
	}

	if tx.Status.IsTerminal() {
		response.BadRequest(c, response.CodeInvalidTransactionState, fmt.Sprintf("Payment is already in a terminal state: %s", tx.Status))
		return
	}

	providerTxID := ""
	if providerTxNull != nil {
		providerTxID = *providerTxNull
	}

	// Construct mock webhook payload
	payload := map[string]any{
		"event_id":                 "evt_sim_" + uuid.New().String(),
		"event_type":               string(eventType),
		"provider_transaction_id": providerTxID,
		"merchant_order_id":       tx.MerchantOrderID,
		"status":                  status,
		"amount":                  tx.Amount,
		"currency":                tx.Currency,
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
		slog.Error("simulator handler: failed to process simulated webhook", slog.String("error", err.Error()))
		if errors.Is(err, repository.ErrTransactionNotFound) {
			response.NotFound(c, response.CodeTransactionNotFound, "Transaction not found for this event")
			return
		}
		response.BadRequest(c, response.CodeInvalidRequest, err.Error())
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
