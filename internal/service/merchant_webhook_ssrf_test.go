package service_test

// merchant_webhook_ssrf_test.go — Phase 8D.2 tests:
//
//   - configuration-time destination validation (Upsert rejects unsafe URLs
//     with WEBHOOK_DESTINATION_BLOCKED semantics);
//   - DELIVERY-time enforcement for stored/legacy rows: literal unsafe URLs
//     rejected before any I/O, hostnames resolving to private addresses
//     refused at the connection boundary with ZERO sockets opened;
//   - safe, bounded diagnostics (no resolver/network detail, sanitised
//     response snippet).

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/dhikaarta/pay-gate-backend/internal/ssrf"
	"github.com/google/uuid"
)

// staticResolver answers every lookup with scripted addresses — deterministic
// connection-time classification without live DNS.
type errorRoundTripper struct{ err error }

func (r errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

type staticResolver struct{ addrs []net.IPAddr }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addrs, nil
}

func staticIPs(t *testing.T, addrs ...string) []net.IPAddr {
	t.Helper()
	out := make([]net.IPAddr, 0, len(addrs))
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			t.Fatalf("bad test IP %q", a)
		}
		out = append(out, net.IPAddr{IP: ip})
	}
	return out
}

// publicWebhookTestEndpoint passes configuration/delivery validation by
// design — it stands in for a merchant's real public endpoint so that tests
// exercise the PRODUCTION guarded client end-to-end (loopback httptest URLs
// are correctly rejected by the unconditional delivery-time pre-check).
const publicWebhookTestEndpoint = "http://public.test/hook"

// rewireTo returns a dialer that connects every attempt to a local test
// listener: the guarded client only ever sees the scripted PUBLIC address in
// the policy path, while the socket itself lands on the httptest server.
func rewireTo(target string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
}

