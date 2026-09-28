package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	Email           EmailConfig
	EmailWorker     EmailWorkerConfig
	EmailCleanup    EmailCleanupConfig
	RateLimit       RateLimitConfig
	HTTP            HTTPConfig
}

// AuthConfig holds configuration for Phase 8 dashboard JWT authentication.
type AuthConfig struct {
	JWTSecret          []byte
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	InvitationTokenTTL time.Duration // Phase 8B: team invitation token validity
	CORSAllowedOrigins string
}

// AdminConfig protects Phase 7C ops endpoints (minimal shared-key auth).
// Not production-grade RBAC — documented limitation.
type AdminConfig struct {
	APIKey     string
	StaleAfter time.Duration // reclaim stuck RECONCILING settlements
}

// EmailConfig controls outbound transactional email over SMTP
// (Phase 8C.1 — email delivery foundation). Credentials are read only from
// the environment and never returned by any API. EMAIL_ENABLED=false keeps
// the SMTP path inert; queued messages are still drained by the Phase 8C.3B
// outbox worker (via the no-op sender).
type EmailConfig struct {
	Enabled  bool
	Host     string // SMTP server; required when enabled
	Port     int    // 587 (starttls) or 465 (implicit); default 587
	Username string // SMTP AUTH user; AUTH skipped when empty
	Password string
	From     string        // default sender ("Name <a@b.c>"); required when enabled
	Timeout  time.Duration // per-send budget; default 10s
	TLSMode  string        // starttls (default) | implicit | none (dev only)
}

// EmailWorkerConfig controls the Phase 8C.3B background outbox worker. Every
// knob mirrors the equivalent WebhookDeliveryConfig setting (same defaults,
// same semantics) — the email worker deliberately does not invent its own
// policy. The per-send SMTP budget is NOT here: it stays SMTP_TIMEOUT
// (EmailSender owns transport timeouts).
type EmailWorkerConfig struct {
	Enabled     bool          // EMAIL_WORKER_ENABLED (default true), mirrors WEBHOOK_DELIVERY_ENABLED
	Interval    time.Duration // poll interval, mirrors WEBHOOK_DELIVERY_INTERVAL (5s)
	BatchSize   int           // rows per claim, mirrors WEBHOOK_DELIVERY_BATCH_SIZE (20)
	MaxAttempts int           // dead-letter bound, mirrors WEBHOOK_MAX_ATTEMPTS (8)
	StaleAfter  time.Duration // reclaim stuck PROCESSING, mirrors WEBHOOK_DELIVERY_STALE_AFTER (2m)
}

type IdempotencyConfig struct {
	TTL time.Duration
}

// EmailCleanupConfig controls the Phase 8C.3C terminal-row retention
// cleanup worker. Defaults encode the APPROVED retention policy —
// SENT 7 days (168h), DEAD 30 days (720h) — and the worker is
// OPT-IN (EMAIL_CLEANUP_ENABLED, default false) so a fresh deployment
// never deletes rows until an operator turns it on. PENDING/PROCESSING
// are out of scope for cleanup entirely (EmailOutboxWorker owns them);
// there is deliberately no knob that widens cleanup beyond SENT/DEAD.
type EmailCleanupConfig struct {
	Enabled       bool          // EMAIL_CLEANUP_ENABLED (default false — opt-in)
	Interval      time.Duration // EMAIL_CLEANUP_INTERVAL (1h), must be > 0
	BatchSize     int           // EMAIL_CLEANUP_BATCH_SIZE (100) rows per DELETE, must be > 0
	SentRetention time.Duration // EMAIL_OUTBOX_SENT_RETENTION (168h = 7 days), must be > 0
	DeadRetention time.Duration // EMAIL_OUTBOX_DEAD_RETENTION (720h = 30 days), must be > 0
}

// RateLimitConfig controls the Phase 8D.1 in-process rate limiters (fixed
// window per minute). The limiters are deliberately in-process — no Redis or
// Kafka dependency — so a single-instance deployment is fully protected and a
// multi-instance deployment degrades to per-instance limits (documented
// limitation; a distributed limiter belongs to a later phase).
//
// Defaults are SENSITIVE (10/60/120 per minute) so an unconfigured
// production deployment is still rate limited. Development/test may explicitly
// set RATE_LIMIT_ENABLED=false; production rejects that setting. A zero or
// negative per-minute value is rejected at startup so a typo can never
// silently mean "unlimited".
type RateLimitConfig struct {
	Enabled        bool // RATE_LIMIT_ENABLED (default true)
	LoginPerMinute int  // RATE_LIMIT_LOGIN_PER_MINUTE (default 10) — per IP and per account
	AdminPerMinute int  // RATE_LIMIT_ADMIN_PER_MINUTE (default 60) — per admin client IP
	APIPerMinute   int  // RATE_LIMIT_API_PER_MINUTE (default 120) — per authenticated merchant (and per client IP pre-auth)
}

