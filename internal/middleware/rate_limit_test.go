package middleware_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/ratelimit"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// fakeTestClock is a manually advanced clock so window tests never sleep.
type fakeTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeTestClock() *fakeTestClock {
	return &fakeTestClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newLimitedRouter mounts a single GET route behind the given middleware.
func newLimitedRouter(t *testing.T, mw gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(mw)
	r.GET("/x", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

// do performs a request from a specific client IP.
func do(r *gin.Engine, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// doPostJSON performs a POST with a JSON body from a specific client IP.
func doPostJSON(r *gin.Engine, ip, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":40000"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func envelope(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse response %q: %v", w.Body.String(), err)
	}
	return body
}

// ─── generic RateLimit / client-IP tests ─────────────────────────────────────

func TestRateLimit_ClientIP_BlocksOverLimitWithEnvelope(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(3, time.Minute, clk.Now)
	r := newLimitedRouter(t, middleware.ClientIPRateLimit(l, time.Minute, "api"))

	for i := 1; i <= 3; i++ {
		if w := do(r, "10.1.1.1"); w.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i, w.Code)
		}
	}

	w := do(r, "10.1.1.1")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("request 4: got %d, want 429", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "60" {
		t.Errorf("Retry-After: got %q, want 60", ra)
	}

	body := envelope(t, w)
	if body["success"] != false {
		t.Errorf("success: got %v, want false", body["success"])
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "RATE_LIMIT_EXCEEDED" {
		t.Errorf("error.code: got %v, want RATE_LIMIT_EXCEEDED", errObj)
	}
	// Existing envelope shape: meta.request_id must be present.
	meta, _ := body["meta"].(map[string]any)
	if meta == nil || meta["request_id"] == "" {
		t.Errorf("meta.request_id missing from envelope: %v", body)
	}
}

func TestRateLimit_WindowResetsWithoutSleeping(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(1, time.Minute, clk.Now)
	r := newLimitedRouter(t, middleware.ClientIPRateLimit(l, time.Minute, "api"))

	if w := do(r, "10.2.2.2"); w.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", w.Code)
	}
	if w := do(r, "10.2.2.2"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", w.Code)
	}

	clk.Advance(time.Minute)
	if w := do(r, "10.2.2.2"); w.Code != http.StatusOK {
		t.Fatalf("request after window reset: got %d, want 200", w.Code)
	}
}

func TestRateLimit_NilLimiterNeverBlocks(t *testing.T) {
	// RATE_LIMIT_ENABLED=false ⇒ main.go builds no limiter ⇒ nil passes through.
	r := newLimitedRouter(t, middleware.ClientIPRateLimit(nil, time.Minute, "api"))

	for i := 0; i < 10; i++ {
		if w := do(r, "10.3.3.3"); w.Code != http.StatusOK {
			t.Fatalf("request %d with disabled limiter: got %d, want 200", i, w.Code)
		}
	}
}

func TestRateLimit_ClientIPUsesOnlyTrustedForwardedHeader(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(1, time.Minute, clk.Now)
	r := newLimitedRouter(t, middleware.ClientIPRateLimit(l, time.Minute, "api"))
	if err := r.SetTrustedProxies([]string{"192.0.2.10"}); err != nil {
		t.Fatal(err)
	}

	doForwarded := func(remote, forwarded string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = remote + ":40000"
		req.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if got := doForwarded("192.0.2.10", "203.0.113.10").Code; got != http.StatusOK {
		t.Fatalf("trusted first request = %d, want 200", got)
	}
	if got := doForwarded("192.0.2.10", "203.0.113.10").Code; got != http.StatusTooManyRequests {
		t.Fatalf("trusted second request = %d, want 429", got)
	}
	// An untrusted socket peer cannot spend the trusted proxy's client bucket
	// by supplying a different forwarded address.
	if got := doForwarded("198.51.100.30", "203.0.113.10").Code; got != http.StatusOK {
		t.Fatalf("untrusted source request = %d, want independent 200", got)
	}
}

func TestRateLimit_DifferentClientIPsAreIsolated(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(1, time.Minute, clk.Now)
	r := newLimitedRouter(t, middleware.ClientIPRateLimit(l, time.Minute, "admin"))

	if w := do(r, "10.4.4.4"); w.Code != http.StatusOK {
		t.Fatalf("ip A first: got %d, want 200", w.Code)
	}
	if w := do(r, "10.4.4.4"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("ip A second: got %d, want 429", w.Code)
	}
	// A different IP has its own admin budget.
	if w := do(r, "10.5.5.5"); w.Code != http.StatusOK {
		t.Fatalf("ip B first: got %d, want 200", w.Code)
	}
}

func TestRateLimit_ConcurrentRequestsExceedingBudget(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(10, time.Minute, clk.Now)
	r := newLimitedRouter(t, middleware.ClientIPRateLimit(l, time.Minute, "api"))

	const goroutines = 100
	var (
		wg     sync.WaitGroup
		ok     atomic.Int64
		denied atomic.Int64
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			switch do(r, "10.6.6.6").Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := ok.Load(); got != 10 {
		t.Errorf("200s: got %d, want exactly 10", got)
	}
	if got := denied.Load(); got != goroutines-10 {
		t.Errorf("429s: got %d, want %d", got, goroutines-10)
	}
}

// ─── login (IP + account) tests ──────────────────────────────────────────────

// newLoginRouter mounts a login-style route whose handler echoes the email it
// binds from the body — proving the limiter's body peek restores the stream.
func newLoginRouter(t *testing.T, l ratelimit.RateLimiter, window time.Duration) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.LoginRateLimit(l, window))
	r.POST("/login", func(c *gin.Context) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false})
			return
		}
		// Echo only in the test response — real handlers never echo passwords.
		c.JSON(http.StatusOK, gin.H{"email": body.Email})
	})
	return r
}

