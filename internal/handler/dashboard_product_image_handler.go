package handler

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// uploadImageFormField is the only accepted multipart field name.
const uploadImageFormField = "image"

// DashboardProductImageHandler handles merchant-scoped product image uploads
// (authenticated) and public image serving.
type DashboardProductImageHandler struct {
	images service.ProductImageService
	cfg    service.ProductImageConfig
}

func NewDashboardProductImageHandler(images service.ProductImageService, cfg service.ProductImageConfig) *DashboardProductImageHandler {
	return &DashboardProductImageHandler{images: images, cfg: cfg}
}

// UploadProductImage godoc
//
// @Summary Upload a product image
// @Description Upload a JPEG, PNG, or WebP product image (multipart/form-data, field "image"). Returns a stable image_url reference for use in product create/PATCH payloads. The upload alone never modifies a product.
// @Tags Dashboard Products
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Param image formData file true "JPEG, PNG, or WebP image"
// @Success 201 {object} response.successEnvelope{data=service.ProductImageUpload}
// @Failure 400 {object} response.errorEnvelope
// @Failure 401 {object} response.errorEnvelope
// @Failure 403 {object} response.errorEnvelope
// @Failure 413 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/product-images [post]
func (h *DashboardProductImageHandler) UploadProductImage(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	file, header, err := c.Request.FormFile(uploadImageFormField)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.PayloadTooLarge(c, response.CodeRequestTooLarge, "Request body exceeds the configured HTTP_MAX_BODY_BYTES limit; raise it above PRODUCT_IMAGE_MAX_BYTES to upload images")
			return
		}
		if errors.Is(err, http.ErrMissingFile) || strings.Contains(strings.ToLower(err.Error()), "missing file") {
			response.ValidationError(c, map[string]string{uploadImageFormField: "An image file is required"})
			return
		}
		response.ValidationError(c, map[string]string{uploadImageFormField: "Unreadable multipart upload; send multipart/form-data with a single image field"})
		return
	}
	defer file.Close()

	// Belt and braces: the declared size is advisory, so the copy below is
	// hard-capped at MaxBytes+1 and anything beyond the cap is rejected.
	if header.Size > h.cfg.MaxBytes {
		response.PayloadTooLarge(c, response.CodeRequestTooLarge, "Image exceeds the maximum upload size")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, h.cfg.MaxBytes+1))
	if err != nil {
		slog.Error("product image read error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
		response.InternalServerError(c)
		return
	}
	if int64(len(data)) > h.cfg.MaxBytes {
		response.PayloadTooLarge(c, response.CodeRequestTooLarge, "Image exceeds the maximum upload size")
		return
	}

	upload, err := h.images.Upload(c.Request.Context(), caller.MerchantID, data, header.Filename)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrProductImageTooLarge):
			response.PayloadTooLarge(c, response.CodeRequestTooLarge, "Image exceeds the maximum upload size")
		case errors.Is(err, service.ErrProductImageUnsupportedType):
			response.ValidationError(c, map[string]string{uploadImageFormField: "Only JPEG, PNG, and WebP images are accepted"})
		case errors.Is(err, service.ErrProductImageInvalid):
			response.ValidationError(c, map[string]string{uploadImageFormField: "Image content is invalid or corrupt"})
		case errors.Is(err, service.ErrProductImageDimensions):
			response.ValidationError(c, map[string]string{uploadImageFormField: "Image dimensions exceed the allowed limits"})
		case errors.Is(err, service.ErrProductImageStore):
			slog.Error("product image store error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		default:
			slog.Error("product image upload error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.Created(c, upload)
}

// DeleteProductImage godoc
//
// @Summary Remove a product image
// @Description Clear the image reference of a merchant-scoped product (sets image_url to null) and delete the backing file when no other product references it. External HTTPS references are unlinked without file operations.
// @Tags Dashboard Products
// @Produce json
// @Security BearerAuth
// @Param product_id path string true "Product UUID"
// @Success 200 {object} response.successEnvelope{data=model.Product}
// @Failure 401 {object} response.errorEnvelope
// @Failure 403 {object} response.errorEnvelope
// @Failure 404 {object} response.errorEnvelope
// @Failure 500 {object} response.errorEnvelope
// @Router /api/v1/dashboard/products/{product_id}/image [delete]
func (h *DashboardProductImageHandler) DeleteProductImage(c *gin.Context) {
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
	product, err := h.images.ClearProductImage(c.Request.Context(), caller.MerchantID, id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrProductImageNotFound):
			response.NotFound(c, response.CodeResourceNotFound, "Product image not found")
		default:
			slog.Error("product image delete error", slog.String("merchant_id", caller.MerchantID.String()), slog.String("error", err.Error()))
			response.InternalServerError(c)
		}
		return
	}
	response.OK(c, product)
}

// ServeProductImage serves a stored product image publicly (no dashboard
// authentication — product images are public catalog assets, equivalent to
// the external HTTPS image_url values they replace). Filenames are
// server-generated UUIDs; invalid or unknown references return a bare 404
// with no filesystem detail.
func (h *DashboardProductImageHandler) ServeProductImage(c *gin.Context) {
	merchantID, err := uuid.Parse(c.Param("merchant_id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	filename := c.Param("filename")
	if !model.IsLocalProductImageRef(model.ProductImageURLPrefix + merchantID.String() + "/" + filename) {
		c.Status(http.StatusNotFound)
		return
	}
	rs, size, contentType, err := h.images.ReadProductImageForServe(merchantID, filename)
	if err != nil {
		if errors.Is(err, service.ErrProductImageNotFound) {
			c.Status(http.StatusNotFound)
			return
		}
		slog.Error("product image serve error", slog.String("error", err.Error()))
		c.Status(http.StatusInternalServerError)
		return
	}
	defer rs.Close()
	// Never log or echo storage paths; the URL reference is public already.
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Header("Cache-Control", "public, max-age=86400, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, filename, time.Time{}, rs)
}
