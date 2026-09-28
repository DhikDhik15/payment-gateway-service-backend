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
//	@Description	The final ACTIVE OWNER of a merchant cannot be disabled (409 LAST_OWNER_REQUIRED).
//	@Description	Disabling a user revokes all of their dashboard sessions immediately.
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
//	@Failure		409		{object}	response.errorEnvelope	"LAST_OWNER_REQUIRED"
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
		case errors.Is(err, service.ErrLastOwnerRequired):
			response.Conflict(c, response.CodeLastOwnerRequired, "Cannot disable the last active OWNER of the merchant")
		case errors.Is(err, service.ErrMerchantInactive):
			response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
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

// UpdateUserRole godoc
//
//	@Summary		Change dashboard user role
//	@Description	Changes the role (OWNER/ADMIN/VIEWER) of a dashboard user in the caller's merchant.
//	@Description	Only OWNER may change roles — ADMIN and VIEWER are rejected with 403.
//	@Description	The target user must belong to the same merchant as the caller; cross-merchant
//	@Description	targets return 404 so other tenants' users are never confirmed to exist.
//	@Description	No merchant_id is accepted from the request body — merchant identity is
//	@Description	derived exclusively from the authenticated caller.
//	@Description
//	@Description	**Last-OWNER invariant:** demoting an ACTIVE OWNER is rejected with
//	@Description	409 LAST_OWNER_REQUIRED when it would leave the merchant with zero ACTIVE
//	@Description	OWNERs. Multiple OWNERs are allowed; an OWNER may demote themselves only
//	@Description	when another ACTIVE OWNER remains. Sessions are NOT revoked on role change —
//	@Description	the role is reloaded from the database on every authenticated request.
//	@Tags			Dashboard Users
//	@Accept			json
//	@Produce		json
//	@Param			user_id	path		string					true	"Dashboard user UUID"
//	@Param			body		body		model.UpdateUserRoleRequest	true	"New role"
//	@Success		200		{object}	model.DashboardUserResponse
//	@Failure		400		{object}	response.errorEnvelope	"VALIDATION_ERROR or INVALID_ROLE"
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		403		{object}	response.errorEnvelope	"INSUFFICIENT_ROLE — only OWNER may change roles"
//	@Failure		404		{object}	response.errorEnvelope	"DASHBOARD_USER_NOT_FOUND (also returned for cross-merchant targets)"
//	@Failure		409		{object}	response.errorEnvelope	"LAST_OWNER_REQUIRED"
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/users/{user_id}/role [patch]
//	@Security		BearerAuth
func (h *DashboardUserHandler) UpdateUserRole(c *gin.Context) {
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

	var req model.UpdateUserRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	updated, err := h.userSvc.UpdateUserRole(
		c.Request.Context(),
		caller.ID,
		caller.Role,
		caller.MerchantID,
		targetUserID,
		req.Role,
	)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInsufficientRole):
			response.Forbidden(c, response.CodeInsufficientRole, "Only OWNER may change user roles")
		case errors.Is(err, service.ErrInvalidRole):
			response.BadRequest(c, response.CodeInvalidRole, "Invalid role — must be OWNER, ADMIN or VIEWER")
		case errors.Is(err, service.ErrLastOwnerRequired):
			response.Conflict(c, response.CodeLastOwnerRequired, "Cannot demote the last active OWNER of the merchant")
		case errors.Is(err, service.ErrMerchantInactive):
			response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
		case errors.Is(err, service.ErrDashboardUserNotFound):
			response.NotFound(c, response.CodeDashboardUserNotFound, "User not found")
		case errors.Is(err, service.ErrCrossmerchantAccess):
			// Return 404 for cross-merchant attempts — do not confirm the user exists.
			response.NotFound(c, response.CodeDashboardUserNotFound, "User not found")
		default:
			slog.Error("dashboard users: update role error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.OK(c, updated)
}

// ChangePassword godoc
//
//	@Summary		Change own password
//	@Description	Changes the password of the currently authenticated dashboard user.
//	@Description	The user identity comes exclusively from the authenticated session/JWT —
//	@Description	no user_id is accepted in the request body, so a caller can never change
//	@Description	another user's password.
//	@Description
//	@Description	**Policy:** current_password is required and must verify against the stored
//	@Description	Argon2id hash (400 INVALID_CURRENT_PASSWORD on mismatch). new_password is
//	@Description	required and must be 8–128 characters (400 VALIDATION_ERROR / INVALID_PASSWORD).
//	@Description	Plaintext passwords are never stored or logged.
//	@Description
//	@Description	**Sessions:** on success ALL refresh sessions of the caller are revoked
//	@Description	(the caller must log in again to obtain a new session). The access token of
//	@Description	the in-flight request stays valid only until its short TTL expires.
//	@Description	Disabled users are rejected by the authentication middleware (401 USER_DISABLED).
//	@Tags			Dashboard Auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		model.ChangePasswordRequest	true	"Current and new password"
//	@Success		200		{object}	model.DashboardUserResponse
//	@Failure		400		{object}	response.errorEnvelope	"VALIDATION_ERROR, INVALID_PASSWORD or INVALID_CURRENT_PASSWORD"
//	@Failure		401		{object}	response.errorEnvelope	"UNAUTHORIZED or USER_DISABLED"
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/me/password [patch]
//	@Security		BearerAuth
func (h *DashboardUserHandler) ChangePassword(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	var req model.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	// Identity comes from the authenticated context only — never from the body.
	updated, err := h.userSvc.ChangePassword(c.Request.Context(), caller.ID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidCurrentPassword):
			response.BadRequest(c, response.CodeInvalidCurrentPassword, "Current password is incorrect")
		case errors.Is(err, service.ErrInvalidPassword):
			response.BadRequest(c, response.CodeInvalidPassword, "New password does not meet the password policy")
		case errors.Is(err, service.ErrUserDisabled):
			response.Unauthorized(c, response.CodeUserDisabled, "Account is disabled")
		case errors.Is(err, service.ErrDashboardUserNotFound), errors.Is(err, service.ErrInvalidCredentials):
			// Caller no longer exists — same non-leaking 401 as the auth middleware.
			response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		default:
			slog.Error("dashboard users: change password error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				// passwords are NEVER logged
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.OK(c, updated)
}
