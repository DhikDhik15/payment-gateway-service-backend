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

// InvitationHandler handles Phase 8B self-service team invitation endpoints.
//
// Create requires dashboard authentication (RequireDashboardAuth +
// RequireRole(OWNER)); GetByToken and Accept are unauthenticated because the
// invitee has no account yet — they are guarded solely by the opaque token.
type InvitationHandler struct {
	svc service.InvitationService
}

// NewInvitationHandler constructs an InvitationHandler.
func NewInvitationHandler(svc service.InvitationService) *InvitationHandler {
	return &InvitationHandler{svc: svc}
}

// Create godoc
//
//	@Summary		Create team invitation
//	@Description	Creates a PENDING invitation for the authenticated user's merchant and
//	@Description	returns the plaintext invitation token ONCE so an external system or
//	@Description	frontend can construct and deliver the invitation link.
//	@Description
//	@Description	**Email delivery (Phase 8C.3):** the invitation email (text + HTML,
//	@Description	same token, link built from
//	@Description	DASHBOARD_BASE_URL/accept-invitation?token=…) is queued for
//	@Description	asynchronous delivery: the invitation row and its queued email job are
//	@Description	committed atomically in one transaction, and a background worker sends
//	@Description	the email outside this request — SMTP is not part of the request path.
//	@Description	The endpoint still returns 201 with the token whenever the invitation
//	@Description	commits; internal delivery state is never exposed.
//	@Description
//	@Description	**Authorization (Phase 8A policy preserved):** only OWNER may create
//	@Description	invitations — ADMIN and VIEWER are rejected with 403 INSUFFICIENT_ROLE.
//	@Description	The role (OWNER/ADMIN/VIEWER) is always explicit; it is never defaulted.
//	@Description	No merchant_id is accepted from the request — merchant identity comes
//	@Description	exclusively from the authenticated caller.
//	@Description
//	@Description	**Rules:** merchant must be ACTIVE; the email must not already be
//	@Description	registered (409 EMAIL_ALREADY_EXISTS); one active invitation per
//	@Description	(merchant, email) (409 INVITATION_ALREADY_PENDING — a stale expired
//	@Description	invitation is replaced automatically). The token is stored only as a
//	@Description	SHA-256 hash and expires after INVITATION_TOKEN_TTL (default 48h).
//	@Tags			Team Invitations
//	@Accept			json
//	@Produce		json
//	@Param			body	body		model.CreateInvitationRequest	true	"Email and role to invite"
//	@Success		201		{object}	model.CreateInvitationResponse	"Token returned ONCE — never retrievable again"
//	@Failure		400		{object}	response.errorEnvelope	"VALIDATION_ERROR, INVALID_ROLE or invalid email"
//	@Failure		401		{object}	response.errorEnvelope	"UNAUTHORIZED or MERCHANT_INACTIVE"
//	@Failure		403		{object}	response.errorEnvelope	"INSUFFICIENT_ROLE (non-OWNER) or MERCHANT_INACTIVE"
//	@Failure		409		{object}	response.errorEnvelope	"EMAIL_ALREADY_EXISTS or INVITATION_ALREADY_PENDING"
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/dashboard/users/invite [post]
//	@Security		BearerAuth
func (h *InvitationHandler) Create(c *gin.Context) {
	caller := dashboardUserFromContext(c)
	if caller == nil {
		response.Unauthorized(c, response.CodeInvalidCredentials, "Authentication required")
		return
	}

	var req model.CreateInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	// Merchant identity comes from the authenticated context only — never
	// from the request body.
	created, err := h.svc.CreateInvitation(c.Request.Context(), caller.Role, caller.MerchantID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInsufficientRole):
			response.Forbidden(c, response.CodeInsufficientRole, "Only OWNER may create invitations")
		case errors.Is(err, service.ErrInvalidRole):
			response.BadRequest(c, response.CodeInvalidRole, "Invalid role — must be OWNER, ADMIN or VIEWER")
		case errors.Is(err, service.ErrInvalidEmail):
			response.ValidationError(c, map[string]string{"email": "must be a valid email address"})
		case errors.Is(err, service.ErrEmailAlreadyExists):
			response.Conflict(c, response.CodeEmailAlreadyExists, "Email is already registered")
		case errors.Is(err, service.ErrInvitationAlreadyPending):
			response.Conflict(c, response.CodeInvitationAlreadyPending, "An invitation is already pending for this email")
		case errors.Is(err, service.ErrMerchantNotActive):
			response.Forbidden(c, response.CodeMerchantInactive, "Merchant is not active")
		case errors.Is(err, repository.ErrMerchantNotFound):
			response.NotFound(c, response.CodeMerchantNotFound, "Merchant not found")
		default:
			slog.Error("invitations: create error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("merchant_id", caller.MerchantID.String()),
				// the invitation token and email are NEVER logged
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	// created.Token is disclosed here exactly once.
	response.Created(c, created)
}

// GetByToken godoc
//
//	@Summary		Preview invitation metadata
//	@Description	Returns public metadata (email, role, merchant name, expiry) for a valid
//	@Description	PENDING invitation token so the invitee can preview before accepting.
//	@Description	Unauthenticated — guarded solely by the opaque token.
//	@Description	Unknown, expired, accepted, and revoked tokens are indistinguishable:
//	@Description	all return 404 INVITATION_NOT_FOUND. Reading metadata is not a
//	@Description	mutation, so a SUSPENDED merchant's invitations can still be previewed;
//	@Description	acceptance itself requires the merchant to be ACTIVE.
//	@Tags			Team Invitations
//	@Produce		json
//	@Param			token	path		string							true	"Opaque invitation token (64 hex chars)"
//	@Success		200		{object}	model.InvitationMetadataResponse
//	@Failure		404		{object}	response.errorEnvelope	"INVITATION_NOT_FOUND"
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/invitations/{token} [get]
func (h *InvitationHandler) GetByToken(c *gin.Context) {
	metadata, err := h.svc.GetInvitationByToken(c.Request.Context(), c.Param("token"))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvitationNotFound):
			response.NotFound(c, response.CodeInvitationNotFound, "Invitation not found or no longer valid")
		default:
			slog.Error("invitations: get by token error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.OK(c, metadata)
}

// Accept godoc
//
//	@Summary		Accept team invitation
//	@Description	Consumes a single-use invitation token and creates the dashboard user
//	@Description	(ACTIVE, with the invited role, in the invitation's merchant) in ONE
//	@Description	transaction — the claim and the user insert are atomic, so a token can
//	@Description	never be accepted twice, even under concurrency.
//	@Description	Unauthenticated — guarded solely by the opaque token.
//	@Description
//	@Description	**Rules:** the password is required (8–128 chars) and stored only as an
//	@Description	Argon2id hash — plaintext is never stored or logged. The merchant must
//	@Description	still be ACTIVE at acceptance time (403 MERCHANT_INACTIVE if it was
//	@Description	suspended after the invitation was created). The invited email must
//	@Description	still be unregistered (409 EMAIL_ALREADY_EXISTS).
//	@Description	Errors: 404 for unknown/expired/revoked tokens, 409
//	@Description	INVITATION_ALREADY_ACCEPTED for a replayed token.
//	@Tags			Team Invitations
//	@Accept			json
//	@Produce		json
//	@Param			token	path		string							true	"Opaque invitation token (64 hex chars)"
//	@Param			body	body		model.AcceptInvitationRequest	true	"New password"
//	@Success		201		{object}	model.DashboardUserResponse
//	@Failure		400		{object}	response.errorEnvelope	"VALIDATION_ERROR or INVALID_PASSWORD"
//	@Failure		403		{object}	response.errorEnvelope	"MERCHANT_INACTIVE"
//	@Failure		404		{object}	response.errorEnvelope	"INVITATION_NOT_FOUND"
//	@Failure		409		{object}	response.errorEnvelope	"INVITATION_ALREADY_ACCEPTED or EMAIL_ALREADY_EXISTS"
//	@Failure		500		{object}	response.errorEnvelope
//	@Router			/api/v1/invitations/{token}/accept [post]
func (h *InvitationHandler) Accept(c *gin.Context) {
	var req model.AcceptInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, parseBindingErrors(err))
		return
	}

	accepted, err := h.svc.AcceptInvitation(c.Request.Context(), c.Param("token"), req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvitationNotFound):
			response.NotFound(c, response.CodeInvitationNotFound, "Invitation not found or no longer valid")
		case errors.Is(err, service.ErrInvitationAlreadyAccepted):
			response.Conflict(c, response.CodeInvitationAlreadyAccepted, "Invitation has already been accepted")
		case errors.Is(err, service.ErrEmailAlreadyExists):
			response.Conflict(c, response.CodeEmailAlreadyExists, "Email is already registered")
		case errors.Is(err, service.ErrMerchantNotActive):
			response.Forbidden(c, response.CodeMerchantInactive, "Merchant is not active")
		case errors.Is(err, service.ErrInvalidPassword):
			response.BadRequest(c, response.CodeInvalidPassword, "Password does not meet the password policy")
		default:
			slog.Error("invitations: accept error",
				slog.String("request_id", c.GetString(response.ContextKey)),
				// the password is NEVER logged
				slog.String("error", err.Error()),
			)
			response.InternalServerError(c)
		}
		return
	}

	response.Created(c, accepted)
}
