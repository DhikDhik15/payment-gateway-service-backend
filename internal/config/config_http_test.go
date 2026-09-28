package config

// config_http_test.go — Phase 8D.2 tests for parseHTTPConfig (server timeout
// profile + body limit defaults, explicit values, fail-closed validation) and
// parseTrustedProxies (explicit X-Forwarded-For trust policy).

import (
	"strings"
	"testing"
	"time"
)

func clearHTTPEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HTTP_MAX_BODY_BYTES",
		"HTTP_READ_HEADER_TIMEOUT",
		"HTTP_READ_TIMEOUT",
		"HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT",
	} {
		t.Setenv(k, "")
	}
}

func clearTrustedProxyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TRUSTED_PROXIES", "")
}

func TestParseHTTPConfig_Defaults(t *testing.T) {
	clearHTTPEnv(t)

	cfg, err := parseHTTPConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxBodyBytes != 1048576 {
		t.Errorf("MaxBodyBytes = %d, want 1048576 (1 MiB)", cfg.MaxBodyBytes)
	}
	if cfg.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", cfg.ReadHeaderTimeout)
	}
	if cfg.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %v, want 15s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 15*time.Second {
		t.Errorf("WriteTimeout = %v, want 15s", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", cfg.IdleTimeout)
	}
	// ReadHeaderTimeout is the new slow-header bound — it must be positive.
	if cfg.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be > 0")
	}
}

func TestParseHTTPConfig_ExplicitValues(t *testing.T) {
	clearHTTPEnv(t)
	t.Setenv("HTTP_MAX_BODY_BYTES", "2097152")
	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "5s")
	t.Setenv("HTTP_READ_TIMEOUT", "30s")
	t.Setenv("HTTP_WRITE_TIMEOUT", "30s")
	t.Setenv("HTTP_IDLE_TIMEOUT", "120s")

	cfg, err := parseHTTPConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxBodyBytes != 2097152 ||
		cfg.ReadHeaderTimeout != 5*time.Second ||
		cfg.ReadTimeout != 30*time.Second ||
		cfg.WriteTimeout != 30*time.Second ||
		cfg.IdleTimeout != 120*time.Second {
		t.Errorf("cfg = %+v, want 2097152/5s/30s/30s/120s", cfg)
	}
}

// A zero, negative, or unparseable value must fail startup — it can never
// silently mean "no limit / no timeout".
func TestParseHTTPConfig_RejectsInvalidValues(t *testing.T) {
	cases := []struct{ name, value string }{
		{"HTTP_MAX_BODY_BYTES", "0"},
		{"HTTP_MAX_BODY_BYTES", "-1"},
		{"HTTP_MAX_BODY_BYTES", "unlimited"},
		{"HTTP_READ_HEADER_TIMEOUT", "0s"},
		{"HTTP_READ_HEADER_TIMEOUT", "-1s"},
		{"HTTP_READ_HEADER_TIMEOUT", "soon"},
		{"HTTP_READ_TIMEOUT", "0s"},
		{"HTTP_WRITE_TIMEOUT", "-5s"},
		{"HTTP_IDLE_TIMEOUT", "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearHTTPEnv(t)
			t.Setenv(tc.name, tc.value)

			_, err := parseHTTPConfig()
			if err == nil {
				t.Fatalf("expected error for %s=%s", tc.name, tc.value)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("error must name %s, got: %v", tc.name, err)
			}
		})
	}
}

func TestParseTrustedProxies_Unset(t *testing.T) {
	clearTrustedProxyEnv(t)

	proxies, err := parseTrustedProxies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proxies != nil {
		t.Fatalf("proxies = %#v, want nil (not configured → gin default + production warning)", proxies)
	}
}

func TestParseTrustedProxies_None(t *testing.T) {
	clearTrustedProxyEnv(t)
	t.Setenv("TRUSTED_PROXIES", "none")

	proxies, err := parseTrustedProxies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proxies == nil || len(proxies) != 0 {
		t.Fatalf("proxies = %#v, want non-nil empty list (trust no proxy)", proxies)
	}
}

func TestParseTrustedProxies_List(t *testing.T) {
	clearTrustedProxyEnv(t)
	t.Setenv("TRUSTED_PROXIES", " 10.0.0.0/8 , 203.0.113.10 ")

	proxies, err := parseTrustedProxies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(proxies) != 2 || proxies[0] != "10.0.0.0/8" || proxies[1] != "203.0.113.10" {
		t.Fatalf("proxies = %#v, want [10.0.0.0/8 203.0.113.10]", proxies)
	}
}

func TestParseTrustedProxies_RejectsEntrylessValue(t *testing.T) {
	clearTrustedProxyEnv(t)
	t.Setenv("TRUSTED_PROXIES", " , ,")

	if _, err := parseTrustedProxies(); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
		t.Fatalf("expected TRUSTED_PROXIES error, got %v", err)
	}
}
