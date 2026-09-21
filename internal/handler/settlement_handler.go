package handler

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SettlementHandler exposes Phase 7C admin/ops settlement + reconciliation APIs.
type SettlementHandler struct {
	settlementSvc service.SettlementService
	reconSvc      service.ReconciliationService
}

func NewSettlementHandler(settlementSvc service.SettlementService, reconSvc service.ReconciliationService) *SettlementHandler {
	return &SettlementHandler{settlementSvc: settlementSvc, reconSvc: reconSvc}
}

// ImportSettlement godoc
//
//	@Summary		Import a provider settlement batch
//	@Description	Imports external settlement evidence. Idempotent on (provider, settlement_ref) + payload hash.
//	@Description	Does NOT mutate payment or refund financial truth.
//	@Tags			admin-settlements
//	@Accept			json
//	@Produce		json
//	@Param			X-Admin-Key	header		string							true	"Ops admin key (NOT a merchant API key)"
//	@Param			body		body		model.ImportSettlementRequest	true	"Settlement import"
//	@Success		201			{object}	response.successEnvelope{data=model.Settlement}
//	@Success		200			{object}	response.successEnvelope{data=model.Settlement}	"Idempotent replay"
//	@Failure		400			{object}	response.errorEnvelope
//	@Failure		401			{object}	response.errorEnvelope
//	@Failure		409			{object}	response.errorEnvelope
//	@Failure		503			{object}	response.errorEnvelope
//	@Router			/api/v1/admin/settlements/import [post]
func (h *SettlementHandler) ImportSettlement(c *gin.Context) {
	var req model.ImportSettlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	st, replayed, err := h.settlementSvc.Import(c.Request.Context(), req)
	if err != nil {
		h.mapError(c, err)
		return
	}
	if replayed {
		response.OK(c, st)
		return
	}
	response.Created(c, st)
}

// ListSettlements godoc
//
//	@Summary		List settlements
//	@Tags			admin-settlements
//	@Produce		json
//	@Param			X-Admin-Key		header	string	true	"Ops admin key"
//	@Param			provider		query	string	false	"Provider filter"
//	@Param			status			query	string	false	"Settlement status"
//	@Param			currency		query	string	false	"Currency"
//	@Param			settlement_ref	query	string	false	"Settlement reference"
//	@Param			date_from		query	string	false	"Settlement date from (YYYY-MM-DD inclusive)"
//	@Param			date_to			query	string	false	"Settlement date to (YYYY-MM-DD exclusive)"
//	@Param			page			query	int		false	"Page"
//	@Param			limit			query	int		false	"Limit"
//	@Success		200				{object}	response.successEnvelope{data=[]model.Settlement}
//	@Router			/api/v1/admin/settlements [get]
func (h *SettlementHandler) ListSettlements(c *gin.Context) {
	filter := model.SettlementListFilter{Page: model.DefaultPage, Limit: model.DefaultLimit}
	if p := c.Query("page"); p != "" {
		if v, e := strconv.Atoi(p); e == nil && v >= 1 {
			filter.Page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		if v, e := strconv.Atoi(l); e == nil && v >= 1 && v <= model.MaxLimit {
			filter.Limit = v
		}
	}
	if v := c.Query("provider"); v != "" {
		filter.Provider = &v
	}
	if v := c.Query("status"); v != "" {
		s := model.SettlementStatus(v)
		filter.Status = &s
	}
	if v := c.Query("currency"); v != "" {
		filter.Currency = &v
	}
	if v := c.Query("settlement_ref"); v != "" {
		filter.SettlementRef = &v
	}
	if v := c.Query("date_from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			response.BadRequest(c, response.CodeInvalidRequest, "date_from must be YYYY-MM-DD")
			return
		}
		filter.DateFrom = &t
	}
	if v := c.Query("date_to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			response.BadRequest(c, response.CodeInvalidRequest, "date_to must be YYYY-MM-DD")
			return
		}
		filter.DateTo = &t
	}
	res, err := h.settlementSvc.List(c.Request.Context(), filter)
	if err != nil {
		h.mapError(c, err)
		return
	}
	response.List(c, res.Settlements, response.Pagination{
		Page: res.Page, Limit: res.Limit, Total: res.Total, TotalPages: res.TotalPages,
	})
}