// HTTPConfig controls inbound HTTP server hardening (Phase 8D.2): the request
// body size limit and the http.Server timeout profile. The defaults preserve
// the previous Read/Write/Idle values and add the previously missing
// ReadHeaderTimeout (slow-header / slowloris protection). These are SERVER
// settings — the outbound webhook client timeout remains
// WEBHOOK_DELIVERY_TIMEOUT.
//
// Every value must be strictly positive when set: a zero or negative value is
// rejected at startup so a typo can never silently disable the protection.
type HTTPConfig struct {
	MaxBodyBytes      int64         // HTTP_MAX_BODY_BYTES (default 1048576 = 1 MiB)
	ReadHeaderTimeout time.Duration // HTTP_READ_HEADER_TIMEOUT (default 10s)
	ReadTimeout       time.Duration // HTTP_READ_TIMEOUT (default 15s)
	WriteTimeout      time.Duration // HTTP_WRITE_TIMEOUT (default 15s)
	IdleTimeout       time.Duration // HTTP_IDLE_TIMEOUT (default 60s)
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
	// FrontendPublicURL is the browser-facing origin for the mock payment
	// provider. It is deliberately separate from the backend APP_PORT/API URL.
	FrontendPublicURL string
	// MockPaymentBaseURL is retained as a deprecated compatibility alias for
	// configurations that used the old environment variable. New deployments
	// must use FRONTEND_PUBLIC_URL.
	MockPaymentBaseURL string
}

