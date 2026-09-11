package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// MerchantHandler handles HTTP requests related to merchant management.
type MerchantHandler struct {
	merchantSvc service.MerchantService
}

// NewMerchantHandler constructs a MerchantHandler.
func NewMerchantHandler(merchantSvc service.MerchantService) *MerchantHandler {
	return &MerchantHandler{merchantSvc: merchantSvc}
}

// Create godoc
//
//	@Summary		Register a new merchant
//	@Description	Creates a merchant account and returns an API key. The API secret is only shown once and is not stored in plain text.
//	@Tags			merchants
//	@Accept			json
//	@Produce		json
//	@Param			body	body		model.CreateMerchantRequest		true	"Merchant registration payload"
//	@Success		201		{object}	response.successEnvelope{data=model.CreateMerchantResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		409		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/merchants [post]
func (h *MerchantHandler) Create(c *gin.Context) {
	var req model.CreateMerchantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	result, err := h.merchantSvc.CreateMerchant(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, service.ErrDuplicateMerchantCode) {
			response.Conflict(c, response.CodeDuplicateMerchantCode, "Merchant code already exists")
			return
		}
		slog.Error("merchant create: unexpected error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.Created(c, result)
}

// GetByID godoc
//
//	@Summary		Get merchant by ID
//	@Description	Returns public merchant information. API secret is never returned.
//	@Tags			merchants
//	@Produce		json
//	@Param			id	path		string	true	"Merchant UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.GetMerchantResponse}
//	@Failure		400	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/merchants/{id} [get]
func (h *MerchantHandler) GetByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid merchant ID format")
		return
	}

	result, err := h.merchantSvc.GetMerchant(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
			return
		}
		slog.Error("merchant get: unexpected error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("merchant_id", id.String()),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.OK(c, result)
}
