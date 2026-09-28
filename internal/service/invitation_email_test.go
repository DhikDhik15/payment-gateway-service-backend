package service

// invitation_email_test.go — Phase 8C.2 unit tests for the invitation email
// builder: content, HTML escaping (XSS), URL/query encoding, subject header
// safety, and expiry rendering. Pure functions — no I/O.

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
)

// ─── Invitation URL ──────────────────────────────────────────────────────────

func TestBuildInvitationURL_AppendsPathAndSetsToken(t *testing.T) {
	got, err := buildInvitationURL("https://dashboard.example.test", "abc123")
	if err != nil {
		t.Fatalf("buildInvitationURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result not a URL: %v", err)
	}
	if u.Scheme != "https" || u.Host != "dashboard.example.test" {
		t.Errorf("origin = %s://%s, want https://dashboard.example.test", u.Scheme, u.Host)
	}
	if u.Path != "/accept-invitation" {
		t.Errorf("path = %q, want /accept-invitation", u.Path)
	}
	if tok := u.Query().Get("token"); tok != "abc123" {
		t.Errorf("token = %q, want abc123", tok)
	}
}

func TestBuildInvitationURL_EncodesTokenViaQueryEscaping(t *testing.T) {
	// Real tokens are 64 hex chars, but prove the mechanism is proper query
	// escaping — never manual concatenation — with hostile input.
	token := "a b&c+d/e?f=g#h+i%j"
	got, err := buildInvitationURL("https://dashboard.example.test", token)
	if err != nil {
		t.Fatalf("buildInvitationURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result not a URL: %v", err)
	}
	q := u.Query()
	if len(q) != 1 {
		t.Errorf("query params = %d (%v), want exactly 1 — token must be escaped so it cannot smuggle params", len(q), q)
	}
	if tok := q.Get("token"); tok != token {
		t.Errorf("round-trip token = %q, want %q", tok, token)
	}
}

func TestBuildInvitationURL_TrailingSlashAndBasePath(t *testing.T) {
	got, err := buildInvitationURL("https://dash.example.com/app/", "tok")
	if err != nil {
		t.Fatalf("buildInvitationURL: %v", err)
	}
	u, _ := url.Parse(got)
	if u.Path != "/app/accept-invitation" {
		t.Errorf("path = %q, want /app/accept-invitation", u.Path)
	}
}

func TestBuildInvitationURL_EmptyBaseGivesRelativeLink(t *testing.T) {
	// Email disabled + unset DASHBOARD_BASE_URL: the link is relative but
	// structurally intact (the noop sender drops it anyway).
	got, err := buildInvitationURL("", "tok")
	if err != nil {
		t.Fatalf("buildInvitationURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result not a URL: %v", err)
	}
	if u.Path != "/accept-invitation" || u.Query().Get("token") != "tok" {
		t.Errorf("got %q, want /accept-invitation?token=tok", got)
	}
}

// ─── Invitation email content ───────────────────────────────────────────────

func TestBuildInvitationEmail_Content(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(48 * time.Hour)
	invURL := "https://dashboard.example.test/accept-invitation?token=t0k"

	msg, err := buildInvitationEmail(
		"Toko Maju", "invitee@example.com", model.DashboardUserRoleAdmin,
		invURL, expires, now,
	)
	if err != nil {
		t.Fatalf("buildInvitationEmail: %v", err)
	}

	// Recipient is the invitation's own address — nothing else.
	if len(msg.To) != 1 || msg.To[0] != "invitee@example.com" {
		t.Errorf("To = %v, want [invitee@example.com]", msg.To)
	}
	if msg.Subject != "You're invited to join Toko Maju" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	if msg.From != "" {
		t.Errorf("From = %q, want empty (EmailSender applies configured SMTP_FROM)", msg.From)
	}

	// Plain text body is required and carries every mandated element.
	if strings.TrimSpace(msg.TextBody) == "" {
		t.Fatal("TextBody is empty — plain text body is required")
	}
	if strings.TrimSpace(msg.HTMLBody) == "" {
		t.Fatal("HTMLBody is empty — HTML body is required")
	}
	for _, want := range []string{"Toko Maju", "ADMIN", invURL} {
		if !strings.Contains(msg.TextBody, want) {
			t.Errorf("TextBody missing %q", want)
		}
		if !strings.Contains(msg.HTMLBody, want) {
			t.Errorf("HTMLBody missing %q", want)
		}
	}
	if !strings.Contains(msg.TextBody, "expires at") || !strings.Contains(msg.TextBody, "48 hours") {
		t.Errorf("TextBody missing expiry information:\n%s", msg.TextBody)
	}
	if !strings.Contains(msg.TextBody, expires.Format(time.RFC1123)) {
		t.Errorf("TextBody missing absolute expiry %q", expires.Format(time.RFC1123))
	}
	if !strings.Contains(msg.HTMLBody, `<a href="`+invURL+`">`) {
		t.Errorf("HTMLBody missing anchored acceptance link:\n%s", msg.HTMLBody)
	}
	if !strings.Contains(msg.TextBody, "dashboard") {
		t.Error("TextBody must indicate this is a dashboard invitation")
	}
}