// AppConfig holds application-level configuration.
type AppConfig struct {
	Name string
	Env  string
	Port string
	// SimulatorEnabled controls the /api/v1/simulator routes. It defaults to
	// FALSE (fail closed): an operator must explicitly opt in via
	// PAYMENT_SIMULATOR_ENABLED=true, and Load rejects true when
	// APP_ENV=production so the simulator can never run in production.
	SimulatorEnabled bool
	// SimulatorMerchantID is the ONLY merchant allowed to operate the payment
	// simulator (startup-validated allowlist). The simulator is authenticated
	// (X-API-Key) and every read/write is additionally scoped to the
	// authenticated merchant context, never to a client-supplied merchant_id.
	// Required (and must be a valid UUID) when SimulatorEnabled is true.
	// Zero UUID when the simulator is disabled.
	SimulatorMerchantID uuid.UUID

	// LegacyAPICredentialsEnabled (LEGACY_API_CREDENTIALS_ENABLED, Phase 8D.3)
	// opens the legacy plaintext credential migration window. When false, the
	// Auth middleware's legacy stage rejects every bare "pk_<hex>" credential
	// with 401 LEGACY_CREDENTIALS_NOT_ENABLED before any database lookup.
	//
	// Defaults (when the variable is unset):
	//   development / test → true  (existing merchants keep working)
	//   production         → false (FAIL-SAFE: production defaults to the
	//                          closed state so a forgotten migration cannot
	//                          keep plaintext credentials live)
	//
	// An explicit value is honoured in every environment; an invalid value
	// fails startup. New-style Phase 5C compound credentials are unaffected
	// by this flag in either position.
	LegacyAPICredentialsEnabled bool

	// DashboardBaseURL is the public base URL of the dashboard SPA
	// (Phase 8C.2), e.g. "https://dashboard.example.com". Used to build the
	// invitation acceptance link {DASHBOARD_BASE_URL}/accept-invitation?token=…
	// Optional when email delivery is disabled; REQUIRED when
	// EMAIL_ENABLED=true (validated in parseDashboardBaseURL). Never
	// hardcoded — read only from the environment.
	DashboardBaseURL string

	// TrustedProxies (TRUSTED_PROXIES, Phase 8D.2) configures gin's trusted
	// reverse-proxy list. ClientIP() — and therefore Phase 8D.1 rate-limit
	// keying — derives from it:
	//   nil       → not configured: gin's permissive default (trust every
	//               source) remains for compatibility; cmd/server logs a
	//               warning when APP_ENV=production.
	//   empty     → the value "none": trust NO proxy, ClientIP uses the
	//               socket peer address.
	//   non-empty → explicit IP/CIDR list (invalid entries fail startup when
	//               applied via SetTrustedProxies).
	TrustedProxies []string
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
	// Set via MOCK_WEBHOOK_SECRET; production rejects the known development
	// fallback and requires at least 32 bytes.
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
	if err != nil || dbPort < 1 || dbPort > 65535 {
		return nil, fmt.Errorf("invalid DB_PORT: must be an integer from 1 to 65535")
	}
	appPort, err := strconv.Atoi(strings.TrimSpace(getEnv("APP_PORT", "8080")))
	if err != nil || appPort < 1 || appPort > 65535 {
		return nil, fmt.Errorf("invalid APP_PORT: must be an integer from 1 to 65535")
	}
	appEnv, err := parseAppEnv()
	if err != nil {
		return nil, err
	}
	dbSSLMode := strings.ToLower(strings.TrimSpace(getEnv("DB_SSLMODE", "disable")))
	if appEnv == "production" && dbSSLMode != "verify-full" {
		return nil, fmt.Errorf("DB_SSLMODE=verify-full is required in production")
	}
	adminAPIKey, err := parseAdminAPIKey(appEnv)
	if err != nil {
		return nil, err
	}

	expiryInterval, err := time.ParseDuration(getEnv("PAYMENT_EXPIRY_INTERVAL", "30s"))
	if err != nil || expiryInterval <= 0 {
		return nil, fmt.Errorf("invalid PAYMENT_EXPIRY_INTERVAL: must be a positive duration")
	}

	expiryBatch, err := strconv.Atoi(getEnv("PAYMENT_EXPIRY_BATCH_SIZE", "100"))
	if err != nil || expiryBatch <= 0 {
		return nil, fmt.Errorf("invalid PAYMENT_EXPIRY_BATCH_SIZE: must be a positive integer")
	}
	providerTimeout, err := time.ParseDuration(getEnv("PAYMENT_PROVIDER_TIMEOUT", "15s"))
	if err != nil || providerTimeout <= 0 {
		return nil, fmt.Errorf("invalid PAYMENT_PROVIDER_TIMEOUT")
	}
	providerName := strings.ToLower(strings.TrimSpace(getEnv("PAYMENT_PROVIDER", "mock")))
	if appEnv == "production" && providerName == "mock" {
		return nil, fmt.Errorf("PAYMENT_PROVIDER=mock is not allowed in production")
	}
	midtransServerKey := strings.TrimSpace(getEnv("MIDTRANS_SERVER_KEY", ""))
	if providerName == "midtrans" && midtransServerKey == "" {
		return nil, fmt.Errorf("MIDTRANS_SERVER_KEY is required when PAYMENT_PROVIDER=midtrans")
	}
	providerBaseURL := getEnv("MIDTRANS_BASE_URL", "https://app.sandbox.midtrans.com")
	frontendPublicURL, err := resolveFrontendPublicURL(providerName)
	if err != nil {
		return nil, err
	}
	if appEnv == "production" && strings.EqualFold(providerName, "midtrans") {
		if strings.TrimSpace(os.Getenv("MIDTRANS_BASE_URL")) == "" {
			return nil, fmt.Errorf("MIDTRANS_BASE_URL must be explicitly configured in production")
		}
		parsed, parseErr := url.ParseRequestURI(providerBaseURL)
		if parseErr != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("MIDTRANS_BASE_URL must be an absolute HTTPS URL without userinfo in production")
		}
		if strings.EqualFold(parsed.Hostname(), "app.sandbox.midtrans.com") {
			return nil, fmt.Errorf("MIDTRANS_BASE_URL must not use the Midtrans sandbox host in production")
		}
	}
	idempotencyTTL, err := time.ParseDuration(getEnv("IDEMPOTENCY_TTL", "24h"))
	if err != nil || idempotencyTTL <= 0 {
		return nil, fmt.Errorf("invalid IDEMPOTENCY_TTL")
	}

	mockWebhookSecret, err := parseMockWebhookSecret(appEnv)
	if err != nil {
		return nil, err
	}
	deliveryInterval, err := time.ParseDuration(getEnv("WEBHOOK_DELIVERY_INTERVAL", "5s"))
	if err != nil || deliveryInterval <= 0 {
		return nil, fmt.Errorf("invalid WEBHOOK_DELIVERY_INTERVAL: must be a positive duration")
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
		encKeyRaw = insecureDevWebhookEncryptionKeyHex
	}
	encKey, err := parseWebhookEncKey(encKeyRaw)
	if err != nil {
		return nil, fmt.Errorf("WEBHOOK_SECRET_ENCRYPTION_KEY: %w", err)
	}
	if appEnv == "production" && hex.EncodeToString(encKey) == insecureDevWebhookEncryptionKeyHex {
		return nil, fmt.Errorf("WEBHOOK_SECRET_ENCRYPTION_KEY must not use the development fallback in production")
	}

	// ── Phase 8: Dashboard JWT auth ──────────────────────────────────────────
	jwtSecretRaw := getEnv("AUTH_JWT_SECRET", "")
	if appEnv == "production" && jwtSecretRaw == "" {
		return nil, fmt.Errorf("AUTH_JWT_SECRET is required in production")
	}
	if appEnv == "production" && len(jwtSecretRaw) < 32 {
		return nil, fmt.Errorf("AUTH_JWT_SECRET must be at least 32 bytes in production")
	}
	if appEnv == "production" && jwtSecretRaw == insecureDevJWTSecret {
		return nil, fmt.Errorf("AUTH_JWT_SECRET must not use the development fallback in production")
	}
	var jwtSecret []byte
	if jwtSecretRaw == "" {
		if appEnv == "production" {
			return nil, fmt.Errorf("AUTH_JWT_SECRET is required in production")
		}
		// Development-only insecure default — NEVER use in production.
		slog.Warn("AUTH_JWT_SECRET is not set; using an insecure development default", slog.String("hint", "set AUTH_JWT_SECRET in production"))
		jwtSecret = []byte(insecureDevJWTSecret)
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
	// Phase 8B: invitation token validity — audit recommends 48–72h; default 48h.
	invitationTokenTTL, err := time.ParseDuration(getEnv("INVITATION_TOKEN_TTL", "48h"))
	if err != nil || invitationTokenTTL <= 0 {
		return nil, fmt.Errorf("invalid INVITATION_TOKEN_TTL")
	}
	corsAllowedOrigins, err := parseCORSAllowedOrigins(appEnv, getEnv("CORS_ALLOWED_ORIGINS", ""))
	if err != nil {
		return nil, err
	}

	// ── Phase 8C.1: email delivery foundation (SMTP) ────────────────────────
	emailCfg, err := parseEmailConfig(appEnv)
	if err != nil {
		return nil, err
	}

	// ── Phase 8C.2: dashboard base URL for invitation links ────────────────
	dashboardBaseURL, err := parseDashboardBaseURL(emailCfg.Enabled)
	if err != nil {
		return nil, err
	}
	if appEnv == "production" && dashboardBaseURL != "" {
		parsed, parseErr := url.ParseRequestURI(dashboardBaseURL)
		if parseErr != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("DASHBOARD_BASE_URL must use HTTPS without userinfo in production")
		}
	}

	// ── Phase 8C.3B: email outbox worker (WEBHOOK_DELIVERY_* mirror) ──────
	emailWorkerInterval, err := time.ParseDuration(getEnv("EMAIL_WORKER_INTERVAL", "5s"))
	if err != nil || emailWorkerInterval <= 0 {
		return nil, fmt.Errorf("invalid EMAIL_WORKER_INTERVAL")
	}
	emailWorkerBatch, err := strconv.Atoi(getEnv("EMAIL_WORKER_BATCH_SIZE", "20"))
	if err != nil || emailWorkerBatch <= 0 {
		return nil, fmt.Errorf("invalid EMAIL_WORKER_BATCH_SIZE")
	}
	emailWorkerMaxAttempts, err := strconv.Atoi(getEnv("EMAIL_WORKER_MAX_ATTEMPTS", "8"))
	if err != nil || emailWorkerMaxAttempts <= 0 {
		return nil, fmt.Errorf("invalid EMAIL_WORKER_MAX_ATTEMPTS")
	}
	emailWorkerStaleAfter, err := time.ParseDuration(getEnv("EMAIL_WORKER_STALE_AFTER", "2m"))
	if err != nil || emailWorkerStaleAfter <= 0 {
		return nil, fmt.Errorf("invalid EMAIL_WORKER_STALE_AFTER")
	}
	emailWorkerEnabled, err := getEnvBoolStrict("EMAIL_WORKER_ENABLED", true)
	if err != nil {
		return nil, err
	}
	emailWorkerCfg := EmailWorkerConfig{
		Enabled:     emailWorkerEnabled,
		Interval:    emailWorkerInterval,
		BatchSize:   emailWorkerBatch,
		MaxAttempts: emailWorkerMaxAttempts,
		StaleAfter:  emailWorkerStaleAfter,
	}

	// ── Phase 8C.3C: email outbox terminal retention cleanup ──────────────
	emailCleanupCfg, err := parseEmailCleanupConfig()
	if err != nil {
		return nil, err
	}

	// ── Phase 8D.1: simulator containment ──────────────────────────────────
	// Fail closed: the simulator is OFF unless explicitly enabled, and it can
	// never be enabled in production (startup validation, not runtime check).
	simulatorEnabled, err := getEnvBoolStrict("PAYMENT_SIMULATOR_ENABLED", false)
	if err != nil {
		return nil, err
	}
	if appEnv == "production" && simulatorEnabled {
		return nil, fmt.Errorf("PAYMENT_SIMULATOR_ENABLED=true is not allowed in production (the payment simulator must never be enabled when APP_ENV=production)")
	}
	simulatorMerchantID, err := parseSimulatorMerchantID(simulatorEnabled)
	if err != nil {
		return nil, err
	}

	// ── Phase 8D.3: legacy plaintext credential migration window ──────────
	// Fail safe in production: an unset variable keeps legacy credentials
	// OFF when APP_ENV=production, and a typo can never slip through.
	legacyAPICredentialsEnabled, err := parseLegacyCredentialsConfig(appEnv)
	if err != nil {
		return nil, err
	}

	// ── Phase 8D.1: in-process rate limiting ───────────────────────────────
	rateLimitCfg, err := parseRateLimitConfig()
	if err != nil {
		return nil, err
	}
	if appEnv == "production" && !rateLimitCfg.Enabled {
		return nil, fmt.Errorf("RATE_LIMIT_ENABLED=false is not allowed in production")
	}

	// ── Phase 8D.2: inbound HTTP hardening (body limit + server timeouts) ──
	httpCfg, err := parseHTTPConfig()
	if err != nil {
		return nil, err
	}

	// ── Phase 8D.2: trusted reverse proxies (ClientIP / X-Forwarded-For) ──
	trustedProxies, err := parseTrustedProxies()
	if err != nil {
		return nil, err
	}
	if appEnv == "production" && trustedProxies == nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES must be explicitly set in production (use none or a trusted IP/CIDR list)")
	}
	requireHTTPS, err := getEnvBoolStrict("WEBHOOK_REQUIRE_HTTPS", appEnv == "production")
	if err != nil {
		return nil, err
	}
	if appEnv == "production" && !requireHTTPS {
		return nil, fmt.Errorf("WEBHOOK_REQUIRE_HTTPS=false is not allowed in production")
	}
	dbHost, err := requiredEnv("DB_HOST")
	if err != nil {
		return nil, err
	}
	dbUser, err := requiredEnv("DB_USER")
	if err != nil {
		return nil, err
	}
	dbPassword, err := requiredEnv("DB_PASSWORD")
	if err != nil {
		return nil, err
	}
	dbName, err := requiredEnv("DB_NAME")
	if err != nil {
		return nil, err
	}
	expiryEnabled, err := getEnvBoolStrict("PAYMENT_EXPIRY_ENABLED", true)
	if err != nil {
		return nil, err
	}
	deliveryEnabled, err := getEnvBoolStrict("WEBHOOK_DELIVERY_ENABLED", true)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		App: AppConfig{
			Name:                        getEnv("APP_NAME", "payment-gateway"),
			Env:                         appEnv,
			Port:                        strconv.Itoa(appPort),
			SimulatorEnabled:            simulatorEnabled,
			SimulatorMerchantID:         simulatorMerchantID,
			LegacyAPICredentialsEnabled: legacyAPICredentialsEnabled,
			DashboardBaseURL:            dashboardBaseURL,
			TrustedProxies:              trustedProxies,
		},
		Database: DatabaseConfig{
			Host:     dbHost,
			Port:     dbPort,
			User:     dbUser,
			Password: dbPassword,
			Name:     dbName,
			SSLMode:  dbSSLMode,
		},
		Webhook: WebhookConfig{
			MockSecret: mockWebhookSecret,
		},
		Expiry: ExpiryConfig{
			Enabled:   expiryEnabled,
			Interval:  expiryInterval,
			BatchSize: expiryBatch,
		},
		Provider: ProviderConfig{
			Name:               providerName,
			BaseURL:            providerBaseURL,
			ServerKey:          midtransServerKey,
			ClientKey:          getEnv("MIDTRANS_CLIENT_KEY", ""),
			Timeout:            providerTimeout,
			FrontendPublicURL:  frontendPublicURL,
			MockPaymentBaseURL: frontendPublicURL, // deprecated compatibility alias
		},
		Idempotency: IdempotencyConfig{TTL: idempotencyTTL},
		WebhookDelivery: WebhookDeliveryConfig{
			Enabled:       deliveryEnabled,
			Interval:      deliveryInterval,
			BatchSize:     deliveryBatch,
			Timeout:       deliveryTimeout,
			MaxAttempts:   deliveryMaxAttempts,
			StaleAfter:    staleAfter,
			EncryptionKey: encKey,
			RequireHTTPS:  requireHTTPS,
		},
		Admin: AdminConfig{
			APIKey:     adminAPIKey,
			StaleAfter: reconStaleAfter,
		},
		Auth: AuthConfig{
			JWTSecret:          jwtSecret,
			AccessTokenTTL:     accessTokenTTL,
			RefreshTokenTTL:    refreshTokenTTL,
			InvitationTokenTTL: invitationTokenTTL,
			CORSAllowedOrigins: corsAllowedOrigins,
		},
		Email:        emailCfg,
		EmailWorker:  emailWorkerCfg,
		EmailCleanup: emailCleanupCfg,
		RateLimit:    rateLimitCfg,
		HTTP:         httpCfg,
	}

	return cfg, nil
}

