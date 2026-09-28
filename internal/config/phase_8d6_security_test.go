package config

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestLoad_RejectsNonPositiveWebhookDeliveryInterval(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("WEBHOOK_DELIVERY_INTERVAL", "0s")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WEBHOOK_DELIVERY_INTERVAL") {
		t.Fatalf("zero webhook delivery interval error = %v", err)
	}
}

func TestLoad_ProductionRejectsDevelopmentFallbackSecrets(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("AUTH_JWT_SECRET", insecureDevJWTSecret)
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_JWT_SECRET") {
		t.Fatalf("development JWT fallback error = %v", err)
	}

	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", insecureDevWebhookEncryptionKeyHex)
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WEBHOOK_SECRET_ENCRYPTION_KEY") {
		t.Fatalf("development webhook encryption fallback error = %v", err)
	}

	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://api.provider.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	devKey, decodeErr := hex.DecodeString(insecureDevWebhookEncryptionKeyHex)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(devKey))
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WEBHOOK_SECRET_ENCRYPTION_KEY") {
		t.Fatalf("base64 development webhook encryption fallback error = %v", err)
	}
}

func TestLoad_ProductionRejectsDisabledRateLimiting(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://api.provider.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("RATE_LIMIT_ENABLED", "false")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RATE_LIMIT_ENABLED") {
		t.Fatalf("disabled production rate limit error = %v", err)
	}
}

func TestLoad_RejectsInvalidOperationalBooleans(t *testing.T) {
	for _, key := range []string{
		"PAYMENT_EXPIRY_ENABLED",
		"WEBHOOK_DELIVERY_ENABLED",
		"EMAIL_WORKER_ENABLED",
		"EMAIL_CLEANUP_ENABLED",
		"EMAIL_ENABLED",
		"RATE_LIMIT_ENABLED",
		"PAYMENT_SIMULATOR_ENABLED",
		"WEBHOOK_REQUIRE_HTTPS",
	} {
		t.Run(key, func(t *testing.T) {
			baseLoadEnv(t)
			t.Setenv(key, "not-a-boolean")
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("invalid %s error = %v", key, err)
			}
		})
	}
}

func TestLoad_RejectsMissingRequiredDatabaseVariablesWithoutPanic(t *testing.T) {
	for _, key := range []string{"DB_HOST", "DB_USER", "DB_PASSWORD", "DB_NAME"} {
		t.Run(key, func(t *testing.T) {
			baseLoadEnv(t)
			t.Setenv(key, "")
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing %s error = %v", key, err)
			}
		})
	}
}

func TestLoad_RejectsInvalidPortValues(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_PORT", "not-a-port")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_PORT") {
		t.Fatalf("invalid APP_PORT error = %v", err)
	}
	baseLoadEnv(t)
	t.Setenv("APP_PORT", "8080")
	t.Setenv("DB_PORT", "70000")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DB_PORT") {
		t.Fatalf("invalid DB_PORT error = %v", err)
	}
}

func TestLoad_ProductionRejectsDisabledDatabaseTLS(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("DB_SSLMODE", "disable")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DB_SSLMODE") {
		t.Fatalf("disabled production DB TLS error = %v", err)
	}
}

func TestLoad_ProductionRejectsMockPaymentProvider(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("PAYMENT_PROVIDER", "mock")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAYMENT_PROVIDER") {
		t.Fatalf("mock production provider error = %v", err)
	}
}

func TestLoad_ProductionRejectsShortJWTSecret(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("AUTH_JWT_SECRET", "short")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_JWT_SECRET") {
		t.Fatalf("short production JWT secret error = %v", err)
	}
}

func TestLoad_ProductionRejectsShortAdminKey(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("ADMIN_API_KEY", "short-admin-key")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_API_KEY") {
		t.Fatalf("short production admin key error = %v", err)
	}
}

func TestLoad_ProductionRejectsHTTPInvitationBaseWhenEmailEnabled(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "no-reply@example.com")
	t.Setenv("DASHBOARD_BASE_URL", "http://dashboard.example.com")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DASHBOARD_BASE_URL") {
		t.Fatalf("insecure production dashboard URL error = %v", err)
	}
}

func TestLoad_ProductionRejectsImplicitMidtransSandboxURL(t *testing.T) {
	for _, endpoint := range []string{
		"",
		"https://app.sandbox.midtrans.com",
		"https://APP.SANDBOX.MIDTRANS.COM/",
	} {
		t.Run(endpoint, func(t *testing.T) {
			baseLoadEnv(t)
			t.Setenv("APP_ENV", "production")
			t.Setenv("PAYMENT_PROVIDER", "midtrans")
			t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
			t.Setenv("MIDTRANS_BASE_URL", endpoint)
			t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
			t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
			t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
			t.Setenv("TRUSTED_PROXIES", "none")

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MIDTRANS_BASE_URL") {
				t.Fatalf("implicit production Midtrans URL error = %v", err)
			}
		})
	}
}

