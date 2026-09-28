package config

// config_email_test.go — Phase 8C.1 tests for parseEmailConfig: defaults,
// cross-field requirements (only enforced when EMAIL_ENABLED=true), and
// transport-security guardrails.

import (
	"strings"
	"testing"
	"time"
)

// clearEmailEnv removes every Phase 8C.1 variable so each test starts from
// the same baseline (getEnv treats an empty value as "unset").
func clearEmailEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EMAIL_ENABLED", "SMTP_HOST", "SMTP_PORT",
		"SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM",
		"SMTP_TLS", "SMTP_TIMEOUT",
	} {
		t.Setenv(k, "")
	}
}

func TestParseEmailConfig_DefaultsDisabled(t *testing.T) {
	clearEmailEnv(t)
	cfg, err := parseEmailConfig("development")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true, want false by default")
	}
	if cfg.Port != 587 {
		t.Errorf("Port = %d, want 587", cfg.Port)
	}
	if cfg.TLSMode != "starttls" {
		t.Errorf("TLSMode = %q, want starttls", cfg.TLSMode)
	}
	if cfg.Timeout.Seconds() != 10 {
		t.Errorf("Timeout = %v, want 10s", cfg.Timeout)
	}
}

func TestParseEmailConfig_EnabledRequiresHost(t *testing.T) {
	clearEmailEnv(t)
	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("SMTP_FROM", "Gateway <no-reply@example.com>")
	_, err := parseEmailConfig("development")
	if err == nil || !strings.Contains(err.Error(), "SMTP_HOST") {
		t.Fatalf("err = %v, want SMTP_HOST required error", err)
	}
}

func TestParseEmailConfig_EnabledRequiresFrom(t *testing.T) {
	clearEmailEnv(t)
	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	_, err := parseEmailConfig("development")
	if err == nil || !strings.Contains(err.Error(), "SMTP_FROM") {
		t.Fatalf("err = %v, want SMTP_FROM required error", err)
	}
}

func TestParseEmailConfig_EnabledValid(t *testing.T) {
	clearEmailEnv(t)
	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_USERNAME", "smtp-user")
	t.Setenv("SMTP_PASSWORD", "smtp-pass")
	t.Setenv("SMTP_FROM", "Gateway <no-reply@example.com>")
	t.Setenv("SMTP_TLS", "IMPLICIT")
	t.Setenv("SMTP_TIMEOUT", "5s")

	cfg, err := parseEmailConfig("production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled || cfg.Host != "smtp.example.com" || cfg.Port != 465 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.Username != "smtp-user" || cfg.Password != "smtp-pass" {
		t.Errorf("credentials not loaded: %+v", cfg)
	}
	if cfg.TLSMode != "implicit" {
		t.Errorf("TLSMode = %q, want case-normalised implicit", cfg.TLSMode)
	}
	if cfg.Timeout.Seconds() != 5 {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
}

func TestParseEmailConfig_InvalidPort(t *testing.T) {
	clearEmailEnv(t)
	for _, port := range []string{"not-a-number", "0", "70000"} {
		t.Setenv("SMTP_PORT", port)
		if _, err := parseEmailConfig("development"); err == nil || !strings.Contains(err.Error(), "SMTP_PORT") {
			t.Errorf("SMTP_PORT=%q: err = %v, want SMTP_PORT error", port, err)
		}
	}
}

func TestParseEmailConfig_InvalidTimeout(t *testing.T) {
	clearEmailEnv(t)
	t.Setenv("SMTP_TIMEOUT", "soon")
	if _, err := parseEmailConfig("development"); err == nil || !strings.Contains(err.Error(), "SMTP_TIMEOUT") {
		t.Fatalf("err = %v, want SMTP_TIMEOUT error", err)
	}
	t.Setenv("SMTP_TIMEOUT", "-1s")
	if _, err := parseEmailConfig("development"); err == nil || !strings.Contains(err.Error(), "SMTP_TIMEOUT") {
		t.Fatalf("err = %v, want SMTP_TIMEOUT error for non-positive value", err)
	}
}

func TestParseEmailConfig_InvalidTLSMode(t *testing.T) {
	clearEmailEnv(t)
	t.Setenv("SMTP_TLS", "opportunistic")
	if _, err := parseEmailConfig("development"); err == nil || !strings.Contains(err.Error(), "SMTP_TLS") {
		t.Fatalf("err = %v, want SMTP_TLS error", err)
	}
}

func TestParseEmailConfig_NoneRejectedOnlyWhenEnabledInProduction(t *testing.T) {
	clearEmailEnv(t)
	t.Setenv("SMTP_TLS", "none")

	// Disabled → plaintext setting is irrelevant (no sender constructed).
	if _, err := parseEmailConfig("production"); err != nil {
		t.Errorf("disabled + production: err = %v, want nil", err)
	}

	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "Gateway <no-reply@example.com>")

	if _, err := parseEmailConfig("development"); err != nil {
		t.Errorf("enabled + development: err = %v, want nil", err)
	}
	if _, err := parseEmailConfig("production"); err == nil || !strings.Contains(err.Error(), "production") {
		t.Errorf("enabled + production: err = %v, want rejection of SMTP_TLS=none", err)
	}
}

