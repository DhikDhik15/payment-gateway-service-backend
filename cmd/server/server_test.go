package main

// server_test.go — Phase 8D.2: the assembled http.Server must carry a
// non-zero timeout profile, including the previously missing
// ReadHeaderTimeout (slow-header protection).

import (
	"net/http"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/config"
)

func TestWaitForWorkerUntilHonorsDeadline(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	start := time.Now()
	waitForWorkerUntil("test", func() {
		<-release
		close(finished)
	}, time.After(10*time.Millisecond))
	if time.Since(start) > time.Second {
		t.Fatal("worker wait exceeded bounded shutdown deadline")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("test worker did not finish after release")
	}
}

func TestWaitForWorkerUntilReturnsAfterCompletion(t *testing.T) {
	called := false
	waitForWorkerUntil("test", func() { called = true }, time.After(time.Second))
	if !called {
		t.Fatal("worker wait function was not called")
	}
}

func TestPaymentProviderFromConfigRejectsMockInProduction(t *testing.T) {
	cfg := &config.Config{
		App:      config.AppConfig{Env: "production"},
		Provider: config.ProviderConfig{Name: "mock"},
	}
	if _, err := paymentProviderFromConfig(cfg); err == nil {
		t.Fatal("mock payment provider was accepted in production")
	}
}

func TestNewHTTPServer_TimeoutsConfigured(t *testing.T) {
	hc := config.HTTPConfig{
		MaxBodyBytes:      1 << 20,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	mux := http.NewServeMux()

	srv := newHTTPServer(":8081", mux, hc)

	if srv.Addr != ":8081" {
		t.Errorf("Addr = %q, want :8081", srv.Addr)
	}
	if srv.Handler != mux {
		t.Error("Handler was not wired through")
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be > 0 (slow-header protection)")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be > 0")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout must be > 0")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be > 0")
	}
	if srv.ReadHeaderTimeout != hc.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, hc.ReadHeaderTimeout)
	}
}
