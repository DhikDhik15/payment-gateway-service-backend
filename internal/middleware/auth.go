package middleware

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

const headerAPIKey = "X-API-Key"

// Auth returns a Gin middleware that authenticates requests using an API key.
//
// Flow:
//  1. Read X-API-Key header.
//  2. Look up the merchant via the merchant service.
//  3. Verify the merchant is ACTIVE.
//  4. Store the merchant in the Gin context for downstream handlers.
//  5. Reject with 401 on any failure.
func Auth(merchantSvc service.MerchantService) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader(headerAPIKey)
		if apiKey == "" {
			slog.Warn("auth: missing api key",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("path", c.Request.URL.Path),
			)
			response.Unauthorized(c, response.CodeInvalidAPIKey, "API key is required")
			c.Abort()
			return
		}

		merchant, err := merchantSvc.GetMerchantByAPIKey(c.Request.Context(), apiKey)
		if err != nil {
			if errors.Is(err, repository.ErrMerchantNotFound) {
				slog.Warn("auth: invalid api key",
					slog.String("request_id", c.GetString(response.ContextKey)),
					slog.String("path", c.Request.URL.Path),
				)
				response.Unauthorized(c, response.CodeInvalidAPIKey, "Invalid API key")
				c.Abort()
				return
			}
			// Unexpected DB/service error — do not leak internals.
			slog.Error("auth: lookup failed",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
			c.Abort()
			return
		}

		if !merchant.IsActive() {
			slog.Warn("auth: merchant not active",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("merchant_id", merchant.ID.String()),
				slog.String("status", string(merchant.Status)),
			)
			response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
			c.Abort()
			return
		}

		// Attach merchant to context so handlers can retrieve it without
		// re-querying the database.
		c.Set(model.ContextKeyMerchant, merchant)
		c.Next()
	}
}

// MerchantFromContext retrieves the authenticated merchant from the Gin context.
// Returns nil when called outside of an Auth-protected route.
func MerchantFromContext(c *gin.Context) *model.Merchant {
	val, exists := c.Get(model.ContextKeyMerchant)
	if !exists {
		return nil
	}
	m, _ := val.(*model.Merchant)
	return m
}