func TestLoginRateLimit_SameAccountFromDifferentIPsBlocked(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(3, time.Minute, clk.Now)
	r := newLoginRouter(t, l, time.Minute)

	body := `{"email":"victim@example.com","password":"x"}`
	// Three different IPs attacking ONE account: the account bucket stops them.
	for i := 1; i <= 3; i++ {
		ip := fmt.Sprintf("10.20.0.%d", i)
		if w := doPostJSON(r, ip, "/login", body); w.Code != http.StatusOK {
			t.Fatalf("attempt %d from %s: got %d, want 200", i, ip, w.Code)
		}
	}
	w := doPostJSON(r, "10.20.0.4", "/login", body)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt on same account: got %d, want 429", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "60" {
		t.Errorf("Retry-After: got %q, want 60", ra)
	}
	// The account identifier must never be echoed in the error response.
	if strings.Contains(w.Body.String(), "victim@example.com") {
		t.Error("429 response must not echo the account identifier")
	}
}

func TestLoginRateLimit_SameIPAcrossAccountsBlocked(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(3, time.Minute, clk.Now)
	r := newLoginRouter(t, l, time.Minute)

	// One IP spraying MANY accounts: the IP bucket stops them.
	for i := 1; i <= 3; i++ {
		body := fmt.Sprintf(`{"email":"user%d@example.com","password":"x"}`, i)
		if w := doPostJSON(r, "10.30.0.1", "/login", body); w.Code != http.StatusOK {
			t.Fatalf("attempt %d: got %d, want 200", i, w.Code)
		}
	}
	body := `{"email":"user4@example.com","password":"x"}`
	if w := doPostJSON(r, "10.30.0.1", "/login", body); w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt from same IP: got %d, want 429", w.Code)
	}
	// A different IP is unaffected (bucket isolation).
	body2 := `{"email":"someone@example.com","password":"x"}`
	if w := doPostJSON(r, "10.30.0.2", "/login", body2); w.Code != http.StatusOK {
		t.Fatalf("fresh IP: got %d, want 200", w.Code)
	}
}

