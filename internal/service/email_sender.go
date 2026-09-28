package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ─── Phase 8C.1 — Email Delivery Foundation ────────────────────────────────────
//
// This file introduces the generic email-delivery abstraction that later
// phases (starting with invitation email delivery in Phase 8C.2) depend on.
//
// Deliberately NOT in this phase: queue/outbox/retry, templates, background
// workers. Send is a synchronous, single-shot SMTP submission.

// ─── Error sentinels ─────────────────────────────────────────────────────────

var (
	// ErrEmailSendFailed is returned when the SMTP submission fails for any
	// reason (dial, TLS, auth, envelope, data, or server rejection).
	// Callers that treat email as best-effort should log and continue.
	ErrEmailSendFailed = errors.New("email send failed")

	// ErrEmailMessageInvalid is returned when an EmailMessage fails validation
	// before any network I/O happens (missing recipient/subject/body, malformed
	// address, or header-injection attempt via CR/LF).
	ErrEmailMessageInvalid = errors.New("invalid email message")
)

// ─── Types ───────────────────────────────────────────────────────────────────

// EmailMessage is the transport-agnostic representation of one email.
//
// Security: the body of an invitation email will contain a single-use
// invitation token. Bodies and subjects are therefore NEVER logged anywhere
// in this package — logs only carry aggregate metadata (recipient count and
// recipient domain, via the existing emailDomain helper).
type EmailMessage struct {
	// From is optional. When empty, the configured SMTP_FROM address is used.
	// Must be a single valid address ("Name <a@b.c>" or bare "a@b.c").
	From string

	// To holds one or more recipient addresses (required, minimum 1).
	To []string

	// Subject is required. CR/LF are rejected (header-injection guard).
	Subject string

	// TextBody is the plain-text body. At least one of TextBody/HTMLBody is
	// required. When both are present the message is sent as
	// multipart/alternative.
	TextBody string

	// HTMLBody is the optional HTML body.
	HTMLBody string
}

// ─── Interface ────────────────────────────────────────────────────────────────

// EmailSender is the abstraction over outbound email delivery.
//
// Adding a new transport (provider API, another SMTP relay) only requires:
//  1. Implementing this interface.
//  2. Registering the implementation in main.go.
//
// Callers depend on this interface — never on a concrete type. This mirrors
// the PaymentProvider/RefundProvider pattern established in earlier phases.
type EmailSender interface {
	// Send submits one message. Returns ErrEmailSendFailed when the
	// submission fails, ErrEmailMessageInvalid when the message is rejected
	// before any I/O, or an error wrapping ctx.Err() when the context is
	// cancelled/expired.
	Send(ctx context.Context, message EmailMessage) error
}

// ─── Configuration ───────────────────────────────────────────────────────────

// TLS modes for SMTP submission.
const (
	// EmailTLSModeStartTLS upgrades the plain connection with STARTTLS and
	// REQUIRES the server to advertise it (default; port 587).
	EmailTLSModeStartTLS = "starttls"

	// EmailTLSModeImplicit wraps the connection in TLS from the first byte
	// (port 465).
	EmailTLSModeImplicit = "implicit"

	// EmailTLSModeNone sends without transport encryption. Development-only —
	// rejected by config.Load when EMAIL_ENABLED=true in production. Note
	// net/smtp's PlainAuth still refuses to emit credentials over an
	// unencrypted connection to a non-loopback host.
	EmailTLSModeNone = "none"
)

// EmailSenderConfig carries the SMTP connection settings. It is populated in
// main.go from config.EmailConfig (the same cfg → service-config mapping used
// for service.AuthConfig).
type EmailSenderConfig struct {
	// Enabled selects the SMTP sender; when false NewEmailSender returns a
	// no-op sender that drops messages (best-effort semantics).
	Enabled bool

	// Host is the SMTP server hostname (required when Enabled).
	Host string

	// Port is the SMTP server port. 587 (starttls) or 465 (implicit).
	Port int

	// Username/Password are the SMTP AUTH credentials. AUTH is attempted
	// only when Username is non-empty.
	Username string
	Password string

	// From is the default envelope/header sender
	// ("Name <no-reply@example.com>"). Required when Enabled.
	From string

	// Timeout bounds a single Send (dial + handshake + submission).
	// Defaults to 10s when non-positive.
	Timeout time.Duration

	// TLSMode is one of EmailTLSModeStartTLS / Implicit / None.
	// Defaults to starttls when empty.
	TLSMode string
}

