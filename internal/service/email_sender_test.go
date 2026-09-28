package service

// email_sender_test.go — Phase 8C.1 unit tests for the email delivery
// foundation: the EmailSender abstraction, message validation, the no-op
// sender, and the SMTP implementation (exercised end-to-end against an
// in-process fake SMTP server — no external network, no real mailbox).

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── In-process fake SMTP server ─────────────────────────────────────────────

// fakeSMTP is a minimal RFC 5321 server sufficient for net/smtp's client:
// greeting, EHLO (advertises AUTH, never STARTTLS), MAIL/RCPT/DATA, QUIT.
type fakeSMTP struct {
	ln         net.Listener
	rejectRcpt bool
	mu         sync.Mutex
	conns      int
	mailFrom   string
	rcpts      []string
	data       string
	gotAUTH    bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{ln: ln}
	go s.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTP) port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

func (s *fakeSMTP) snapshot() (conns int, mailFrom string, rcpts []string, data string, gotAUTH bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns, s.mailFrom, append([]string(nil), s.rcpts...), s.data, s.gotAUTH
}

func (s *fakeSMTP) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))

	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	say := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format, args...)
		_ = w.Flush()
	}

	say("220 fake.local ESMTP\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			// Advertises AUTH; deliberately never advertises STARTTLS so the
			// "STARTTLS required but unsupported" path is testable.
			say("250-fake.local\r\n250-AUTH PLAIN LOGIN\r\n250 SIZE 10485760\r\n")
		case strings.HasPrefix(upper, "HELO"):
			say("250 fake.local\r\n")
		case strings.HasPrefix(upper, "AUTH "):
			s.mu.Lock()
			s.gotAUTH = true
			s.mu.Unlock()
			say("235 2.7.0 Authentication successful\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			from := line[strings.Index(line, ":")+1:]
			from = strings.Trim(from, "<>")
			if i := strings.IndexAny(from, " "); i >= 0 {
				from = from[:i]
			}
			s.mu.Lock()
			s.mailFrom = from
			s.mu.Unlock()
			say("250 2.1.0 Ok\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			if s.rejectRcpt {
				say("550 5.1.1 Rejected\r\n")
				continue
			}
			rcpt := line[strings.Index(line, ":")+1:]
			rcpt = strings.Trim(rcpt, "<>")
			if i := strings.IndexAny(rcpt, " "); i >= 0 {
				rcpt = rcpt[:i]
			}
			s.mu.Lock()
			s.rcpts = append(s.rcpts, rcpt)
			s.mu.Unlock()
			say("250 2.1.5 Ok\r\n")
		case upper == "DATA":
			say("354 End data with <CR><LF>.<CR><LF>\r\n")
			var body strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				body.WriteString(dl)
			}
			s.mu.Lock()
			s.data = body.String()
			s.mu.Unlock()
			say("250 2.0.0 Ok: queued\r\n")
		case upper == "RSET":
			say("250 2.0.0 Ok\r\n")
		case upper == "QUIT":
			say("221 2.0.0 Bye\r\n")
			return
		default:
			say("500 5.5.2 Unrecognized command\r\n")
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func testSMTPConfig(srv *fakeSMTP) EmailSenderConfig {
	return EmailSenderConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    srv.port(),
		From:    "Payment Gateway <no-reply@test.local>",
		Timeout: 5 * time.Second,
		TLSMode: EmailTLSModeNone, // fake server speaks plaintext
	}
}

func newTestSMTPSender(t *testing.T, cfg EmailSenderConfig) *smtpEmailSender {
	t.Helper()
	s, ok := NewEmailSender(cfg).(*smtpEmailSender)
	if !ok {
		t.Fatal("expected *smtpEmailSender when Enabled=true")
	}
	return s
}

func validMessage() EmailMessage {
	return EmailMessage{
		To:       []string{"invitee@example.com"},
		Subject:  "You are invited",
		TextBody: "Hello, you have been invited to join the team.",
	}
}

