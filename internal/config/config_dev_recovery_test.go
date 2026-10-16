package config

import (
	"strings"
	"testing"
)

// Development-only account recovery config: fail-closed contract tests.

func devRecoveryBaseEnv(t *testing.T) {
	t.Helper()
	baseLoadEnv(t)
	for k, v := range map[string]string{
		"DEV_RECOVERY_ENABLED": "",
		"DEV_RECOVERY_KEY":     "",
		"APP_BIND_HOST":        "",
	} {
		t.Setenv(k, v)
	}
}

func TestParseDevRecovery_DisabledByDefault(t *testing.T) {
	devRecoveryBaseEnv(t)
	cfg, err := parseDevRecoveryConfig("development", "")
	if err != nil {
		t.Fatalf("disabled by default must not error, got %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true, want false by default (fail closed)")
	}
	if cfg.Key != "" {
		t.Errorf("Key = %q, want empty when disabled", cfg.Key)
	}
}

func TestLoad_DevRecoveryDisabledByDefault(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("DEV_RECOVERY_ENABLED", "")
	t.Setenv("DEV_RECOVERY_KEY", "")
	t.Setenv("APP_BIND_HOST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DevRecovery.Enabled {
		t.Error("DevRecovery.Enabled = true, want false by default (fail closed)")
	}
}

func TestParseDevRecovery_EnabledInDevelopment(t *testing.T) {
	devRecoveryBaseEnv(t)
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "0123456789abcdef0123456789abcdef")
	cfg, err := parseDevRecoveryConfig("development", "127.0.0.1")
	if err != nil {
		t.Fatalf("enabled in development must parse, got %v", err)
	}
	if !cfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.Key != "0123456789abcdef0123456789abcdef" {
		t.Error("Key was not loaded")
	}
}

func TestLoad_DevRecoveryEnabledInDevelopment(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("APP_BIND_HOST", "127.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with dev recovery in development: %v", err)
	}
	if !cfg.DevRecovery.Enabled {
		t.Error("DevRecovery.Enabled = false, want true when explicitly enabled in development")
	}
	if cfg.DevRecovery.Key != "0123456789abcdef0123456789abcdef" {
		t.Error("DevRecovery.Key was not loaded")
	}
	if cfg.App.BindHost != "127.0.0.1" {
		t.Errorf("BindHost = %q, want 127.0.0.1", cfg.App.BindHost)
	}
}

func TestLoad_DevRecoveryEnabledInProductionFailsClosed(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("APP_BIND_HOST", "127.0.0.1")
	// Satisfy the other production-required variables so the dev-recovery gate
	// (not an unrelated missing variable) is what rejects this configuration.
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test-32")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("TRUSTED_PROXIES", "none")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup/config error when dev recovery is enabled in production")
	}
	if !strings.Contains(err.Error(), "DEV_RECOVERY_ENABLED") {
		t.Errorf("error must name DEV_RECOVERY_ENABLED, got: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "production") {
		t.Errorf("error must mention production, got: %v", err)
	}
}

func TestLoad_DevRecoveryEnabledInTestEnvFails(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "test")
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("APP_BIND_HOST", "127.0.0.1")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup/config error when dev recovery is enabled outside development")
	}
	if !strings.Contains(err.Error(), "DEV_RECOVERY_ENABLED") {
		t.Errorf("error must name DEV_RECOVERY_ENABLED, got: %v", err)
	}
}

func TestLoad_DevRecoveryEnabledWithoutKeyFails(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "")
	t.Setenv("APP_BIND_HOST", "127.0.0.1")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup/config error when dev recovery is enabled without a key")
	}
	if !strings.Contains(err.Error(), "DEV_RECOVERY_KEY") {
		t.Errorf("error must name DEV_RECOVERY_KEY, got: %v", err)
	}
}

func TestLoad_DevRecoveryEnabledWithShortKeyFails(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "short-key")
	t.Setenv("APP_BIND_HOST", "127.0.0.1")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup/config error for a short dev recovery key")
	}
	if !strings.Contains(err.Error(), "DEV_RECOVERY_KEY") {
		t.Errorf("error must name DEV_RECOVERY_KEY, got: %v", err)
	}
}

func TestLoad_DevRecoveryEnabledWithoutLoopbackBindFails(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("DEV_RECOVERY_ENABLED", "true")
	t.Setenv("DEV_RECOVERY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("APP_BIND_HOST", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected startup/config error when dev recovery is enabled without loopback binding")
	}
	if !strings.Contains(err.Error(), "APP_BIND_HOST") {
		t.Errorf("error must name APP_BIND_HOST, got: %v", err)
	}
}
