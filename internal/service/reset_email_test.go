package service

// Unit tests for the password-reset email builders (Phase 8E). No network,
// no database. The plaintext token travels only inside the rendered email.

import (
	"strings"
	"testing"
	"time"
)

func TestBuildResetURL(t *testing.T) {
	url, err := buildResetURL("https://dash.example/", "tok-123")
	if err != nil {
		t.Fatalf("build url: %v", err)
	}
	if url != "https://dash.example/reset-password?token=tok-123" {
		t.Errorf("url = %q", url)
	}
	if _, err := buildResetURL("ftp://dash.example", "tok"); err == nil {
		t.Errorf("non-http base must be rejected")
	}
}

func TestBuildResetEmail(t *testing.T) {
	now := time.Now().UTC()
	msg, err := buildResetEmail("owner@example.com",
		"https://dash.example/reset-password?token=tok-123", now.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("build email: %v", err)
	}
	if len(msg.To) != 1 || msg.To[0] != "owner@example.com" {
		t.Errorf("recipient = %v", msg.To)
	}
	for _, body := range []string{msg.TextBody, msg.HTMLBody} {
		if !strings.Contains(body, "https://dash.example/reset-password?token=tok-123") {
			t.Errorf("body must contain the reset link")
		}
		if !strings.Contains(body, "only once") {
			t.Errorf("body must state single-use")
		}
	}
	if _, err := buildResetEmail("", "https://dash.example/reset-password?token=x", now, now); err == nil {
		t.Errorf("empty recipient must be rejected")
	}
}