// mustAddr parses an address using the same stdlib the sender uses, so header
// assertions track mail.Address.String()'s exact rendering (it quotes
// display names containing spaces and always adds angle brackets).
func mustAddr(t *testing.T, raw string) *mail.Address {
	t.Helper()
	a, err := mail.ParseAddress(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return a
}

// ─── Constructor ─────────────────────────────────────────────────────────────

func TestNewEmailSender_DisabledReturnsNoop(t *testing.T) {
	s := NewEmailSender(EmailSenderConfig{Enabled: false})
	if _, ok := s.(*noopEmailSender); !ok {
		t.Fatalf("expected *noopEmailSender when disabled, got %T", s)
	}
}

func TestNewEmailSender_EnabledReturnsSMTPWithDefaults(t *testing.T) {
	s, ok := NewEmailSender(EmailSenderConfig{Enabled: true, Host: "smtp.test"}).(*smtpEmailSender)
	if !ok {
		t.Fatalf("expected *smtpEmailSender when enabled, got %T", s)
	}
	if s.cfg.Port != 587 {
		t.Errorf("default port = %d, want 587", s.cfg.Port)
	}
	if s.cfg.Timeout != 10*time.Second {
		t.Errorf("default timeout = %v, want 10s", s.cfg.Timeout)
	}
	if s.cfg.TLSMode != EmailTLSModeStartTLS {
		t.Errorf("default TLS mode = %q, want starttls", s.cfg.TLSMode)
	}
}

func TestSMTPSender_TLSConfig(t *testing.T) {
	s := &smtpEmailSender{cfg: EmailSenderConfig{Host: "smtp.example.com"}}
	tlsCfg := s.tlsConfig()
	if tlsCfg.ServerName != "smtp.example.com" {
		t.Errorf("ServerName = %q, want smtp.example.com", tlsCfg.ServerName)
	}
	if tlsCfg.MinVersion != 0x0303 /* tls.VersionTLS12 */ {
		t.Errorf("MinVersion = %#x, want TLS1.2", tlsCfg.MinVersion)
	}
}

// ─── No-op sender ────────────────────────────────────────────────────────────

func TestNoopEmailSender_SendReturnsNil(t *testing.T) {
	s := NewEmailSender(EmailSenderConfig{Enabled: false})
	if err := s.Send(context.Background(), validMessage()); err != nil {
		t.Fatalf("noop send: %v, want nil (email must be best-effort when disabled)", err)
	}
}

func TestNoopEmailSender_CancelledContext(t *testing.T) {
	s := NewEmailSender(EmailSenderConfig{Enabled: false})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Send(ctx, validMessage())
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ─── Message validation (happens before any I/O) ────────────────────────────

func TestSMTP_Send_ValidationRejections(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(*EmailSenderConfig)
		msg  func(*EmailMessage)
	}{
		{"no recipients", nil, func(m *EmailMessage) { m.To = nil }},
		{"empty recipient", nil, func(m *EmailMessage) { m.To = []string{""} }},
		{"invalid recipient", nil, func(m *EmailMessage) { m.To = []string{"not-an-email"} }},
		{"no subject", nil, func(m *EmailMessage) { m.Subject = "  " }},
		{"subject header injection", nil, func(m *EmailMessage) {
			m.Subject = "hi\r\nBcc: attacker@evil.example"
		}},
		{"no body", nil, func(m *EmailMessage) { m.TextBody = ""; m.HTMLBody = "" }},
		{"whitespace-only body", nil, func(m *EmailMessage) { m.TextBody = "   " }},
		{"invalid explicit From", nil, func(m *EmailMessage) { m.From = "not-an-address" }},
		{"From header injection", nil, func(m *EmailMessage) { m.From = "a@b.c\r\nBcc: x@y.z" }},
		{"no From anywhere", func(c *EmailSenderConfig) { c.From = "" }, func(m *EmailMessage) { m.From = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeSMTP(t)
			cfg := testSMTPConfig(srv)
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			s := newTestSMTPSender(t, cfg)
			msg := validMessage()
			tc.msg(&msg)

			err := s.Send(context.Background(), msg)
			if err == nil || !errors.Is(err, ErrEmailMessageInvalid) {
				t.Fatalf("err = %v, want ErrEmailMessageInvalid", err)
			}
			if conns, _, _, _, _ := srv.snapshot(); conns != 0 {
				t.Errorf("validation failure opened %d connection(s), want 0", conns)
			}
		})
	}
}

// ─── SMTP round-trips ────────────────────────────────────────────────────────

func TestSMTP_Send_PlainTextSucceeds(t *testing.T) {
	srv := newFakeSMTP(t)
	s := newTestSMTPSender(t, testSMTPConfig(srv))

	msg := validMessage()
	msg.To = append(msg.To, "Second Recipient <second@example.com>")
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	_, mailFrom, rcpts, data, _ := srv.snapshot()
	if mailFrom != "no-reply@test.local" {
		t.Errorf("MAIL FROM = %q, want no-reply@test.local (configured default)", mailFrom)
	}
	if len(rcpts) != 2 || rcpts[0] != "invitee@example.com" || rcpts[1] != "second@example.com" {
		t.Errorf("RCPT TO = %v, want both envelope recipients", rcpts)
	}
	for _, want := range []string{
		"From: " + mustAddr(t, "Payment Gateway <no-reply@test.local>").String(),
		"To: " + mustAddr(t, "invitee@example.com").String() + ", " + mustAddr(t, "Second Recipient <second@example.com>").String(),
		"Subject: You are invited",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: base64",
		"Message-ID: <",
		base64Body(msg.TextBody), // body travels base64-encoded
	} {
		if !strings.Contains(data, want) {
			t.Errorf("DATA missing %q\n---\n%s", want, data)
		}
	}
}

func TestSMTP_Send_ExplicitFromOverridesDefault(t *testing.T) {
	srv := newFakeSMTP(t)
	s := newTestSMTPSender(t, testSMTPConfig(srv))

	msg := validMessage()
	msg.From = "Inviting Owner <owner@test.local>"
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, mailFrom, _, data, _ := srv.snapshot()
	if mailFrom != "owner@test.local" {
		t.Errorf("MAIL FROM = %q, want owner@test.local", mailFrom)
	}
	wantFrom := "From: " + mustAddr(t, "Inviting Owner <owner@test.local>").String()
	if !strings.Contains(data, wantFrom) {
		t.Errorf("From header not overridden (want %q):\n%s", wantFrom, data)
	}
}

func TestSMTP_Send_MultipartAlternativeWhenBothBodies(t *testing.T) {
	srv := newFakeSMTP(t)
	s := newTestSMTPSender(t, testSMTPConfig(srv))

	msg := validMessage()
	msg.HTMLBody = "<p>Hello, you have been invited.</p>"
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, _, _, data, _ := srv.snapshot()
	for _, want := range []string{
		"Content-Type: multipart/alternative; boundary=",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: text/html; charset=utf-8",
		base64Body(msg.TextBody),
		base64Body(msg.HTMLBody),
	} {
		if !strings.Contains(data, want) {
			t.Errorf("multipart DATA missing %q\n---\n%s", want, data)
		}
	}
}

func TestSMTP_Send_NonASCIISubjectIsRFC2047Encoded(t *testing.T) {
	srv := newFakeSMTP(t)
	s := newTestSMTPSender(t, testSMTPConfig(srv))

	msg := validMessage()
	msg.Subject = "Anda diundang ✓"
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, _, _, data, _ := srv.snapshot()
	if !strings.Contains(strings.ToLower(data), "subject: =?utf-8?q?") {
		t.Errorf("subject not Q-encoded:\n%s", data)
	}
}

func TestSMTP_Send_AuthSentWhenConfigured(t *testing.T) {
	srv := newFakeSMTP(t)
	cfg := testSMTPConfig(srv)
	cfg.Username = "smtp-user"
	cfg.Password = "smtp-pass"
	s := newTestSMTPSender(t, cfg)

	if err := s.Send(context.Background(), validMessage()); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, _, _, _, gotAUTH := srv.snapshot()
	if !gotAUTH {
		t.Error("server never received AUTH (credentials configured)")
	}
}

func TestSMTP_Send_ServerRejectsRecipient(t *testing.T) {
	srv := newFakeSMTP(t)
	srv.rejectRcpt = true
	s := newTestSMTPSender(t, testSMTPConfig(srv))

	err := s.Send(context.Background(), validMessage())
	if err == nil || !errors.Is(err, ErrEmailSendFailed) {
		t.Fatalf("err = %v, want ErrEmailSendFailed", err)
	}
	if _, _, _, data, _ := srv.snapshot(); data != "" {
		t.Error("DATA was submitted despite rejected recipient")
	}
}

func TestSMTP_Send_StartTLSRequiredButUnsupportedFailsClosed(t *testing.T) {
	srv := newFakeSMTP(t) // never advertises STARTTLS
	cfg := testSMTPConfig(srv)
	cfg.TLSMode = EmailTLSModeStartTLS
	s := newTestSMTPSender(t, cfg)

	err := s.Send(context.Background(), validMessage())
	if err == nil || !errors.Is(err, ErrEmailSendFailed) {
		t.Fatalf("err = %v, want ErrEmailSendFailed (must not degrade to plaintext)", err)
	}
	if _, mailFrom, _, _, _ := srv.snapshot(); mailFrom != "" {
		t.Error("envelope stage reached despite missing STARTTLS")
	}
}

func TestSMTP_Send_DialFailure(t *testing.T) {
	// Grab a port that is very likely closed: bind, then close.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	s := newTestSMTPSender(t, EmailSenderConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    closedPort,
		From:    "no-reply@test.local",
		Timeout: 2 * time.Second,
		TLSMode: EmailTLSModeNone,
	})
	err = s.Send(context.Background(), validMessage())
	if err == nil || !errors.Is(err, ErrEmailSendFailed) {
		t.Fatalf("err = %v, want ErrEmailSendFailed", err)
	}
}