// ─── Phase 8C.2: DASHBOARD_BASE_URL ─────────────────────────────────────────

func clearDashboardBaseURL(t *testing.T) {
	t.Helper()
	t.Setenv("DASHBOARD_BASE_URL", "")
}

func TestParseDashboardBaseURL_OptionalWhenEmailDisabled(t *testing.T) {
	clearDashboardBaseURL(t)
	got, err := parseDashboardBaseURL(false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseDashboardBaseURL_RequiredWhenEmailEnabled(t *testing.T) {
	clearDashboardBaseURL(t)
	_, err := parseDashboardBaseURL(true)
	if err == nil || !strings.Contains(err.Error(), "DASHBOARD_BASE_URL") {
		t.Fatalf("err = %v, want DASHBOARD_BASE_URL required error", err)
	}
}

func TestParseDashboardBaseURL_ValidNormalisesTrailingSlash(t *testing.T) {
	clearDashboardBaseURL(t)
	t.Setenv("DASHBOARD_BASE_URL", "https://dashboard.example.com/")
	got, err := parseDashboardBaseURL(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://dashboard.example.com" {
		t.Errorf("got %q, want trailing slash normalised away", got)
	}
}

func TestParseDashboardBaseURL_Invalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no scheme", "dashboard.example.com"},
		{"wrong scheme", "ftp://dashboard.example.com"},
		{"no host", "https://"},
		{"query present", "https://dashboard.example.com/?utm=x"},
		{"fragment present", "https://dashboard.example.com/#/app"},
		{"userinfo present", "https://user:password@dashboard.example.com"},
		{"path present", "https://dashboard.example.com/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearDashboardBaseURL(t)
			t.Setenv("DASHBOARD_BASE_URL", tc.raw)
			_, err := parseDashboardBaseURL(true)
			if err == nil || !strings.Contains(err.Error(), "DASHBOARD_BASE_URL") {
				t.Fatalf("raw %q: err = %v, want DASHBOARD_BASE_URL error", tc.raw, err)
			}
		})
	}
}

// ─── Phase 8C.3C: EMAIL_CLEANUP_* / EMAIL_OUTBOX_*_RETENTION ────────────────

// clearEmailCleanupEnv removes every Phase 8C.3C variable so each test starts
// from the same baseline (getEnv treats an empty value as "unset").
func clearEmailCleanupEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EMAIL_CLEANUP_ENABLED", "EMAIL_CLEANUP_INTERVAL",
		"EMAIL_CLEANUP_BATCH_SIZE",
		"EMAIL_OUTBOX_SENT_RETENTION", "EMAIL_OUTBOX_DEAD_RETENTION",
	} {
		t.Setenv(k, "")
	}
}

