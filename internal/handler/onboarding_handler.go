package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// OnboardingHandler handles secure tenant onboarding (admin-only).
type OnboardingHandler struct {
	onboardingSvc service.OnboardingService
}

// NewOnboardingHandler constructs an OnboardingHandler.
func NewOnboardingHandler(onboardingSvc service.OnboardingService) *OnboardingHandler {
	return &OnboardingHandler{onboardingSvc: onboardingSvc}
}

// OnboardMerchant godoc
//
//	@Summary		Onboard a new merchant tenant (admin)
//	@Description	Atomically provisions a new SaaS tenant: merchant row, OWNER dashboard user, and initial Phase 5C API credential.
//	@Description	Requires X-Admin-Key. Merchant API keys and dashboard JWTs are NOT accepted.
//	@Description	All three records are created in a single PostgreSQL transaction — failure rolls back completely.
//	@Description	api_credential.secret is a one-time plaintext secret; store it securely. It will never be returned again.
//	@Description	Password and password hashes are never returned. Provider credentials remain platform-level (out of scope).
//	@Tags			Admin
//	@Accept			json
//	@Produce		json
//	@Param			X-Admin-Key	header		string						true	"Ops admin key (NOT a merchant API key)"
//	@Param			body		body		model.OnboardMerchantRequest	true	"Tenant onboarding payload"
//	@Success		201			{object}	response.successEnvelope{data=model.OnboardMerchantResponse}
//	@Failure		400			{object}	response.errorEnvelope	"Validation error"
//	@Failure		401			{object}	response.errorEnvelope	"Missing or invalid X-Admin-Key"
//	@Failure		409			{object}	response.errorEnvelope	"Duplicate merchant code or owner email"
//	@Failure		500			{object}	response.errorEnvelope
//	@Failure		503			{object}	response.errorEnvelope	"Admin API not configured"
//	@Router			/api/v1/admin/onboarding/merchants [post]
//	@Security		AdminKeyAuth
func (h *OnboardingHandler) OnboardMerchant(c *gin.Context) {
	var req model.OnboardMerchantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	result, err := h.onboardingSvc.OnboardMerchant(c.Request.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrDuplicateMerchantCode):
			response.Conflict(c, response.CodeDuplicateMerchantCode, "Merchant code already exists")
		case errors.Is(err, service.ErrEmailAlreadyExists):
			response.Conflict(c, response.CodeEmailAlreadyExists, "Email is already registered")
		case errors.Is(err, service.ErrInvalidEmail):
			response.ValidationError(c, map[string]string{"owner_email": "must be a valid email address"})
		default:
			slog.Error("merchant onboarding: unexpected error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
				// never log password or API secret
			)
			response.InternalServerError(c)
		}
		return
	}

	response.Created(c, result)
}
