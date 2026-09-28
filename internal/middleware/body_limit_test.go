package middleware_test

// body_limit_test.go — Phase 8D.2 request body limit tests: under limit,
// exactly at limit, over limit with a declared Content-Length (immediate 413),
// over limit without a declared length (chunked → hard-capped at bind time),
// and pass-through behaviour for bodyless/zero-limit requests.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

const testBodyLimit = 1024

type bodyLimitReq struct {
	Msg string `json:"msg"`
}

type errorEnvelope struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

// newBodyLimitRouter builds a router with the body cap and a handler that
// mimics the API contract: bind → mutate → 200. `handled` records whether
// business logic ever saw an oversized body.
func newBodyLimitRouter(limit int64, handled *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.MaxBodyBytes(limit))
	r.POST("/echo", func(c *gin.Context) {
		var req bodyLimitReq
		if err := c.ShouldBindJSON(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				// Mirrors parseBindingErrors' stable message for a capped read.
				response.ValidationError(c, map[string]string{"_": "Request body exceeds the configured size limit"})
				return
			}
			response.ValidationError(c, map[string]string{"_": "invalid body"})
			return
		}
		*handled = true
		response.OK(c, gin.H{"msg": req.Msg})
	})
	r.GET("/ping", func(c *gin.Context) {
		*handled = true
		response.OK(c, gin.H{"pong": true})
	})
	return r
}

func postJSON(r *gin.Engine, body string, declaredLength bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if !declaredLength {
		// Simulate chunked / unknown-length bodies.
		req.ContentLength = -1
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMaxBodyBytes_SmallJSONSucceeds(t *testing.T) {
	handled := false
	r := newBodyLimitRouter(testBodyLimit, &handled)

	w := postJSON(r, `{"msg":"hello"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", w.Code, w.Body.String())
	}
	if !handled {
		t.Fatal("handler did not run for an under-limit body")
	}
}

func TestMaxBodyBytes_ExactlyAtLimitSucceeds(t *testing.T) {
	handled := false
	r := newBodyLimitRouter(testBodyLimit, &handled)

	// {"msg":"…"} is 9 bytes of overhead — pad to exactly the limit.
	padding := strings.Repeat("a", testBodyLimit-len(`{"msg":""}`))
	body := `{"msg":"` + padding + `"}`
	if len(body) != testBodyLimit {
		t.Fatalf("test body = %d bytes, want exactly %d", len(body), testBodyLimit)
	}

	w := postJSON(r, body, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200 (body exactly at limit)", w.Code, w.Body.String())
	}
	if !handled {
		t.Fatal("handler did not run for a body exactly at the limit")
	}
}

func TestMaxBodyBytes_DeclaredOversizeRejectedWith413(t *testing.T) {
	handled := false
	r := newBodyLimitRouter(testBodyLimit, &handled)

	body := `{"msg":"` + strings.Repeat("a", testBodyLimit) + `"}"` // over the limit
	w := postJSON(r, body, true)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body = %s, want 413", w.Code, w.Body.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not the standard error envelope: %v", err)
	}
	if env.Success || env.Error.Code != string(response.CodeRequestTooLarge) {
		t.Fatalf("envelope = %+v, want success=false code=%s", env.Error, response.CodeRequestTooLarge)
	}
	if handled {
		t.Fatal("business logic ran for an oversized body")
	}
}

func TestMaxBodyBytes_ChunkedOversizeHardCappedAtBind(t *testing.T) {
	handled := false
	r := newBodyLimitRouter(testBodyLimit, &handled)

	// No declared Content-Length: MaxBytesReader must cap the read itself.
	body := `{"msg":"` + strings.Repeat("b", testBodyLimit*2) + `"}`
	w := postJSON(r, body, false)
	if w.Code/100 != 4 {
		t.Fatalf("status = %d body = %s, want a 4xx client error", w.Code, w.Body.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not the standard error envelope: %v", err)
	}
	if env.Success {
		t.Fatal("oversized chunked body reported success")
	}
	if handled {
		t.Fatal("business logic ran for an oversized chunked body")
	}
}

func TestMaxBodyBytes_BodylessRequestsUnaffected(t *testing.T) {
	handled := false
	r := newBodyLimitRouter(testBodyLimit, &handled)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !handled {
		t.Fatalf("GET status = %d handled = %v, want 200 and handler ran", w.Code, handled)
	}
}
