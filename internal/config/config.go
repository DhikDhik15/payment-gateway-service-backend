package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	App             AppConfig
	Database        DatabaseConfig
	Webhook         WebhookConfig
	Expiry          ExpiryConfig
	Provider        ProviderConfig
	Idempotency     IdempotencyConfig
	WebhookDelivery WebhookDeliveryConfig
	Admin           AdminConfig
	Auth            AuthConfig
}

// AuthConfig holds configuration for Phase 8 dashboard JWT authentication.
type AuthConfig struct {
	JWTSecret          []byte
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	CORSAllowedOrigins string
}

// AdminConfig protects Phase 7C ops endpoints (minimal shared-key auth).
// Not production-grade RBAC — documented limitation.
type AdminConfig struct {
	APIKey     string
	StaleAfter time.Duration // reclaim stuck RECONCILING settlements
}

type IdempotencyConfig struct {
	TTL time.Duration
}

// WebhookDeliveryConfig controls outbound merchant webhook delivery (Phase 6).
type WebhookDeliveryConfig struct {
	Enabled       bool
	Interval      time.Duration
	BatchSize     int
	Timeout       time.Duration
	MaxAttempts   int
	StaleAfter    time.Duration
	EncryptionKey []byte
	RequireHTTPS  bool
}

// ProviderConfig selects the payment adapter. Credentials are intentionally
// read only from the environment and never returned by any API.
type ProviderConfig struct {
	Name      string
	BaseURL   string
	ServerKey string
	ClientKey string
	Timeout   time.Duration
}

// AppConfig holds application-level configuration.
type AppConfig struct {
	Name             string
	Env              string
	Port             string
	SimulatorEnabled bool
}

// DatabaseConfig holds PostgreSQL connection configuration.
type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

// WebhookConfig holds per-provider webhook secrets.
type WebhookConfig struct {
	// MockSecret is the HMAC-SHA256 key for the MOCK provider webhook.
	// Set via MOCK_WEBHOOK_SECRET environment variable.
	MockSecret string
}

// ExpiryConfig controls the payment expiry background worker.
type ExpiryConfig struct {
	// Enabled controls whether the expiry worker starts.
	Enabled bool

	// Interval is how often the worker checks for expired transactions.
	Interval time.Duration

	// BatchSize is the maximum number of transactions processed per run.
	BatchSize int
}

