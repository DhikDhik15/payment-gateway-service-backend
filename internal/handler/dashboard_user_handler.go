package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DashboardUserHandler handles dashboard user management endpoints.
// These endpoints require dashboard authentication (RequireDashboardAuth middleware)
// and are scoped to the authenticated user's merchant via merchant isolation.
type DashboardUserHandler struct {
	userSvc service.DashboardUserService
}

// NewDashboardUserHandler constructs a DashboardUserHandler.
func NewDashboardUserHandler(userSvc service.DashboardUserService) *DashboardUserHandler {
	return &DashboardUserHandler{userSvc: userSvc}
}

// ListUsers godoc
//
//	@Summary		List dashboard users
//	@Description	Returns all dashboard users belonging to the authenticated user's merchant.
//	@Description	Results are scoped to the calling user's merchant — cross-merchant access is not possible.
//	@Tags			Dashboard Users
//	@Produce		json
//	@Success		200	{array}		model.DashboardUserResponse
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/users [get]
//	@Security		BearerAuth
func (h *DashboardUserHandler) ListUsers(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	users, err := h.userSvc.ListUsers(c.Request.Context(), caller.MerchantID)
	if err != nil {
		slog.Error("dashboard users: list error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("merchant_id", caller.MerchantID.String()),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.OK(c, users)
}

// UpdateUserStatus godoc
//
//	@Summary		Update dashboard user status
//	@Description	Changes the status (ACTIVE/DISABLED) of a dashboard user.
//	@Description	Only OWNER role can change user status.
//	@Description	A user cannot disable their own account.
//	@Description	The target user must belong to the same merchant as the caller.
//	@Tags			Dashboard Users
//	@Accept			json
//	@Produce		json
//	@Param			user_id	path		string							true	"Dashboard user UUID"
//	@Param			body	body		model.UpdateUserStatusRequest	true	"New status"
//	@Success		200		{object}	model.DashboardUserResponse
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		403		{object}	response.errorEnvelope	"Insufficient role or cross-merchant access"
//	@Failure		404		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/users/{user_id}/status [patch]
//	@Security		BearerAuth
func (h *DashboardUserHandler) UpdateUserStatus(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid user ID format")
		return
	}

	var req model.UpdateUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	updated, err := h.userSvc.UpdateUserStatus(
		c.Request.Context(),
		caller.ID,
		caller.Role,
		caller.MerchantID,
		targetUserID,
		req.Status,
	)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInsufficientRole):
			response.Forbidden(c, response.CodeInsufficientRole, "Only OWNER may change user status")
		case errors.Is(err, service.ErrSelfDisable):
			response.BadRequest(c, response.CodeInvalidRequest, "Cannot disable your own account")
		case errors.Is(err, service.ErrDashboardUserNotFound):
			response.NotFound(c, response.CodeDashboardUserNotFound, "User not found")
		case errors.Is(err, service.ErrCrossmerchantAccess):
			// Return 404 for cross-merchant attempts — do not confirm the user exists.
			response.NotFound(c, response.CodeDashboardUserNotFound, "User not found")
		default:
			slog.Error("dashboard users: update status error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.OK(c, updated)
}