// resolveFrontendPublicURL resolves the browser-facing origin used to construct
// mock PaymentURLs. FRONTEND_PUBLIC_URL is the canonical setting and is
// intentionally independent from APP_PORT, which is only the process listen
// port. MOCK_PAYMENT_BASE_URL remains a read-only compatibility alias for
// older deployments; new configuration should always set FRONTEND_PUBLIC_URL.
func resolveFrontendPublicURL(providerName string) (string, error) {
	raw := strings.TrimSpace(os.Getenv("FRONTEND_PUBLIC_URL"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("MOCK_PAYMENT_BASE_URL"))
	}
	if raw == "" {
		if providerName == "mock" {
			return "", fmt.Errorf("FRONTEND_PUBLIC_URL is required when PAYMENT_PROVIDER=mock")
		}
		return "", nil
	}

	raw = strings.TrimRight(raw, "/")
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("FRONTEND_PUBLIC_URL must be an absolute URL without userinfo, query, or fragment")
	}
	return raw, nil
}

// parseEmailCleanupConfig resolves the Phase 8C.3C retention-cleanup
// settings. Extracted as its own function (parseEmailConfig style) so the
// validation contract is unit-testable without booting the whole Config.
//
// Every knob must be strictly positive. A ZERO OR NEGATIVE RETENTION IS
// REJECTED AT STARTUP — it must never silently mean "delete everything
// immediately"; the same guard applies to interval and batch size so a typo
// cannot turn cleanup into an unbounded hot loop.
func parseEmailCleanupConfig() (EmailCleanupConfig, error) {
	interval, err := time.ParseDuration(getEnv("EMAIL_CLEANUP_INTERVAL", "1h"))
	if err != nil || interval <= 0 {
		return EmailCleanupConfig{}, fmt.Errorf("invalid EMAIL_CLEANUP_INTERVAL")
	}
	batch, err := strconv.Atoi(getEnv("EMAIL_CLEANUP_BATCH_SIZE", "100"))
	if err != nil || batch <= 0 {
		return EmailCleanupConfig{}, fmt.Errorf("invalid EMAIL_CLEANUP_BATCH_SIZE")
	}
	sentRetention, err := time.ParseDuration(getEnv("EMAIL_OUTBOX_SENT_RETENTION", "168h"))
	if err != nil || sentRetention <= 0 {
		return EmailCleanupConfig{}, fmt.Errorf("invalid EMAIL_OUTBOX_SENT_RETENTION")
	}
	deadRetention, err := time.ParseDuration(getEnv("EMAIL_OUTBOX_DEAD_RETENTION", "720h"))
	if err != nil || deadRetention <= 0 {
		return EmailCleanupConfig{}, fmt.Errorf("invalid EMAIL_OUTBOX_DEAD_RETENTION")
	}
	enabled, err := getEnvBoolStrict("EMAIL_CLEANUP_ENABLED", false)
	if err != nil {
		return EmailCleanupConfig{}, err
	}
	return EmailCleanupConfig{
		Enabled:       enabled,
		Interval:      interval,
		BatchSize:     batch,
		SentRetention: sentRetention,
		DeadRetention: deadRetention,
	}, nil
}

