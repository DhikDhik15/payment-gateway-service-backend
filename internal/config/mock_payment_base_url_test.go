package config

import "testing"

func TestLoad_FrontendPublicURLIsRespected(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_PORT", "8080")
	t.Setenv("FRONTEND_PUBLIC_URL", "http://localhost:5173")
	// The new setting must win over the old compatibility alias.
	t.Setenv("MOCK_PAYMENT_BASE_URL", "http://localhost:8081")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.Provider.FrontendPublicURL, "http://localhost:5173"; got != want {
		t.Fatalf("FrontendPublicURL = %q, want %q", got, want)
	}
}

func TestLoad_AppPortDoesNotDetermineFrontendPublicURL(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_PORT", "8081")
	t.Setenv("FRONTEND_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("MOCK_PAYMENT_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.Provider.FrontendPublicURL, "http://localhost:5173"; got != want {
		t.Fatalf("FrontendPublicURL = %q, want %q", got, want)
	}
	if got := cfg.Provider.MockPaymentBaseURL; got != cfg.Provider.FrontendPublicURL {
		t.Fatalf("deprecated MockPaymentBaseURL = %q, want compatibility value %q", got, cfg.Provider.FrontendPublicURL)
	}
}

func TestLoad_MockProviderRequiresFrontendPublicURL(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("APP_PORT", "8081")
	t.Setenv("FRONTEND_PUBLIC_URL", "")
	t.Setenv("MOCK_PAYMENT_BASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() derived a frontend PaymentURL from APP_PORT")
	}
}

func TestLoad_FrontendPublicURLRejectsInvalidValue(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("FRONTEND_PUBLIC_URL", "/payments")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a relative FRONTEND_PUBLIC_URL")
	}
}

func TestLoad_LegacyMockPaymentBaseURLRemainsCompatible(t *testing.T) {
	baseLoadEnv(t)
	t.Setenv("FRONTEND_PUBLIC_URL", "")
	t.Setenv("MOCK_PAYMENT_BASE_URL", "http://localhost:5173")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.Provider.FrontendPublicURL, "http://localhost:5173"; got != want {
		t.Fatalf("FrontendPublicURL = %q, want legacy value %q", got, want)
	}
}