// publicWebhookTestClient builds the production guarded client for a test
// server: scripted public DNS answer + socket rewire to the server listener.
func publicWebhookTestClient(timeout time.Duration, listenerAddr string) *http.Client {
	return ssrf.NewHTTPClient(timeout, &ssrf.Dialer{
		Resolver: staticResolver{addrs: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
		Dial:     rewireTo(listenerAddr),
	})
}

// countingDial stands in for the raw socket layer: it records attempts and
// never connects. Blocked destinations must never reach it.
type countingDial struct {
	mu sync.Mutex
	n  int
}

func (d *countingDial) dial(context.Context, string, string) (net.Conn, error) {
	d.mu.Lock()
	d.n++
	d.mu.Unlock()
	return nil, errors.New("test dialer: network disabled")
}

func (d *countingDial) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

// seedWebhookRow creates an ACTIVE config (with a decryptable secret) and a
// due PENDING delivery pointing at endpoint — equivalent to a legacy row that
// was persisted before the destination policy existed.
func seedWebhookRow(t *testing.T, cfgRepo *memWebhookConfigRepo, delRepo *memWebhookDeliveryRepo, mid uuid.UUID, endpoint string) uuid.UUID {
	t.Helper()
	enc, err := service.EncryptWebhookSecret(testEncKey(), "whsec_x")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgRepo.Upsert(context.Background(), &model.MerchantWebhookConfig{
		ID: uuid.New(), MerchantID: mid, URL: endpoint,
		EncryptedSecret: enc, Status: model.MerchantWebhookConfigStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	d := &model.MerchantWebhookDelivery{
		ID: uuid.New(), MerchantID: mid, EventID: "evt_ssrf_" + uuid.NewString(),
		EventType: model.MerchantWebhookEventPaymentPaid, TransactionID: uuid.New(),
		EndpointURL: endpoint, Payload: []byte(`{"id":"evt_ssrf"}`),
		Status:        model.MerchantWebhookDeliveryStatusPending,
		NextAttemptAt: time.Now().UTC().Add(-time.Second),
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := delRepo.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// ─── Configuration time ──────────────────────────────────────────────────────

func TestMerchantWebhookConfigService_BlocksUnsafeDestinations(t *testing.T) {
	mid := uuid.New()
	merchantRepo := &memMerchantRepoForWebhook{m: &model.Merchant{ID: mid, Status: model.MerchantStatusActive}}
	svc := service.NewMerchantWebhookConfigService(
		newMemWebhookConfigRepo(), newMemWebhookDeliveryRepo(), merchantRepo, testEncKey(), false,
	)

	blocked := []string{
		// private / loopback / link-local / metadata IPv4
		"http://127.0.0.1/hook",
		"http://10.0.0.1/hook",
		"http://172.16.0.1/hook",
		"http://192.168.1.1/hook",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0/hook",
		// IPv6 private / loopback / link-local
		"http://[::1]/hook",
		"http://[fc00::1]/hook",
		"http://[fd00::1]/hook",
		"http://[fe80::1]/hook",
		// IPv4-mapped IPv6
		"http://[::ffff:127.0.0.1]/hook",
		"http://[::ffff:10.0.0.1]/hook",
		"http://[::ffff:192.168.1.1]/hook",
		// hostnames
		"http://localhost/hook",
		"http://LOCALHOST./hook",
		"http://metadata.google.internal/hook",
	}
	for _, u := range blocked {
		_, err := svc.Upsert(context.Background(), mid, model.UpsertMerchantWebhookRequest{URL: u})
		if !errors.Is(err, service.ErrWebhookDestinationBlocked) {
			t.Errorf("Upsert(%q) err = %v, want ErrWebhookDestinationBlocked", u, err)
		}
	}

	invalid := []string{
		"notaurl",
		"ftp://example.com/hook",
		"http://user:pass@example.com/hook",
		"http://example.com:99999/hook",
		"http://foo..com/hook",
	}
	for _, u := range invalid {
		_, err := svc.Upsert(context.Background(), mid, model.UpsertMerchantWebhookRequest{URL: u})
		if !errors.Is(err, service.ErrWebhookInvalidURL) {
			t.Errorf("Upsert(%q) err = %v, want ErrWebhookInvalidURL", u, err)
		}
	}

	// Public destinations remain accepted.
	res, err := svc.Upsert(context.Background(), mid, model.UpsertMerchantWebhookRequest{URL: "https://hooks.example.com/hook"})
	if err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
	if res.URL != "https://hooks.example.com/hook" {
		t.Errorf("URL = %q, want the configured public URL", res.URL)
	}
}

// ─── Delivery time (stored / legacy rows) ────────────────────────────────────

// A stored literal unsafe URL (e.g. metadata IP persisted before the policy)
// must fail the delivery permanently with a safe diagnostic — no socket, no
// resolver output, no internal detail in last_error.
func TestMerchantWebhookDispatcher_BlocksLegacyStoredUnsafeURL(t *testing.T) {
	mid := uuid.New()
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	endpoint := "http://169.254.169.254/latest/meta-data/"
	deliveryID := seedWebhookRow(t, cfgRepo, delRepo, mid, endpoint)

	dial := &countingDial{}
	// Production-shaped guarded client (real resolver — the literal parses
	// without DNS) with the socket layer replaced by the counter.
	client := ssrf.NewHTTPClient(time.Second, &ssrf.Dialer{Dial: dial.dial})
	dispatcher := service.NewMerchantWebhookDispatcher(delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10, client)
	if _, err := dispatcher.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, err := delRepo.FindByID(context.Background(), mid, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.MerchantWebhookDeliveryStatusFailed {
		t.Fatalf("status = %s, want FAILED", got.Status)
	}
	lastErr := ""
	if got.LastError != nil {
		lastErr = *got.LastError
	}
	if !strings.Contains(lastErr, "webhook destination blocked by security policy") {
		t.Fatalf("last_error = %q, want the stable destination-blocked message", lastErr)
	}
	for _, leak := range []string{"lookup", "dial tcp", "resolve", "metadata"} {
		if strings.Contains(lastErr, leak) {
			t.Errorf("last_error leaks %q: %q", leak, lastErr)
		}
	}
	if n := dial.count(); n != 0 {
		t.Fatalf("socket dialled %d time(s) — a blocked URL must never reach the network", n)
	}
}

// A hostname that PASSES string validation but resolves to a private address
// must be refused at the CONNECTION boundary: the guarded dialer classifies
// the resolved address before any socket opens (DNS-rebinding defence at
// delivery time).
func TestMerchantWebhookDispatcher_BlocksResolvedPrivateDestinationAtDial(t *testing.T) {
	mid := uuid.New()
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	// Not on any hostname blocklist — only DNS reveals it as internal.
	endpoint := "http://internal-alias.test/hook"
	deliveryID := seedWebhookRow(t, cfgRepo, delRepo, mid, endpoint)

	dial := &countingDial{}
	client := ssrf.NewHTTPClient(time.Second, &ssrf.Dialer{
		Resolver: staticResolver{addrs: staticIPs(t, "10.99.88.77")},
		Dial:     dial.dial,
	})
	dispatcher := service.NewMerchantWebhookDispatcher(delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10, client)
	if _, err := dispatcher.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, err := delRepo.FindByID(context.Background(), mid, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.MerchantWebhookDeliveryStatusFailed {
		t.Fatalf("status = %s, want FAILED (policy failure is permanent, not retried)", got.Status)
	}
	lastErr := ""
	if got.LastError != nil {
		lastErr = *got.LastError
	}
	if !strings.Contains(lastErr, "webhook destination blocked by security policy") {
		t.Fatalf("last_error = %q, want the stable destination-blocked message", lastErr)
	}
	if n := dial.count(); n != 0 {
		t.Fatalf("socket dialled %d time(s) towards a private resolved address", n)
	}
}

// The persisted diagnostic snippet stays bounded and printable: binary bytes
// from a response body never survive into last_error (the 8 KiB read cap is
// unchanged).
func TestMerchantWebhookDispatcher_DoesNotDeliverDisabledConfig(t *testing.T) {
	mid := uuid.New()
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	deliveryID := seedWebhookRow(t, cfgRepo, delRepo, mid, publicWebhookTestEndpoint)
	if _, err := cfgRepo.Disable(context.Background(), mid); err != nil {
		t.Fatalf("disable webhook config: %v", err)
	}

	dispatcher := service.NewMerchantWebhookDispatcher(
		delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10,
		publicWebhookTestClient(time.Second, "127.0.0.1:1"),
	)
	if _, err := dispatcher.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := delRepo.FindByID(context.Background(), mid, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.MerchantWebhookDeliveryStatusFailed {
		t.Fatalf("disabled config delivery status = %s, want FAILED", got.Status)
	}
	if got.LastError == nil || *got.LastError != "webhook secret unavailable" {
		t.Fatalf("disabled config last_error = %v, want stable unavailable category", got.LastError)
	}
}

func TestMerchantWebhookDispatcher_SanitizesTransportError(t *testing.T) {
	mid := uuid.New()
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	deliveryID := seedWebhookRow(t, cfgRepo, delRepo, mid, publicWebhookTestEndpoint)

	client := &http.Client{Transport: errorRoundTripper{err: errors.New("SQLSTATE 42P01 secret_token=do-not-persist")}}
	dispatcher := service.NewMerchantWebhookDispatcher(
		delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10, client,
	)
	if _, err := dispatcher.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := delRepo.FindByID(context.Background(), mid, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError == nil {
		t.Fatal("last_error is nil")
	}
	if *got.LastError != "http delivery failed" {
		t.Fatalf("last_error = %q, want stable transport category", *got.LastError)
	}
	if strings.Contains(*got.LastError, "SQLSTATE") || strings.Contains(*got.LastError, "secret_token") {
		t.Fatalf("transport diagnostic leaked internal/secret text: %q", *got.LastError)
	}
}

func TestMerchantWebhookDispatcher_SanitizesResponseSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte{0x00, 0x01, 'H', 'i', 0xff, 0x7f})
	}))
	defer srv.Close()

	mid := uuid.New()
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	deliveryID := seedWebhookRow(t, cfgRepo, delRepo, mid, publicWebhookTestEndpoint)

	dispatcher := service.NewMerchantWebhookDispatcher(
		delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10,
		publicWebhookTestClient(time.Second, srv.Listener.Addr().String()),
	)
	if _, err := dispatcher.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, err := delRepo.FindByID(context.Background(), mid, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.MerchantWebhookDeliveryStatusFailed {
		t.Fatalf("status = %s, want FAILED (HTTP 400 is non-retryable)", got.Status)
	}
	if got.LastError == nil {
		t.Fatal("last_error is nil")
	}
	lastErr := *got.LastError
	if !strings.Contains(lastErr, "http 400") || !strings.Contains(lastErr, "Hi") {
		t.Errorf("last_error = %q, want the http 400 status with readable text", lastErr)
	}
	if strings.ContainsRune(lastErr, 0x00) || strings.ContainsRune(lastErr, 0xff) || strings.ContainsRune(lastErr, 0x7f) {
		t.Errorf("last_error contains raw binary bytes: %q", lastErr)
	}
}
