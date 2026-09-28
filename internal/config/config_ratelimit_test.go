package config

// config_ratelimit_test.go — Phase 8D.1 tests for parseRateLimitConfig:
// sensitive defaults, explicit values, and the fail-closed validation that
// keeps a typo from becoming "unlimited".

import (
	"strings"
	"testing"
)

// clearRateLimitEnv removes every Phase 8D.1 variable so each test starts from
// the same baseline (getEnv treats an empty value as "unset").
func clearRateLimitEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"RATE_LIMIT_ENABLED",
		"RATE_LIMIT_LOGIN_PER_MINUTE",
		"RATE_LIMIT_ADMIN_PER_MINUTE",
		"RATE_LIMIT_API_PER_MINUTE",
	} {
		t.Setenv(k, "")
	}
}

func TestParseRateLimitConfig_DefaultsEnabledWithSensitiveLimits(t *testing.T) {
	clearRateLimitEnv(t)

	cfg, err := parseRateLimitConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled {
		t.Error("Enabled = false, want true by default (rate limiting on)")
	}
	if cfg.LoginPerMinute != 10 {
		t.Errorf("LoginPerMinute = %d, want 10", cfg.LoginPerMinute)
	}
	if cfg.AdminPerMinute != 60 {
		t.Errorf("AdminPerMinute = %d, want 60", cfg.AdminPerMinute)
	}
	if cfg.APIPerMinute != 120 {
		t.Errorf("APIPerMinute = %d, want 120", cfg.APIPerMinute)
	}
}

func TestParseRateLimitConfig_ExplicitValues(t *testing.T) {
	clearRateLimitEnv(t)
	t.Setenv("RATE_LIMIT_LOGIN_PER_MINUTE", "5")
	t.Setenv("RATE_LIMIT_ADMIN_PER_MINUTE", "30")
	t.Setenv("RATE_LIMIT_API_PER_MINUTE", "60")

	cfg, err := parseRateLimitConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoginPerMinute != 5 || cfg.AdminPerMinute != 30 || cfg.APIPerMinute != 60 {
		t.Errorf("limits = %d/%d/%d, want 5/30/60", cfg.LoginPerMinute, cfg.AdminPerMinute, cfg.APIPerMinute)
	}
}

func TestParseRateLimitConfig_DisabledFlag(t *testing.T) {
	clearRateLimitEnv(t)
	t.Setenv("RATE_LIMIT_ENABLED", "false")

	cfg, err := parseRateLimitConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true, want false when RATE_LIMIT_ENABLED=false")
	}
	// Limits are still parsed (middleware receives nil limiter when disabled).
	if cfg.LoginPerMinute != 10 {
		t.Errorf("LoginPerMinute = %d, want default 10", cfg.LoginPerMinute)
	}
}

// A ZERO OR NEGATIVE limit must be rejected at startup — it must never mean
// "unlimited". The only supported off switch is RATE_LIMIT_ENABLED=false.
func TestParseRateLimitConfig_RejectsNonPositiveLimits(t *testing.T) {
	for _, tc := range []struct {
		varName string
		value   string
	}{
		{"RATE_LIMIT_LOGIN_PER_MINUTE", "0"},
		{"RATE_LIMIT_LOGIN_PER_MINUTE", "-1"},
		{"RATE_LIMIT_ADMIN_PER_MINUTE", "0"},
		{"RATE_LIMIT_ADMIN_PER_MINUTE", "-5"},
		{"RATE_LIMIT_API_PER_MINUTE", "0"},
		{"RATE_LIMIT_API_PER_MINUTE", "-120"},
	} {
		t.Run(tc.varName+"="+tc.value, func(t *testing.T) {
			clearRateLimitEnv(t)
			t.Setenv(tc.varName, tc.value)

			_, err := parseRateLimitConfig()
			if err == nil {
				t.Fatalf("expected error for %s=%s, got nil (would mean unlimited)", tc.varName, tc.value)
			}
			if !strings.Contains(err.Error(), tc.varName) {
				t.Errorf("error must name %s, got: %v", tc.varName, err)
			}
		})
	}
}

func TestParseRateLimitConfig_RejectsNonNumericLimit(t *testing.T) {
	clearRateLimitEnv(t)
	t.Setenv("RATE_LIMIT_LOGIN_PER_MINUTE", "ten")

	if _, err := parseRateLimitConfig(); err == nil || !strings.Contains(err.Error(), "RATE_LIMIT_LOGIN_PER_MINUTE") {
		t.Fatalf("expected RATE_LIMIT_LOGIN_PER_MINUTE error, got %v", err)
	}
}