// parseEmailConfig resolves the Phase 8C.1 email/SMTP settings.
// Cross-field validation follows the PAYMENT_PROVIDER=midtrans precedent:
// the variables are only mandatory when the feature is enabled, so a
// deployment without email runs fine with EMAIL_ENABLED=false (the default).
func parseEmailConfig(appEnv string) (EmailConfig, error) {
	enabled, err := getEnvBoolStrict("EMAIL_ENABLED", false)
	if err != nil {
		return EmailConfig{}, err
	}
	cfg := EmailConfig{
		Enabled:  enabled,
		Host:     strings.TrimSpace(getEnv("SMTP_HOST", "")),
		Username: getEnv("SMTP_USERNAME", ""),
		Password: getEnv("SMTP_PASSWORD", ""),
		From:     strings.TrimSpace(getEnv("SMTP_FROM", "")),
		TLSMode:  strings.ToLower(getEnv("SMTP_TLS", "starttls")),
	}

	port, err := strconv.Atoi(getEnv("SMTP_PORT", "587"))
	if err != nil || port < 1 || port > 65535 {
		return EmailConfig{}, fmt.Errorf("invalid SMTP_PORT")
	}
	cfg.Port = port

	timeout, err := time.ParseDuration(getEnv("SMTP_TIMEOUT", "10s"))
	if err != nil || timeout <= 0 {
		return EmailConfig{}, fmt.Errorf("invalid SMTP_TIMEOUT")
	}
	cfg.Timeout = timeout

	switch cfg.TLSMode {
	case "starttls", "implicit", "none":
	default:
		return EmailConfig{}, fmt.Errorf("invalid SMTP_TLS: must be starttls, implicit or none")
	}

	if cfg.Enabled {
		if cfg.Host == "" {
			return EmailConfig{}, fmt.Errorf("SMTP_HOST is required when EMAIL_ENABLED=true")
		}
		if cfg.From == "" {
			return EmailConfig{}, fmt.Errorf("SMTP_FROM is required when EMAIL_ENABLED=true")
		}
		// Plaintext submission is never allowed in production.
		if appEnv == "production" && cfg.TLSMode == "none" {
			return EmailConfig{}, fmt.Errorf("SMTP_TLS=none is not allowed in production")
		}
	}

	return cfg, nil
}

