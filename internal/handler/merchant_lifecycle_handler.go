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

// MerchantLifecycleHandler handles admin-only merchant lifecycle endpoints.
type MerchantLifecycleHandler struct {
	merchantSvc service.MerchantService
}

// NewMerchantLifecycleHandler constructs a MerchantLifecycleHandler.
func NewMerchantLifecycleHandler(merchantSvc service.MerchantService) *MerchantLifecycleHandler {
	return &MerchantLifecycleHandler{merchantSvc: merchantSvc}
}

// UpdateStatus godoc
//
//	@Summary		Update merchant lifecycle status (admin)
//	@Description	Changes a merchant's lifecycle status (ACTIVE, SUSPENDED, INACTIVE).
//	@Description	Requires X-Admin-Key. Merchant API keys and dashboard JWTs are NOT accepted.
//	@Description
//	@Description	**Allowed transitions:**
//	@Description	- ACTIVE    → SUSPENDED  (temporary block)
//	@Description	- ACTIVE    → INACTIVE   (permanent deactivation)
//	@Description	- SUSPENDED → ACTIVE     (reinstate)
//	@Description	- INACTIVE  → ACTIVE     (reactivate)
//	@Description
//	@Description	**Idempotent:** requesting the current status returns 200 without error.
//	@Description
//	@Description	**Session invalidation:** transitioning to SUSPENDED or INACTIVE
//	@Description	immediately deletes all active dashboard sessions for that merchant.
//	@Description	Existing non-revoked Phase 5C API keys are not modified; they are
//	@Description	blocked at authentication time by the merchant status check.
//	@Tags			Admin
//	@Accept			json
//	@Produce		json
//	@Param			X-Admin-Key		header		string									true	"Ops admin key"
//	@Param			merchant_id		path		string									true	"Merchant UUID"
//	@Param			body			body		model.UpdateMerchantStatusRequest		true	"Target status"
//	@Success		200				{object}	response.successEnvelope{data=model.GetMerchantResponse}
//	@Failure		400				{object}	response.errorEnvelope	"VALIDATION_ERROR — invalid status value"
//	@Failure		401				{object}	response.errorEnvelope	"ADMIN_UNAUTHORIZED"
//	@Failure		404				{object}	response.errorEnvelope	"MERCHANT_NOT_FOUND"
//	@Failure		422				{object}	response.errorEnvelope	"INVALID_STATUS_TRANSITION"
//	@Failure		500				{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Failure		503				{object}	response.errorEnvelope	"ADMIN_NOT_CONFIGURED"
//	@Router			/api/v1/admin/merchants/{merchant_id}/status [patch]
//	@Security		AdminKeyAuth
func (h *MerchantLifecycleHandler) UpdateStatus(c *gin.Context) {
	merchantID, err := uuid.Parse(c.Param("merchant_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid merchant ID format")
		return
	}

	var req model.UpdateMerchantStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	// Validate that the supplied status string is one of the known values.
	if !req.Status.IsValid() {
		response.ValidationError(c, map[string]string{
			"status": "must be one of: ACTIVE, SUSPENDED, INACTIVE",
		})
		return
	}

	result, err := h.merchantSvc.UpdateMerchantStatus(c.Request.Context(), merchantID, req.Status)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrMerchantNotFound):
			response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
		case errors.Is(err, service.ErrInvalidStatusTransition):
			response.UnprocessableEntity(c, response.CodeInvalidStatusTransition,
				"Status transition is not allowed", map[string]string{
					"current_status":   "", // intentionally omitted — expose minimum info
					"requested_status": string(req.Status),
				})
		default:
			slog.Error("merchant lifecycle: update status error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("merchant_id", merchantID.String()),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.OK(c, result)
}