func TestSMTP_Send_CancelledContextNeverDials(t *testing.T) {
	srv := newFakeSMTP(t)
	s := newTestSMTPSender(t, testSMTPConfig(srv))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Send(ctx, validMessage())
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, ErrEmailSendFailed) {
		t.Errorf("err = %v, want wrapped ErrEmailSendFailed", err)
	}
	if conns, _, _, _, _ := srv.snapshot(); conns != 0 {
		t.Errorf("cancelled send opened %d connection(s), want 0", conns)
	}
}

// ─── Body encoding ───────────────────────────────────────────────────────────

func TestBase64Body_WrapsAndRoundTrips(t *testing.T) {
	original := strings.Repeat("invitation-token-payload.", 40) // > 76*10 chars
	encoded := base64Body(original)

	for _, line := range strings.Split(encoded, "\r\n") {
		if len(line) > 76 {
			t.Fatalf("line too long: %d bytes", len(line))
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\r\n", ""))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != original {
		t.Error("round-trip mismatch")
	}
}

// ─── MockEmailSender (test double for later phases) ─────────────────────────

func TestMockEmailSender_RecordsAndFails(t *testing.T) {
	m := NewMockEmailSender()
	if err := m.Send(context.Background(), validMessage()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if m.Len() != 1 {
		t.Fatalf("Len = %d, want 1", m.Len())
	}
	got := m.Messages()
	if got[0].Subject != "You are invited" {
		t.Errorf("recorded subject = %q", got[0].Subject)
	}

	m.ShouldFail = true
	if err := m.Send(context.Background(), validMessage()); !errors.Is(err, ErrEmailSendFailed) {
		t.Errorf("err = %v, want ErrEmailSendFailed", err)
	}
	if m.Len() != 1 {
		t.Errorf("failed send was recorded: Len = %d", m.Len())
	}

	m.Reset()
	if m.Len() != 0 {
		t.Errorf("Reset left Len = %d", m.Len())
	}
}
