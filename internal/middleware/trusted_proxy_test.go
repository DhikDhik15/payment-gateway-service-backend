package middleware_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTrustedProxyClientIPSemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		proxies    []string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{
			name:       "trusted proxy forwards client address",
			proxies:    []string{"192.0.2.10"},
			remoteAddr: "192.0.2.10:4312",
			forwarded:  "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "untrusted source cannot spoof forwarded address",
			proxies:    []string{"192.0.2.10"},
			remoteAddr: "198.51.100.20:4312",
			forwarded:  "203.0.113.9",
			want:       "198.51.100.20",
		},
		{
			name:       "none ignores forwarded address",
			proxies:    []string{},
			remoteAddr: "198.51.100.21:4312",
			forwarded:  "203.0.113.9",
			want:       "198.51.100.21",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			if err := router.SetTrustedProxies(tc.proxies); err != nil {
				t.Fatalf("SetTrustedProxies: %v", err)
			}
			router.GET("/", func(c *gin.Context) {
				c.String(200, c.ClientIP())
			})

			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.forwarded)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			if got := recorder.Body.String(); got != tc.want {
				t.Fatalf("ClientIP response = %q, want %q", got, tc.want)
			}
		})
	}
}
