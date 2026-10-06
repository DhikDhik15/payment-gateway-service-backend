package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/config"
	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/ratelimit"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/database"
	_ "github.com/dhikaarta/pay-gate-backend/swagger" // generated swagger docs
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @title						Payment Gateway API
// @version					9.0
// @description				Payment Gateway Backend API — Phase 9. Supports merchant registration, API key authentication, payment lifecycle, inbound provider webhooks, payment expiry worker, idempotent payment creation, paginated listing, outbound merchant webhook delivery with HMAC signing, retries, and transactional outbox. Phase 7B adds partial/full/multiple refund support with over-refund protection. Phase 7C adds settlement import and reconciliation. Phase 8 adds dashboard authentication and user management with JWT + HttpOnly refresh token session design. Phase 9 adds merchant-scoped dashboard APIs (overview, payments, refunds, webhooks, API keys, reconciliation, settings) authenticated via Bearer JWT — never Merchant API Keys.
// @termsOfService				http://swagger.io/terms/
//
// @contact.name				API Support
// @contact.email				support@example.com
//
// @license.name				MIT
//
// @host						localhost:8081
// @BasePath					/
//
// @securityDefinitions.apikey	ApiKeyAuth
// @in							header
// @name						X-API-Key
// @description				API key issued to the merchant at registration.
//
// @securityDefinitions.apikey	BearerAuth
// @in							header
// @name						Authorization
// @description				JWT access token for dashboard authentication. Format: Bearer <token>
//
// @securityDefinitions.apikey	AdminKeyAuth
// @in							header
// @name						X-Admin-Key
// @description				Admin API key for ops and bootstrap endpoints.
func main() {
	// -------------------------------------------------------------------------
	// Logger
	// -------------------------------------------------------------------------
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// -------------------------------------------------------------------------
	// Configuration
	// -------------------------------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	slog.Info("configuration loaded",
		slog.String("app", cfg.App.Name),
		slog.String("env", cfg.App.Env),
		slog.String("port", cfg.App.Port),
	)

	// Phase 8D.3: make the migration-window position explicit at startup so an
	// operator can see immediately whether bare legacy keys can authenticate.
	// The flag value is configuration, never a secret.
	if cfg.App.LegacyAPICredentialsEnabled {
		slog.Info("legacy api credentials enabled — migration window open (Phase 8D.3)",
			slog.String("env", cfg.App.Env),
		)
	} else {
		slog.Info("legacy api credentials disabled — bare legacy keys rejected (Phase 8D.3)",
			slog.String("env", cfg.App.Env),
		)
	}

	// -------------------------------------------------------------------------
	// Database
	// -------------------------------------------------------------------------
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.Database.DSN())
	if err != nil {
		slog.Error("failed to connect to database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer pool.Close()
	if err := database.CheckSchema(ctx, pool); err != nil {
		slog.Error("database schema is not ready", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// -------------------------------------------------------------------------
	// Gin engine
	// -------------------------------------------------------------------------
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()

	// Middleware order matters:
	//   1. Recovery    — catches panics before anything else runs
	//   2. RequestID   — stamps every request, including CORS preflight
	//   3. CORS        — handles browser origin policy
	//   4. Logger      — logs after RequestID so the ID is available
	//   5. BodyLimit   — caps request bodies before ANY handler or route
	//                    middleware reads them (Phase 8D.2)
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS(cfg.Auth.CORSAllowedOrigins))
	r.Use(requestLogger())
	r.Use(middleware.MaxBodyBytes(cfg.HTTP.MaxBodyBytes))

	// Phase 8D.2: make ClientIP()'s forwarded-header trust EXPLICIT.
	// Development may leave TRUSTED_PROXIES unset for Gin's compatibility
	// default. Production config.Load requires an explicit "none" or IP/CIDR
	// policy; invalid entries fail startup.
	if cfg.App.TrustedProxies != nil {
		if err := r.SetTrustedProxies(cfg.App.TrustedProxies); err != nil {
			slog.Error("invalid TRUSTED_PROXIES", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	// -------------------------------------------------------------------------
	// Dependencies — Repositories
	// -------------------------------------------------------------------------
	// Phase 8D.5: one centralized append-only audit writer. Transaction-owning
	// repositories receive this narrow recorder and insert the request event
	// before their existing COMMIT.
	auditRepo := repository.NewAuditLogRepository(pool)
	auditSvc := service.NewAuditService(auditRepo)
	merchantRepo := repository.NewMerchantRepository(pool, auditSvc)
	if err := validateSimulatorMerchant(ctx, cfg, merchantRepo); err != nil {
		slog.Error("simulator configuration invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}
	txRepo := repository.NewTransactionRepository(pool)
	mockPaymentRepo := repository.NewMockPaymentRepository(pool)
	attemptRepo := repository.NewPaymentAttemptRepository(pool)
	idempotencyRepo := repository.NewIdempotencyKeyRepository(pool)
	webhookRepo := repository.NewWebhookEventRepository(pool)
	apiKeyRepo := repository.NewMerchantAPIKeyRepository(pool, auditSvc)
	// Phase 8D.3: atomic legacy credential state transitions (FOR UPDATE row
	// lock + Phase 5C key insert in one transaction).
	legacyCredentialStore := repository.NewPGLegacyCredentialStore(pool, apiKeyRepo, auditSvc)
	merchantWebhookConfigRepo := repository.NewMerchantWebhookConfigRepository(pool, auditSvc)
	merchantWebhookDeliveryRepo := repository.NewMerchantWebhookDeliveryRepository(pool)
	refundRepo := repository.NewRefundRepository(pool)
	refundAttemptRepo := repository.NewRefundAttemptRepository(pool)
	settlementRepo := repository.NewSettlementRepository(pool)
	reconciliationRepo := repository.NewReconciliationRepository(pool)
	// Phase 8: dashboard authentication
	merchantUserRepo := repository.NewMerchantUserRepository(pool, auditSvc)
	dashboardSessionRepo := repository.NewDashboardSessionRepository(pool)
	// Phase 8B: team invitations — Phase 8C.3A: the invitation row and its
	// rendered email outbox row commit in ONE transaction (repository-owned
	// tx; no separate connection for the enqueue). Phase 8C.3B: the same
	// repository serves the worker's claim/mark operations from the pool.
	emailOutboxRepo := repository.NewEmailOutboxRepository(pool)
	invitationRepo := repository.NewMerchantInvitationRepository(pool, emailOutboxRepo, auditSvc)

	// -------------------------------------------------------------------------
	// Dependencies — Services
	// -------------------------------------------------------------------------
	merchantSvc := service.NewMerchantServiceWithSessions(merchantRepo, dashboardSessionRepo)
	apiKeySvc := service.NewMerchantAPIKeyService(apiKeyRepo, merchantRepo)
	// Phase 8D.3: legacy plaintext credential migration window.
	legacyCredentialSvc := service.NewLegacyCredentialService(legacyCredentialStore)

	provider, err := paymentProviderFromConfigWithMockStore(cfg, mockPaymentRepo)
	if err != nil {
		slog.Error("failed to configure payment provider", slog.String("error", err.Error()))
		os.Exit(1)
	}
	paymentSvc := service.NewPaymentServiceWithIdempotency(txRepo, attemptRepo, provider, idempotencyRepo, cfg.Idempotency.TTL)

	// Merchant outbound webhooks (Phase 6) — distinct from inbound provider webhooks.
	merchantWebhookPublisher := service.NewMerchantWebhookPublisher(merchantWebhookConfigRepo, merchantWebhookDeliveryRepo)
	outboxUpdater := service.NewOutboxStatusUpdater(pool, merchantWebhookPublisher)
	service.ConfigurePaymentMerchantWebhooks(paymentSvc, merchantWebhookPublisher, outboxUpdater)

	merchantWebhookCfgSvc := service.NewMerchantWebhookConfigService(
		merchantWebhookConfigRepo,
		merchantWebhookDeliveryRepo,
		merchantRepo,
		cfg.WebhookDelivery.EncryptionKey,
		cfg.WebhookDelivery.RequireHTTPS,
	)
	// Phase 8D.2: nil client → the SSRF-guarded outbound webhook client
	// (destination validation at the dial boundary, no environment proxy,
	// redirects refused, TLS verification untouched).
	merchantWebhookDispatcher := service.NewMerchantWebhookDispatcher(
		merchantWebhookDeliveryRepo,
		merchantWebhookConfigRepo,
		cfg.WebhookDelivery.EncryptionKey,
		cfg.WebhookDelivery.Timeout,
		cfg.WebhookDelivery.MaxAttempts,
		cfg.WebhookDelivery.StaleAfter,
		cfg.WebhookDelivery.BatchSize,
		nil,
	)
	merchantWebhookWorker := service.NewMerchantWebhookWorker(merchantWebhookDispatcher, cfg.WebhookDelivery.Interval)

	// Webhook parsers — register only the configured production provider. This
	// keeps a known/unneeded mock webhook surface out of a Midtrans deployment;
	// development/staging mock deployments still register the mock parser.
	var webhookParsers []service.PaymentWebhookParser
	switch strings.ToLower(cfg.Provider.Name) {
	case "", "mock":
		webhookParsers = append(webhookParsers, service.NewMockWebhookParser(cfg.Webhook.MockSecret))
	case "midtrans":
		webhookParsers = append(webhookParsers, service.NewMidtransWebhookParser(cfg.Provider.ServerKey))
	}
	webhookSvc := service.NewWebhookService(txRepo, webhookRepo, webhookParsers...)
	service.ConfigureWebhookMerchantPublisher(webhookSvc, merchantWebhookPublisher)
	service.ConfigureWebhookRefundRepository(webhookSvc, refundRepo)

	// Expiry service + worker.
	expirySvc := service.NewExpiryService(txRepo)
	service.ConfigureExpiryMerchantWebhooks(expirySvc, merchantWebhookPublisher, outboxUpdater)
	expiryWorker := service.NewExpiryWorker(expirySvc, cfg.Expiry.Interval, cfg.Expiry.BatchSize)

	// Refund service (Phase 7B).
	refundProvider := service.NewMockRefundProvider()
	refundSvc := service.NewRefundService(
		txRepo, refundRepo, refundAttemptRepo,
		idempotencyRepo, refundProvider, merchantWebhookPublisher,
		cfg.Idempotency.TTL, cfg.Provider.Name,
	)
	settlementSvc := service.NewSettlementServiceForProvider(
		settlementRepo,
		cfg.Provider.Name,
		service.NewMockSettlementImporter(),
	)
	reconciliationSvc := service.NewReconciliationService(
		settlementRepo,
		reconciliationRepo,
		txRepo,
		refundRepo,
		cfg.Admin.StaleAfter,
	)

	// Phase 8: dashboard auth + user management services.
	authSvc := service.NewAuthService(
		merchantUserRepo,
		dashboardSessionRepo,
		service.AuthConfig{
			JWTSecret:       cfg.Auth.JWTSecret,
			AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
			RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
		},
		merchantRepo,
	)
	dashboardUserSvc := service.NewDashboardUserService(merchantUserRepo, dashboardSessionRepo, merchantRepo)

	// Phase 8C.1: email delivery foundation — SMTP EmailSender (no-op when
	// EMAIL_ENABLED=false). Config validation lives in config.Load.
	emailSender := service.NewEmailSender(service.EmailSenderConfig{
		Enabled:  cfg.Email.Enabled,
		Host:     cfg.Email.Host,
		Port:     cfg.Email.Port,
		Username: cfg.Email.Username,
		Password: cfg.Email.Password,
		From:     cfg.Email.From,
		Timeout:  cfg.Email.Timeout,
		TLSMode:  cfg.Email.TLSMode,
	})

	// Phase 8C.3B: asynchronous email outbox worker — the ONLY consumer of
	// EmailSender. Structural mirror of the merchant webhook
	// dispatcher/worker pair: claim → send → SENT/retry/DEAD on an interval.
	// InvitationService must never take EmailSender (SMTP stays out of the
	// HTTP request path).
	emailOutboxDispatcher := service.NewEmailOutboxDispatcher(
		emailOutboxRepo,
		emailSender,
		cfg.EmailWorker.MaxAttempts,
		cfg.EmailWorker.StaleAfter,
		cfg.EmailWorker.BatchSize,
	)
	emailOutboxWorker := service.NewEmailOutboxWorker(emailOutboxDispatcher, cfg.EmailWorker.Interval)

	// Phase 8C.3C: terminal retention cleanup — the ONLY component that ever
	// DELETEs from email_outbox. Disjoint from the delivery state machine by
	// construction (repository matches only SENT/NEVER PENDING/PROCESSING),
	// never touches merchant_user_invitations, and is opt-in via
	// EMAIL_CLEANUP_ENABLED (default off). Dependency graph mirrors the other
	// workers: repo → worker → workerCtx → Start()/Wait().
	emailOutboxCleanupWorker := service.NewEmailOutboxCleanupWorker(
		emailOutboxRepo,
		cfg.EmailCleanup.Interval,
		cfg.EmailCleanup.BatchSize,
		cfg.EmailCleanup.SentRetention,
		cfg.EmailCleanup.DeadRetention,
	)

	// Phase 8B + 8C.3A: self-service team invitations (API-first — token
	// returned once). The invitation email is rendered and committed into
	// email_outbox atomically with the invitation; the acceptance link is
	// built from DASHBOARD_BASE_URL. SMTP is NOT part of the request path —
	// asynchronous delivery arrives with the Phase 8C.3B worker.
	invitationSvc := service.NewInvitationService(
		invitationRepo,
		merchantUserRepo,
		merchantRepo,
		cfg.Auth.InvitationTokenTTL,
		cfg.App.DashboardBaseURL,
	)
	onboardingSvc := service.NewOnboardingService(
		service.NewPGOnboardingProvisioner(pool, merchantRepo, merchantUserRepo, apiKeyRepo, auditSvc),
	)

	// Phase 9: dashboard overview aggregates.
	dashboardOverviewRepo := repository.NewDashboardOverviewRepository(pool)
	dashboardOverviewSvc := service.NewDashboardOverviewService(dashboardOverviewRepo, paymentSvc)

	// -------------------------------------------------------------------------
	// Dependencies — Handlers
	// -------------------------------------------------------------------------
	merchantHandler := handler.NewMerchantHandler(merchantSvc)
	merchantLifecycleHandler := handler.NewMerchantLifecycleHandler(merchantSvc)
	paymentHandler := handler.NewPaymentHandler(paymentSvc)
	webhookHandler := handler.NewWebhookHandler(webhookSvc)
	healthHandler := handler.NewHealthHandler(pool)
	apiKeyHandler := handler.NewMerchantAPIKeyHandler(apiKeySvc)
	merchantWebhookHandler := handler.NewMerchantWebhookHandler(merchantWebhookCfgSvc)
	refundHandler := handler.NewRefundHandler(refundSvc)
	settlementHandler := handler.NewSettlementHandler(settlementSvc, reconciliationSvc)
	// Phase 8: dashboard authentication handlers.
	isProd := cfg.App.Env == "production"
	authHandler := handler.NewAuthHandler(authSvc, isProd, cfg.Auth.RefreshTokenTTL)
	adminUserHandler := handler.NewAdminUserHandler(dashboardUserSvc)
	onboardingHandler := handler.NewOnboardingHandler(onboardingSvc)
	dashboardUserHandler := handler.NewDashboardUserHandler(dashboardUserSvc)
	// Phase 8B: team invitation handler (create is dashboard-auth; get/accept
	// are unauthenticated token endpoints).
	invitationHandler := handler.NewInvitationHandler(invitationSvc)
	// Phase 9: dashboard resource handlers (Bearer JWT only).
	dashboardOverviewHandler := handler.NewDashboardOverviewHandler(dashboardOverviewSvc)
	dashboardPaymentHandler := handler.NewDashboardPaymentHandler(paymentSvc, refundSvc, webhookSvc, cfg.Webhook.MockSecret)
	dashboardRefundHandler := handler.NewDashboardRefundHandler(refundSvc)
	dashboardAPIKeyHandler := handler.NewDashboardAPIKeyHandler(apiKeySvc)
	dashboardWebhookHandler := handler.NewDashboardWebhookHandler(merchantWebhookCfgSvc)
	dashboardReconHandler := handler.NewDashboardReconciliationHandler(reconciliationSvc)
	dashboardSettingsHandler := handler.NewDashboardSettingsHandler(merchantSvc)
	// Phase 8D.3: legacy credential migrate/disable (dashboard, tenant-scoped).
	dashboardLegacyCredentialHandler := handler.NewDashboardLegacyCredentialHandler(legacyCredentialSvc)
	simulatorHandler := handler.NewSimulatorHandler(
		paymentSvc,
		webhookSvc,
		cfg.Webhook.MockSecret,
		cfg.App.SimulatorEnabled,
		cfg.App.SimulatorMerchantID,
	)
	mockPaymentHandler := handler.NewMockPaymentHandler(mockPaymentRepo, webhookSvc, cfg.Webhook.MockSecret)

	// -------------------------------------------------------------------------
	// Phase 8D.1 — in-process rate limiters (fixed one-minute windows)
	// -------------------------------------------------------------------------
	// Nil limiters (RATE_LIMIT_ENABLED=false) make every rate-limit middleware
	// a pass-through, so the only supported off switch is the config flag.
	// The limiters themselves panic on invalid input, but config validation
	// (parseRateLimitConfig) rejects non-positive limits before we get here.
	rateWindow := time.Minute
	var loginLimiter, adminLimiter, apiLimiter ratelimit.RateLimiter
	if cfg.RateLimit.Enabled {
		loginLimiter = ratelimit.NewFixedWindow(cfg.RateLimit.LoginPerMinute, rateWindow)
		adminLimiter = ratelimit.NewFixedWindow(cfg.RateLimit.AdminPerMinute, rateWindow)
		apiLimiter = ratelimit.NewFixedWindow(cfg.RateLimit.APIPerMinute, rateWindow)
		slog.Info("rate limiting enabled",
			slog.Int("login_per_minute", cfg.RateLimit.LoginPerMinute),
			slog.Int("admin_per_minute", cfg.RateLimit.AdminPerMinute),
			slog.Int("api_per_minute", cfg.RateLimit.APIPerMinute),
		)
	} else {
		slog.Warn("rate limiting is DISABLED (RATE_LIMIT_ENABLED=false)")
	}
	// Middleware factories. Admin and API surfaces get separate key spaces;
	// the API surface gets BOTH a pre-auth client-IP limiter (counts rejected
	// credential attempts) and a post-auth merchant limiter (budget follows
	// the authenticated tenant, never a client-supplied merchant_id).
	rateLimitLogin := middleware.LoginRateLimit(loginLimiter, rateWindow)
	rateLimitAdminIP := middleware.ClientIPRateLimit(adminLimiter, rateWindow, "admin")
	rateLimitAPIIP := middleware.ClientIPRateLimit(apiLimiter, rateWindow, "api")
	rateLimitAPIMerchant := middleware.MerchantRateLimit(apiLimiter, rateWindow, "api")

	// -------------------------------------------------------------------------
	// Routes — Health
	// -------------------------------------------------------------------------
	r.GET("/health", healthHandler.Live)
	r.GET("/health/ready", healthHandler.Ready)
	// Backend JSON provider projection. The React frontend owns the browser
	// /pay/:identifier page and consumes /api/v1/mock-payments/:identifier.
	r.GET("/pay/:identifier", mockPaymentHandler.Get)

	// -------------------------------------------------------------------------
	// Swagger UI
	// -------------------------------------------------------------------------
	// Accessible at http://localhost:8081/swagger/index.html when using Docker.
	// Register HEAD as well: curl -I is commonly used for health/acceptance
	// checks, while Gin does not implicitly route HEAD requests to GET handlers.
	swaggerHandler := ginSwagger.WrapHandler(swaggerFiles.Handler)
	r.GET("/swagger/*any", swaggerHandler)
	r.HEAD("/swagger/*any", func(c *gin.Context) {
		c.Request.Method = http.MethodGet
		swaggerHandler(c)
	})

	// -------------------------------------------------------------------------
	// Routes — API v1
	// -------------------------------------------------------------------------
	v1 := r.Group("/api/v1")
	{
		// Customer-facing development-provider contract. It intentionally has no
		// dashboard JWT or merchant API-key middleware.
		v1.GET("/mock-payments/:identifier", mockPaymentHandler.Get)
		v1.POST("/mock-payments/:identifier/success", mockPaymentHandler.Success)
		v1.POST("/mock-payments/:identifier/fail", mockPaymentHandler.Fail)
		// Phase 8D.1: simulator containment — disabled by default, rejected at
		// startup when APP_ENV=production, authenticated via X-API-Key, and
		// allowlisted + tenant-scoped to SIMULATOR_MERCHANT_ID in the handler.
		simulator := v1.Group("/simulator")
		{
			simulator.Use(rateLimitAPIIP)
			simulator.Use(middleware.Auth(merchantSvc, apiKeySvc, cfg.App.LegacyAPICredentialsEnabled))
			simulator.Use(rateLimitAPIMerchant)
			simulator.POST("/payments", simulatorHandler.CreatePayment)
			simulator.GET("/payments/:payment_id", simulatorHandler.GetPayment)
			simulator.POST("/payments/:payment_id/success", simulatorHandler.SimulateSuccess)
			simulator.POST("/payments/:payment_id/fail", simulatorHandler.SimulateFailure)
		}

		// Merchant endpoints.
		// POST create is admin-only (X-Admin-Key) — low-level merchant-row create.
		// Prefer POST /api/v1/admin/onboarding/merchants for full tenant provisioning.
		// GET by id is admin-only (Phase 7) — no longer public.
		// API key routes share the same :id wildcard as GET /merchants/:id —
		// Gin forbids mixing :id and :merchant_id at the same path segment.
		merchants := v1.Group("/merchants")
		{
			merchants.POST("", rateLimitAdminIP, middleware.AdminAuth(cfg.Admin.APIKey, auditSvc), merchantHandler.Create)
			merchants.GET("/:id", rateLimitAdminIP, middleware.AdminAuth(cfg.Admin.APIKey, auditSvc), merchantHandler.GetByID)

			// Merchant API key lifecycle (auth required; path :id = merchant UUID).
			apiKeys := merchants.Group("/:id/api-keys")
			apiKeys.Use(rateLimitAPIIP)
			apiKeys.Use(middleware.Auth(merchantSvc, apiKeySvc, cfg.App.LegacyAPICredentialsEnabled))
			apiKeys.Use(rateLimitAPIMerchant)
			{
				apiKeys.POST("", apiKeyHandler.CreateAPIKey)
				apiKeys.GET("", apiKeyHandler.ListAPIKeys)
				apiKeys.DELETE("/:key_id", apiKeyHandler.RevokeAPIKey)
				apiKeys.POST("/:key_id/rotate", apiKeyHandler.RotateAPIKey)
			}

			// Merchant outbound webhook config + delivery audit (Phase 6).
			wh := merchants.Group("/:id/webhook")
			wh.Use(rateLimitAPIIP)
			wh.Use(middleware.Auth(merchantSvc, apiKeySvc, cfg.App.LegacyAPICredentialsEnabled))
			wh.Use(rateLimitAPIMerchant)
			{
				wh.POST("", merchantWebhookHandler.UpsertWebhook)
				wh.GET("", merchantWebhookHandler.GetWebhook)
				wh.POST("/rotate", merchantWebhookHandler.RotateWebhookSecret)
				wh.DELETE("", merchantWebhookHandler.DisableWebhook)
				wh.GET("/deliveries", merchantWebhookHandler.ListDeliveries)
				wh.GET("/deliveries/:delivery_id", merchantWebhookHandler.GetDelivery)
				wh.POST("/deliveries/:delivery_id/retry", merchantWebhookHandler.RetryDelivery)
			}
		}

		// Payment endpoints (auth required)
		payments := v1.Group("/payments")
		payments.Use(rateLimitAPIIP)
		payments.Use(middleware.Auth(merchantSvc, apiKeySvc, cfg.App.LegacyAPICredentialsEnabled))
		payments.Use(rateLimitAPIMerchant)
		{
			payments.GET("", paymentHandler.List)
			payments.POST("", paymentHandler.Create)
			payments.GET("/:id", paymentHandler.GetByID)
			payments.POST("/:id/cancel", paymentHandler.Cancel)
			// Refund endpoints (Phase 7B)
			payments.POST("/:id/refunds", refundHandler.CreateRefund)
			payments.GET("/:id/refunds", refundHandler.ListRefunds)
		}

		// Refund get-by-id (auth required)
		refunds := v1.Group("/refunds")
		refunds.Use(rateLimitAPIIP)
		refunds.Use(middleware.Auth(merchantSvc, apiKeySvc, cfg.App.LegacyAPICredentialsEnabled))
		refunds.Use(rateLimitAPIMerchant)
		{
			refunds.GET("/:id", refundHandler.GetRefund)
		}

		// Webhook endpoints (NO merchant auth — requests originate from providers)
		webhooks := v1.Group("/webhooks")
		{
			webhooks.POST("/providers/:provider", webhookHandler.Receive)
		}

		// Settlement and reconciliation endpoints use the dedicated ops key.
		// Merchant API keys are intentionally not accepted here.
		// Phase 8D.1: the admin client-IP limiter runs BEFORE AdminAuth so
		// brute-force key attempts are counted even when rejected.
		admin := v1.Group("/admin")
		admin.Use(rateLimitAdminIP)
		admin.Use(middleware.AdminAuth(cfg.Admin.APIKey, auditSvc))
		{
			settlements := admin.Group("/settlements")
			{
				settlements.POST("/import", settlementHandler.ImportSettlement)
				settlements.GET("", settlementHandler.ListSettlements)
				settlements.GET("/:id", settlementHandler.GetSettlement)
				settlements.POST("/:id/reconcile", settlementHandler.ReconcileSettlement)
				settlements.GET("/:id/reconciliation", settlementHandler.ListSettlementReconciliation)
			}
			reconciliation := admin.Group("/reconciliation")
			{
				reconciliation.GET("/mismatches", settlementHandler.ListMismatches)
				reconciliation.GET("/mismatches/:id", settlementHandler.GetMismatch)
			}
			// Phase 8: admin bootstrap — create dashboard user (X-Admin-Key required).
			merchants := admin.Group("/merchants")
			{
				merchants.POST("/:merchant_id/users", adminUserHandler.CreateUser)
				// Phase 7: merchant lifecycle status mutation.
				merchants.PATCH("/:merchant_id/status", merchantLifecycleHandler.UpdateStatus)
			}
			// Secure tenant onboarding — atomic merchant + OWNER + Phase 5C API credential.
			onboarding := admin.Group("/onboarding")
			{
				onboarding.POST("/merchants", onboardingHandler.OnboardMerchant)
			}
		}

		// Phase 8: dashboard authentication — unauthenticated (login/logout/refresh).
		// Phase 8D.1: the login limiter runs FIRST (before any auth) so both
		// rejected and accepted attempts count, keyed per client IP and per
		// submitted account email. It is attached only to login: refresh
		// concurrency must deterministically return 401 for token-reuse
		// losers, not consume the login budget and turn them into 429s.
		auth := v1.Group("/auth")
		{
			auth.POST("/login", rateLimitLogin, authHandler.Login)
			auth.POST("/logout", authHandler.Logout)
			auth.POST("/refresh", authHandler.Refresh)
		}

		// Phase 8: GET /auth/me — requires valid access token.
		authProtected := v1.Group("/auth")
		authProtected.Use(middleware.RequireDashboardAuth(authSvc, merchantUserRepo, merchantRepo))
		{
			authProtected.GET("/me", authHandler.Me)
		}

		// Phase 8B: team invitations — unauthenticated (the invitee has no
		// account yet). Guarded solely by the opaque single-use token.
		invitations := v1.Group("/invitations")
		{
			invitations.GET("/:token", invitationHandler.GetByToken)
			invitations.POST("/:token/accept", invitationHandler.Accept)
		}

		// Phase 8+9: authenticated dashboard endpoints (Bearer JWT only).
		// Merchant identity is always derived from the authenticated user — never from client input.
		dashboard := v1.Group("/dashboard")
		dashboard.Use(middleware.RequireDashboardAuth(authSvc, merchantUserRepo, merchantRepo))
		{
			// Phase 8: user management — scoped to authenticated user's merchant.
			// Phase 8A: self-service role management + password change.
			users := dashboard.Group("/users")
			{
				users.GET("", dashboardUserHandler.ListUsers)
				// Phase 8B: invite a teammate — OWNER-only, same policy as the
				// Phase 8A role/status mutations. The plaintext token is
				// returned once in the response (email delivery is 8C).
				users.POST("/invite",
					middleware.RequireRole(model.DashboardUserRoleOwner),
					invitationHandler.Create,
				)
				users.PATCH("/:user_id/status",
					middleware.RequireRole(model.DashboardUserRoleOwner),
					dashboardUserHandler.UpdateUserStatus,
				)
				users.PATCH("/:user_id/role",
					middleware.RequireRole(model.DashboardUserRoleOwner),
					dashboardUserHandler.UpdateUserRole,
				)
			}

			// Phase 8A: self-service password change — identity comes from the
			// authenticated session; no user_id is ever accepted from the client.
			dashboard.PATCH("/me/password", dashboardUserHandler.ChangePassword)

			// Phase 9: overview + payments (all roles: OWNER, ADMIN, VIEWER).
			dashboard.GET("/overview", dashboardOverviewHandler.GetOverview)
			dashboard.GET("/payments", dashboardPaymentHandler.ListPayments)
			dashboard.POST("/payments",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardPaymentHandler.CreatePayment,
			)
			dashboard.GET("/payments/:payment_id", dashboardPaymentHandler.GetPayment)

			// Phase 9: refunds — read all roles; create OWNER/ADMIN.
			dashboard.GET("/refunds", dashboardRefundHandler.ListRefunds)
			dashboard.GET("/refunds/:refund_id", dashboardRefundHandler.GetRefund)
			dashboard.POST("/payments/:payment_id/refunds",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardPaymentHandler.CreateRefund,
			)

			// Phase 5: payment simulation — JWT-authenticated simulation for dashboard users.
			// Reuses the existing webhook processing logic for state transitions and outbox.
			dashboard.POST("/payments/:payment_id/simulate/success",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardPaymentHandler.SimulateSuccess,
			)
			dashboard.POST("/payments/:payment_id/simulate/fail",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardPaymentHandler.SimulateFailure,
			)

			// Phase 9: API keys — read all roles; mutate OWNER/ADMIN.
			dashboard.GET("/api-keys", dashboardAPIKeyHandler.ListAPIKeys)
			dashboard.POST("/api-keys",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardAPIKeyHandler.CreateAPIKey,
			)
			dashboard.POST("/api-keys/:id/revoke",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardAPIKeyHandler.RevokeAPIKey,
			)

			// Phase 9: webhook config + deliveries.
			dashboard.GET("/webhook-config", dashboardWebhookHandler.GetWebhookConfig)
			dashboard.PUT("/webhook-config",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardWebhookHandler.UpsertWebhookConfig,
			)
			dashboard.POST("/webhook-config/rotate",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardWebhookHandler.RotateWebhookSecret,
			)
			dashboard.DELETE("/webhook-config",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardWebhookHandler.DisableWebhookConfig,
			)
			dashboard.GET("/webhooks", dashboardWebhookHandler.ListDeliveries)
			dashboard.GET("/webhooks/:id", dashboardWebhookHandler.GetDelivery)
			dashboard.POST("/webhooks/:id/retry",
				middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
				dashboardWebhookHandler.RetryDelivery,
			)

			// Phase 9: reconciliation mismatches (merchant-scoped). Settlements deferred
			// because settlement batches have no merchant_id column.
			dashboard.GET("/reconciliation/mismatches", dashboardReconHandler.ListMismatches)
			dashboard.GET("/reconciliation/mismatches/:id", dashboardReconHandler.GetMismatch)

			// Phase 9: merchant settings (read-only; profile update not yet supported).
			dashboard.GET("/settings", dashboardSettingsHandler.GetSettings)

			// Phase 8D.3: legacy plaintext credential migration window.
			// A dedicated /legacy-credential prefix (not /merchants/:id/…)
			// avoids a Gin wildcard conflict with the existing :id siblings,
			// and the merchant is always taken from the JWT — never from input.
			// No rate-limit middleware: dashboard routes have never had one and
			// the Phase 8D.1 limiters on API/login/admin routes are untouched.
			legacyCred := dashboard.Group("/legacy-credential")
			{
				legacyCred.POST("/migrate",
					middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
					dashboardLegacyCredentialHandler.MigrateLegacyCredential,
				)
				legacyCred.POST("/disable",
					middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
					dashboardLegacyCredentialHandler.DisableLegacyCredential,
				)
			}
		}
	}

	// -------------------------------------------------------------------------
	// Start expiry worker
	// -------------------------------------------------------------------------
	// workerCtx is cancelled when we receive SIGINT/SIGTERM, giving the worker
	// a chance to finish its current batch before the process exits.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if cfg.Expiry.Enabled {
		expiryWorker.Start(workerCtx)
		slog.Info("expiry worker started",
			slog.Duration("interval", cfg.Expiry.Interval),
			slog.Int("batch_size", cfg.Expiry.BatchSize),
		)
	} else {
		slog.Info("expiry worker disabled (PAYMENT_EXPIRY_ENABLED=false)")
	}

	if cfg.WebhookDelivery.Enabled {
		merchantWebhookWorker.Start(workerCtx)
		slog.Info("merchant webhook worker started",
			slog.Duration("interval", cfg.WebhookDelivery.Interval),
			slog.Int("batch_size", cfg.WebhookDelivery.BatchSize),
		)
	} else {
		slog.Info("merchant webhook worker disabled (WEBHOOK_DELIVERY_ENABLED=false)")
	}

	// Phase 8C.3B: the email outbox worker runs regardless of EMAIL_ENABLED —
	// with email disabled the no-op sender accepts messages, so queued rows
	// still drain to SENT instead of accumulating. Never bypass the worker
	// because SMTP is off; gate it only via EMAIL_WORKER_ENABLED.
	if cfg.EmailWorker.Enabled {
		emailOutboxWorker.Start(workerCtx)
		slog.Info("email outbox worker started",
			slog.Duration("interval", cfg.EmailWorker.Interval),
			slog.Int("batch_size", cfg.EmailWorker.BatchSize),
		)
	} else {
		slog.Info("email outbox worker disabled (EMAIL_WORKER_ENABLED=false)")
	}

	// Phase 8C.3C: retention cleanup — opt-in (default false) so a fresh
	// deployment never deletes rows until an operator enables it.
	if cfg.EmailCleanup.Enabled {
		emailOutboxCleanupWorker.Start(workerCtx)
		slog.Info("email outbox cleanup worker started",
			slog.Duration("interval", cfg.EmailCleanup.Interval),
			slog.Int("batch_size", cfg.EmailCleanup.BatchSize),
			slog.Duration("sent_retention", cfg.EmailCleanup.SentRetention),
			slog.Duration("dead_retention", cfg.EmailCleanup.DeadRetention),
		)
	} else {
		slog.Info("email outbox cleanup worker disabled (EMAIL_CLEANUP_ENABLED=false)")
	}

	// -------------------------------------------------------------------------
	// HTTP Server with graceful shutdown
	// -------------------------------------------------------------------------
	srv := newHTTPServer(fmt.Sprintf(":%s", cfg.App.Port), r, cfg.HTTP)

	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("server starting",
			slog.String("addr", srv.Addr),
			slog.Duration("read_header_timeout", cfg.HTTP.ReadHeaderTimeout),
			slog.Duration("read_timeout", cfg.HTTP.ReadTimeout),
			slog.Duration("write_timeout", cfg.HTTP.WriteTimeout),
			slog.Duration("idle_timeout", cfg.HTTP.IdleTimeout),
			slog.Int64("max_body_bytes", cfg.HTTP.MaxBodyBytes),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
	}()

	// Wait for either a termination signal or a fatal listener error. Both
	// paths use the same coordinated drain instead of calling os.Exit from a
	// goroutine and bypassing worker/pool cleanup.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	select {
	case <-quit:
		slog.Info("shutdown signal received, draining connections...")
	case err := <-serverErrCh:
		slog.Error("server listener failed; shutting down", slog.String("error", err.Error()))
	}

	// Stop accepting new HTTP work and drain in-flight requests while workers
	// are still available to complete their current operations.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", slog.String("error", err.Error()))
	}
	shutdownCancel()

	// Then stop background workers. A worker may be inside a database/provider
	// call; cap the total worker drain so a broken dependency cannot hold the
	// process indefinitely.
	workerCancel()
	workerDeadline := time.After(10 * time.Second)
	waitForWorker := func(name string, waitFn func()) {
		waitForWorkerUntil(name, waitFn, workerDeadline)
	}
	if cfg.Expiry.Enabled {
		waitForWorker("expiry", expiryWorker.Wait)
	}
	if cfg.WebhookDelivery.Enabled {
		waitForWorker("merchant webhook", merchantWebhookWorker.Wait)
	}
	if cfg.EmailWorker.Enabled {
		waitForWorker("email outbox", emailOutboxWorker.Wait)
	}
	if cfg.EmailCleanup.Enabled {
		waitForWorker("email cleanup", emailOutboxCleanupWorker.Wait)
	}

	slog.Info("server stopped")
}

// waitForWorkerUntil waits for one worker without allowing shutdown to block
// indefinitely. deadline is shared by all workers in a shutdown sequence.
func waitForWorkerUntil(name string, waitFn func(), deadline <-chan time.Time) {
	done := make(chan struct{})
	go func() {
		waitFn()
		close(done)
	}()

	select {
	case <-done:
		slog.Info(name + " worker stopped")
	case <-deadline:
		slog.Warn(name + " worker did not stop before shutdown deadline")
	}
}

// newHTTPServer builds the public HTTP server with the Phase 8D.2 inbound
// timeout profile:
//
//   - ReadHeaderTimeout bounds slow-header (slowloris-style) request attacks —
//     this was previously unset (ReadTimeout covered it implicitly, an
//     explicit bound is clearer and tested);
//   - ReadTimeout/WriteTimeout bound full request/response transfer (values
//     unchanged from the previous implementation);
//   - IdleTimeout reaps idle keep-alive connections (unchanged).
//
// These are SERVER timeouts — the outbound webhook client timeout remains
// separately configured via WEBHOOK_DELIVERY_TIMEOUT.
func newHTTPServer(addr string, h http.Handler, hc config.HTTPConfig) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: hc.ReadHeaderTimeout,
		ReadTimeout:       hc.ReadTimeout,
		WriteTimeout:      hc.WriteTimeout,
		IdleTimeout:       hc.IdleTimeout,
	}
}

func validateSimulatorMerchant(ctx context.Context, cfg *config.Config, merchantRepo repository.MerchantRepository) error {
	if !cfg.App.SimulatorEnabled {
		return nil
	}
	if cfg.App.SimulatorMerchantID == uuid.Nil {
		return fmt.Errorf("SIMULATOR_MERCHANT_ID is required when PAYMENT_SIMULATOR_ENABLED=true")
	}
	merchant, err := merchantRepo.GetByID(ctx, cfg.App.SimulatorMerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return fmt.Errorf("simulator merchant not found")
		}
		return fmt.Errorf("load simulator merchant: %w", err)
	}
	if !merchant.IsActive() {
		return fmt.Errorf("simulator merchant is not active")
	}
	return nil
}

// paymentProviderFromConfig remains the lightweight constructor used by unit
// tests. Server wiring supplies durable provider state through the variant.
func paymentProviderFromConfig(cfg *config.Config) (service.PaymentProvider, error) {
	return paymentProviderFromConfigWithMockStore(cfg, nil)
}

func paymentProviderFromConfigWithMockStore(cfg *config.Config, mockPaymentRepo repository.MockPaymentRepository) (service.PaymentProvider, error) {
	providerName := strings.ToLower(cfg.Provider.Name)
	if cfg.App.Env == "production" && (providerName == "" || providerName == "mock") {
		return nil, fmt.Errorf("mock payment provider is not allowed in production")
	}
	switch providerName {
	case "", "mock":
		if mockPaymentRepo == nil {
			return service.NewMockPaymentProvider(), nil
		}
		frontendPublicURL := cfg.Provider.FrontendPublicURL
		if frontendPublicURL == "" {
			// Compatibility for manually assembled Config values from older
			// callers; config.Load always populates FrontendPublicURL for mock.
			frontendPublicURL = cfg.Provider.MockPaymentBaseURL
		}
		if frontendPublicURL == "" {
			return nil, fmt.Errorf("FRONTEND_PUBLIC_URL is required for the persisted mock payment provider")
		}
		return service.NewPersistedMockPaymentProvider(mockPaymentRepo, frontendPublicURL), nil
	case "midtrans":
		return service.NewMidtransProvider(cfg.Provider.BaseURL, cfg.Provider.ServerKey, cfg.Provider.Timeout, nil)
	default:
		return nil, fmt.Errorf("unsupported PAYMENT_PROVIDER %q (supported: mock, midtrans)", cfg.Provider.Name)
	}
}

// requestLogger returns a Gin middleware that emits a structured log line for
// every HTTP request. It must run after the RequestID middleware so the ID is
// available in the context.
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		rid, _ := c.Get("request_id")
		requestID, _ := rid.(string)
		// Use the route template, never the raw URL path. Public invitation
		// routes contain a bearer token in a path parameter; logging the raw
		// path would turn the access log into a second token store.
		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}

		slog.Info("http request",
			slog.String("request_id", requestID),
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
		)
	}
}