func TestLoad_ProductionRejectsInsecureMidtransBaseURL(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "http://midtrans.example.com")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MIDTRANS_BASE_URL") {
		t.Fatalf("insecure production Midtrans URL error = %v", err)
	}
}

func TestLoad_RejectsInvalidExpiryWorkerSettings(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("PAYMENT_EXPIRY_INTERVAL", "0s")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAYMENT_EXPIRY_INTERVAL") {
		t.Fatalf("zero expiry interval error = %v", err)
	}
	t.Setenv("PAYMENT_EXPIRY_INTERVAL", "30s")
	t.Setenv("PAYMENT_EXPIRY_BATCH_SIZE", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAYMENT_EXPIRY_BATCH_SIZE") {
		t.Fatalf("zero expiry batch error = %v", err)
	}
}

func TestParseCORSAllowedOrigins_ProductionRequiresHTTPS(t *testing.T) {
	if _, err := parseCORSAllowedOrigins("production", "http://dashboard.example.com"); err == nil {
		t.Fatal("HTTP production CORS origin was accepted")
	}
	got, err := parseCORSAllowedOrigins("production", "https://dashboard.example.com/")
	if err != nil || got != "https://dashboard.example.com" {
		t.Fatalf("HTTPS CORS config = %q, %v", got, err)
	}
	if _, err := parseCORSAllowedOrigins("production", "*"); err == nil {
		t.Fatal("wildcard CORS origin was accepted")
	}
	if _, err := parseCORSAllowedOrigins("production", "https://dashboard.example.com/app"); err == nil {
		t.Fatal("CORS origin with a path was accepted")
	}
}

func TestLoad_RejectsWhitespaceMidtransServerKey(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "   ")
	t.Setenv("MIDTRANS_BASE_URL", "https://api.provider.example")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MIDTRANS_SERVER_KEY") {
		t.Fatalf("whitespace Midtrans key error = %v", err)
	}
}

func TestParseAppEnv_NormalizesProductionAndRejectsUnknown(t *testing.T) {
	t.Setenv("APP_ENV", "  Production  ")
	got, err := parseAppEnv()
	if err != nil || got != "production" {
		t.Fatalf("parseAppEnv() = %q, %v; want production", got, err)
	}
	t.Setenv("APP_ENV", "prod")
	if _, err := parseAppEnv(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("unknown APP_ENV error = %v", err)
	}
}

func TestParseMockWebhookSecret_ProductionRejectsKnownDefault(t *testing.T) {
	t.Setenv("MOCK_WEBHOOK_SECRET", insecureMockWebhookSecret)
	if _, err := parseMockWebhookSecret("production"); err == nil || !strings.Contains(err.Error(), "MOCK_WEBHOOK_SECRET") {
		t.Fatalf("default mock secret error = %v, want production rejection", err)
	}
}

func TestParseMockWebhookSecret_ProductionRequiresStrongValue(t *testing.T) {
	t.Setenv("MOCK_WEBHOOK_SECRET", "short")
	if _, err := parseMockWebhookSecret("production"); err == nil {
		t.Fatal("short mock secret was accepted in production")
	}
}

func TestLoad_ProductionRequiresExplicitTrustedProxyPolicy(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
		t.Fatalf("unset production trusted proxy error = %v", err)
	}
}

func TestLoad_ProductionRejectsWebhookHTTPSDisabled(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("WEBHOOK_REQUIRE_HTTPS", "false")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "WEBHOOK_REQUIRE_HTTPS") {
		t.Fatalf("production HTTP webhook error = %v", err)
	}
}

func TestLoad_ProductionDefaultsToHTTPSWithExplicitProxyNone(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PAYMENT_PROVIDER", "midtrans")
	t.Setenv("MIDTRANS_SERVER_KEY", "production-midtrans-key-for-config-test")
	t.Setenv("MIDTRANS_BASE_URL", "https://sandbox.midtrans.example")
	t.Setenv("WEBHOOK_SECRET_ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("AUTH_JWT_SECRET", "production-jwt-secret-for-config-test")
	t.Setenv("MOCK_WEBHOOK_SECRET", "production-mock-secret-with-at-least-32-bytes")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("WEBHOOK_REQUIRE_HTTPS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("safe production config rejected: %v", err)
	}
	if !cfg.WebhookDelivery.RequireHTTPS {
		t.Fatal("production webhook HTTPS default = false, want true")
	}
	if cfg.App.TrustedProxies == nil || len(cfg.App.TrustedProxies) != 0 {
		t.Fatalf("trusted proxy config = %#v, want explicit empty/none policy", cfg.App.TrustedProxies)
	}
}