func parseAppEnv() (string, error) {
	raw := os.Getenv("APP_ENV")
	if raw == "" {
		return "development", nil
	}
	env := strings.ToLower(strings.TrimSpace(raw))
	switch env {
	case "development", "test", "production":
		return env, nil
	default:
		return "", fmt.Errorf("invalid APP_ENV: must be development, test, or production")
	}
}

func parseAdminAPIKey(appEnv string) (string, error) {
	key := strings.TrimSpace(getEnv("ADMIN_API_KEY", ""))
	if appEnv == "production" && key != "" && len(key) < 32 {
		return "", fmt.Errorf("ADMIN_API_KEY must be at least 32 bytes in production")
	}
	return key, nil
}

const insecureMockWebhookSecret = "change-me-in-production"
const insecureDevJWTSecret = "dev-only-insecure-jwt-secret-change-me!"
const insecureDevWebhookEncryptionKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// parseMockWebhookSecret prevents the development placeholder from becoming a
// production HMAC key. The mock parser is registered for inbound provider
// compatibility, so an unset/known default would otherwise allow forged mock
// webhook signatures in production.
func parseMockWebhookSecret(appEnv string) (string, error) {
	secret := strings.TrimSpace(getEnv("MOCK_WEBHOOK_SECRET", insecureMockWebhookSecret))
	if appEnv == "production" {
		if secret == "" || secret == insecureMockWebhookSecret || len(secret) < 32 {
			return "", fmt.Errorf("MOCK_WEBHOOK_SECRET must be a non-default secret of at least 32 bytes in production")
		}
	}
	return secret, nil
}

