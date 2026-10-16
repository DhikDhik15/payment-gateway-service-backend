package service

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
)

// resetURLPath is the dashboard SPA route that consumes a password-reset
// token. Query-string style matches the invitation convention
// ({base}/accept-invitation?token=…): the token is captured into memory on
// page load and scrubbed from browser history by the frontend.
const resetURLPath = "/reset-password"

// ─── Templates ───────────────────────────────────────────────────────────────

// resetEmailData is the render context for both bodies. Every field is
// dynamic and therefore always rendered through html/template escaping.
type resetEmailData struct {
	Email     string // account address (from the server-side user record)
	URL       string
	ExpiresAt string // absolute, UTC
	ExpiresIn string // humanised relative window
}

// resetTextTmpl is the text/plain part (a plain-text body is required).
var resetTextTmpl = template.Must(template.New("resetText").Parse(
	`Someone requested a password reset for your PayGate dashboard account.

Reset your password:
{{.URL}}

This request was made for {{.Email}}. It expires at {{.ExpiresAt}} (in {{.ExpiresIn}}), and the link works only once.

If you did not request this, you can ignore this email — your password stays unchanged.
`))

// resetHTMLTmpl is the text/html part. html/template escapes every dynamic
// value for its context (element text and the href attribute).
var resetHTMLTmpl = template.Must(template.New("resetHTML").Parse(
	`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Reset your password</title></head>
<body>
  <h2>Reset your dashboard password</h2>
  <p>Someone requested a password reset for your PayGate dashboard account ({{.Email}}).</p>
  <p><a href="{{.URL}}">Reset your password</a></p>
  <p>Or copy this link into your browser:<br>{{.URL}}</p>
  <p>It expires at {{.ExpiresAt}} (in {{.ExpiresIn}}), and the link works only once.</p>
  <p>If you did not request this, you can ignore this email &mdash; your password stays unchanged.</p>
</body>
</html>
`))

// ─── Builders ────────────────────────────────────────────────────────────────

// buildResetURL constructs the reset URL:
//
//	{dashboardBaseURL}/reset-password?token={token}
//
// The token is encoded through url.Values (standard query escaping). Any
// error is raised while parsing the configured base URL — before the token is
// attached — so error text can never contain the token.
func buildResetURL(dashboardBaseURL, token string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(dashboardBaseURL), "/")
	u, err := url.Parse(base) // config validates absolute http(s); re-checked defensively here
	if err != nil {
		return "", fmt.Errorf("build reset url: parse base: %w", err)
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("build reset url: base scheme must be http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/") + resetURLPath
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// buildResetEmail renders the multipart reset email (both text and HTML
// parts — EmailSender transmits them as multipart/alternative).
//
// accountEmail MUST come from the server-side user record; callers have no
// way to specify a different recipient.
func buildResetEmail(accountEmail, resetURL string, expiresAt, now time.Time) (EmailMessage, error) {
	if strings.TrimSpace(accountEmail) == "" {
		return EmailMessage{}, fmt.Errorf("build reset email: empty recipient")
	}
	if strings.TrimSpace(resetURL) == "" {
		return EmailMessage{}, fmt.Errorf("build reset email: empty reset url")
	}
	data := resetEmailData{
		Email:     accountEmail,
		URL:       resetURL,
		ExpiresAt: expiresAt.UTC().Format(time.RFC1123),
		ExpiresIn: humaniseDuration(expiresAt.Sub(now)),
	}
	var text, html strings.Builder
	if err := resetTextTmpl.Execute(&text, data); err != nil {
		return EmailMessage{}, fmt.Errorf("build reset email: text template: %w", err)
	}
	if err := resetHTMLTmpl.Execute(&html, data); err != nil {
		return EmailMessage{}, fmt.Errorf("build reset email: html template: %w", err)
	}
	return EmailMessage{
		To:       []string{accountEmail},
		Subject:  "Reset your PayGate dashboard password",
		TextBody: text.String(),
		HTMLBody: html.String(),
	}, nil
}

// newResetEmailOutbox maps the rendered message onto the transactional outbox
// row. The entry carries the fully rendered bodies, reset link included —
// the audited outbox design (token_hash is never written here). From is
// intentionally not persisted (resolved to SMTP_FROM at send time, mirroring
// invitations). merchantID comes from the server-side user record.
func newResetEmailOutbox(merchantID, tokenID uuid.UUID, msg EmailMessage, now time.Time) *model.EmailOutbox {
	recipient := ""
	if len(msg.To) > 0 {
		recipient = msg.To[0] // buildResetEmail always sets exactly one
	}
	return &model.EmailOutbox{
		ID:            uuid.New(),
		MerchantID:    merchantID,
		ReferenceID:   &tokenID, // correlation to password_reset_tokens
		Type:          model.EmailOutboxTypePasswordReset,
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
