package middleware

import (
	"crypto/subtle"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

const headerAdminKey = "X-Admin-Key"

// AdminAuth protects Phase 7C ops endpoints with a shared admin key.
//
// IMPORTANT: This is a minimal ops abstraction for Phase 7C — NOT production-grade
// admin authentication (no RBAC, rotation, MFA, or audit identity). Merchant API
// keys must NEVER be accepted here.
//
// If adminKey is empty, all admin routes return 503 ADMIN_NOT_CONFIGURED.
func AdminAuth(adminKey string) gin.HandlerFunc {
	configured := strings.TrimSpace(adminKey)
	return func(c *gin.Context) {
		if configured == "" {
			slog.Warn("admin auth: ADMIN_API_KEY not configured",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("path", c.Request.URL.Path),
			)
			response.ServiceUnavailable(c, response.CodeAdminNotConfigured, "Admin API is not configured")
			c.Abort()
			return
		}
		provided := strings.TrimSpace(c.GetHeader(headerAdminKey))
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) != 1 {
			slog.Warn("admin auth: invalid or missing admin key",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("path", c.Request.URL.Path),
			)
			response.Unauthorized(c, response.CodeAdminUnauthorized, "Valid X-Admin-Key is required")
			c.Abort()
			return
		}
		c.Next()
	}
}
