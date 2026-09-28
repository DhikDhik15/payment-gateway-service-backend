package main

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

func TestPhase8D5RequestLoggerDoesNotPersistPathBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(requestLogger())
	const secret = "TEST_INVITATION_SECRET_ABC"
	router.GET("/api/v1/invitations/:token", func(c *gin.Context) { c.Status(204) })

	req := httptest.NewRequest("GET", "/api/v1/invitations/"+secret, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != 204 {
		t.Fatalf("status = %d, want 204", resp.Code)
	}
	text := logs.String()
	if strings.Contains(text, secret) {
		t.Fatalf("request log contains invitation bearer token: %s", text)
	}
	if !strings.Contains(text, "/api/v1/invitations/:token") {
		t.Fatalf("request log does not contain safe route template: %s", text)
	}
}
