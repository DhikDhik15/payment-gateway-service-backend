package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// Ensure model types are visible to swag type resolution.
var _ = model.DashboardAnalyticsResponse{}

// DashboardAnalyticsHandler serves GET /api/v1/dashboard/analytics.
type DashboardAnalyticsHandler struct {
	analyticsSvc service.DashboardAnalyticsService
}

// NewDashboardAnalyticsHandler constructs a DashboardAnalyticsHandler.
func NewDashboardAnalyticsHandler(analyticsSvc service.DashboardAnalyticsService) *DashboardAnalyticsHandler {
	return &DashboardAnalyticsHandler{analyticsSvc: analyticsSvc}
}

// GetAnalytics godoc
//
//	@Summary		Dashboard analytics
//	@Description	Returns merchant-scoped revenue and product-sales analytics for a reporting period.
//	@Description
//	@Description	**Authentication**: Bearer JWT (dashboard). Merchant API keys are not accepted.
//	@Description	**Merchant isolation**: merchant_id is taken from the authenticated dashboard user.
//	@Description
//	@Description	**Period**: default is the current UTC calendar month `[month_start, next_month)`.
//	@Description	Optional `from` / `to` override the window (half-open `[from, to)`).
//	@Description	Accepts RFC3339 or `YYYY-MM-DD` (interpreted as UTC midnight).
//	@Description
//	@Description	**Revenue**: gross SUM(amount) where status=PAID and paid_at ∈ [from, to).
//	@Description	Refunds are reported by the overview endpoint and never subtracted here.
//	@Description	**Orders**: an order belongs to the window when the paid_at of its
//	@Description	single PAID transaction falls in [from, to). Retried failures never
//	@Description	duplicate sales. Order-less legacy payments count toward revenue but
//	@Description	not toward units or average order value.
//	@Description	**Products**: immutable order-item snapshots (quantities, prices, names
//	@Description	at purchase); live catalog prices are never used. Deleted products
//	@Description	appear under their snapshot name with a null product_id.
//	@Description	**Trend**: daily buckets up to 62-day windows, Monday-start weekly
//	@Description	buckets beyond that. Empty buckets return zero revenue.
//	@Tags			Dashboard Analytics
//	@Produce		json
//	@Security		BearerAuth
//	@Param			from	query		string	false	"Period start (RFC3339 or YYYY-MM-DD UTC, inclusive)"
//	@Param			to		query		string	false	"Period end (RFC3339 or YYYY-MM-DD UTC, exclusive)"
//	@Success		200		{object}	response.successEnvelope{data=model.DashboardAnalyticsResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/analytics [get]
func (h *DashboardAnalyticsHandler) GetAnalytics(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	from, to, verrs := parseOverviewPeriod(c)
	if len(verrs) > 0 {
		response.ValidationError(c, verrs)
		return
	}

	result, err := h.analyticsSvc.GetAnalytics(c.Request.Context(), caller.MerchantID, from, to)
	if err != nil {
		if errors.Is(err, service.ErrInvalidOverviewPeriod) {
			response.ValidationError(c, map[string]string{"from": "from must be before to"})
			return
		}
		slog.Error("dashboard analytics error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("merchant_id", caller.MerchantID.String()),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.OK(c, result)
}
