package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// DashboardSettingsHandler exposes safe merchant profile fields for the dashboard.
type DashboardSettingsHandler struct {
	merchantSvc service.MerchantService
}

// NewDashboardSettingsHandler constructs a DashboardSettingsHandler.
func NewDashboardSettingsHandler(merchantSvc service.MerchantService) *DashboardSettingsHandler {
	return &DashboardSettingsHandler{merchantSvc: merchantSvc}
}

// GetSettings godoc
//
//	@Summary		Get merchant settings
//	@Description	Returns safe merchant profile fields for the authenticated merchant.
//	@Description	Never includes API secrets, webhook secrets, or credentials.
//	@Tags			Dashboard Settings
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	response.successEnvelope{data=model.DashboardMerchantSettingsResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/settings [get]
func (h *DashboardSettingsHandler) GetSettings(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	m, err := h.merchantSvc.GetMerchant(c.Request.Context(), caller.MerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
			return
		}
		slog.Error("dashboard settings error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.OK(c, model.DashboardMerchantSettingsResponse{
		ID:                         m.ID,
		Name:                       m.Name,
		Code:                       m.Code,
		Status:                     m.Status,
		CreatedAt:                  m.CreatedAt,
		UpdatedAt:                  m.UpdatedAt,
		LegacyCredentialState:      m.LegacyCredentialState,
		LegacyCredentialDisabledAt: m.LegacyCredentialDisabledAt,
	})
}
