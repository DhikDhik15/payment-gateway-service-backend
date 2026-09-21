package handler

import (
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DashboardReconciliationHandler exposes merchant-scoped reconciliation mismatch reads.
// Settlement batches themselves are provider-level (no merchant_id) and are not listed here.
type DashboardReconciliationHandler struct {
	reconSvc service.ReconciliationService
}

// NewDashboardReconciliationHandler constructs a DashboardReconciliationHandler.
func NewDashboardReconciliationHandler(reconSvc service.ReconciliationService) *DashboardReconciliationHandler {
	return &DashboardReconciliationHandler{reconSvc: reconSvc}
}

// ListMismatches godoc
//
//	@Summary		List reconciliation mismatches
//	@Description	Returns reconciliation mismatches for the authenticated merchant only.
//	@Description	merchant_id is forced from JWT — client-supplied merchant_id is ignored.
//	@Tags			Dashboard Reconciliation
//	@Produce		json
//	@Security		BearerAuth
//	@Param			settlement_id	query		string	false	"Filter by settlement UUID"
//	@Param			result_type		query		string	false	"Result type filter"
//	@Param			reason_code		query		string	false	"Reason code filter"
//	@Param			date_from		query		string	false	"RFC3339"
//	@Param			date_to			query		string	false	"RFC3339"
//	@Param			page			query		int		false	"Page"
//	@Param			limit			query		int		false	"Limit"
//	@Success		200				{object}	response.successEnvelope{data=[]model.ReconciliationResult}
//	@Failure		400				{object}	response.errorEnvelope
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/reconciliation/mismatches [get]
func (h *DashboardReconciliationHandler) ListMismatches(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	filter := model.ReconciliationListFilter{
		Page:           model.DefaultPage,
		Limit:          model.DefaultLimit,
		MismatchesOnly: true,
		MerchantID:     &caller.MerchantID,
	}
	if verrs := applyDashboardReconQuery(c, &filter); len(verrs) > 0 {
		response.ValidationError(c, verrs)
		return
	}

	res, err := h.reconSvc.ListResults(c.Request.Context(), filter)
	if err != nil {
		slog.Error("dashboard reconciliation list error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}
	data := res.Results
	if data == nil {
		data = []model.ReconciliationResult{}
	}
	response.List(c, data, response.Pagination{
		Page: res.Page, Limit: res.Limit, Total: res.Total, TotalPages: res.TotalPages,
	})
}

// GetMismatch godoc
//
//	@Summary		Get reconciliation mismatch
//	@Description	Returns one mismatch if it belongs to the authenticated merchant; otherwise 404.
//	@Tags			Dashboard Reconciliation
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Reconciliation result UUID"
//	@Success		200	{object}	response.successEnvelope{data=model.ReconciliationResult}
//	@Failure		400	{object}	response.errorEnvelope
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/reconciliation/mismatches/{id} [get]
func (h *DashboardReconciliationHandler) GetMismatch(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid mismatch ID format")
		return
	}

	res, err := h.reconSvc.GetResult(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrReconciliationNotFound) {
			response.NotFound(c, response.CodeResourceNotFound, "Reconciliation result not found")
			return
		}
		slog.Error("dashboard reconciliation get error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	// Merchant isolation: do not reveal cross-merchant or unattributed results.
	if res.MerchantID == nil || *res.MerchantID != caller.MerchantID {
		response.NotFound(c, response.CodeResourceNotFound, "Reconciliation result not found")
		return
	}

	response.OK(c, res)
}

func applyDashboardReconQuery(c *gin.Context, filter *model.ReconciliationListFilter) map[string]string {
	errs := make(map[string]string)
	if p := c.Query("page"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil || v < 1 {
			errs["page"] = "must be an integer >= 1"
		} else {
			filter.Page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v < 1 || v > model.MaxLimit {
			errs["limit"] = "must be an integer between 1 and 100"
		} else {
			filter.Limit = v
		}
	}
	if v := c.Query("settlement_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			errs["settlement_id"] = "must be a valid UUID"
		} else {
			filter.SettlementID = &id
		}
	}
	if v := c.Query("result_type"); v != "" {
		rt := model.ReconciliationResultType(v)
		filter.ResultType = &rt
	}
	if v := c.Query("reason_code"); v != "" {
		filter.ReasonCode = &v
	}
	if v := c.Query("date_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			errs["date_from"] = "must be RFC3339"
		} else {
			utc := t.UTC()
			filter.DateFrom = &utc
		}
	}
	if v := c.Query("date_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			errs["date_to"] = "must be RFC3339"
		} else {
			utc := t.UTC()
			filter.DateTo = &utc
		}
	}
	return errs
}
