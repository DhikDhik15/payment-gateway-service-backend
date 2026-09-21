package handler

import (
	"errors"
	"log/slog"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// MerchantAPIKeyHandler handles HTTP requests for the merchant API key lifecycle.
// It is intentionally thin: parse → validate format → authorize → call service → respond.
type MerchantAPIKeyHandler struct {
	keySvc service.MerchantAPIKeyService
}

// NewMerchantAPIKeyHandler constructs a MerchantAPIKeyHandler.
func NewMerchantAPIKeyHandler(keySvc service.MerchantAPIKeyService) *MerchantAPIKeyHandler {
	return &MerchantAPIKeyHandler{keySvc: keySvc}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// parseMerchantID extracts and validates the merchant :id path parameter.
// Gin requires this to share the same wildcard name as GET /merchants/:id.
func parseMerchantID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid merchant ID format")
		return uuid.UUID{}, false
	}
	return id, true
}

// parseKeyID extracts and validates the :key_id path parameter.
func parseKeyID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("key_id"))
	if err != nil {
		response.BadRequest(c, response.CodeInvalidRequest, "Invalid API key ID format")
		return uuid.UUID{}, false
	}
	return id, true
}

// authorizeForMerchant ensures the authenticated merchant is the same as the
// one in the path parameter. Cross-merchant key operations are forbidden.
func authorizeForMerchant(c *gin.Context, targetMerchantID uuid.UUID) bool {
	m := middleware.MerchantFromContext(c)
	if m == nil {
		response.InternalServerError(c)
		return false
	}
	if m.ID != targetMerchantID {
		response.Forbidden(c, response.CodeForbidden, "You do not have access to this merchant's resources")
		return false
	}
	return true
}

// ─── Create ───────────────────────────────────────────────────────────────────

// CreateAPIKey godoc
//
//	@Summary		Create an API key
//	@Description	Creates a new named API key for the specified merchant.
//	@Description
//	@Description	**Authorization**: the authenticated merchant (X-API-Key) must match path `{id}`.
//	@Description
//	@Description	**One-time secret**: the `secret` field in the response is the plaintext credential
//	@Description	and is returned **only on creation**. It cannot be retrieved again. Store it securely.
//	@Description
//	@Description	The full credential used for authentication is:  `key_id:secret`
//	@Description
//	@Description	**Expiration**: if `expires_at` is supplied it must be an RFC3339 timestamp in the future.
//	@Tags			api-keys
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id		path		string								true	"Merchant UUID"
//	@Param			body	body		model.CreateMerchantAPIKeyRequest	true	"Key creation request"
//	@Success		201		{object}	response.successEnvelope{data=model.CreateMerchantAPIKeyResponse}	"Key created; secret returned once only"
//	@Failure		400		{object}	response.errorEnvelope	"INVALID_REQUEST or VALIDATION_ERROR"
//	@Failure		401		{object}	response.errorEnvelope	"INVALID_API_KEY or MERCHANT_INACTIVE"
//	@Failure		403		{object}	response.errorEnvelope	"FORBIDDEN — wrong merchant"
//	@Failure		404		{object}	response.errorEnvelope	"MERCHANT_NOT_FOUND"
//	@Failure		500		{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/merchants/{id}/api-keys [post]
func (h *MerchantAPIKeyHandler) CreateAPIKey(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}

	var req model.CreateMerchantAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	result, err := h.keySvc.CreateKey(c.Request.Context(), merchantID, req)
	if err != nil {
		h.handleKeyServiceError(c, err)
		return
	}

	response.Created(c, result)
}

// ─── List ─────────────────────────────────────────────────────────────────────

// ListAPIKeys godoc
//
//	@Summary		List API keys
//	@Description	Returns all API keys for the specified merchant.
//	@Description
//	@Description	**Authorization**: the authenticated merchant (X-API-Key) must match path `{id}`.
//	@Description
//	@Description	The response never includes `secret` or `secret_hash`.
//	@Tags			api-keys
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id	path		string	true	"Merchant UUID"
//	@Success		200	{object}	response.successEnvelope{data=[]model.MerchantAPIKeyResponse}	"List of API keys"
//	@Failure		401	{object}	response.errorEnvelope	"INVALID_API_KEY or MERCHANT_INACTIVE"
//	@Failure		403	{object}	response.errorEnvelope	"FORBIDDEN — wrong merchant"
//	@Failure		500	{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/merchants/{id}/api-keys [get]
func (h *MerchantAPIKeyHandler) ListAPIKeys(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}

	keys, err := h.keySvc.ListKeys(c.Request.Context(), merchantID)
	if err != nil {
		slog.Error("api key handler list: unexpected error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
		return
	}

	if keys == nil {
		keys = []model.MerchantAPIKeyResponse{}
	}
	response.OK(c, keys)
}