// ─── Constructor ─────────────────────────────────────────────────────────────

// NewEmailSender returns the configured EmailSender implementation:
// an SMTP sender when Enabled, otherwise a no-op sender that discards
// messages. Config validation (host/from present, legal TLS mode) is done by
// config.Load at startup, mirroring the PAYMENT_PROVIDER=midtrans check.
func NewEmailSender(cfg EmailSenderConfig) EmailSender {
	if !cfg.Enabled {
		return &noopEmailSender{}
	}
	if cfg.Port <= 0 {
		cfg.Port = 587
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.TLSMode == "" {
		cfg.TLSMode = EmailTLSModeStartTLS
	}
	return &smtpEmailSender{cfg: cfg}
}

// ─── No-op sender (EMAIL_ENABLED=false) ─────────────────────────────────────

// noopEmailSender drops every message and returns nil. Returning nil (not an
// error) keeps email best-effort: a deployment without SMTP can still run the
// full application, and Phase 8C.2 invitation creation will not fail when
// email delivery is switched off.
type noopEmailSender struct{}

func (n *noopEmailSender) Send(ctx context.Context, msg EmailMessage) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrEmailSendFailed, err)
	}
	domains := make([]string, 0, len(msg.To))
	for _, to := range msg.To {
		domains = append(domains, emailDomain(normalizeEmailAddr(to)))
	}
	// Subject/body/recipients are NEVER logged — only aggregate metadata.
	slog.Info("email send skipped (EMAIL_ENABLED=false)",
		slog.Int("recipients", len(msg.To)),
		slog.String("recipient_domains", strings.Join(domains, ",")),
	)
	return nil
}

// ─── SMTP sender ─────────────────────────────────────────────────────────────

type smtpEmailSender struct {
	cfg EmailSenderConfig
}

func (s *smtpEmailSender) Send(ctx context.Context, msg EmailMessage) error {
	// 1. Validate before touching the network.
	from, to, err := s.validateMessage(msg)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrEmailSendFailed, err)
	}

	// 2. Build the RFC 5322 message bytes.
	raw, err := buildEmailMessage(msg, from, to)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEmailMessageInvalid, err)
	}

	// 3. Dial (context-aware) and bound the whole session by the deadline.
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w", ErrEmailSendFailed, ctxErr)
		}
		return fmt.Errorf("%w: dial %s: %w", ErrEmailSendFailed, addr, err)
	}
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	// 4. Transport security.
	switch s.cfg.TLSMode {
	case EmailTLSModeImplicit:
		tlsConn := tls.Client(conn, s.tlsConfig())
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("%w: tls handshake: %w", ErrEmailSendFailed, err)
		}
		conn = tlsConn
	case EmailTLSModeStartTLS, EmailTLSModeNone:
		// Plain dial; STARTTLS (required) is negotiated below, after EHLO.
	default:
		_ = conn.Close()
		return fmt.Errorf("%w: unknown SMTP TLS mode %q", ErrEmailSendFailed, s.cfg.TLSMode)
	}

	// 5. SMTP session.
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%w: %w", ErrEmailSendFailed, err)
	}
	defer func() { _ = client.Close() }() //nolint:errcheck // no-op after Quit

	// STARTTLS mode REQUIRES the upgrade — if the server does not advertise
	// STARTTLS the send fails instead of silently degrading to plaintext.
	if s.cfg.TLSMode == EmailTLSModeStartTLS {
		if err := client.StartTLS(s.tlsConfig()); err != nil {
			return fmt.Errorf("%w: starttls required but failed: %w", ErrEmailSendFailed, err)
		}
	}

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("%w: auth: %w", ErrEmailSendFailed, err)
		}
	}

	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("%w: mail from: %w", ErrEmailSendFailed, err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt.Address); err != nil {
			return fmt.Errorf("%w: rcpt to: %w", ErrEmailSendFailed, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: data: %w", ErrEmailSendFailed, err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("%w: data write: %w", ErrEmailSendFailed, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%w: data close: %w", ErrEmailSendFailed, err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("%w: quit: %w", ErrEmailSendFailed, err)
	}

	// Aggregate metadata only — subject/body/addresses are NEVER logged.
	slog.Info("email sent",
		slog.Int("recipients", len(to)),
		slog.String("recipient_domain", emailDomain(from.Address)),
	)
	return nil
}

