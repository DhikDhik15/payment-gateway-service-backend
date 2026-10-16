package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// DevRecoveryKeyHeader carries the independent development-only authorization
// credential. It is a header (like X-Admin-Key) so the secret never appears
// in JSON bodies, URLs, or access logs.
const DevRecoveryKeyHeader = "X-Dev-Recovery-Key"

// DevRecoveryHandler serves the development-only account recovery endpoints.
// Every request is gated on the backend: frontend visibility is UX only.
type DevRecoveryHandler struct {
	svc service.DevRecoveryService
}

// NewDevRecoveryHandler wires the recovery endpoints over the recovery service.
func NewDevRecoveryHandler(svc service.DevRecoveryService) *DevRecoveryHandler {
	return &DevRecoveryHandler{svc: svc}
}

// Status godoc
//
//	@Summary		Development recovery capability
//	@Description	Reports whether development-only account recovery is enabled. Never carries secrets.
//	@Tags			Dashboard Auth
//	@Produce		json
//	@Success		200	{object}	response.successEnvelope{data=model.DevRecoveryStatusResponse}
//	@Router			/api/v1/auth/dev-recovery/status [get]
func (h *DevRecoveryHandler) Status(c *gin.Context) {
	enabled := h.svc != nil && h.svc.Enabled()
	response.OK(c, model.DevRecoveryStatusResponse{Enabled: enabled})
}

// Recover godoc
//
//	@Summary		Recover an account password (development only)
//	@Description	Development-only password recovery without SMTP delivery. Requires the X-Dev-Recovery-Key header, an explicit account email, and matching new/confirm passwords (8–128 chars). Revokes refresh sessions. Disabled (404) unless explicitly enabled in development.
//	@Tags			Dashboard Auth
//	@Accept			json
//	@Produce		json
//	@Param			X-Dev-Recovery-Key	header		string							true	"Development recovery key (never in body or URL)"
//	@Param			request				body		model.DevRecoveryRequest	true	"Account email and new password"
//	@Success		200					{object}	response.successEnvelope{data=model.DevRecoveryResponse}
//	@Failure		400					{object}	response.errorEnvelope
//	@Failure		401					{object}	response.errorEnvelope
//	@Failure		404					{object}	response.errorEnvelope
//	@Failure		429					{object}	response.errorEnvelope
//	@Failure		500					{object}	response.errorEnvelope
//	@Router			/api/v1/auth/dev-recovery/recover [post]
func (h *DevRecoveryHandler) Recover(c *gin.Context) {
	// Fail closed at the HTTP layer: when the service reports disabled (or is
	// unconfigured), the endpoint behaves like a missing route so its
	// existence is not advertised.
	if h.svc == nil || !h.svc.Enabled() {
		response.NotFound(c, response.CodeResourceNotFound, "Not found.")
		return
	}

	var req model.DevRecoveryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	// The authorization credential comes only from the header — never from
	// frontend-supplied role/permission claims and never from the URL.
	providedKey := c.GetHeader(DevRecoveryKeyHeader)

	resp, err := h.svc.Recover(c.Request.Context(), providedKey, req.Email, req.NewPassword, req.ConfirmPassword)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrDevRecoveryDisabled):
			// Indistinguishable from a missing route.
			response.NotFound(c, response.CodeResourceNotFound, "Not found.")
		case errors.Is(err, service.ErrDevRecoveryUnauthorized):
			// Generic: reveals nothing about account existence.
			response.Unauthorized(c, response.CodeUnauthorized, "Invalid recovery credentials.")
		case errors.Is(err, service.ErrDevRecoveryNotFound):
			// Only reachable after successful authorization.
			response.NotFound(c, response.CodeDashboardUserNotFound, "Account not found or ineligible for recovery.")
		case errors.Is(err, service.ErrDevRecoveryPasswordMismatch):
			response.ValidationError(c, map[string]string{"confirm_password": "Passwords do not match."})
		case errors.Is(err, service.ErrInvalidPassword):
			response.ValidationError(c, map[string]string{"new_password": "Password must be 8–128 characters."})
		default:
			slog.Error("dev recovery: recover error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				// emails, passwords, and recovery keys are NEVER logged
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}
	// The response carries no hashes, tokens, or secrets — only the account
	// identifier the caller already supplied.
	response.OK(c, model.DevRecoveryResponse{
		Message: "Password has been reset. Sign in with your new password.",
		Email:   resp.Email,
	})
}
