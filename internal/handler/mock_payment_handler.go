package handler

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// MockPaymentHandler exposes the customer-facing development provider API.
// It is intentionally separate from the authenticated merchant simulator.
type MockPaymentHandler struct {
	store    repository.MockPaymentRepository
	webhooks service.WebhookService
	secret   string
}

func NewMockPaymentHandler(store repository.MockPaymentRepository, webhooks service.WebhookService, secret string) *MockPaymentHandler {
	return &MockPaymentHandler{store, webhooks, secret}
}

type publicMockPayment struct {
	Reference             string                  `json:"reference"`
	Amount                int64                   `json:"amount"`
	Currency              string                  `json:"currency"`
	PaymentMethod         string                  `json:"payment_method"`
	Status                model.TransactionStatus `json:"status"`
	ExpiredAt             interface{}             `json:"expired_at"`
	ProviderTransactionID string                  `json:"provider_transaction_id"`
	QRISPayload           string                  `json:"qris_payload,omitempty"`
}

func (h *MockPaymentHandler) Get(c *gin.Context) {
	p, ok := h.find(c)
	if !ok {
		return
	}
	response.OK(c, h.public(p))
}
func (h *MockPaymentHandler) Success(c *gin.Context) {
	h.transition(c, model.TransactionStatusPaid, model.WebhookEventTypePaymentPaid)
}
func (h *MockPaymentHandler) Fail(c *gin.Context) {
	h.transition(c, model.TransactionStatusFailed, model.WebhookEventTypePaymentFailed)
}
func (h *MockPaymentHandler) find(c *gin.Context) (*model.MockPayment, bool) {
	id := strings.TrimSpace(c.Param("identifier"))
	if id == "" {
		response.NotFound(c, response.CodeTransactionNotFound, "Payment not found")
		return nil, false
	}
	p, err := h.store.FindByPublicID(c.Request.Context(), id)
	if errors.Is(err, repository.ErrMockPaymentNotFound) {
		response.NotFound(c, response.CodeTransactionNotFound, "Payment not found")
		return nil, false
	}
	if err != nil {
		response.InternalServerError(c)
		return nil, false
	}
	return p, true
}
func (h *MockPaymentHandler) public(p *model.MockPayment) publicMockPayment {
	out := publicMockPayment{Reference: p.MerchantOrderID, Amount: p.Amount, Currency: p.Currency, PaymentMethod: p.PaymentMethod, Status: p.Status, ExpiredAt: p.ExpiredAt, ProviderTransactionID: p.ProviderTransactionID}
	if p.PaymentMethod == "QRIS" {
		out.QRISPayload = "PAYGATE-MOCK-QRIS:" + p.PublicID
	}
	return out
}
func (h *MockPaymentHandler) transition(c *gin.Context, to model.TransactionStatus, eventType model.WebhookEventType) {
	p, ok := h.find(c)
	if !ok {
		return
	}
	previousStatus := p.Status
	updated, err := h.store.Transition(c.Request.Context(), p.PublicID, to)
	if errors.Is(err, repository.ErrMockPaymentExpired) {
		response.BadRequest(c, response.CodeInvalidTransactionState, "Payment has expired")
		return
	}
	if errors.Is(err, repository.ErrMockPaymentInvalidState) {
		response.BadRequest(c, response.CodeInvalidTransactionState, "Payment is already in a terminal state")
		return
	}
	if err != nil {
		response.InternalServerError(c)
		return
	}
	// An already-successful request is safe: provider state is unchanged and
	// webhook processing is not duplicated. A different terminal transition was
	// rejected by the repository above.
	if updated.Status == to && previousStatus == to {
		response.OK(c, h.public(updated))
		return
	}
	payload, _ := json.Marshal(map[string]any{"event_id": "evt_mock_" + uuid.NewString(), "event_type": string(eventType), "provider_transaction_id": updated.ProviderTransactionID, "merchant_order_id": updated.MerchantOrderID, "status": string(to), "amount": updated.Amount, "currency": updated.Currency})
	if _, err = h.webhooks.ProcessWebhook(c.Request.Context(), "MOCK", payload, service.SignMockWebhookPayload(payload, h.secret)); err != nil {
		response.InternalServerError(c)
		return
	}
	response.OK(c, h.public(updated))
}