// ─── HTML escaping (XSS) ─────────────────────────────────────────────────────

func TestBuildInvitationEmail_HTMLEscapesHostileMerchantName(t *testing.T) {
	now := time.Now().UTC()
	// Hostile merchant name exercising < > " ' &.
	hostile := `<script>alert("xss")</script> & O'Brien`

	msg, err := buildInvitationEmail(
		hostile, "invitee@example.com", model.DashboardUserRoleViewer,
		"https://dashboard.example.test/accept-invitation?token=t0k",
		now.Add(48*time.Hour), now,
	)
	if err != nil {
		t.Fatalf("buildInvitationEmail: %v", err)
	}

	if strings.Contains(msg.HTMLBody, "<script>") {
		t.Errorf("HTMLBody contains a raw <script> element — merchant name not escaped:\n%s", msg.HTMLBody)
	}
	for _, want := range []string{
		"&lt;script&gt;", // < >
		"&#34;",          // "
		"&#39;",          // '
		"&amp;",          // &
	} {
		if !strings.Contains(msg.HTMLBody, want) {
			t.Errorf("HTMLBody missing escaped form %q", want)
		}
	}
	// The document itself must remain structurally valid.
	if !strings.Contains(msg.HTMLBody, "<html") || !strings.Contains(msg.HTMLBody, `<a href="`) {
		t.Errorf("HTMLBody structure damaged:\n%s", msg.HTMLBody)
	}
	// The link must survive untouched (URL contains only safe characters).
	if !strings.Contains(msg.HTMLBody, `href="https://dashboard.example.test/accept-invitation?token=t0k"`) {
		t.Errorf("acceptance link not intact in HTML:\n%s", msg.HTMLBody)
	}
	// The subject is plain text (Q-encoded at transport) — the raw name there
	// is inert, but it must not gain control characters (next test).
	if msg.Subject != "You're invited to join "+hostile {
		t.Errorf("Subject = %q", msg.Subject)
	}
}

func TestBuildInvitationEmail_SanitisesSubjectLineBreaks(t *testing.T) {
	now := time.Now().UTC()
	msg, err := buildInvitationEmail(
		"Evil\r\nBcc: attacker@evil.com", "invitee@example.com", model.DashboardUserRoleOwner,
		"https://dashboard.example.test/accept-invitation?token=t0k",
		now.Add(48*time.Hour), now,
	)
	if err != nil {
		t.Fatalf("buildInvitationEmail: %v", err)
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		t.Errorf("Subject contains a line break — header injection vector: %q", msg.Subject)
	}
	if !strings.Contains(msg.Subject, "Evil") || !strings.Contains(msg.Subject, "Bcc: attacker@evil.com") {
		t.Errorf("Subject unexpectedly mangled: %q", msg.Subject)
	}
}

// ─── Expiry rendering ────────────────────────────────────────────────────────

func TestHumaniseDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{48 * time.Hour, "48 hours"},
		{72 * time.Hour, "72 hours"},
		{24 * time.Hour, "24 hours"},
		{168 * time.Hour, "7 days"},
		{time.Hour, "1 hour"},
		{90 * time.Minute, "2 hours"}, // rounded up — never understate validity
		{0, "no time"},
		{-time.Hour, "no time"},
	}
	for _, tc := range cases {
		if got := humaniseDuration(tc.in); got != tc.want {
			t.Errorf("humaniseDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
