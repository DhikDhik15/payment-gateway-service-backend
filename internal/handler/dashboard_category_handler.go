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

// DashboardCategoryHandler handles merchant-scoped category CRUD.
type DashboardCategoryHandler struct {
	categorySvc service.CategoryService
}

func NewDashboardCategoryHandler(categorySvc service.CategoryService) *DashboardCategoryHandler {
	return &DashboardCategoryHandler{categorySvc: categorySvc}
}

// CreateCategory godoc
//
// @Summary Create category
// @Description Create a merchant-scoped category.
// @Tags Dashboard Categories
// @Produce json
// @Security BearerAuth
// @Param request body model.CreateCategoryRequest true "Category payload"
// @Success 201 {object} response.successEnvelope{data=model.Category}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 409 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/categories [post]
func (h *DashboardCategoryHandler) CreateCategory(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	var req model.CreateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	category, err := h.categorySvc.Create(c.Request.Context(), caller.MerchantID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCategoryInvalidName):
			response.ValidationError(c, map[string]string{"name": "Category name is required"})
		case errors.Is(err, service.ErrCategoryNameTaken):
			response.Conflict(c, response.CodeDuplicateMerchantCode, "Category name already exists")
		default:
			slog.Error("dashboard category create error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.Created(c, category)
}

// ListCategories godoc
//
// @Summary List categories
// @Description List categories for the authenticated merchant.
// @Tags Dashboard Categories
// @Produce json
// @Security BearerAuth
// @Success 200 {object} response.successEnvelope{data=[]model.Category}
// @Failure 401 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/categories [get]
func (h *DashboardCategoryHandler) ListCategories(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	items, err := h.categorySvc.List(c.Request.Context(), caller.MerchantID)
	if err != nil {
		slog.Error("dashboard category list error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
		response.InternalServerError(c)
		return
	}
	response.OK(c, items)
}

// GetCategory godoc
//
// @Summary Get category
// @Description Fetch one category for the authenticated merchant.
// @Tags Dashboard Categories
// @Produce json
// @Security BearerAuth
// @Param category_id path string true "Category UUID"
// @Success 200 {object} response.successEnvelope{data=model.Category}
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/categories/{category_id} [get]
func (h *DashboardCategoryHandler) GetCategory(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("category_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid category id")
		return
	}
	category, err := h.categorySvc.Get(c.Request.Context(), caller.MerchantID, id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCategoryNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Category not found")
		default:
			slog.Error("dashboard category get error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, category)
}

// UpdateCategory godoc
//
// @Summary Update category
// @Description Update a merchant-scoped category.
// @Tags Dashboard Categories
// @Produce json
// @Security BearerAuth
// @Param category_id path string true "Category UUID"
// @Param request body model.UpdateCategoryRequest true "Category updates"
// @Success 200 {object} response.successEnvelope{data=model.Category}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 409 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/categories/{category_id} [patch]
func (h *DashboardCategoryHandler) UpdateCategory(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("category_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid category id")
		return
	}
	var req model.UpdateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	category, err := h.categorySvc.Update(c.Request.Context(), caller.MerchantID, id, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCategoryNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Category not found")
		case errors.Is(err, service.ErrCategoryInvalidName):
			response.ValidationError(c, map[string]string{"name": "Category name is required"})
		case errors.Is(err, service.ErrCategoryNameTaken):
			response.Conflict(c, response.CodeDuplicateMerchantCode, "Category name already exists")
		default:
			slog.Error("dashboard category update error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, category)
}

// DeleteCategory godoc
//
// @Summary Delete category
// @Description Delete a category owned by the authenticated merchant.
// @Tags Dashboard Categories
// @Produce json
// @Security BearerAuth
// @Param category_id path string true "Category UUID"
// @Success 200 {object} response.successEnvelope{data=map[string]string}
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/categories/{category_id} [delete]
func (h *DashboardCategoryHandler) DeleteCategory(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("category_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid category id")
		return
	}
	if err := h.categorySvc.Delete(c.Request.Context(), caller.MerchantID, id); err != nil {
		switch {
		case errors.Is(err, service.ErrCategoryNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Category not found")
		default:
			slog.Error("dashboard category delete error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, gin.H{"status": "deleted"})
}