func TestLoginRateLimit_BodyStillReadableByHandler(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(5, time.Minute, clk.Now)
	r := newLoginRouter(t, l, time.Minute)

	// Account key is normalised (trim+lower) exactly like the Login service,
	// and the handler must still receive the FULL original body.
	body := `{"email":"  Mixed.Case@Example.COM ","password":"secret"}`
	w := doPostJSON(r, "10.40.0.1", "/login", body)
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d, want 200 (body must be restored after peek)", w.Code)
	}
	got := envelope(t, w)
	if got["email"] != "  Mixed.Case@Example.COM " {
		t.Errorf("handler received corrupted body: got %v", got["email"])
	}

	// Exceed the account budget under the NORMALISED key from other IPs.
	for i := 2; i <= 5; i++ {
		ip := fmt.Sprintf("10.40.0.%d", i)
		if w := doPostJSON(r, ip, "/login", body); w.Code != http.StatusOK {
			t.Fatalf("attempt %d: got %d, want 200", i, w.Code)
		}
	}
	w = doPostJSON(r, "10.40.0.6", "/login", body)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("6th attempt: got %d, want 429", w.Code)
	}
}

func TestLoginRateLimit_NilLimiterNeverBlocks(t *testing.T) {
	r := newLoginRouter(t, nil, time.Minute)
	for i := 0; i < 10; i++ {
		body := `{"email":"dev@example.com","password":"x"}`
		if w := doPostJSON(r, "10.50.0.1", "/login", body); w.Code != http.StatusOK {
			t.Fatalf("attempt %d with disabled limiter: got %d, want 200", i, w.Code)
		}
	}
}

// ─── merchant (post-auth identity) tests ─────────────────────────────────────

// newMerchantRouter simulates main.go ordering: Auth first (test-only context
// injection stands in for it), then the merchant-keyed limiter, then a handler.
func newMerchantRouter(t *testing.T, l ratelimit.RateLimiter) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	// Test stand-in for Auth: inject an authenticated merchant from a header.
	r.Use(func(c *gin.Context) {
		if raw := c.GetHeader("X-Test-Merchant"); raw != "" {
			c.Set(model.ContextKeyMerchant, &model.Merchant{ID: uuid.MustParse(raw)})
		}
		c.Next()
	})
	r.Use(middleware.MerchantRateLimit(l, time.Minute, "api"))
	r.GET("/m", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func doMerchant(r *gin.Engine, ip, merchantID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/m", nil)
	req.RemoteAddr = ip + ":40000"
	if merchantID != "" {
		req.Header.Set("X-Test-Merchant", merchantID)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMerchantRateLimit_KeysByAuthenticatedMerchantNotClient(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(2, time.Minute, clk.Now)
	r := newMerchantRouter(t, l)

	merchantA := uuid.New().String()
	merchantB := uuid.New().String()

	// Two DIFFERENT client IPs must share one budget for the SAME merchant…
	if w := doMerchant(r, "10.60.0.1", merchantA); w.Code != http.StatusOK {
		t.Fatalf("merchant A req 1: got %d, want 200", w.Code)
	}
	if w := doMerchant(r, "10.60.0.2", merchantA); w.Code != http.StatusOK {
		t.Fatalf("merchant A req 2: got %d, want 200", w.Code)
	}
	if w := doMerchant(r, "10.60.0.3", merchantA); w.Code != http.StatusTooManyRequests {
		t.Fatalf("merchant A req 3: got %d, want 429 (identity is the merchant, not the IP)", w.Code)
	}
	// …while another merchant on the SAME IP keeps its own budget.
	if w := doMerchant(r, "10.60.0.3", merchantB); w.Code != http.StatusOK {
		t.Fatalf("merchant B on same IP: got %d, want 200", w.Code)
	}
}

func TestMerchantRateLimit_SkipsWithoutAuthenticatedMerchant(t *testing.T) {
	clk := newFakeTestClock()
	l := ratelimit.NewFixedWindowWithClock(1, time.Minute, clk.Now)
	r := newMerchantRouter(t, l)

	// No merchant in context: the merchant layer skips (pre-auth IP layer,
	// registered before Auth in main.go, is the one that counts these).
	for i := 0; i < 3; i++ {
		if w := doMerchant(r, "10.70.0.1", ""); w.Code != http.StatusOK {
			t.Fatalf("request %d without merchant context: got %d, want 200 (skip)", i, w.Code)
		}
	}
}
