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

// DashboardAPIKeyHandler serves dashboard API key management.
// Merchant ID is always taken from the authenticated dashboard user.
type DashboardAPIKeyHandler struct {
	keySvc service.MerchantAPIKeyService
}

// NewDashboardAPIKeyHandler constructs a DashboardAPIKeyHandler.
func NewDashboardAPIKeyHandler(keySvc service.MerchantAPIKeyService) *DashboardAPIKeyHandler {
	return &DashboardAPIKeyHandler{keySvc: keySvc}
}

// ListAPIKeys godoc
//
//	@Summary		List merchant API keys (dashboard)
//	@Description	Returns safe API key metadata for the authenticated merchant. Never includes secrets.
//	@Tags			Dashboard API Keys
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	response.successEnvelope{data=[]model.MerchantAPIKeyResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/api-keys [get]
func (h *DashboardAPIKeyHandler) ListAPIKeys(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	keys, err := h.keySvc.ListKeys(c.Request.Context(), caller.MerchantID)
	if err != nil {
		slog.Error("dashboard api keys list error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}
	if keys == nil {
		keys = []model.MerchantAPIKeyResponse{}
	}
	response.OK(c, keys)
}

// CreateAPIKey godoc
//
//	@Summary		Create merchant API key (dashboard)
//	@Description	Creates a named API key. Plaintext secret is returned **once only**.
//	@Description	Requires OWNER or ADMIN. VIEWER receives 403.
//	@Tags			Dashboard API Keys
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		model.CreateMerchantAPIKeyRequest	true	"Key creation request"
//	@Success		201		{object}	response.successEnvelope{data=model.CreateMerchantAPIKeyResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		403		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/api-keys [post]
func (h *DashboardAPIKeyHandler) CreateAPIKey(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	var req model.CreateMerchantAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	result, err := h.keySvc.CreateKey(c.Request.Context(), caller.MerchantID, req)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.Created(c, result)
}

// RevokeAPIKey godoc
//
//	@Summary		Revoke merchant API key (dashboard)
//	@Description	Revokes an API key owned by the authenticated merchant.
//	@Description	Requires OWNER or ADMIN. VIEWER receives 403.
//	@Tags			Dashboard API Keys
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"API key UUID"
//	@Success		200	{object}	response.successEnvelope{data=object}
//	@Failure		400	{object}	response.errorEnvelope
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		409	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/api-keys/{id}/revoke [post]
func (h *DashboardAPIKeyHandler) RevokeAPIKey(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	keyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid API key ID format")
		return
	}

	if err := h.keySvc.RevokeKey(c.Request.Context(), caller.MerchantID, keyID); err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, map[string]string{"message": "API key revoked"})
}

func (h *DashboardAPIKeyHandler) handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAPIKeyNotFound):
		response.NotFound(c, response.CodeAPIKeyNotFound, "API key not found")
	case errors.Is(err, service.ErrAPIKeyAlreadyRevoked):
		response.Conflict(c, response.CodeAPIKeyAlreadyRevoked, "API key is already revoked")
	case errors.Is(err, service.ErrAPIKeyExpirationPast):
		response.ValidationError(c, map[string]string{"expires_at": "must be a future timestamp"})
	case errors.Is(err, repository.ErrMerchantNotFound):
		response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
	default:
		slog.Error("dashboard api key error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