// GetSettlement godoc
//
//	@Summary		Get settlement detail with items
//	@Tags			admin-settlements
//	@Produce		json
//	@Param			X-Admin-Key	header	string	true	"Ops admin key"
//	@Param			id			path	string	true	"Settlement UUID"
//	@Success		200			{object}	response.successEnvelope{data=model.SettlementDetailResponse}
//	@Failure		404			{object}	response.errorEnvelope
//	@Router			/api/v1/admin/settlements/{id} [get]
func (h *SettlementHandler) GetSettlement(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid settlement ID")
		return
	}
	res, err := h.settlementSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		h.mapError(c, err)
		return
	}
	response.OK(c, res)
}

// ReconcileSettlement godoc
//
//	@Summary		Reconcile a settlement batch
//	@Description	Matches settlement items against PAID transactions / SUCCEEDED refunds.
//	@Description	Safe to rerun. Never mutates payment or refund financial counters.
//	@Tags			admin-settlements
//	@Produce		json
//	@Param			X-Admin-Key	header	string	true	"Ops admin key"
//	@Param			id			path	string	true	"Settlement UUID"
//	@Success		200			{object}	response.successEnvelope{data=model.ReconciliationSummary}
//	@Failure		404			{object}	response.errorEnvelope
//	@Failure		409			{object}	response.errorEnvelope
//	@Router			/api/v1/admin/settlements/{id}/reconcile [post]
func (h *SettlementHandler) ReconcileSettlement(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid settlement ID")
		return
	}
	summary, err := h.reconSvc.ReconcileSettlement(c.Request.Context(), id)
	if err != nil {
		h.mapError(c, err)
		return
	}
	response.OK(c, summary)
}

// ListSettlementReconciliation godoc
//
//	@Summary		List reconciliation results for a settlement
//	@Tags			admin-settlements
//	@Produce		json
//	@Param			X-Admin-Key	header	string	true	"Ops admin key"
//	@Param			id			path	string	true	"Settlement UUID"
//	@Param			result_type	query	string	false	"Result type filter"
//	@Param			status		query	string	false	"MATCHED|MISMATCH|UNMATCHED"
//	@Param			page		query	int		false	"Page"
//	@Param			limit		query	int		false	"Limit"
//	@Success		200			{object}	response.successEnvelope{data=[]model.ReconciliationResult}
//	@Router			/api/v1/admin/settlements/{id}/reconciliation [get]
func (h *SettlementHandler) ListSettlementReconciliation(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid settlement ID")
		return
	}
	filter := model.ReconciliationListFilter{Page: model.DefaultPage, Limit: model.DefaultLimit, SettlementID: &id}
	h.applyReconQuery(c, &filter)
	res, err := h.reconSvc.ListResults(c.Request.Context(), filter)
	if err != nil {
		h.mapError(c, err)
		return
	}
	response.List(c, res.Results, response.Pagination{
		Page: res.Page, Limit: res.Limit, Total: res.Total, TotalPages: res.TotalPages,
	})
}

