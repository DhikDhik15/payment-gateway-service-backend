package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type phase8D5AuditCapture struct {
	event audit.Event
	err   error
}

func (c *phase8D5AuditCapture) Record(_ context.Context, event audit.Event) error {
	c.event = event
	return c.err
}

func (c *phase8D5AuditCapture) RecordInTx(_ context.Context, _ pgx.Tx, event audit.Event) error {
	c.event = event
	return c.err
}

func (c *phase8D5AuditCapture) RecordBestEffort(ctx context.Context, event audit.Event) error {
	return c.Record(ctx, event)
}

func TestAdminAuthRecordsFailureWithoutSecretAndPreserves401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capture := &phase8D5AuditCapture{}
	r := gin.New()
	r.Use(RequestID())
	r.GET("/admin/test", AdminAuth("correct-admin-key", capture), func(c *gin.Context) {
		c.Status(200)
	})

	req := httptest.NewRequest("GET", "/admin/test", nil)
	req.RemoteAddr = "198.51.100.30:1234"
	req.Header.Set("X-Admin-Key", "TEST_ADMIN_SECRET_999")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != 401 {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
	if capture.event.Action != audit.ActionAdminAuthFailed {
		t.Fatalf("action = %q, want %q", capture.event.Action, audit.ActionAdminAuthFailed)
	}
	if capture.event.ActorType != audit.ActorTypeUnauthenticated {
		t.Fatalf("actor type = %q, want %q", capture.event.ActorType, audit.ActorTypeUnauthenticated)
	}
	if capture.event.RequestID == nil || *capture.event.RequestID == "" {
		t.Fatal("failed admin audit has no request ID")
	}
	if capture.event.IP == nil || *capture.event.IP != "198.51.100.30" {
		t.Fatalf("failed admin audit IP = %v", capture.event.IP)
	}
	if string(capture.event.Metadata) == "" || containsAuditSecret(string(capture.event.Metadata), "TEST_ADMIN_SECRET_999") {
		t.Fatalf("audit metadata contains admin secret: %s", capture.event.Metadata)
	}
}

func TestAdminAuthRecordsMissingWrongAndMalformedCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		header     string
		wantReason string
	}{
		{name: "missing", header: "", wantReason: "missing_key"},
		{name: "wrong", header: "wrong-admin-secret", wantReason: "invalid_key"},
		{name: "merchant key", header: "pk_merchant:sk_wrong", wantReason: "invalid_key"},
		{name: "malformed", header: "Bearer malformed", wantReason: "invalid_key"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			capture := &phase8D5AuditCapture{}
			r := gin.New()
			r.Use(RequestID())
			r.POST("/admin/test", AdminAuth("correct-admin-key", capture), func(c *gin.Context) { c.Status(200) })
			req := httptest.NewRequest("POST", "/admin/test", nil)
			if tt.header != "" {
				req.Header.Set("X-Admin-Key", tt.header)
			}
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)
			if resp.Code != 401 {
				t.Fatalf("status = %d, want 401", resp.Code)
			}
			if capture.event.Action != audit.ActionAdminAuthFailed ||
				capture.event.ActorType != audit.ActorTypeUnauthenticated ||
				!strings.Contains(string(capture.event.Metadata), tt.wantReason) {
				t.Fatalf("audit event = %#v, want unauthenticated ADMIN_AUTH_FAILED reason %q", capture.event, tt.wantReason)
			}
		})
	}
}

func TestAdminAuthAuditFailureDoesNotChangeAuthenticationResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capture := &phase8D5AuditCapture{err: errors.New("audit unavailable")}
	r := gin.New()
	r.Use(RequestID())
	r.GET("/admin/test", AdminAuth("correct-admin-key", capture), func(c *gin.Context) {
		c.Status(200)
	})

	req := httptest.NewRequest("GET", "/admin/test", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != 401 {
		t.Fatalf("status = %d, want 401 when audit write fails", resp.Code)
	}
}

func TestAdminAuthSetsAdminActorWithoutInventingUserIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capture := &phase8D5AuditCapture{}
	r := gin.New()
	r.Use(RequestID())
	r.GET("/admin/test", AdminAuth("correct-admin-key", capture), func(c *gin.Context) {
		actor := audit.ActorFromContext(c.Request.Context())
		if actor.Type != audit.ActorTypeAdmin || actor.UserID != nil {
			t.Errorf("actor = %#v, want ADMIN with nil user ID", actor)
		}
		c.Status(200)
	})

	req := httptest.NewRequest("GET", "/admin/test", nil)
	req.Header.Set("X-Admin-Key", "correct-admin-key")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != 200 {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
}

func containsAuditSecret(value, secret string) bool {
	return len(secret) > 0 && len(value) >= len(secret) && stringContains(value, secret)
}

func stringContains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
