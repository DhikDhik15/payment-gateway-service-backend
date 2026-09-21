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
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/pkg/database"
	_ "github.com/dhikaarta/pay-gate-backend/swagger" // generated swagger docs
	"github.com/gin-gonic/gin"
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

	// -------------------------------------------------------------------------
	// Gin engine
	// -------------------------------------------------------------------------
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()

	// Middleware order matters:
	//   1. Recovery    — catches panics before anything else runs
	//   2. CORS        — must run before RequestID so preflight OPTIONS aborts early
	//   3. RequestID   — stamps every request so all subsequent logs/responses carry it
	//   4. Logger      — logs after RequestID so the ID is available
	r.Use(gin.Recovery())
	r.Use(middleware.CORS(cfg.Auth.CORSAllowedOrigins))
	r.Use(middleware.RequestID())
	r.Use(requestLogger())

	// -------------------------------------------------------------------------
	// Dependencies — Repositories
	// -------------------------------------------------------------------------
	merchantRepo := repository.NewMerchantRepository(pool)
	txRepo := repository.NewTransactionRepository(pool)
	attemptRepo := repository.NewPaymentAttemptRepository(pool)
	idempotencyRepo := repository.NewIdempotencyKeyRepository(pool)
	webhookRepo := repository.NewWebhookEventRepository(pool)
	apiKeyRepo := repository.NewMerchantAPIKeyRepository(pool)
	merchantWebhookConfigRepo := repository.NewMerchantWebhookConfigRepository(pool)
	merchantWebhookDeliveryRepo := repository.NewMerchantWebhookDeliveryRepository(pool)
	refundRepo := repository.NewRefundRepository(pool)
	refundAttemptRepo := repository.NewRefundAttemptRepository(pool)
	settlementRepo := repository.NewSettlementRepository(pool)
	reconciliationRepo := repository.NewReconciliationRepository(pool)
	// Phase 8: dashboard authentication
	merchantUserRepo := repository.NewMerchantUserRepository(pool)
	dashboardSessionRepo := repository.NewDashboardSessionRepository(pool)

	// -------------------------------------------------------------------------
	// Dependencies — Services
	// -------------------------------------------------------------------------
	merchantSvc := service.NewMerchantService(merchantRepo)
	apiKeySvc := service.NewMerchantAPIKeyService(apiKeyRepo, merchantRepo)

	provider, err := paymentProviderFromConfig(cfg)
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
	merchantWebhookDispatcher := service.NewMerchantWebhookDispatcher(
		merchantWebhookDeliveryRepo,
		merchantWebhookConfigRepo,
		cfg.WebhookDelivery.EncryptionKey,
		cfg.WebhookDelivery.Timeout,
		cfg.WebhookDelivery.MaxAttempts,
		cfg.WebhookDelivery.StaleAfter,
		cfg.WebhookDelivery.BatchSize,
	)
	merchantWebhookWorker := service.NewMerchantWebhookWorker(merchantWebhookDispatcher, cfg.WebhookDelivery.Interval)

	// Webhook parsers — one per supported provider.
	mockParser := service.NewMockWebhookParser(cfg.Webhook.MockSecret)
	midtransParser := service.NewMidtransWebhookParser(cfg.Provider.ServerKey)
	webhookSvc := service.NewWebhookService(txRepo, webhookRepo, mockParser, midtransParser)
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
		cfg.Idempotency.TTL,
	)
	settlementSvc := service.NewSettlementService(
		settlementRepo,
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
	)
	dashboardUserSvc := service.NewDashboardUserService(merchantUserRepo, dashboardSessionRepo, merchantRepo)

	// Phase 9: dashboard overview aggregates.
	dashboardOverviewRepo := repository.NewDashboardOverviewRepository(pool)
	dashboardOverviewSvc := service.NewDashboardOverviewService(dashboardOverviewRepo, paymentSvc)

	// -------------------------------------------------------------------------
	// Dependencies — Handlers
	// -------------------------------------------------------------------------
	merchantHandler := handler.NewMerchantHandler(merchantSvc)
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
	dashboardUserHandler := handler.NewDashboardUserHandler(dashboardUserSvc)
	// Phase 9: dashboard resource handlers (Bearer JWT only).
	dashboardOverviewHandler := handler.NewDashboardOverviewHandler(dashboardOverviewSvc)
	dashboardPaymentHandler := handler.NewDashboardPaymentHandler(paymentSvc, refundSvc)
	dashboardRefundHandler := handler.NewDashboardRefundHandler(refundSvc)
	dashboardAPIKeyHandler := handler.NewDashboardAPIKeyHandler(apiKeySvc)
	dashboardWebhookHandler := handler.NewDashboardWebhookHandler(merchantWebhookCfgSvc)
	dashboardReconHandler := handler.NewDashboardReconciliationHandler(reconciliationSvc)
	dashboardSettingsHandler := handler.NewDashboardSettingsHandler(merchantSvc)
	simulatorHandler := handler.NewSimulatorHandler(pool, paymentSvc, webhookSvc, cfg.Webhook.MockSecret, cfg.App.SimulatorEnabled)

	// -------------------------------------------------------------------------
	// Routes — Health
	// -------------------------------------------------------------------------
	r.GET("/health", healthHandler.Live)
	r.GET("/health/ready", healthHandler.Ready)

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
		// Simulator endpoints (no merchant auth required, public/local development tool)
		simulator := v1.Group("/simulator")
		{
			simulator.POST("/payments", simulatorHandler.CreatePayment)
			simulator.GET("/payments/:payment_id", simulatorHandler.GetPayment)
			simulator.POST("/payments/:payment_id/success", simulatorHandler.SimulateSuccess)
			simulator.POST("/payments/:payment_id/fail", simulatorHandler.SimulateFailure)
		}

		// Merchant endpoints (no auth required for create/get).
		// API key routes share the same :id wildcard as GET /merchants/:id —
		// Gin forbids mixing :id and :merchant_id at the same path segment.
		merchants := v1.Group("/merchants")
		{
			merchants.POST("", merchantHandler.Create)
			merchants.GET("/:id", merchantHandler.GetByID)

			// Merchant API key lifecycle (auth required; path :id = merchant UUID).
			apiKeys := merchants.Group("/:id/api-keys")
			apiKeys.Use(middleware.Auth(merchantSvc, apiKeySvc))
			{
				apiKeys.POST("", apiKeyHandler.CreateAPIKey)
				apiKeys.GET("", apiKeyHandler.ListAPIKeys)
				apiKeys.DELETE("/:key_id", apiKeyHandler.RevokeAPIKey)
				apiKeys.POST("/:key_id/rotate", apiKeyHandler.RotateAPIKey)
			}

			// Merchant outbound webhook config + delivery audit (Phase 6).
			wh := merchants.Group("/:id/webhook")
			wh.Use(middleware.Auth(merchantSvc, apiKeySvc))
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
		payments.Use(middleware.Auth(merchantSvc, apiKeySvc))
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
		refunds.Use(middleware.Auth(merchantSvc, apiKeySvc))
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
		admin := v1.Group("/admin")
		admin.Use(middleware.AdminAuth(cfg.Admin.APIKey))
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
			}
		}

		// Phase 8: dashboard authentication — unauthenticated (login/logout/refresh).
		auth := v1.Group("/auth")
		{
			auth.POST("/login", authHandler.Login)
			auth.POST("/logout", authHandler.Logout)
			auth.POST("/refresh", authHandler.Refresh)
		}

		// Phase 8: GET /auth/me — requires valid access token.
		authProtected := v1.Group("/auth")
		authProtected.Use(middleware.RequireDashboardAuth(authSvc, merchantUserRepo))
		{
			authProtected.GET("/me", authHandler.Me)
		}

		// Phase 8+9: authenticated dashboard endpoints (Bearer JWT only).
		// Merchant identity is always derived from the authenticated user — never from client input.
		dashboard := v1.Group("/dashboard")
		dashboard.Use(middleware.RequireDashboardAuth(authSvc, merchantUserRepo))
		{
			// Phase 8: user management — scoped to authenticated user's merchant.
			users := dashboard.Group("/users")
			{
				users.GET("", dashboardUserHandler.ListUsers)
				users.PATCH("/:user_id/status", middleware.RequireRole(model.DashboardUserRoleOwner), dashboardUserHandler.UpdateUserStatus)
			}

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

	// -------------------------------------------------------------------------
	// HTTP Server with graceful shutdown
	// -------------------------------------------------------------------------
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.App.Port),
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("server starting", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	// Block until we receive SIGINT or SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutdown signal received, draining connections...")

	// Stop background workers first.
	workerCancel()
	if cfg.Expiry.Enabled {
		expiryWorker.Wait()
		slog.Info("expiry worker stopped")
	}
	if cfg.WebhookDelivery.Enabled {
		merchantWebhookWorker.Wait()
		slog.Info("merchant webhook worker stopped")
	}

	// Then shut down the HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", slog.String("error", err.Error()))
	}

	slog.Info("server stopped")
}

func paymentProviderFromConfig(cfg *config.Config) (service.PaymentProvider, error) {
	switch strings.ToLower(cfg.Provider.Name) {
	case "", "mock":
		return service.NewMockPaymentProvider(), nil
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
		path := c.Request.URL.Path

		c.Next()

		rid, _ := c.Get("request_id")
		requestID, _ := rid.(string)

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
