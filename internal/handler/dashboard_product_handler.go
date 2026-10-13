package handler

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DashboardProductHandler handles merchant-scoped product CRUD.
type DashboardProductHandler struct {
	productSvc service.ProductService
}

func NewDashboardProductHandler(productSvc service.ProductService) *DashboardProductHandler {
	return &DashboardProductHandler{productSvc: productSvc}
}

// CreateProduct godoc
//
// @Summary Create product
// @Description Create a merchant-scoped product.
// @Tags Dashboard Products
// @Produce json
// @Security BearerAuth
// @Param request body model.CreateProductRequest true "Product payload"
// @Success 201 {object} response.successEnvelope{data=model.Product}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 409 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/products [post]
func (h *DashboardProductHandler) CreateProduct(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	var req model.CreateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	product, err := h.productSvc.Create(c.Request.Context(), caller.MerchantID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCategoryNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Category not found")
		case errors.Is(err, service.ErrProductInvalidName):
			response.ValidationError(c, map[string]string{"name": "Product name is required"})
		case errors.Is(err, service.ErrProductInvalidPrice):
			response.ValidationError(c, map[string]string{"price": "Price must be zero or positive"})
		case errors.Is(err, service.ErrProductInvalidCurrency):
			response.ValidationError(c, map[string]string{"currency": "Currency must be IDR"})
		case errors.Is(err, service.ErrProductInvalidImage):
			response.ValidationError(c, map[string]string{"image_url": "Must be a valid HTTPS URL"})
		case errors.Is(err, service.ErrProductSKUUsed):
			response.Conflict(c, response.CodeDuplicateMerchantCode, "SKU already exists")
		default:
			slog.Error("dashboard product create error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.Created(c, product)
}

// ListProducts godoc
//
// @Summary List products
// @Description List merchant products with optional filtering and pagination.
// @Tags Dashboard Products
// @Produce json
// @Security BearerAuth
// @Param category_id query string false "Category UUID filter"
// @Param active query bool false "Filter by active status"
// @Param search query string false "Search in name, SKU, description"
// @Param page query int false "Page (default 1)"
// @Param limit query int false "Page size (default 20, max 100)"
// @Success 200 {object} response.successEnvelope{data=[]model.Product}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/products [get]
func (h *DashboardProductHandler) ListProducts(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	filter := model.ListProductsFilter{Search: c.Query("search")}
	if rawCategory := c.Query("category_id"); rawCategory != "" {
		id, err := uuid.Parse(rawCategory)
		if err != nil {
			response.ValidationError(c, map[string]string{"category_id": "Must be a valid UUID"})
			return
		}
		filter.CategoryID = &id
	}
	if rawActive := c.Query("active"); rawActive != "" {
		value, err := strconv.ParseBool(rawActive)
		if err != nil {
			response.ValidationError(c, map[string]string{"active": "Must be true or false"})
			return
		}
		filter.Active = &value
	}
	page, errPage := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, errLimit := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if errPage != nil || errLimit != nil || page < 1 || limit < 1 || limit > model.MaxLimit {
		response.ValidationError(c, map[string]string{"page": "Must be a positive integer", "limit": "Must be between 1 and 100"})
		return
	}
	result, err := h.productSvc.List(c.Request.Context(), caller.MerchantID, filter, page, limit)
	if err != nil {
		if errors.Is(err, service.ErrProductInvalidPage) {
			response.ValidationError(c, map[string]string{"page": "Page is too large"})
			return
		}
		slog.Error("dashboard product list error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
		response.InternalServerError(c)
		return
	}
	response.List(c, result.Products, response.Pagination{Page: result.Page, Limit: result.Limit, Total: result.Total, TotalPages: result.TotalPages})
}

// GetProduct godoc
//
// @Summary Get product
// @Description Fetch one product for the authenticated merchant.
// @Tags Dashboard Products
// @Produce json
// @Security BearerAuth
// @Param product_id path string true "Product UUID"
// @Success 200 {object} response.successEnvelope{data=model.Product}
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/products/{product_id} [get]
func (h *DashboardProductHandler) GetProduct(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("product_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid product id")
		return
	}
	product, err := h.productSvc.Get(c.Request.Context(), caller.MerchantID, id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrProductNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Product not found")
		default:
			slog.Error("dashboard product get error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, product)
}

// UpdateProduct godoc
//
// @Summary Update product
// @Description Update a merchant-scoped product.
// @Tags Dashboard Products
// @Produce json
// @Security BearerAuth
// @Param product_id path string true "Product UUID"
// @Param request body model.UpdateProductRequest true "Product updates"
// @Success 200 {object} response.successEnvelope{data=model.Product}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 409 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/products/{product_id} [patch]
func (h *DashboardProductHandler) UpdateProduct(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("product_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid product id")
		return
	}
	var req model.UpdateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}
	product, err := h.productSvc.Update(c.Request.Context(), caller.MerchantID, id, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCategoryNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Category not found")
		case errors.Is(err, service.ErrProductNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Product not found")
		case errors.Is(err, service.ErrProductInvalidName):
			response.ValidationError(c, map[string]string{"name": "Product name is required"})
		case errors.Is(err, service.ErrProductInvalidPrice):
			response.ValidationError(c, map[string]string{"price": "Price must be zero or positive"})
		case errors.Is(err, service.ErrProductInvalidCurrency):
			response.ValidationError(c, map[string]string{"currency": "Currency must be IDR"})
		case errors.Is(err, service.ErrProductInvalidImage):
			response.ValidationError(c, map[string]string{"image_url": "Must be a valid HTTPS URL"})
		case errors.Is(err, service.ErrProductSKUUsed):
			response.Conflict(c, response.CodeDuplicateMerchantCode, "SKU already exists")
		default:
			slog.Error("dashboard product update error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, product)
}

// DeleteProduct godoc
//
// @Summary Delete product
// @Description Delete a product from the authenticated merchant catalog.
// @Tags Dashboard Products
// @Produce json
// @Security BearerAuth
// @Param product_id path string true "Product UUID"
// @Success 200 {object} response.successEnvelope{data=map[string]string}
// @Failure 401 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/products/{product_id} [delete]
func (h *DashboardProductHandler) DeleteProduct(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("product_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid product id")
		return
	}
	if err := h.productSvc.Delete(c.Request.Context(), caller.MerchantID, id); err != nil {
		switch {
		case errors.Is(err, service.ErrProductNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Product not found")
		default:
			slog.Error("dashboard product delete error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, gin.H{"status": "deleted"})
}
