package middleware

import (
	"net/http"

	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// MaxBodyBytes caps the request body size for every route (Phase 8D.2).
//
// Two layers, neither of which consumes the body before handlers need it:
//
//  1. When the client DECLARES a Content-Length above the limit the request
//     is rejected immediately with 413 REQUEST_TOO_LARGE — no handler or
//     route middleware runs.
//  2. The body is wrapped in http.MaxBytesReader so chunked/undeclared
//     bodies are hard-capped while the handler reads: binding fails with
//     *http.MaxBytesError and the standard 400 path reports it
//     (parseBindingErrors maps it to a stable message). Oversized bytes
//     never reach business logic either way.
//
// max <= 0 disables the middleware — a defensive branch only; config
// validation (parseHTTPConfig) rejects non-positive HTTP_MAX_BODY_BYTES at
// startup, so this is unreachable in a running deployment.
func MaxBodyBytes(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if max <= 0 {
			c.Next()
			return
		}
		if c.Request != nil && c.Request.ContentLength > max {
			response.PayloadTooLarge(c, response.CodeRequestTooLarge, "Request body is too large")
			c.Abort()
			return
		}
		if c.Request != nil && c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}
