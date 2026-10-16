package config

// config_bind_host_test.go — APP_BIND_HOST (optional HTTP bind interface):
// defaults preserve the historical ":port" behaviour, 127.0.0.1 restricts
// the listener to localhost, and malformed values fail fast at startup.

import (
	"strings"
	"testing"
)

func clearBindHostEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_BIND_HOST", "")
}

func TestParseBindHost_UnsetPreservesWildcardBehaviour(t *testing.T) {
	clearBindHostEnv(t)

	host, err := parseBindHost()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "" {
		t.Fatalf("host = %q, want empty (unset → bind all interfaces)", host)
	}
}

func TestParseBindHost_LoopbackAccepted(t *testing.T) {
	clearBindHostEnv(t)
	t.Setenv("APP_BIND_HOST", "127.0.0.1")

	host, err := parseBindHost()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q, want 127.0.0.1", host)
	}
}

func TestParseBindHost_TrimsWhitespace(t *testing.T) {
	clearBindHostEnv(t)
	t.Setenv("APP_BIND_HOST", "  127.0.0.1  ")

	host, err := parseBindHost()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q, want trimmed 127.0.0.1", host)
	}
}

func TestParseBindHost_RejectsHostPortAndURLValues(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1:8080",   // port belongs to APP_PORT
		"localhost:8080",   // port belongs to APP_PORT
		"http://127.0.0.1", // scheme is never part of a bind host
		"127.0.0.1/",       // path is never part of a bind host
		"127.0.0.1?x=1",    // query is never part of a bind host
		"127.0.0.1#frag",   // fragment is never part of a bind host
		"127.0.0.1 8080",   // whitespace is never valid
		"user@127.0.0.1",   // userinfo is never part of a bind host
	} {
		clearBindHostEnv(t)
		t.Setenv("APP_BIND_HOST", raw)
		if _, err := parseBindHost(); err == nil || !strings.Contains(err.Error(), "APP_BIND_HOST") {
			t.Errorf("APP_BIND_HOST=%q: err = %v, want APP_BIND_HOST error", raw, err)
		}
	}
}

func TestBuildListenAddr_PreservesWildcardWhenHostUnset(t *testing.T) {
	if got, want := BuildListenAddr("", "8080"), ":8080"; got != want {
		t.Fatalf("BuildListenAddr = %q, want %q (historical behaviour)", got, want)
	}
}

func TestBuildListenAddr_BindsLoopbackWhenConfigured(t *testing.T) {
	if got, want := BuildListenAddr("127.0.0.1", "8080"), "127.0.0.1:8080"; got != want {
		t.Fatalf("BuildListenAddr = %q, want %q", got, want)
	}
}

func TestBuildListenAddr_UsesConfiguredPortWithoutHardcoding(t *testing.T) {
	if got, want := BuildListenAddr("127.0.0.1", "18080"), "127.0.0.1:18080"; got != want {
		t.Fatalf("BuildListenAddr = %q, want %q (port must come from APP_PORT)", got, want)
	}
}

func TestBuildListenAddr_BracketsIPv6(t *testing.T) {
	if got, want := BuildListenAddr("::1", "8080"), "[::1]:8080"; got != want {
		t.Fatalf("BuildListenAddr = %q, want %q", got, want)
	}
}

func TestLoad_BindHostDefaultsToWildcard(t *testing.T) {
	baseLoadEnv(t)
	clearBindHostEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.BindHost != "" {
		t.Fatalf("BindHost = %q, want empty (unset → :port)", cfg.App.BindHost)
	}
	if got, want := BuildListenAddr(cfg.App.BindHost, cfg.App.Port), ":"+cfg.App.Port; got != want {
		t.Fatalf("listen addr = %q, want %q", got, want)
	}
}

func TestLoad_BindHostLoopbackRestrictsListener(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_BIND_HOST", "127.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.BindHost != "127.0.0.1" {
		t.Fatalf("BindHost = %q, want 127.0.0.1", cfg.App.BindHost)
	}
	if got, want := BuildListenAddr(cfg.App.BindHost, cfg.App.Port), "127.0.0.1:"+cfg.App.Port; got != want {
		t.Fatalf("listen addr = %q, want %q", got, want)
	}
}

func TestLoad_RejectsInvalidBindHost(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_BIND_HOST", "127.0.0.1:8080")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_BIND_HOST") {
		t.Fatalf("Load() err = %v, want APP_BIND_HOST error", err)
	}
}
