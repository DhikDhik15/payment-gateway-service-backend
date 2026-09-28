// Package middleware provides Gin middleware for the payment gateway.
package middleware

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const headerAuthorization = "Authorization"

// RequireDashboardAuth returns a Gin middleware that authenticates requests
// using a JWT access token in the Authorization: Bearer header.
//
// On success:
//   - Validates JWT signature and expiry via authSvc.VerifyAccessToken
//   - Loads the authenticated user from the database
//   - Verifies the user is still ACTIVE
//   - Loads the user's merchant and verifies the merchant is ACTIVE
//   - Stores the user as model.ContextKeyDashboardUser in the Gin context
//
// On failure:
//   - Returns 401 with a generic error (no internal details exposed)
//   - Aborts the handler chain
func RequireDashboardAuth(authSvc service.AuthService, userRepo repository.MerchantUserRepository, merchantRepo repository.MerchantRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			slog.Warn("dashboard auth: missing token",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("path", c.Request.URL.Path),
			)
			response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
			c.Abort()
			return
		}

		claims, err := authSvc.VerifyAccessToken(token)
		if err != nil {
			if errors.Is(err, service.ErrJWTExpiredPublic) {
				response.Unauthorized(c, response.CodeInvalidCredentials, "Access token has expired")
			} else {
				response.Unauthorized(c, response.CodeInvalidCredentials, "Invalid access token")
			}
			c.Abort()
			return
		}

		// Parse user UUID from claims subject.
		userID, err := uuid.Parse(claims.Subject)
		if err != nil {
			response.Unauthorized(c, response.CodeInvalidCredentials, "Invalid access token")
			c.Abort()
			return
		}

		// Load user from DB to get current status (a disabled user must be
		// rejected even if their token has not yet expired).
		user, err := userRepo.GetByID(c.Request.Context(), userID)
		if err != nil {
			if errors.Is(err, repository.ErrMerchantUserNotFound) {
				// User was deleted after the token was issued.
				response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
			} else {
				slog.Error("dashboard auth: user lookup error",
					slog.String("request_id", c.GetString(response.ContextKey)),
					slog.String("error", err.Error()),
				)
				response.InternalServerError(c)
			}
			c.Abort()
			return
		}

		if !user.IsActive() {
			slog.Info("dashboard auth: disabled user request",
				slog.String("user_id", userID.String()),
			)
			response.Unauthorized(c, response.CodeUserDisabled, "Account is disabled")
			c.Abort()
			return
		}

		// Load the merchant and verify it is ACTIVE.
		// Merchant identity is derived from the authenticated user — never from
		// the request body or URL parameters.
		merchant, err := merchantRepo.GetByID(c.Request.Context(), user.MerchantID)
		if err != nil {
			if errors.Is(err, repository.ErrMerchantNotFound) {
				// Merchant was deleted after the user was created.
				response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
			} else {
				slog.Error("dashboard auth: merchant lookup error",
					slog.String("request_id", c.GetString(response.ContextKey)),
					slog.String("merchant_id", user.MerchantID.String()),
					slog.String("error", err.Error()),
				)
				response.InternalServerError(c)
			}
			c.Abort()
			return
		}

		if !merchant.IsActive() {
			slog.Info("dashboard auth: merchant not active",
				slog.String("user_id", userID.String()),
				slog.String("merchant_id", merchant.ID.String()),
				slog.String("merchant_status", string(merchant.Status)),
			)
			response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
			c.Abort()
			return
		}

		// Attach authenticated user to context.
		c.Set(model.ContextKeyDashboardUser, user)
		c.Request = c.Request.WithContext(audit.WithActor(c.Request.Context(), audit.Actor{
			Type:       audit.ActorTypeDashboardUser,
			UserID:     audit.UUIDPtr(user.ID),
			MerchantID: audit.UUIDPtr(user.MerchantID),
		}))
		c.Next()
	}
}

// RequireRole returns a Gin middleware that checks the authenticated dashboard
// user's role against the provided allowed roles.
//
// Must be used AFTER RequireDashboardAuth so the user is already in context.
func RequireRole(roles ...model.DashboardUserRole) gin.HandlerFunc {
	allowed := make(map[model.DashboardUserRole]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(c *gin.Context) {
		val, exists := c.Get(model.ContextKeyDashboardUser)
		if !exists {
			response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
			c.Abort()
			return
		}
		user, ok := val.(*model.MerchantUser)
		if !ok || user == nil {
			response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
			c.Abort()
			return
		}

		if !allowed[user.Role] {
			slog.Warn("dashboard auth: insufficient role",
				slog.String("user_id", user.ID.String()),
				slog.String("user_role", string(user.Role)),
				slog.String("path", c.Request.URL.Path),
			)
			response.Forbidden(c, response.CodeInsufficientRole, "Insufficient permissions for this operation")
			c.Abort()
			return
		}

		c.Next()
	}
}

// DashboardUserFromContext retrieves the authenticated dashboard user from the
// Gin context. Returns nil when called outside a RequireDashboardAuth route.
func DashboardUserFromContext(c *gin.Context) *model.MerchantUser {
	val, exists := c.Get(model.ContextKeyDashboardUser)
	if !exists {
		return nil
	}
	u, _ := val.(*model.MerchantUser)
	return u
}

// bearerToken extracts the token string from "Authorization: Bearer <token>".
// Returns "" when the header is absent or malformed.
func bearerToken(c *gin.Context) string {
	header := c.GetHeader(headerAuthorization)
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
