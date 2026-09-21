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

// AdminUserHandler handles the admin-only dashboard user bootstrap endpoint.
// This endpoint is protected by the existing AdminAuth middleware (X-Admin-Key).
type AdminUserHandler struct {
	userSvc service.DashboardUserService
}

// NewAdminUserHandler constructs an AdminUserHandler.
func NewAdminUserHandler(userSvc service.DashboardUserService) *AdminUserHandler {
	return &AdminUserHandler{userSvc: userSvc}
}

// CreateUser godoc
//
//	@Summary		Create dashboard user (admin bootstrap)
//	@Description	Creates a new dashboard user account for the specified merchant.
//	@Description	This endpoint requires the X-Admin-Key header — it is NOT accessible with a merchant API key.
//	@Description	Use this to bootstrap the first OWNER account for a merchant.
//	@Description	The response never includes password or password_hash.
//	@Tags			Admin
//	@Accept			json
//	@Produce		json
//	@Param			merchant_id	path		string								true	"Merchant UUID"
//	@Param			body		body		model.CreateDashboardUserRequest	true	"New user details"
//	@Success		201			{object}	model.DashboardUserResponse
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope	"Merchant not found"
//	@Failure		409			{object}	response.errorEnvelope	"Email already registered"
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/admin/merchants/{merchant_id}/users [post]
//	@Security		AdminKeyAuth
func (h *AdminUserHandler) CreateUser(c *gin.Context) {
	merchantID, err := uuid.Parse(c.Param("merchant_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid merchant ID format")
		return
	}

	var req model.CreateDashboardUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	created, err := h.userSvc.CreateUser(c.Request.Context(), merchantID, req)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrMerchantNotFound):
			response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
		case errors.Is(err, service.ErrEmailAlreadyExists):
			response.Conflict(c, response.CodeEmailAlreadyExists, "Email is already registered")
		case errors.Is(err, service.ErrInvalidEmail):
			response.ValidationError(c, map[string]string{"email": "must be a valid email address"})
		default:
			slog.Error("admin user create: error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("merchant_id", merchantID.String()),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.Created(c, created)
}
