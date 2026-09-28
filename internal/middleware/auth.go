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
)

const headerAPIKey = "X-API-Key"

// Auth returns a Gin middleware that authenticates requests using an API key.
//
// Authentication strategy (backward-compatible, two-stage):
//
//  1. Try the new merchant API key system first when the credential looks like
//     the Phase 5C compound format: "pk_<hex>:sk_<hex>".
//     On success → resolve merchant, set context, proceed.
//     On failure → 401 immediately (no legacy fallback for compound credentials).
//     This stage is completely unaffected by legacyCredentialsEnabled.
//
//  2. Fall back to legacy API key authentication (merchants.api_key column).
//     Legacy keys are bare "pk_<hex>" values WITHOUT a ":sk_" secret segment.
//     Phase 8D.3 gates this stage before anything else:
//     - legacyCredentialsEnabled == false → 401 LEGACY_CREDENTIALS_NOT_ENABLED
//     with NO database lookup, so the response never confirms whether the
//     presented credential exists or is valid.
//     - the merchant's LegacyCredentialState must AllowLegacyAuth
//     (LEGACY or MIGRATED). LEGACY_DISABLED — or any unknown value — is
//     rejected as a uniform 401 INVALID_API_KEY (no validity oracle), and
//     this check runs BEFORE the ACTIVE check so a disabled credential can
//     never be distinguished from an unknown one by a status code.
//
//  3. If both fail → 401 INVALID_API_KEY.
//
// Important invariants:
//   - The merchant must be ACTIVE for both auth paths (unchanged from 8A–8D.2).
//   - A revoked or expired new key does NOT fall through to legacy auth —
//     an explicitly invalid compound credential produces 401 immediately.
//   - Legacy credentials (bare pk_…) are never routed into the new-key verifier,
//     so existing Phase 1–4 merchants continue to work unchanged while the
//     migration window is open.
//   - The flag and the state are checked BEFORE the credential lookup; the flag
//     is configuration (never secret), the state result is indistinguishable
//     from an unknown key.
func Auth(merchantSvc service.MerchantService, apiKeySvc service.MerchantAPIKeyService, legacyCredentialsEnabled bool) gin.HandlerFunc {
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

		// ── Stage 1: new merchant API key authentication ───────────────────
		// Compound format only: "pk_<hex>:sk_<hex>".
		// Bare legacy "pk_<hex>" must NOT enter this path.
		if isNewStyleKey(apiKey) {
			merchant, err := apiKeySvc.AuthenticateByAPIKey(c.Request.Context(), apiKey)
			if err == nil {
				// New key auth succeeded.
				if !merchant.IsActive() {
					slog.Warn("auth: merchant not active (new key)",
						slog.String("request_id", c.GetString(response.ContextKey)),
						slog.String("merchant_id", merchant.ID.String()),
					)
					response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
					c.Abort()
					return
				}
				c.Set(model.ContextKeyMerchant, merchant)
				c.Request = c.Request.WithContext(audit.WithActor(c.Request.Context(), audit.Actor{
					Type:       audit.ActorTypeAPIKey,
					MerchantID: audit.UUIDPtr(merchant.ID),
				}))
				c.Next()
				return
			}

			// Compound credential failed verification — do not attempt legacy
			// fallback (would allow an invalid new secret to match a legacy key
			// string by coincidence, and would confuse credential formats).
			logAuthFailure(c, apiKey)
			response.Unauthorized(c, response.CodeInvalidAPIKey, "Invalid API key")
			c.Abort()
			return
		}

		// ── Stage 2: legacy API key authentication (merchants.api_key) ────

		// Phase 8D.3 gate 1: the migration window flag. Checked BEFORE any
		// database lookup so a closed window never reveals whether a bare key
		// matches a stored credential.
		if !legacyCredentialsEnabled {
			slog.Warn("auth: legacy credentials not enabled",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("path", c.Request.URL.Path),
				// credential is intentionally NOT logged
			)
			response.Unauthorized(c, response.CodeLegacyCredentialsNotEnabled, "Legacy API credentials are not enabled")
			c.Abort()
			return
		}

		merchant, err := merchantSvc.GetMerchantByAPIKey(c.Request.Context(), apiKey)
		if err != nil {
			if errors.Is(err, repository.ErrMerchantNotFound) {
				logAuthFailure(c, apiKey)
				response.Unauthorized(c, response.CodeInvalidAPIKey, "Invalid API key")
				c.Abort()
				return
			}
			// Unexpected DB/service error.
			slog.Error("auth: legacy lookup failed",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
			c.Abort()
			return
		}

		// Phase 8D.3 gate 2: the merchant's migration state. Runs before the
		// ACTIVE check so a LEGACY_DISABLED credential is uniformly
		// indistinguishable from an unknown key (no status oracle), and an
		// unknown/empty state fails closed.
		if !merchant.LegacyCredentialState.AllowsLegacyAuth() {
			logAuthFailure(c, apiKey)
			response.Unauthorized(c, response.CodeInvalidAPIKey, "Invalid API key")
			c.Abort()
			return
		}

		if !merchant.IsActive() {
			slog.Warn("auth: merchant not active (legacy)",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("merchant_id", merchant.ID.String()),
				slog.String("status", string(merchant.Status)),
			)
			response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
			c.Abort()
			return
		}

		c.Set(model.ContextKeyMerchant, merchant)
		c.Request = c.Request.WithContext(audit.WithActor(c.Request.Context(), audit.Actor{
			Type:       audit.ActorTypeAPIKey,
			MerchantID: audit.UUIDPtr(merchant.ID),
		}))
		c.Next()
	}
}

// isNewStyleKey returns true when the credential is the Phase 5C compound
// format "pk_<id>:sk_<secret>". Legacy Phase 1 keys are bare "pk_<hex>" and
// must return false so authentication falls through to merchants.api_key.
func isNewStyleKey(key string) bool {
	idx := strings.Index(key, ":")
	if idx < 0 {
		return false
	}
	return strings.HasPrefix(key, "pk_") && strings.HasPrefix(key[idx+1:], "sk_")
}

// logAuthFailure emits a warning log without including the credential value.
func logAuthFailure(c *gin.Context, _ string) {
	slog.Warn("auth: invalid api key",
		slog.String("request_id", c.GetString(response.ContextKey)),
		slog.String("path", c.Request.URL.Path),
		// credential is intentionally NOT logged
	)
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