func parseCORSAllowedOrigins(appEnv, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ",")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimRight(strings.TrimSpace(part), "/")
		if origin == "" || origin == "*" {
			return "", fmt.Errorf("CORS_ALLOWED_ORIGINS contains an empty or wildcard origin")
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
			return "", fmt.Errorf("CORS_ALLOWED_ORIGINS contains an invalid origin")
		}
		if appEnv == "production" && u.Scheme != "https" {
			return "", fmt.Errorf("CORS_ALLOWED_ORIGINS must use HTTPS in production")
		}
		normalized = append(normalized, origin)
	}
	return strings.Join(normalized, ","), nil
}

// parseDashboardBaseURL resolves DASHBOARD_BASE_URL — the public dashboard
// SPA origin used to build invitation acceptance links (Phase 8C.2).
//
// Cross-field validation follows the Phase 8C.1 style: the variable is only
// mandatory when email delivery is enabled (a disabled deployment never needs
// to construct an invitation link outside the API response). When set it must
// be an absolute http(s) origin without query/fragment; a trailing slash is
// normalised away so the service can append /accept-invitation safely.
func parseDashboardBaseURL(emailEnabled bool) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(getEnv("DASHBOARD_BASE_URL", "")), "/")
	if raw == "" {
		if emailEnabled {
			return "", fmt.Errorf("DASHBOARD_BASE_URL is required when EMAIL_ENABLED=true")
		}
		return "", nil // optional while email delivery is disabled
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid DASHBOARD_BASE_URL: must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid DASHBOARD_BASE_URL: scheme must be http or https")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid DASHBOARD_BASE_URL: must not contain a query or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("invalid DASHBOARD_BASE_URL: must be an origin without a path")
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid DASHBOARD_BASE_URL: must not contain userinfo")
	}
	return raw, nil
}

// parseRateLimitConfig resolves the Phase 8D.1 in-process rate-limit settings.
// Extracted in the parseEmailCleanupConfig style so the validation contract is
// unit-testable without booting the whole Config.
//
// Defaults (10/60/120) keep every surface limited even when the variables are
// omitted. Every limit must be strictly positive when enabled — a zero or
// negative value is rejected at startup so a typo can never silently mean
// "unlimited"; the only supported off switch is RATE_LIMIT_ENABLED=false.
func parseRateLimitConfig() (RateLimitConfig, error) {
	enabled, err := getEnvBoolStrict("RATE_LIMIT_ENABLED", true)
	if err != nil {
		return RateLimitConfig{}, err
	}
	cfg := RateLimitConfig{Enabled: enabled}
	loginPerMinute, err := strconv.Atoi(getEnv("RATE_LIMIT_LOGIN_PER_MINUTE", "10"))
	if err != nil || loginPerMinute <= 0 {
		return RateLimitConfig{}, fmt.Errorf("invalid RATE_LIMIT_LOGIN_PER_MINUTE: must be a positive integer (set RATE_LIMIT_ENABLED=false to disable rate limiting)")
	}
	adminPerMinute, err := strconv.Atoi(getEnv("RATE_LIMIT_ADMIN_PER_MINUTE", "60"))
	if err != nil || adminPerMinute <= 0 {
		return RateLimitConfig{}, fmt.Errorf("invalid RATE_LIMIT_ADMIN_PER_MINUTE: must be a positive integer (set RATE_LIMIT_ENABLED=false to disable rate limiting)")
	}
	apiPerMinute, err := strconv.Atoi(getEnv("RATE_LIMIT_API_PER_MINUTE", "120"))
	if err != nil || apiPerMinute <= 0 {
		return RateLimitConfig{}, fmt.Errorf("invalid RATE_LIMIT_API_PER_MINUTE: must be a positive integer (set RATE_LIMIT_ENABLED=false to disable rate limiting)")
	}
	cfg.LoginPerMinute = loginPerMinute
	cfg.AdminPerMinute = adminPerMinute
	cfg.APIPerMinute = apiPerMinute
	return cfg, nil
}

