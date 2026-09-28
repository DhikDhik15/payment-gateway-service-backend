package service

// invitation_email.go — Phase 8C.2 invitation email construction.
//
// The builder is a pure function: it never touches the network, the database,
// configuration, or logging. The service supplies the same plaintext token
// that Phase 8B generated for the API response — this file NEVER generates,
// regenerates, re-fetches, or persists a token, and it never sees the
// token_hash.
//
// HTML safety: all dynamic values are rendered through html/template, which
// contextually auto-escapes text and attribute content (a merchant named
// <script>alert(1)</script> cannot inject markup). The acceptance URL is
// assembled with net/url — the token is query-escaped by url.Values.Encode,
// never by manual string concatenation.

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
)

// invitationURLPath is the dashboard SPA route that consumes an invitation
// token. This repository is an API-only backend with no frontend routes of
// its own, and no other invitation route convention exists in the project —
// so the route follows the documented Phase 8C.2 contract:
//
//	{DASHBOARD_BASE_URL}/accept-invitation?token={TOKEN}
const invitationURLPath = "/accept-invitation"

// ─── Templates ───────────────────────────────────────────────────────────────

// invitationEmailData is the render context for both bodies. Every field is
// dynamic and therefore always rendered through html/template escaping.
type invitationEmailData struct {
	MerchantName string
	Email        string // invitee address (from the invitation record)
	Role         string
	RolePhrase   string // "an ADMIN" / "an OWNER" / "a VIEWER"
	URL          string
	ExpiresAt    string // absolute, UTC
	ExpiresIn    string // humanised relative window
}

// invitationTextTmpl is the text/plain part (plain text body is REQUIRED by
// the Phase 8C.2 contract).
var invitationTextTmpl = template.Must(template.New("invitationText").Parse(
	`You have been invited to join {{.MerchantName}} as {{.RolePhrase}}.

Accept your invitation:
{{.URL}}

This invitation was sent to {{.Email}} for the {{.MerchantName}} dashboard.
It expires at {{.ExpiresAt}} (in {{.ExpiresIn}}).

If you did not expect this invitation, you can ignore this email — no
account is created until the invitation is accepted.
`))

// invitationHTMLTmpl is the text/html part. html/template escapes every
// dynamic value for its context (element text and the href attribute).
var invitationHTMLTmpl = template.Must(template.New("invitationHTML").Parse(
	`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Dashboard invitation</title></head>
<body>
  <h2>You&rsquo;re invited to join {{.MerchantName}}</h2>
  <p>You have been invited to join <strong>{{.MerchantName}}</strong> as <strong>{{.RolePhrase}}</strong>.</p>
  <p>This invitation was sent to <strong>{{.Email}}</strong> for the {{.MerchantName}} dashboard.</p>
  <p><a href="{{.URL}}">Accept your invitation</a></p>
  <p>Or copy this link into your browser:<br>{{.URL}}</p>
  <p>It expires at {{.ExpiresAt}} (in {{.ExpiresIn}}).</p>
  <p>If you did not expect this invitation, you can ignore this email &mdash; no account is created until the invitation is accepted.</p>
</body>
</html>
`))

// ─── Builders ────────────────────────────────────────────────────────────────