// ─── Revoke ───────────────────────────────────────────────────────────────────

// RevokeAPIKey godoc
//
//	@Summary		Revoke an API key
//	@Description	Revokes an API key. The key immediately becomes unusable for authentication.
//	@Description
//	@Description	**Authorization**: the authenticated merchant (X-API-Key) must match path `{id}`.
//	@Description
//	@Description	Revocation is permanent (soft delete — record is preserved with status REVOKED).
//	@Tags			api-keys
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id		path		string	true	"Merchant UUID"
//	@Param			key_id	path		string	true	"API key UUID"
//	@Success		200		{object}	response.successEnvelope{data=object}	"Key revoked"
//	@Failure		400		{object}	response.errorEnvelope	"INVALID_REQUEST"
//	@Failure		401		{object}	response.errorEnvelope	"INVALID_API_KEY or MERCHANT_INACTIVE"
//	@Failure		403		{object}	response.errorEnvelope	"FORBIDDEN — wrong merchant"
//	@Failure		404		{object}	response.errorEnvelope	"API_KEY_NOT_FOUND"
//	@Failure		409		{object}	response.errorEnvelope	"API_KEY_ALREADY_REVOKED"
//	@Failure		500		{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/merchants/{id}/api-keys/{key_id} [delete]
func (h *MerchantAPIKeyHandler) RevokeAPIKey(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}

	keyID, ok := parseKeyID(c)
	if !ok {
		return
	}

	if err := h.keySvc.RevokeKey(c.Request.Context(), merchantID, keyID); err != nil {
		h.handleKeyServiceError(c, err)
		return
	}

	response.OK(c, map[string]string{"message": "API key revoked"})
}

// ─── Rotate ───────────────────────────────────────────────────────────────────

// RotateAPIKey godoc
//
//	@Summary		Rotate an API key
//	@Description	Atomically revokes the existing key and creates a new replacement.
//	@Description
//	@Description	**Authorization**: the authenticated merchant (X-API-Key) must match path `{id}`.
//	@Description
//	@Description	**One-time secret**: the `secret` field in the response is the new plaintext credential.
//	@Description	It is returned **only on rotation**. Store it securely — it cannot be retrieved again.
//	@Description
//	@Description	The old key is immediately revoked and can no longer authenticate.
//	@Description
//	@Description	The new credential format: `key_id:secret`
//	@Tags			api-keys
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			id		path		string	true	"Merchant UUID"
//	@Param			key_id	path		string	true	"API key UUID (the key to rotate)"
//	@Success		201		{object}	response.successEnvelope{data=model.RotateMerchantAPIKeyResponse}	"New key; secret returned once only"
//	@Failure		400		{object}	response.errorEnvelope	"INVALID_REQUEST"
//	@Failure		401		{object}	response.errorEnvelope	"INVALID_API_KEY or MERCHANT_INACTIVE"
//	@Failure		403		{object}	response.errorEnvelope	"FORBIDDEN — wrong merchant"
//	@Failure		404		{object}	response.errorEnvelope	"API_KEY_NOT_FOUND"
//	@Failure		409		{object}	response.errorEnvelope	"API_KEY_ALREADY_REVOKED"
//	@Failure		500		{object}	response.errorEnvelope	"INTERNAL_ERROR"
//	@Router			/api/v1/merchants/{id}/api-keys/{key_id}/rotate [post]
func (h *MerchantAPIKeyHandler) RotateAPIKey(c *gin.Context) {
	merchantID, ok := parseMerchantID(c)
	if !ok {
		return
	}
	if !authorizeForMerchant(c, merchantID) {
		return
	}

	keyID, ok := parseKeyID(c)
	if !ok {
		return
	}

	result, err := h.keySvc.RotateKey(c.Request.Context(), merchantID, keyID)
	if err != nil {
		h.handleKeyServiceError(c, err)
		return
	}

	response.Created(c, result)
}

// ─── error mapping ────────────────────────────────────────────────────────────

func (h *MerchantAPIKeyHandler) handleKeyServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAPIKeyNotFound):
		response.NotFound(c, response.CodeAPIKeyNotFound, "API key not found")

	case errors.Is(err, service.ErrAPIKeyAlreadyRevoked):
		response.Conflict(c, response.CodeAPIKeyAlreadyRevoked, "API key is already revoked")

	case errors.Is(err, service.ErrAPIKeyExpirationPast):
		response.ValidationError(c, map[string]string{"expires_at": "must be a future timestamp"})

	case errors.Is(err, repository.ErrMerchantNotFound):
		response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")

	default:
		slog.Error("api key handler: unexpected error",
			slog.String("request_id", c.GetString(response.ContextKey)),
			slog.String("error", err.Error()),
		)
		response.InternalServerError(c)
	}
}
