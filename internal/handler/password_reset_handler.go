package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// PasswordResetHandler serves the public forgot-password endpoints. Both are
// unauthenticated by design: the requester proves ownership of the mailbox,
// not of a session.
type PasswordResetHandler struct {
	svc service.PasswordResetService
}

// NewPasswordResetHandler wires the reset endpoints over the reset service.
func NewPasswordResetHandler(svc service.PasswordResetService) *PasswordResetHandler {
	return &PasswordResetHandler{svc: svc}
}

// RequestReset godoc
//
//	@Summary		Request a password reset
//	@Description	Sends a single-use password-reset email when the address belongs to an eligible account. Always returns the same generic response so account existence is never revealed.
//	@Tags			Dashboard Auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		model.RequestPasswordResetRequest	true	"Account email"
//	@Success		200		{object}	response.successEnvelope{data=model.RequestPasswordResetResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		429		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/auth/password-reset/request [post]
func (h *PasswordResetHandler) RequestReset(c *gin.Context) {
	var req model.RequestPasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	// The service resolves unknown/ineligible addresses to the same generic
	// outcome. Only infrastructure failures reach 500 — and even those must
	// not distinguish the address.
	if err := h.svc.RequestPasswordReset(c.Request.Context(), req.Email); err != nil {
		slog.Error("password reset: request error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			// the email address is NEVER logged: its presence must not leak
			// whether an account exists
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}
	response.OK(c, model.RequestPasswordResetResponse{
		Message: "If an account exists for this email, a password-reset link has been sent.",
	})
}

// ConfirmReset godoc
//
//	@Summary		Reset the password with a reset token
//	@Description	Consumes a single-use reset token and sets a new password (8–128 characters). Does not establish a session — sign in with the new password afterwards.
//	@Tags			Dashboard Auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		model.ConfirmPasswordResetRequest	true	"Reset token and new password"
//	@Success		200		{object}	response.successEnvelope{data=model.ConfirmPasswordResetResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		409		{object}	response.errorEnvelope
//	@Failure		410		{object}	response.errorEnvelope
//	@Failure		429		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/auth/password-reset/confirm [post]
func (h *PasswordResetHandler) ConfirmReset(c *gin.Context) {
	var req model.ConfirmPasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	if err := h.svc.ConfirmPasswordReset(c.Request.Context(), req.Token, req.Password); err != nil {
		switch {
		case errors.Is(err, service.ErrPasswordResetInvalid):
			response.BadRequest(c, response.CodeResetTokenInvalid, "This reset link is invalid.")
		case errors.Is(err, service.ErrInvalidPassword):
			response.ValidationError(c, map[string]string{"password": "Password must be 8–128 characters."})
		case errors.Is(err, service.ErrPasswordResetExpired):
			response.Gone(c, response.CodeResetTokenExpired, "This reset link has expired. Request a new one.")
		case errors.Is(err, service.ErrPasswordResetConsumed):
			response.Conflict(c, response.CodeResetTokenConsumed, "This reset link has already been used.")
		default:
			slog.Error("password reset: confirm error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				// the token is NEVER logged
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, model.ConfirmPasswordResetResponse{
		Message: "Your password has been reset. Sign in with your new password.",
	})
}
