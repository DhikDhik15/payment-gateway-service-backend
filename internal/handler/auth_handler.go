package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

const refreshTokenCookie = "refresh_token"

// AuthHandler handles dashboard authentication endpoints.
type AuthHandler struct {
	authSvc         service.AuthService
	isProd          bool
	refreshTokenTTL time.Duration
}

// NewAuthHandler constructs an AuthHandler.
// refreshTokenTTL is used to set the MaxAge on the refresh cookie.
func NewAuthHandler(authSvc service.AuthService, isProd bool, refreshTokenTTL time.Duration) *AuthHandler {
	return &AuthHandler{
		authSvc:         authSvc,
		isProd:          isProd,
		refreshTokenTTL: refreshTokenTTL,
	}
}

// ─── Login ─────────────────────────────────────────────────────────────────────

// Login godoc
//
//	@Summary		Dashboard login
//	@Description	Authenticates a dashboard user with email and password.
//	@Description	On success, sets an HttpOnly refresh_token cookie and returns a short-lived access token.
//	@Description	Include the access token in subsequent requests as: Authorization: Bearer <token>
//	@Description	The refresh token is delivered via HttpOnly cookie only — not in the JSON body.
//	@Tags			Dashboard Auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		model.LoginRequest	true	"Login credentials"
//	@Success		200		{object}	model.LoginResponse
//	@Failure		400		{object}	response.errorEnvelope
//	@Failure		401		{object}	response.errorEnvelope	"Invalid credentials or disabled account"
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	loginResp, plainRefresh, err := h.authSvc.Login(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			slog.Info("dashboard auth: login failed — invalid credentials",
				slog.String("request_id", c.GetString(response.ContextKey)),
				// email is NOT logged to avoid confirming user existence
			)
			response.Unauthorized(c, response.CodeInvalidCredentials, "Invalid email or password")
			return
		}
		if errors.Is(err, service.ErrInvalidEmail) {
			response.ValidationError(c, map[string]string{"email": "must be a valid email address"})
			return
		}
		if errors.Is(err, service.ErrUserDisabled) {
			slog.Info("dashboard auth: login failed — account disabled",
				slog.String("request_id", c.GetString(response.ContextKey)),
			)
			// Return 401 (not 403) — same status for both failure modes to prevent enumeration.
			response.Unauthorized(c, response.CodeUserDisabled, "Account is disabled")
			return
		}
		slog.Error("dashboard auth: login error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	h.setRefreshCookie(c, plainRefresh)
	response.OK(c, loginResp)
}

// ─── Logout ───────────────────────────────────────────────────────────────────

// Logout godoc
//
//	@Summary		Dashboard logout
//	@Description	Revokes the current session. The HttpOnly refresh_token cookie is cleared.
//	@Description	Accepts the refresh token from the HttpOnly cookie or X-Refresh-Token header.
//	@Tags			Dashboard Auth
//	@Produce		json
//	@Success		200	{object}	object	"Empty object"
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/auth/logout [post]
//	@Security		BearerAuth
func (h *AuthHandler) Logout(c *gin.Context) {
	plainRefresh := refreshTokenFromRequest(c)

	if plainRefresh != "" {
		tokenHash := service.HashRefreshTokenPublic(plainRefresh)
		if err := h.authSvc.Logout(c.Request.Context(), tokenHash); err != nil {
			slog.Error("dashboard auth: logout error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
				// token/hash are NEVER logged
			)
			response.InternalServerError(c)
			return
		}
	}

	// Always clear the cookie regardless of whether a valid token was found.
	h.clearRefreshCookie(c)
	response.OK(c, gin.H{})
}

// ─── Me ───────────────────────────────────────────────────────────────────────

// Me godoc
//
//	@Summary		Get current dashboard user
//	@Description	Returns the profile of the currently authenticated dashboard user.
//	@Description	Requires a valid access token in the Authorization: Bearer header.
//	@Tags			Dashboard Auth
//	@Produce		json
//	@Success		200	{object}	model.DashboardUserResponse
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/auth/me [get]
//	@Security		BearerAuth
func (h *AuthHandler) Me(c *gin.Context) {
	user := dashboardUserFromContext(c)
	if user == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}
	resp := model.DashboardUserResponse{
		ID:          user.ID,
		MerchantID:  user.MerchantID,
		Email:       user.Email,
		Role:        user.Role,
		Status:      user.Status,
		LastLoginAt: user.LastLoginAt,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
	}
	response.OK(c, resp)
}

// ─── Refresh ──────────────────────────────────────────────────────────────────

// Refresh godoc
//
//	@Summary		Refresh access token
//	@Description	Issues a new short-lived access token using the HttpOnly refresh_token cookie.
//	@Description	The old session is rotated (revoked and replaced with a new one).
//	@Tags			Dashboard Auth
//	@Produce		json
//	@Success		200	{object}	model.LoginResponse
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	plainRefresh := refreshTokenFromRequest(c)
	if plainRefresh == "" {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Refresh token is required")
		return
	}

	loginResp, newPlainRefresh, err := h.authSvc.RefreshAccessToken(c.Request.Context(), plainRefresh)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) || errors.Is(err, service.ErrUserDisabled) {
			h.clearRefreshCookie(c)
			response.Unauthorized(c, response.CodeInvalidCredentials, "Invalid or expired refresh token")
			return
		}
		slog.Error("dashboard auth: refresh error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	h.setRefreshCookie(c, newPlainRefresh)
	response.OK(c, loginResp)
}

// ─── cookie helpers ──────────────────────────────────────────────────────────

func (h *AuthHandler) setRefreshCookie(c *gin.Context, plainToken string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     refreshTokenCookie,
		Value:    plainToken,
		MaxAge:   int(h.refreshTokenTTL.Seconds()),
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   h.isProd,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *AuthHandler) clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     refreshTokenCookie,
		Value:    "",
		MaxAge:   -1,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   h.isProd,
		SameSite: http.SameSiteStrictMode,
	})
}

// ─── context helpers ──────────────────────────────────────────────────────────

// refreshTokenFromRequest reads the plaintext refresh token from the HttpOnly
// cookie first, then falls back to X-Refresh-Token header for non-browser clients.
func refreshTokenFromRequest(c *gin.Context) string {
	if cookie, err := c.Cookie(refreshTokenCookie); err == nil && cookie != "" {
		return cookie
	}
	return strings.TrimSpace(c.GetHeader("X-Refresh-Token"))
}

// dashboardUserFromContext retrieves the authenticated dashboard user stored
// by the RequireDashboardAuth middleware.
func dashboardUserFromContext(c *gin.Context) *model.MerchantUser {
	val, exists := c.Get(model.ContextKeyDashboardUser)
	if !exists {
		return nil
	}
	u, _ := val.(*model.MerchantUser)
	return u
}