// buildInvitationURL constructs the acceptance URL:
//
//	{dashboardBaseURL}/accept-invitation?token={token}
//
// The token is encoded through url.Values (standard query escaping). Any
// error is raised while parsing the configured base URL — before the token is
// attached — so error text can never contain the token.
func buildInvitationURL(dashboardBaseURL, token string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(dashboardBaseURL), "/")
	u, err := url.Parse(base) // config validates absolute http(s); re-checked defensively here
	if err != nil {
		return "", fmt.Errorf("build invitation url: parse base: %w", err)
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("build invitation url: base scheme must be http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/") + invitationURLPath
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// buildInvitationEmail renders the multipart invitation email (both text and
// HTML parts — EmailSender transmits them as multipart/alternative).
//
// merchantName MUST come from the server-side merchant record of the
// authenticated caller — never from client input. inviteeEmail MUST be the
// invitation record's email; callers have no way to specify a different
// recipient.
func buildInvitationEmail(
	merchantName string,
	inviteeEmail string,
	role model.DashboardUserRole,
	invitationURL string,
	expiresAt time.Time,
	now time.Time,
) (EmailMessage, error) {
	data := invitationEmailData{
		MerchantName: merchantName,
		Email:        inviteeEmail,
		Role:         string(role),
		RolePhrase:   rolePhrase(role),
		URL:          invitationURL,
		ExpiresAt:    expiresAt.UTC().Format(time.RFC1123),
		ExpiresIn:    humaniseDuration(expiresAt.Sub(now)),
	}

	var text strings.Builder
	if err := invitationTextTmpl.Execute(&text, data); err != nil {
		// Template execution errors never echo the render context values.
		return EmailMessage{}, fmt.Errorf("build invitation email: text: %w", err)
	}
	var htmlBody strings.Builder
	if err := invitationHTMLTmpl.Execute(&htmlBody, data); err != nil {
		return EmailMessage{}, fmt.Errorf("build invitation email: html: %w", err)
	}

	subject := fmt.Sprintf("You're invited to join %s", sanitizeInline(merchantName))

	return EmailMessage{
		// Recipient is ALWAYS the invitation's own email address — the API
		// client cannot choose a separate delivery target.
		To:       []string{inviteeEmail},
		Subject:  subject,
		TextBody: text.String(),
		HTMLBody: htmlBody.String(),
	}, nil
}

// ─── Outbox mapping (Phase 8C.3A) ────────────────────────────────────────────

// newInvitationEmailOutbox converts the rendered invitation email into the
// email_outbox row that commits alongside the invitation.
//
// Mapping (EmailMessage.From is intentionally NOT persisted — the configured
// SMTP_FROM is resolved by EmailSender at actual send time, Phase 8C.3B):
//
//	To[0]     → recipient      Subject   → subject
//	TextBody  → text_body      HTMLBody  → html_body
//
// The entry carries the fully rendered bodies, plaintext token included —
// that is the audited Phase 8C.3A design (token_hash is never written here).
// Delivery defaults: status=PENDING, attempt_count=0, next_attempt_at=now,
// processing/last_attempt/sent/last_error all NULL.
func newInvitationEmailOutbox(inv *model.MerchantInvitation, msg EmailMessage, now time.Time) *model.EmailOutbox {
	referenceID := inv.ID
	recipient := ""
	if len(msg.To) > 0 {
		recipient = msg.To[0] // buildInvitationEmail always sets exactly one
	}
	return &model.EmailOutbox{
		ID:            uuid.New(),
		MerchantID:    inv.MerchantID, // authenticated merchant — never client-supplied
		ReferenceID:   &referenceID,   // correlation to merchant_user_invitations
		Type:          model.EmailOutboxTypeInvitation,
		Recipient:     recipient,
		Subject:       msg.Subject,
		TextBody:      msg.TextBody,
		HTMLBody:      msg.HTMLBody,
		Status:        model.EmailOutboxStatusPending,
		AttemptCount:  0,
		NextAttemptAt: now,
		ProcessingAt:  nil,
		LastAttemptAt: nil,
		SentAt:        nil,
		LastError:     nil,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// ─── Small helpers ───────────────────────────────────────────────────────────

// rolePhrase renders a role with the right indefinite article for prose
// ("an ADMIN", "a VIEWER"). Unknown roles fall back to the bare value.
func rolePhrase(role model.DashboardUserRole) string {
	switch role {
	case model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin:
		return "an " + string(role)
	case model.DashboardUserRoleViewer:
		return "a " + string(role)
	}
	return string(role)
}

// sanitizeInline removes CR/LF so a hostile merchant name can never turn the
// email subject into additional headers. (html/template escaping covers the
// bodies; the subject is plain text and is Q-encoded by EmailSender, but the
// CR/LF are stripped here as defence in depth — EmailSender also rejects
// control characters outright.)
func sanitizeInline(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n':
			return ' '
		}
		return r
	}, s)
}

// humaniseDuration renders the remaining validity window in prose
// ("48 hours", "7 days"). Invitation TTLs are hour-based (default 48h), so
// hours are used up to one week; longer windows switch to days.
func humaniseDuration(d time.Duration) string {
	if d <= 0 {
		return "no time"
	}
	hours := int((d + time.Hour - 1) / time.Hour) // round up — never understate validity
	if hours >= 168 && hours%24 == 0 {
		days := hours / 24
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	if hours == 1 {
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", hours)
}
