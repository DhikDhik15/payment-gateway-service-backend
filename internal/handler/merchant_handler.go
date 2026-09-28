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
//	@Summary		[FROZEN] Legacy merchant registration (admin)
//	@Description	**FROZEN since Phase 8D.3 — always returns 409 LEGACY_CREDENTIAL_CREATION_DISABLED.**
//	@Description	Creation of new row-level legacy plaintext credentials is permanently disabled; the endpoint is retained only so existing clients receive a stable, documented error instead of 404.
//	@Description	Provision new tenants with POST /api/v1/admin/onboarding/merchants (atomic merchant + OWNER + Phase 5C credential). Existing tenants migrate via POST /api/v1/dashboard/legacy-credential/migrate.
//	@Description	Requires X-Admin-Key. Merchant API keys and dashboard JWTs are NOT accepted.
//	@Description	The 201 shape below is documentary only — the endpoint never succeeds.
//	@Tags			merchants
//	@Accept			json
//	@Produce		json
//	@Param			X-Admin-Key	header		string						true	"Ops admin key (NOT a merchant API key)"
//	@Param			body		body		model.CreateMerchantRequest	true	"Merchant registration payload"
//	@Success		201			{object}	response.successEnvelope{data=model.CreateMerchantResponse}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		409			{object}	response.errorEnvelope
//	@Router			/api/v1/merchants [post]
//	@Security		AdminKeyAuth
func (h *MerchantHandler) Create(c *gin.Context) {
	var req model.CreateMerchantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	result, err := h.merchantSvc.CreateMerchant(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, service.ErrLegacyCredentialCreationDisabled) {
			response.Conflict(c, response.CodeLegacyCredentialCreationDisabled,
				"Legacy credential creation is disabled; use POST /api/v1/admin/onboarding/merchants")
			return
		}
		if errors.Is(err, service.ErrDuplicateMerchantCode) {
			// Retained for completeness — unreachable while creation is frozen.
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
//	@Description	Returns public merchant information. API secret is never returned. Includes legacy_credential_state (Phase 8D.3 migration state). Requires the admin key in production.
//	@Tags			merchants
//	@Security		AdminKeyAuth
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
