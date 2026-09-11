package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/config"
	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/pkg/database"
	"github.com/gin-gonic/gin"
)

// @title						Payment Gateway API
// @version					1.0
// @description				Payment Gateway Backend API — Phase 1
// @termsOfService				http://swagger.io/terms/
//
// @contact.name				API Support
// @contact.email				support@example.com
//
// @license.name				MIT
//
// @host						localhost:8080
// @BasePath					/
//
// @securityDefinitions.apikey	ApiKeyAuth
// @in							header
// @name						X-API-Key
// @description				API key issued to the merchant at registration.
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
	//   2. RequestID   — stamps every request so all subsequent logs/responses carry it
	//   3. Logger      — logs after RequestID so the ID is available
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(requestLogger())

	// -------------------------------------------------------------------------
	// Routes — Health
	// -------------------------------------------------------------------------
	healthHandler := handler.NewHealthHandler(pool)

	r.GET("/health", healthHandler.Live)
	r.GET("/health/ready", healthHandler.Ready)

	// -------------------------------------------------------------------------
	// Routes — API v1 (placeholder, filled in subsequent steps)
	// -------------------------------------------------------------------------
	// v1 := r.Group("/api/v1")
	// { ... }

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

	// Start server in a goroutine so we can listen for OS signals.
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", slog.String("error", err.Error()))
	}

	slog.Info("server stopped")
}

// requestLogger returns a Gin middleware that emits a structured log line for
// every HTTP request. It must run after the RequestID middleware so the ID is
// available in the context.
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		// Retrieve request ID set by the RequestID middleware.
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
