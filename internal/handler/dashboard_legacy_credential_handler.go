package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// Swagger type anchors. The @Success annotations above reference these
// model types; swag can only resolve them while the package is imported, and
// Go rejects an import used solely by comments — so pin them here.
var (
	_ *model.MigrateLegacyCredentialResponse
	_ *model.LegacyCredentialStatusResponse
)

// DashboardLegacyCredentialHandler serves the Phase 8D.3 legacy plaintext
// credential migration window: migrate a tenant onto the canonical Phase 5C
// key system, then disable its row-level legacy credential.
//
// Tenant isolation: the merchant is ALWAYS derived from the authenticated
// dashboard user's JWT claims. No merchant_id is accepted from the path, the
// query, or the body, so a caller can only ever act on its own merchant.
type DashboardLegacyCredentialHandler struct {
	legacySvc service.LegacyCredentialService
}

// NewDashboardLegacyCredentialHandler constructs a DashboardLegacyCredentialHandler.
func NewDashboardLegacyCredentialHandler(legacySvc service.LegacyCredentialService) *DashboardLegacyCredentialHandler {
	return &DashboardLegacyCredentialHandler{legacySvc: legacySvc}
}

// MigrateLegacyCredential godoc
//
//	@Summary		Migrate the legacy credential to a Phase 5C API key (dashboard)
//	@Description	One-time, atomic LEGACY → MIGRATED transition for the authenticated merchant.
//	@Description	Creates a canonical Phase 5C credential and returns its plaintext secret **exactly once** — it is never stored, logged, or retrievable afterwards.
//	@Description	Migrating again returns 409 LEGACY_CREDENTIAL_ALREADY_MIGRATED (including the losing side of a concurrent call).
//	@Description	The legacy key (if any) keeps working until POST /legacy-credential/disable.
//	@Description	Requires OWNER or ADMIN. VIEWER receives 403.
//	@Tags			Dashboard Legacy Credentials
//	@Produce		json
//	@Security		BearerAuth
//	@Success		201	{object}	response.successEnvelope{data=model.MigrateLegacyCredentialResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		409	{object}	response.errorEnvelope	"LEGACY_CREDENTIAL_ALREADY_MIGRATED"
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/legacy-credential/migrate [post]
func (h *DashboardLegacyCredentialHandler) MigrateLegacyCredential(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	result, err := h.legacySvc.Migrate(c.Request.Context(), caller.MerchantID)
	if err != nil {
		h.handleError(c, err, "migrate")
		return
	}
	// 201 carries the one-time plaintext secret — never logged, never echoed
	// anywhere else.
	response.Created(c, result)
}

// DisableLegacyCredential godoc
//
//	@Summary		Disable the legacy plaintext credential (dashboard)
//	@Description	Atomic MIGRATED → LEGACY_DISABLED transition for the authenticated merchant: the legacy key can never authenticate again.
//	@Description	Idempotent — repeating the call returns 200 with already_disabled=true and never rewrites the original legacy_credential_disabled_at.
//	@Description	Requires the merchant to be MIGRATED first; a still-LEGACY merchant returns 409 LEGACY_CREDENTIAL_MIGRATION_REQUIRED.
//	@Description	Requires OWNER or ADMIN. VIEWER receives 403.
//	@Tags			Dashboard Legacy Credentials
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	response.successEnvelope{data=model.LegacyCredentialStatusResponse}
//	@Failure		401	{object}	response.errorEnvelope
//	@Failure		403	{object}	response.errorEnvelope
//	@Failure		404	{object}	response.errorEnvelope
//	@Failure		409	{object}	response.errorEnvelope	"LEGACY_CREDENTIAL_MIGRATION_REQUIRED"
//	@Failure		500	{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/legacy-credential/disable [post]
func (h *DashboardLegacyCredentialHandler) DisableLegacyCredential(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	result, err := h.legacySvc.Disable(c.Request.Context(), caller.MerchantID)
	if err != nil {
		h.handleError(c, err, "disable")
		return
	}
	response.OK(c, result)
}

// handleError maps service errors to stable HTTP responses. Messages are
// static strings — no credential material, no internal detail.
func (h *DashboardLegacyCredentialHandler) handleError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, service.ErrLegacyCredentialAlreadyMigrated):
		response.Conflict(c, response.CodeLegacyCredentialAlreadyMigrated,
			"Legacy credential is already migrated")
	case errors.Is(err, service.ErrLegacyCredentialMigrationRequired):
		response.Conflict(c, response.CodeLegacyCredentialMigrationRequired,
			"Migrate the legacy credential before disabling it")
	case errors.Is(err, repository.ErrMerchantNotFound):
		response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
	default:
		slog.Error("dashboard legacy credential error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("operation", op),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
