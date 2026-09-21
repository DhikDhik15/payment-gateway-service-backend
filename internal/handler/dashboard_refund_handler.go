package handler

import (
	"errors"
	"strconv"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DashboardRefundHandler serves merchant-wide dashboard refund read APIs.
type DashboardRefundHandler struct {
	refundSvc service.RefundService
}

// NewDashboardRefundHandler constructs a DashboardRefundHandler.
func NewDashboardRefundHandler(refundSvc service.RefundService) *DashboardRefundHandler {
	return &DashboardRefundHandler{refundSvc: refundSvc}
}

// ListRefunds godoc
//
//	@Summary		List dashboard refunds
//	@Description	Merchant-wide paginated refund list. Scoped to authenticated merchant_id.
//	@Tags			Dashboard Refunds
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page			query		int		false	"Page (default 1)"
//	@Param			limit			query		int		false	"Page size (default 20, max 100)"
//	@Param			status			query		string	false	"PENDING|PROCESSING|SUCCEEDED|FAILED"
//	@Param			payment_id		query		string	false	"Filter by payment UUID"
//	@Param			created_from	query		string	false	"RFC3339 inclusive lower bound"
//	@Param			created_to		query		string	false	"RFC3339 exclusive upper bound"
//	@Success		200				{object}	response.successEnvelope{data=[]model.RefundResponse}
//	@Failure		400				{object}	response.errorEnvelope
//	@Failure		401				{object}	response.errorEnvelope
//	@Failure		404				{object}	response.errorEnvelope
//	@Failure		500				{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/refunds [get]
func (h *DashboardRefundHandler) ListRefunds(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	filter, verrs := parseDashboardRefundListFilter(c)
	if len(verrs) > 0 {
		response.ValidationError(c, verrs)
		return
	}

	result, err := h.refundSvc.ListRefundsByMerchant(c.Request.Context(), caller.MerchantID, filter)
	if err != nil {
		h.handleError(c, err)
		return
	}
	data := result.Refunds
	if data == nil {
		data = []model.RefundResponse{}
	}
	response.List(c, data, response.Pagination{
		Page: result.Page, Limit: result.Limit, Total: result.Total, TotalPages: result.TotalPages,
	})
}

// GetRefund godoc
//
//	@Summary		Get dashboard refund detail
//	@Description	Returns a refund belonging to the authenticated merchant.
//	@Description	Cross-merchant IDs return 404 (no existence leak).
//	@Tags			Dashboard Refunds
//	@Produce		json
//	@Security		BearerAuth
//	@Param			refund_id	path		string	true	"Refund UUID"
//	@Success		200			{object}	response.successEnvelope{data=model.RefundResponse}
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		500			{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/refunds/{refund_id} [get]
func (h *DashboardRefundHandler) GetRefund(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	refundID, err := uuid.Parse(c.Param("refund_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid refund ID format")
		return
	}

	resp, err := h.refundSvc.GetRefund(c.Request.Context(), caller.MerchantID, refundID)
	if err != nil {
		h.handleError(c, err)
		return
	}
	response.OK(c, resp)
}

func parseDashboardRefundListFilter(c *gin.Context) (model.RefundListFilter, map[string]string) {
	errs := make(map[string]string)
	filter := model.RefundListFilter{Page: model.DefaultPage, Limit: model.DefaultLimit}

	if raw := c.Query("page"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			errs["page"] = "must be an integer >= 1"
		} else {
			filter.Page = v
		}
	}
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > model.MaxLimit {
			errs["limit"] = "must be an integer between 1 and 100"
		} else {
			filter.Limit = v
		}
	}
	if raw := c.Query("status"); raw != "" {
		s := model.RefundStatus(raw)
		switch s {
		case model.RefundStatusPending, model.RefundStatusProcessing, model.RefundStatusSucceeded, model.RefundStatusFailed:
			filter.Status = &s
		default:
			errs["status"] = "must be one of: PENDING, PROCESSING, SUCCEEDED, FAILED"
		}
	}
	if raw := c.Query("payment_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			errs["payment_id"] = "must be a valid UUID"
		} else {
			filter.TransactionID = &id
		}
	}
	var from, to *time.Time
	if raw := c.Query("created_from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs["created_from"] = "must be RFC3339"
		} else {
			utc := t.UTC()
			from = &utc
		}
	}
	if raw := c.Query("created_to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs["created_to"] = "must be RFC3339"
		} else {
			utc := t.UTC()
			to = &utc
		}
	}
	if from != nil && to != nil && !from.Before(*to) {
		errs["created_from"] = "created_from must be before created_to"
	}
	if _, ok := errs["created_from"]; !ok {
		filter.CreatedFrom = from
	}
	if _, ok := errs["created_to"]; !ok {
		filter.CreatedTo = to
	}
	return filter, errs
}

func (h *DashboardRefundHandler) handleError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrInvalidDateRange) {
		response.ValidationError(c, map[string]string{"created_from": "created_from must be before created_to"})
		return
	}
	(&DashboardPaymentHandler{}).handleRefundError(c, err)
}