// tlsConfig builds the TLS parameters for implicit TLS and STARTTLS.
// ServerName is the configured SMTP host (certificate verification).
func (s *smtpEmailSender) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName: s.cfg.Host,
		MinVersion: tls.VersionTLS12,
	}
}

// validateMessage applies defaults and validates the message before any I/O.
// Returns the parsed envelope sender and recipients.
func (s *smtpEmailSender) validateMessage(msg EmailMessage) (*mail.Address, []*mail.Address, error) {
	// Default From from configuration.
	rawFrom := strings.TrimSpace(msg.From)
	if rawFrom == "" {
		rawFrom = strings.TrimSpace(s.cfg.From)
	}
	if rawFrom == "" {
		return nil, nil, fmt.Errorf("%w: no sender address", ErrEmailMessageInvalid)
	}
	from, err := parseSingleAddress(rawFrom, "From")
	if err != nil {
		return nil, nil, err
	}

	if len(msg.To) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one recipient is required", ErrEmailMessageInvalid)
	}
	to := make([]*mail.Address, 0, len(msg.To))
	for _, raw := range msg.To {
		addr, err := parseSingleAddress(raw, "To")
		if err != nil {
			return nil, nil, err
		}
		if err := validateEmail(normalizeEmailAddr(addr.Address)); err != nil {
			return nil, nil, fmt.Errorf("%w: invalid recipient address", ErrEmailMessageInvalid)
		}
		to = append(to, addr)
	}

	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		return nil, nil, fmt.Errorf("%w: subject is required", ErrEmailMessageInvalid)
	}
	// Header-injection guard: a subject must never be able to smuggle
	// additional headers, even though Q-encoding would also neutralise it.
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, nil, fmt.Errorf("%w: subject contains illegal control characters", ErrEmailMessageInvalid)
	}
	if len(subject) > 998 {
		return nil, nil, fmt.Errorf("%w: subject too long", ErrEmailMessageInvalid)
	}

	if strings.TrimSpace(msg.TextBody) == "" && strings.TrimSpace(msg.HTMLBody) == "" {
		return nil, nil, fmt.Errorf("%w: a text or HTML body is required", ErrEmailMessageInvalid)
	}

	return from, to, nil
}

// parseSingleAddress parses exactly one RFC 5322 address and rejects
// CR/LF anywhere (header-injection guard — subjects and addresses must never
// be able to smuggle extra headers).
func parseSingleAddress(raw, field string) (*mail.Address, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return nil, fmt.Errorf("%w: %s contains illegal control characters", ErrEmailMessageInvalid, field)
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s address", ErrEmailMessageInvalid, field)
	}
	return addr, nil
}

// normalizeEmailAddr lowercases/trims an address for validation and logging.
func normalizeEmailAddr(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// ─── Message construction ────────────────────────────────────────────────────

// buildEmailMessage renders the wire-format RFC 5322 message.
//
// Bodies are always base64-encoded (line-wrapped at 76 chars) so arbitrary
// UTF-8 content is transported safely and no SMTP line-length rules are
// violated. Non-ASCII subjects are RFC 2047 (Q-)encoded.
func buildEmailMessage(msg EmailMessage, from *mail.Address, to []*mail.Address) ([]byte, error) {
	var b bytes.Buffer

	rcptAddrs := make([]string, 0, len(to))
	for _, a := range to {
		rcptAddrs = append(rcptAddrs, a.String())
	}

	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(rcptAddrs, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", strings.TrimSpace(msg.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@payment-gateway>\r\n", uuid.NewString())
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")

	text := msg.TextBody
	html := msg.HTMLBody
	switch {
	case text != "" && html != "":
		var pb bytes.Buffer
		mw := multipart.NewWriter(&pb)
		if err := writeTextPart(mw, "text/plain", text); err != nil {
			return nil, err
		}
		if err := writeTextPart(mw, "text/html", html); err != nil {
			return nil, err
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary())
		b.Write(pb.Bytes())
	case text != "":
		fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
		fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(base64Body(text))
	default:
		fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n")
		fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(base64Body(html))
	}
	b.WriteString("\r\n") // final CRLF terminating the body
	return b.Bytes(), nil
}

// writeTextPart writes one base64-encoded MIME part.
func writeTextPart(mw *multipart.Writer, contentType, body string) error {
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType + "; charset=utf-8"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(base64Body(body)))
	return err
}

// base64Body encodes s as base64 wrapped at 76 characters per line (RFC 2045),
// CRLF-delimited as required on the SMTP wire.
func base64Body(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}
