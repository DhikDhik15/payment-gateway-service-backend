package middleware

import (
	"crypto/subtle"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
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
func AdminAuth(adminKey string, auditServices ...service.AuditService) gin.HandlerFunc {
	configured := strings.TrimSpace(adminKey)
	var auditSvc service.AuditService
	if len(auditServices) > 0 {
		auditSvc = auditServices[0]
	}
	return func(c *gin.Context) {
		if configured == "" {
			recordAdminAuthFailure(c, auditSvc, "not_configured")
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
			reason := "invalid_key"
			if provided == "" {
				reason = "missing_key"
			}
			recordAdminAuthFailure(c, auditSvc, reason)
			slog.Warn("admin auth: invalid or missing admin key",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("path", c.Request.URL.Path),
			)
			response.Unauthorized(c, response.CodeAdminUnauthorized, "Valid X-Admin-Key is required")
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(audit.WithActor(c.Request.Context(), audit.Actor{
			Type: audit.ActorTypeAdmin,
		}))
		c.Next()
	}
}

func recordAdminAuthFailure(c *gin.Context, auditSvc service.AuditService, reason string) {
	if auditSvc == nil {
		return
	}
	path := c.FullPath()
	if path == "" {
		path = "unmatched"
	}
	event, err := audit.NewEventFromContext(
		c.Request.Context(),
		audit.ActionAdminAuthFailed,
		audit.TargetAdminAuth,
		nil,
		nil,
		map[string]any{
			"reason": reason,
			"path":   path,
		},
	)
	event.ActorType = audit.ActorTypeUnauthenticated
	event.ActorUserID = nil
	if err != nil {
		slog.Warn("admin auth: failed to construct audit event",
			slog.String("request_id", audit.RequestMetadataFromContext(c.Request.Context()).RequestID),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := auditSvc.RecordBestEffort(c.Request.Context(), event); err != nil {
		// The authentication response remains 401 regardless of audit storage
		// health; RecordBestEffort has already logged safe event dimensions.
		slog.Warn("admin auth: audit failure did not alter authentication result",
			slog.String("request_id", audit.RequestMetadataFromContext(c.Request.Context()).RequestID),
			slog.String("error", err.Error()),
		)
	}
}