// parseHTTPConfig resolves the Phase 8D.2 inbound HTTP hardening settings
// (request body limit + http.Server timeouts). Extracted in the
// parseRateLimitConfig style so the validation contract is unit-testable
// without booting the whole Config.
//
// Defaults keep every surface protected when the variables are omitted
// (1 MiB body cap; Read/Write/Idle preserve the previous 15s/15s/60s values;
// ReadHeaderTimeout is the new 10s slow-header bound). Every value must be
// strictly positive — a typo can never silently disable the protection.
func parseHTTPConfig() (HTTPConfig, error) {
	maxBody, err := strconv.ParseInt(getEnv("HTTP_MAX_BODY_BYTES", "1048576"), 10, 64)
	if err != nil || maxBody < 1 {
		return HTTPConfig{}, fmt.Errorf("invalid HTTP_MAX_BODY_BYTES: must be a positive integer")
	}
	readHeader, err := time.ParseDuration(getEnv("HTTP_READ_HEADER_TIMEOUT", "10s"))
	if err != nil || readHeader <= 0 {
		return HTTPConfig{}, fmt.Errorf("invalid HTTP_READ_HEADER_TIMEOUT: must be a positive duration")
	}
	readTimeout, err := time.ParseDuration(getEnv("HTTP_READ_TIMEOUT", "15s"))
	if err != nil || readTimeout <= 0 {
		return HTTPConfig{}, fmt.Errorf("invalid HTTP_READ_TIMEOUT: must be a positive duration")
	}
	writeTimeout, err := time.ParseDuration(getEnv("HTTP_WRITE_TIMEOUT", "15s"))
	if err != nil || writeTimeout <= 0 {
		return HTTPConfig{}, fmt.Errorf("invalid HTTP_WRITE_TIMEOUT: must be a positive duration")
	}
	idleTimeout, err := time.ParseDuration(getEnv("HTTP_IDLE_TIMEOUT", "60s"))
	if err != nil || idleTimeout <= 0 {
		return HTTPConfig{}, fmt.Errorf("invalid HTTP_IDLE_TIMEOUT: must be a positive duration")
	}
	return HTTPConfig{
		MaxBodyBytes:      maxBody,
		ReadHeaderTimeout: readHeader,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}, nil
}

// parseTrustedProxies resolves TRUSTED_PROXIES (Phase 8D.2) — the explicit
// trust policy for X-Forwarded-For (ClientIP) so security-sensitive logic
// (rate-limit keying) never silently trusts spoofable headers:
//
//	unset/empty → nil: NOT configured. gin's permissive default (trust every
//	              source) is preserved for compatibility and cmd/server logs a
//	              production warning.
//	"none"      → empty list: trust NO proxy; ClientIP uses the socket peer.
//	otherwise   → comma-separated IP/CIDR entries. Syntax errors are caught
//	              here; invalid IP/CIDR entries fail startup when gin applies
//	              the list via SetTrustedProxies (fail fast).
func parseTrustedProxies() ([]string, error) {
	raw := strings.TrimSpace(getEnv("TRUSTED_PROXIES", ""))
	if raw == "" {
		return nil, nil
	}
	if strings.EqualFold(raw, "none") {
		return []string{}, nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("invalid TRUSTED_PROXIES: expected a comma-separated IP/CIDR list or \"none\"")
	}
	return out, nil
}

// parseSimulatorMerchantID resolves SIMULATOR_MERCHANT_ID.
// When the simulator is enabled the variable is required and must be a UUID.
// When disabled, an empty value is allowed (returns uuid.Nil).
func parseSimulatorMerchantID(simulatorEnabled bool) (uuid.UUID, error) {
	raw := strings.TrimSpace(os.Getenv("SIMULATOR_MERCHANT_ID"))
	if !simulatorEnabled {
		if raw == "" {
			return uuid.Nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid SIMULATOR_MERCHANT_ID: must be a valid UUID")
		}
		return id, nil
	}
	if raw == "" {
		return uuid.Nil, fmt.Errorf("SIMULATOR_MERCHANT_ID is required when PAYMENT_SIMULATOR_ENABLED=true")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid SIMULATOR_MERCHANT_ID: must be a valid UUID")
	}
	return id, nil
}

// parseLegacyCredentialsConfig resolves LEGACY_API_CREDENTIALS_ENABLED
// (Phase 8D.3).
//
// Contract:
//   - unset  → development/test: true (migration window open);
//     production and any other env: false (FAIL-SAFE closed default).
//   - set    → parsed strictly with strconv.ParseBool; anything that is not a
//     boolean fails startup (unlike getEnvBool, a typo can never silently
//     fall back to a default).
//
// An explicit value is honoured even in production: closing the window is the
// default, reopening it must be a deliberate operator action during a
// migration.
func parseLegacyCredentialsConfig(appEnv string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv("LEGACY_API_CREDENTIALS_ENABLED"))
	if raw == "" {
		switch appEnv {
		case "development", "test":
			return true, nil
		default:
			return false, nil
		}
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid LEGACY_API_CREDENTIALS_ENABLED: must be true or false")
	}
	return b, nil
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

// getEnvBoolStrict returns a boolean setting or a named configuration error.
// Operational flags must never silently fall back after a typo.
func getEnvBoolStrict(key string, fallback bool) (bool, error) {
	val := os.Getenv(key)
	if val == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, nil
}

// requiredEnv returns a required environment value as a normal configuration
// error. Startup code must never panic on a missing deployment variable.
func requiredEnv(key string) (string, error) {
	val := os.Getenv(key)
	if val == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return val, nil
}
