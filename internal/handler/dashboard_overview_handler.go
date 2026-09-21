package handler

import (
	"errors"
	"log/slog"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// Ensure model types are visible to swag type resolution.
var _ = model.DashboardOverviewResponse{}

// DashboardOverviewHandler serves GET /api/v1/dashboard/overview.
type DashboardOverviewHandler struct {
	overviewSvc service.DashboardOverviewService
}

// NewDashboardOverviewHandler constructs a DashboardOverviewHandler.
func NewDashboardOverviewHandler(overviewSvc service.DashboardOverviewService) *DashboardOverviewHandler {
	return &DashboardOverviewHandler{overviewSvc: overviewSvc}
}

// GetOverview godoc
//
//	@Summary		Dashboard overview
//	@Description	Returns merchant-scoped payment, revenue, and refund aggregates for a reporting period.
//	@Description
//	@Description	**Authentication**: Bearer JWT (dashboard). Merchant API keys are not accepted.
//	@Description	**Merchant isolation**: merchant_id is taken from the authenticated dashboard user.
//	@Description
//	@Description	**Period**: default is the current UTC calendar month `[month_start, next_month)`.
//	@Description	Optional `from` / `to` override the window (half-open `[from, to)`).
//	@Description	Accepts RFC3339 or `YYYY-MM-DD` (interpreted as UTC midnight).
//	@Description
//	@Description	**Revenue**: SUM(amount) where status=PAID and paid_at ∈ [from, to) — Phase 7A financial truth.
//	@Description	**Payment counts**: grouped by status where created_at ∈ [from, to).
//	@Description	**Refunds**: SUCCEEDED refunds where COALESCE(succeeded_at, created_at) ∈ [from, to).
//	@Description	**Recent payments**: last 10 payments for the merchant (not limited to the period).
//	@Tags			Dashboard Overview
//	@Produce		json
//	@Security		BearerAuth
//	@Param			from	query		string	false	"Period start (RFC3339 or YYYY-MM-DD UTC, inclusive)"
//	@Param			to		query		string	false	"Period end (RFC3339 or YYYY-MM-DD UTC, exclusive)"
//	@Success		200		{object}	response.successEnvelope{data=model.DashboardOverviewResponse}
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/overview [get]
func (h *DashboardOverviewHandler) GetOverview(c *gin.Context) {
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

	result, err := h.overviewSvc.GetOverview(c.Request.Context(), caller.MerchantID, from, to)
	if err != nil {
		if errors.Is(err, service.ErrInvalidOverviewPeriod) {
			response.ValidationError(c, map[string]string{"from": "from must be before to"})
			return
		}
		slog.Error("dashboard overview error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("merchant_id", caller.MerchantID.String()),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	response.OK(c, result)
}

// parseOverviewPeriod reads optional from/to query params.
// Both must be supplied together; neither means "use service default".
func parseOverviewPeriod(c *gin.Context) (*time.Time, *time.Time, map[string]string) {
	errs := make(map[string]string)
	rawFrom := c.Query("from")
	rawTo := c.Query("to")

	if rawFrom == "" && rawTo == "" {
		return nil, nil, nil
	}
	if rawFrom == "" || rawTo == "" {
		errs["from"] = "from and to must both be provided"
		return nil, nil, errs
	}

	from, err := parseDashboardTime(rawFrom)
	if err != nil {
		errs["from"] = "must be RFC3339 or YYYY-MM-DD"
	}
	to, err := parseDashboardTime(rawTo)
	if err != nil {
		errs["to"] = "must be RFC3339 or YYYY-MM-DD"
	}
	if len(errs) > 0 {
		return nil, nil, errs
	}
	if !from.Before(to) {
		errs["from"] = "from must be before to"
		return nil, nil, errs
	}
	return &from, &to, nil
}

// parseDashboardTime accepts RFC3339 or date-only YYYY-MM-DD (UTC midnight).
func parseDashboardTime(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}
