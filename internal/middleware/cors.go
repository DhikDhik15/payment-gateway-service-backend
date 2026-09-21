// Package middleware provides Gin middleware for the payment gateway.
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS returns a Gin middleware that handles Cross-Origin Resource Sharing.
//
// Security rules:
//   - allowedOrigins must be an explicit list (never "*" when credentials are allowed).
//   - Access-Control-Allow-Credentials: true is only set when the request origin
//     is in the allowedOrigins list.
//   - Preflight OPTIONS requests are terminated early with 204.
//   - If allowedOrigins is empty, CORS headers are not emitted (no access).
//
// For the React dashboard, configure:
//
//	CORS_ALLOWED_ORIGINS=http://localhost:5173
func CORS(allowedOrigins string) gin.HandlerFunc {
	allowed := parseOrigins(allowedOrigins)

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		if origin != "" && isAllowedOrigin(origin, allowed) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID, X-Refresh-Token, X-Admin-Key, X-API-Key, Idempotency-Key")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Max-Age", "86400") // 24 hours preflight cache
			c.Header("Vary", "Origin")

			// Handle preflight.
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
		}

		c.Next()
	}
}

// parseOrigins splits a comma-separated list of origins into a trimmed slice.
func parseOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isAllowedOrigin checks whether origin is in the allowed list.
// Comparison is case-sensitive per the HTTP spec.
func isAllowedOrigin(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}