// DSN returns a PostgreSQL connection string.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// Load reads the .env file (if present) and returns a Config populated from
// environment variables. Required variables missing from the environment will
// cause Load to return an error.
func Load() (*Config, error) {
	// Load .env file — ignore error when running inside Docker where env vars
	// are injected directly.
	_ = godotenv.Load()

	dbPort, err := strconv.Atoi(getEnv("DB_PORT", "5432"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_PORT value: %w", err)
	}

	expiryInterval, err := time.ParseDuration(getEnv("PAYMENT_EXPIRY_INTERVAL", "30s"))
	if err != nil {
		return nil, fmt.Errorf("invalid PAYMENT_EXPIRY_INTERVAL value: %w", err)
	}

	expiryBatch, err := strconv.Atoi(getEnv("PAYMENT_EXPIRY_BATCH_SIZE", "100"))
	if err != nil {
		return nil, fmt.Errorf("invalid PAYMENT_EXPIRY_BATCH_SIZE value: %w", err)
	}
	providerTimeout, err := time.ParseDuration(getEnv("PAYMENT_PROVIDER_TIMEOUT", "15s"))
	if err != nil || providerTimeout <= 0 {
		return nil, fmt.Errorf("invalid PAYMENT_PROVIDER_TIMEOUT")
	}
	providerName := getEnv("PAYMENT_PROVIDER", "mock")
	if providerName == "midtrans" && getEnv("MIDTRANS_SERVER_KEY", "") == "" {
		return nil, fmt.Errorf("MIDTRANS_SERVER_KEY is required when PAYMENT_PROVIDER=midtrans")
	}
	idempotencyTTL, err := time.ParseDuration(getEnv("IDEMPOTENCY_TTL", "24h"))
	if err != nil || idempotencyTTL <= 0 {
		return nil, fmt.Errorf("invalid IDEMPOTENCY_TTL")
	}

	appEnv := getEnv("APP_ENV", "development")
	deliveryInterval, err := time.ParseDuration(getEnv("WEBHOOK_DELIVERY_INTERVAL", "5s"))
	if err != nil {
		return nil, fmt.Errorf("invalid WEBHOOK_DELIVERY_INTERVAL: %w", err)
	}
	deliveryTimeout, err := time.ParseDuration(getEnv("WEBHOOK_DELIVERY_TIMEOUT", "10s"))
	if err != nil || deliveryTimeout <= 0 {
		return nil, fmt.Errorf("invalid WEBHOOK_DELIVERY_TIMEOUT")
	}
	deliveryBatch, err := strconv.Atoi(getEnv("WEBHOOK_DELIVERY_BATCH_SIZE", "20"))
	if err != nil || deliveryBatch <= 0 {
		return nil, fmt.Errorf("invalid WEBHOOK_DELIVERY_BATCH_SIZE")
	}
	deliveryMaxAttempts, err := strconv.Atoi(getEnv("WEBHOOK_MAX_ATTEMPTS", "8"))
	if err != nil || deliveryMaxAttempts <= 0 {
		return nil, fmt.Errorf("invalid WEBHOOK_MAX_ATTEMPTS")
	}
	staleAfter, err := time.ParseDuration(getEnv("WEBHOOK_DELIVERY_STALE_AFTER", "2m"))
	if err != nil || staleAfter <= 0 {
		return nil, fmt.Errorf("invalid WEBHOOK_DELIVERY_STALE_AFTER")
	}
	reconStaleAfter, err := time.ParseDuration(getEnv("SETTLEMENT_RECON_STALE_AFTER", "2m"))
	if err != nil || reconStaleAfter <= 0 {
		return nil, fmt.Errorf("invalid SETTLEMENT_RECON_STALE_AFTER")
	}

	encKeyRaw := getEnv("WEBHOOK_SECRET_ENCRYPTION_KEY", "")
	if encKeyRaw == "" {
		if appEnv == "production" {
			return nil, fmt.Errorf("WEBHOOK_SECRET_ENCRYPTION_KEY is required in production")
		}
		// Development-only default (32 zero-ish bytes as hex). NEVER use in production.
		encKeyRaw = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	}
	encKey, err := parseWebhookEncKey(encKeyRaw)
	if err != nil {
		return nil, fmt.Errorf("WEBHOOK_SECRET_ENCRYPTION_KEY: %w", err)
	}

	// ── Phase 8: Dashboard JWT auth ──────────────────────────────────────────
	jwtSecretRaw := getEnv("AUTH_JWT_SECRET", "")
	var jwtSecret []byte
	if jwtSecretRaw == "" {
		if appEnv == "production" {
			return nil, fmt.Errorf("AUTH_JWT_SECRET is required in production")
		}
		// Development-only insecure default — NEVER use in production.
		fmt.Println("[WARN] AUTH_JWT_SECRET is not set — using an insecure dev default. Set AUTH_JWT_SECRET in production.")
		jwtSecret = []byte("dev-only-insecure-jwt-secret-change-me!")
	} else {
		jwtSecret = []byte(jwtSecretRaw)
	}

	accessTokenTTL, err := time.ParseDuration(getEnv("AUTH_ACCESS_TOKEN_TTL", "15m"))
	if err != nil || accessTokenTTL <= 0 {
		return nil, fmt.Errorf("invalid AUTH_ACCESS_TOKEN_TTL")
	}
	refreshTokenTTL, err := time.ParseDuration(getEnv("AUTH_REFRESH_TOKEN_TTL", "168h"))
	if err != nil || refreshTokenTTL <= 0 {
		return nil, fmt.Errorf("invalid AUTH_REFRESH_TOKEN_TTL")
	}
	corsAllowedOrigins := getEnv("CORS_ALLOWED_ORIGINS", "")

	cfg := &Config{
		App: AppConfig{
			Name:             getEnv("APP_NAME", "payment-gateway"),
			Env:              appEnv,
			Port:             getEnv("APP_PORT", "8080"),
			SimulatorEnabled: getEnvBool("PAYMENT_SIMULATOR_ENABLED", true),
		},
		Database: DatabaseConfig{
			Host:     mustGetEnv("DB_HOST"),
			Port:     dbPort,
			User:     mustGetEnv("DB_USER"),
			Password: mustGetEnv("DB_PASSWORD"),
			Name:     mustGetEnv("DB_NAME"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		Webhook: WebhookConfig{
			MockSecret: getEnv("MOCK_WEBHOOK_SECRET", "change-me-in-production"),
		},
		Expiry: ExpiryConfig{
			Enabled:   getEnvBool("PAYMENT_EXPIRY_ENABLED", true),
			Interval:  expiryInterval,
			BatchSize: expiryBatch,
		},
		Provider:    ProviderConfig{Name: providerName, BaseURL: getEnv("MIDTRANS_BASE_URL", "https://app.sandbox.midtrans.com"), ServerKey: getEnv("MIDTRANS_SERVER_KEY", ""), ClientKey: getEnv("MIDTRANS_CLIENT_KEY", ""), Timeout: providerTimeout},
		Idempotency: IdempotencyConfig{TTL: idempotencyTTL},
		WebhookDelivery: WebhookDeliveryConfig{
			Enabled:       getEnvBool("WEBHOOK_DELIVERY_ENABLED", true),
			Interval:      deliveryInterval,
			BatchSize:     deliveryBatch,
			Timeout:       deliveryTimeout,
			MaxAttempts:   deliveryMaxAttempts,
			StaleAfter:    staleAfter,
			EncryptionKey: encKey,
			RequireHTTPS:  getEnvBool("WEBHOOK_REQUIRE_HTTPS", appEnv == "production"),
		},
		Admin: AdminConfig{
			APIKey:     getEnv("ADMIN_API_KEY", ""),
			StaleAfter: reconStaleAfter,
		},
		Auth: AuthConfig{
			JWTSecret:          jwtSecret,
			AccessTokenTTL:     accessTokenTTL,
			RefreshTokenTTL:    refreshTokenTTL,
			CORSAllowedOrigins: corsAllowedOrigins,
		},
	}

	return cfg, nil
}

func parseWebhookEncKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 64 {
		key, err := hex.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("must be 64 hex characters or 32-byte base64")
		}
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("must be 64 hex characters or 32-byte base64")
	}
	return key, nil
}

// getEnv returns the value of the environment variable named by key, or
// fallback if the variable is not set.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// getEnvBool returns the boolean value of the environment variable named by
// key, or fallback if the variable is not set or cannot be parsed.
func getEnvBool(key string, fallback bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return fallback
	}
	return b
}

// mustGetEnv returns the value of the environment variable named by key.
// It panics if the variable is not set or is empty.
func mustGetEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return val
}