// TestParseEmailCleanupConfig_Defaults — the defaults ARE the approved
// retention policy, and the worker is opt-in: with Enabled=false main.go
// never calls Start() (the disabled-worker gate, §12.G.20).
func TestParseEmailCleanupConfig_Defaults(t *testing.T) {
	clearEmailCleanupEnv(t)
	cfg, err := parseEmailCleanupConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true, want false by default (opt-in)")
	}
	if cfg.Interval != time.Hour {
		t.Errorf("Interval = %v, want 1h", cfg.Interval)
	}
	if cfg.BatchSize != 100 {
		t.Errorf("BatchSize = %d, want 100", cfg.BatchSize)
	}
	if cfg.SentRetention != 168*time.Hour {
		t.Errorf("SentRetention = %v, want 168h (7 days)", cfg.SentRetention)
	}
	if cfg.DeadRetention != 720*time.Hour {
		t.Errorf("DeadRetention = %v, want 720h (30 days)", cfg.DeadRetention)
	}
}

// §12.I.24/25 — zero or negative retention must be REJECTED at startup; it
// must never silently mean "delete everything immediately".
func TestParseEmailCleanupConfig_RejectsNonPositiveRetention(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"EMAIL_OUTBOX_SENT_RETENTION", "0s"},
		{"EMAIL_OUTBOX_SENT_RETENTION", "-1h"},
		{"EMAIL_OUTBOX_DEAD_RETENTION", "0s"},
		{"EMAIL_OUTBOX_DEAD_RETENTION", "-1h"},
		{"EMAIL_OUTBOX_SENT_RETENTION", "not-a-duration"},
	} {
		clearEmailCleanupEnv(t)
		t.Setenv(tc.key, tc.val)
		_, err := parseEmailCleanupConfig()
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s=%q: err = %v, want %s error", tc.key, tc.val, err, tc.key)
		}
	}
}

// §12.I.26 — a zero/negative interval would hot-loop the worker.
func TestParseEmailCleanupConfig_RejectsNonPositiveInterval(t *testing.T) {
	for _, val := range []string{"0s", "-5s", "soon"} {
		clearEmailCleanupEnv(t)
		t.Setenv("EMAIL_CLEANUP_INTERVAL", val)
		_, err := parseEmailCleanupConfig()
		if err == nil || !strings.Contains(err.Error(), "EMAIL_CLEANUP_INTERVAL") {
			t.Errorf("EMAIL_CLEANUP_INTERVAL=%q: err = %v, want error", val, err)
		}
	}
}

// §12.I.27 — a zero/negative batch size would make the DELETE unbounded.
func TestParseEmailCleanupConfig_RejectsNonPositiveBatchSize(t *testing.T) {
	for _, val := range []string{"0", "-1", "many"} {
		clearEmailCleanupEnv(t)
		t.Setenv("EMAIL_CLEANUP_BATCH_SIZE", val)
		_, err := parseEmailCleanupConfig()
		if err == nil || !strings.Contains(err.Error(), "EMAIL_CLEANUP_BATCH_SIZE") {
			t.Errorf("EMAIL_CLEANUP_BATCH_SIZE=%q: err = %v, want error", val, err)
		}
	}
}

// Explicit overrides are honoured (operator may tune, but must stay > 0).
func TestParseEmailCleanupConfig_OverridesAccepted(t *testing.T) {
	clearEmailCleanupEnv(t)
	t.Setenv("EMAIL_CLEANUP_ENABLED", "true")
	t.Setenv("EMAIL_CLEANUP_INTERVAL", "30m")
	t.Setenv("EMAIL_CLEANUP_BATCH_SIZE", "50")
	t.Setenv("EMAIL_OUTBOX_SENT_RETENTION", "24h")
	t.Setenv("EMAIL_OUTBOX_DEAD_RETENTION", "336h")
	cfg, err := parseEmailCleanupConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled || cfg.Interval != 30*time.Minute || cfg.BatchSize != 50 ||
		cfg.SentRetention != 24*time.Hour || cfg.DeadRetention != 336*time.Hour {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}