// ListMismatches godoc
//
//	@Summary		List reconciliation mismatches across settlements
//	@Tags			admin-settlements
//	@Produce		json
//	@Param			X-Admin-Key		header	string	true	"Ops admin key"
//	@Param			provider		query	string	false	"Provider"
//	@Param			settlement_id	query	string	false	"Settlement UUID"
//	@Param			merchant_id		query	string	false	"Merchant UUID"
//	@Param			result_type		query	string	false	"Result type"
//	@Param			reason_code		query	string	false	"Reason code"
//	@Param			date_from		query	string	false	"RFC3339"
//	@Param			date_to			query	string	false	"RFC3339"
//	@Param			page			query	int		false	"Page"
//	@Param			limit			query	int		false	"Limit"
//	@Success		200				{object}	response.successEnvelope{data=[]model.ReconciliationResult}
//	@Router			/api/v1/admin/reconciliation/mismatches [get]
func (h *SettlementHandler) ListMismatches(c *gin.Context) {
	filter := model.ReconciliationListFilter{Page: model.DefaultPage, Limit: model.DefaultLimit, MismatchesOnly: true}
	h.applyReconQuery(c, &filter)
	if v := c.Query("settlement_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, response.CodeInvalidRequest, "Invalid settlement_id")
			return
		}
		filter.SettlementID = &id
	}
	if v := c.Query("merchant_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, response.CodeInvalidRequest, "Invalid merchant_id")
			return
		}
		filter.MerchantID = &id
	}
	if v := c.Query("provider"); v != "" {
		filter.Provider = &v
	}
	res, err := h.reconSvc.ListResults(c.Request.Context(), filter)
	if err != nil {
		h.mapError(c, err)
		return
	}
	response.List(c, res.Results, response.Pagination{
		Page: res.Page, Limit: res.Limit, Total: res.Total, TotalPages: res.TotalPages,
	})
}

// GetMismatch godoc
//
//	@Summary		Get a reconciliation result by ID
//	@Tags			admin-settlements
//	@Produce		json
//	@Param			X-Admin-Key	header	string	true	"Ops admin key"
//	@Param			id			path	string	true	"Reconciliation result UUID"
//	@Success		200			{object}	response.successEnvelope{data=model.ReconciliationResult}
//	@Failure		404			{object}	response.errorEnvelope
//	@Router			/api/v1/admin/reconciliation/mismatches/{id} [get]
func (h *SettlementHandler) GetMismatch(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid reconciliation result ID")
		return
	}
	res, err := h.reconSvc.GetResult(c.Request.Context(), id)
	if err != nil {
		h.mapError(c, err)
		return
	}
	response.OK(c, res)
}

func (h *SettlementHandler) applyReconQuery(c *gin.Context, filter *model.ReconciliationListFilter) {
	if p := c.Query("page"); p != "" {
		if v, e := strconv.Atoi(p); e == nil && v >= 1 {
			filter.Page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		if v, e := strconv.Atoi(l); e == nil && v >= 1 && v <= model.MaxLimit {
			filter.Limit = v
		}
	}
	if v := c.Query("result_type"); v != "" {
		rt := model.ReconciliationResultType(v)
		filter.ResultType = &rt
	}
	if v := c.Query("status"); v != "" {
		st := model.ReconciliationResultStatus(v)
		filter.Status = &st
	}
	if v := c.Query("reason_code"); v != "" {
		filter.ReasonCode = &v
	}
	if v := c.Query("date_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.DateFrom = &t
		}
	}
	if v := c.Query("date_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.DateTo = &t
		}
	}
}

func (h *SettlementHandler) mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrSettlementNotFound):
		response.NotFound(c, response.CodeSettlementNotFound, "Settlement not found")
	case errors.Is(err, service.ErrReconciliationNotFound):
		response.NotFound(c, response.CodeReconciliationNotFound, "Reconciliation result not found")
	case errors.Is(err, service.ErrSettlementImportInvalid), errors.Is(err, service.ErrUnknownSettlementProvider):
		response.BadRequest(c, response.CodeSettlementImportInvalid, strings.TrimPrefix(err.Error(), "settlement import invalid: "))
	case errors.Is(err, service.ErrSettlementAlreadyExists):
		response.Conflict(c, response.CodeSettlementAlreadyExists, "Settlement already exists")
	case errors.Is(err, service.ErrSettlementImportConflict):
		response.Conflict(c, response.CodeSettlementImportConflict, "Settlement reference exists with a different payload")
	case errors.Is(err, service.ErrReconciliationRunning):
		response.Conflict(c, response.CodeReconciliationAlreadyRunning, "Reconciliation already running")
	case errors.Is(err, service.ErrSettlementInvalidStatus):
		response.Conflict(c, response.CodeSettlementInvalidStatus, "Settlement cannot be reconciled in its current status")
	default:
		slog.Error("settlement handler error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
