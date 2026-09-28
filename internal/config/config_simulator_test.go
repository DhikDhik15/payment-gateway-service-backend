package config

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseSimulatorMerchantID_RequiredWhenEnabled(t *testing.T) {
	t.Setenv("SIMULATOR_MERCHANT_ID", "")
	_, err := parseSimulatorMerchantID(true)
	if err == nil || !strings.Contains(err.Error(), "SIMULATOR_MERCHANT_ID is required") {
		t.Fatalf("expected required error, got %v", err)
	}
}

func TestParseSimulatorMerchantID_InvalidUUID(t *testing.T) {
	t.Setenv("SIMULATOR_MERCHANT_ID", "not-a-uuid")
	_, err := parseSimulatorMerchantID(true)
	if err == nil || !strings.Contains(err.Error(), "invalid SIMULATOR_MERCHANT_ID") {
		t.Fatalf("expected invalid UUID error, got %v", err)
	}
}

func TestParseSimulatorMerchantID_ValidWhenEnabled(t *testing.T) {
	want := uuid.MustParse("755ef533-1b2e-4dfd-a0d2-7ab36efbd389")
	t.Setenv("SIMULATOR_MERCHANT_ID", want.String())
	got, err := parseSimulatorMerchantID(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestParseSimulatorMerchantID_OptionalWhenDisabled(t *testing.T) {
	_ = os.Unsetenv("SIMULATOR_MERCHANT_ID")
	got, err := parseSimulatorMerchantID(false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != uuid.Nil {
		t.Fatalf("expected Nil when disabled and unset, got %s", got)
	}
}

func TestParseSimulatorMerchantID_InvalidWhenDisabledButSet(t *testing.T) {
	t.Setenv("SIMULATOR_MERCHANT_ID", "bad")
	_, err := parseSimulatorMerchantID(false)
	if err == nil || !strings.Contains(err.Error(), "invalid SIMULATOR_MERCHANT_ID") {
		t.Fatalf("expected invalid UUID error when set while disabled, got %v", err)
	}
}

// ─── Phase 8D.1: simulator is disabled by default and fails closed ───────────

// baseLoadEnv pins every variable Load() requires so the simulator/rate-limit
// assertions are not affected by the ambient environment.
func baseLoadEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"DB_HOST":                     "localhost",
		"DB_USER":                     "test",
		"DB_PASSWORD":                 "test",
		"DB_NAME":                     "test",
		"DB_SSLMODE":                  "verify-full",
		"APP_ENV":                     "development",
		"PAYMENT_SIMULATOR_ENABLED":   "", // empty ⇒ default
		"SIMULATOR_MERCHANT_ID":       "",
		"RATE_LIMIT_ENABLED":          "",
		"RATE_LIMIT_LOGIN_PER_MINUTE": "",
		"RATE_LIMIT_ADMIN_PER_MINUTE": "",
		"RATE_LIMIT_API_PER_MINUTE":   "",
		"EMAIL_ENABLED":               "false",
		"DASHBOARD_BASE_URL":          "",
		"CORS_ALLOWED_ORIGINS":        "",
		"PAYMENT_PROVIDER":            "mock",
		"FRONTEND_PUBLIC_URL":         "http://localhost:5173",
		"MOCK_PAYMENT_BASE_URL":       "",
		"ADMIN_API_KEY":               "",
		"MOCK_WEBHOOK_SECRET":         "test-mock-secret-with-at-least-32-bytes",
	} {
		t.Setenv(k, v)
	}
}

func TestLoad_SimulatorDisabledByDefault(t *testing.T) {
	baseLoadEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.SimulatorEnabled {
		t.Error("SimulatorEnabled = true, want false by default (fail closed)")
	}
	if cfg.App.SimulatorMerchantID != uuid.Nil {
		t.Errorf("SimulatorMerchantID = %s, want Nil when disabled", cfg.App.SimulatorMerchantID)
	}
}

func TestLoad_SimulatorEnabledOutsideProduction(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("PAYMENT_SIMULATOR_ENABLED", "true")
	t.Setenv("SIMULATOR_MERCHANT_ID", "755ef533-1b2e-4dfd-a0d2-7ab36efbd389")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.App.SimulatorEnabled {
		t.Error("SimulatorEnabled = false, want true when explicitly enabled outside production")
	}
}

func TestLoad_SimulatorEnabledInProductionFailsClosed(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_SIMULATOR_ENABLED", "true")
	// Satisfy the other production-required variables so the simulator gate
	// (not an unrelated missing variable) is what rejects this configuration.
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("TRUSTED_PROXIES", "none")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup/config error when the simulator is enabled in production")
	}
	if !strings.Contains(err.Error(), "PAYMENT_SIMULATOR_ENABLED") {
		t.Errorf("error must name PAYMENT_SIMULATOR_ENABLED, got: %v", err)
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error must mention production, got: %v", err)
	}
}
