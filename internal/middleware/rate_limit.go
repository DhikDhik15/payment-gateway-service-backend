package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/ratelimit"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
)

// loginEmailPeekLimit bounds how much of the request body the login limiter
// reads to derive the per-account key. Bodies larger than this skip the
// account bucket (the per-IP bucket still applies) and are left untouched for
// the handler — peeking must never grow unbounded or corrupt the body.
const loginEmailPeekLimit = 64 * 1024

// rejectTooManyRequests aborts the request with the standard error envelope
// (code RATE_LIMIT_EXCEEDED) plus a Retry-After hint.
//
// The limiter key is deliberately never logged or echoed: login keys contain
// an account identifier.
func rejectTooManyRequests(c *gin.Context, window time.Duration) {
	secs := int(window.Seconds())
	if secs < 1 {
		secs = 1
	}
	c.Header("Retry-After", strconv.Itoa(secs))
	response.TooManyRequests(c, "Too many requests, please retry later")
	c.Abort()
}

// RateLimit returns middleware enforcing one limiter budget per derived key.
//
//   - nil limiter (RATE_LIMIT_ENABLED=false) ⇒ pass-through: requests are
//     never blocked.
//   - Empty derived key ⇒ skip limiting for this request (key derivation
//     failure, e.g. merchant context absent; the pre-authentication IP
//     limiter already covers such requests).
//
// window is only used for the Retry-After response header.
func RateLimit(l ratelimit.RateLimiter, window time.Duration, keyFn func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if l == nil {
			c.Next()
			return
		}
		key := keyFn(c)
		if key == "" {
			c.Next()
			return
		}
		if !l.Allow(key) {
			rejectTooManyRequests(c, window)
			return
		}
		c.Next()
	}
}

// ClientIPRateLimit keys the limiter by the request's client IP under a
// namespace prefix (so admin traffic and merchant-API traffic have separate
// budgets on their respective limiters).
//
// Used PRE-authentication so unauthenticated and rejected credential
// attempts are counted too (brute-force protection), never keyed by anything
// the client claims to be.
func ClientIPRateLimit(l ratelimit.RateLimiter, window time.Duration, prefix string) gin.HandlerFunc {
	return RateLimit(l, window, func(c *gin.Context) string {
		return prefix + ":ip:" + c.ClientIP()
	})
}

// MerchantRateLimit keys the limiter by the AUTHENTICATED merchant placed in
// the context by Auth — never by a client-supplied merchant_id. Register it
// AFTER Auth. Requests without a merchant in context skip this layer (they
// were already counted by the pre-authentication IP limiter).
func MerchantRateLimit(l ratelimit.RateLimiter, window time.Duration, prefix string) gin.HandlerFunc {
	return RateLimit(l, window, func(c *gin.Context) string {
		m := MerchantFromContext(c)
		if m == nil {
			return ""
		}
		return prefix + ":merchant:" + m.ID.String()
	})
}

// LoginRateLimit protects the login endpoint with two budgets on the same
// limiter:
//
//   - login:ip:<client IP>      — slows distributed password guessing
//   - login:account:<email>     — slows guessing against ONE account from
//     many IPs (the account is read from the JSON body; normalised
//     trim+lowercase exactly like the Login service)
//
// The body is peeked (bounded read) and then RESTORED, so the handler still
// binds it normally. Keys derived here are never logged or echoed.
func LoginRateLimit(l ratelimit.RateLimiter, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if l == nil {
			c.Next()
			return
		}
		if !l.Allow("login:ip:" + c.ClientIP()) {
			rejectTooManyRequests(c, window)
			return
		}
		if email := peekLoginEmail(c); email != "" {
			if !l.Allow("login:account:" + email) {
				rejectTooManyRequests(c, window)
				return
			}
		}
		c.Next()
	}
}

// peekLoginEmail reads a bounded prefix of the request body looking for an
// "email" field and restores the body (including any unread remainder) so
// downstream binding is unaffected. Returns "" when there is no body, the
// body is not JSON, or it exceeds the peek limit (IP limiting still applies).
func peekLoginEmail(c *gin.Context) string {
	if c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return ""
	}
	// Bounded read: never buffer an arbitrarily large body here.
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, loginEmailPeekLimit))
	// Restore FIRST (even on a read error): peeked bytes first, then whatever
	// the LimitedReader did not consume — the handler sees the original stream.
	c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), c.Request.Body))
	if err != nil {
		return ""
	}

	var probe struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "" // not (complete) JSON — handler will 400; IP bucket applies
	}
	return strings.ToLower(strings.TrimSpace(probe.Email))
}
