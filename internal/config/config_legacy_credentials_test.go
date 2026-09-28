// config_legacy_credentials_test.go — Phase 8D.3 tests for
// parseLegacyCredentialsConfig (LEGACY_API_CREDENTIALS_ENABLED): the migration
// window default per environment, the fail-safe production default, and the
// strict parse that makes a typo fail startup instead of silently choosing a
// default.
package config

import (
	"strings"
	"testing"
)

// ─── unset → environment default ──────────────────────────────────────────────

func TestParseLegacyCredentialsConfig_UnsetDevelopmentDefaultsTrue(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "")

	got, err := parseLegacyCredentialsConfig("development")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("want true in development when unset (existing merchants keep working)")
	}
}

func TestParseLegacyCredentialsConfig_UnsetTestDefaultsTrue(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "")

	got, err := parseLegacyCredentialsConfig("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("want true in test when unset")
	}
}

// TestParseLegacyCredentialsConfig_UnsetProductionDefaultsFalse is the
// fail-safe requirement: an operator who forgets the variable in production
// gets the CLOSED window, never an accidentally live plaintext credential path.
func TestParseLegacyCredentialsConfig_UnsetProductionDefaultsFalse(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "")

	got, err := parseLegacyCredentialsConfig("production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("want false in production when unset (FAIL-SAFE default)")
	}
}

// Any environment other than development/test must also fail safe.
func TestParseLegacyCredentialsConfig_UnsetUnknownEnvDefaultsFalse(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "")

	for _, env := range []string{"staging", "uat", "prod", ""} {
		got, err := parseLegacyCredentialsConfig(env)
		if err != nil {
			t.Fatalf("env %q: unexpected error: %v", env, err)
		}
		if got {
			t.Errorf("env %q: want false when unset (only development/test default open)", env)
		}
	}
}

// ─── explicit values are honoured everywhere ─────────────────────────────────

func TestParseLegacyCredentialsConfig_ExplicitTrueInProduction(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "true")

	got, err := parseLegacyCredentialsConfig("production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("explicit true in production must be honoured (deliberate operator override)")
	}
}

func TestParseLegacyCredentialsConfig_ExplicitFalseInDevelopment(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "false")

	got, err := parseLegacyCredentialsConfig("development")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("explicit false in development must be honoured (close the window locally)")
	}
}

func TestParseLegacyCredentialsConfig_TruthyFormsAccepted(t *testing.T) {
	// strconv.ParseBool contract: 1, t, T, TRUE, true, True.
	for _, raw := range []string{"1", "t", "T", "TRUE", "true", "True"} {
		t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", raw)
		got, err := parseLegacyCredentialsConfig("development")
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", raw, err)
		}
		if !got {
			t.Errorf("%q: want true", raw)
		}
	}
	for _, raw := range []string{"0", "f", "F", "FALSE", "false", "False"} {
		t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", raw)
		got, err := parseLegacyCredentialsConfig("development")
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", raw, err)
		}
		if got {
			t.Errorf("%q: want false", raw)
		}
	}
}

func TestParseLegacyCredentialsConfig_WhitespaceTrimmed(t *testing.T) {
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "  true  ")

	got, err := parseLegacyCredentialsConfig("development")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("want true after trimming surrounding whitespace")
	}
}

// ─── strict parse: a typo fails startup ──────────────────────────────────────

// TestParseLegacyCredentialsConfig_InvalidValueFailsStartup guards the
// difference from getEnvBool: a value that is not a boolean must ERROR, never
// silently fall back to a default (which could reopen the window by mistake).
func TestParseLegacyCredentialsConfig_InvalidValueFailsStartup(t *testing.T) {
	for _, raw := range []string{"yes", "no", "on", "off", "enabled", "tru", "2", "-1", "trueish"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", raw)

			_, err := parseLegacyCredentialsConfig("development")
			if err == nil {
				t.Fatalf("want error for %q, got nil", raw)
			}
			if !strings.Contains(err.Error(), "LEGACY_API_CREDENTIALS_ENABLED") {
				t.Errorf("error must name the variable, got: %v", err)
			}
		})
	}
}

// ─── integration through config.Load ─────────────────────────────────────────

func TestLoad_LegacyCredentialsUnsetDevelopmentOpen(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.App.LegacyAPICredentialsEnabled {
		t.Error("App.LegacyAPICredentialsEnabled = false, want true (development, unset)")
	}
}

// TestLoad_LegacyCredentialsUnsetProductionFailSafe proves the closed default
// reaches AppConfig when APP_ENV=production and the variable is absent.
func TestLoad_LegacyCredentialsUnsetProductionFailSafe(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.LegacyAPICredentialsEnabled {
		t.Error("App.LegacyAPICredentialsEnabled = true, want false (production fail-safe default)")
	}
}

func TestLoad_LegacyCredentialsInvalidFailsStartup(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("LEGACY_API_CREDENTIALS_ENABLED", "yep")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to fail on a non-boolean LEGACY_API_CREDENTIALS_ENABLED")
	}
	if !strings.Contains(err.Error(), "LEGACY_API_CREDENTIALS_ENABLED") {
		t.Errorf("error must name the variable, got: %v", err)
	}
}
