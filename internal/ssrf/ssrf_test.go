package ssrf

// ssrf_test.go — Phase 8D.2 tests for the outbound destination policy:
// IP classification, URL validation, connection-time DNS enforcement, the
// DNS-rebinding regression, and redirect-based SSRF.
//
// All network-touching tests use an injected resolver + recording dialer (or
// a test-only rewire to a local httptest server). Production SSRF rules are
// never relaxed to make a test pass.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Test doubles ────────────────────────────────────────────────────────────

// fakeResolver returns scripted answers in sequence (the last answer repeats).
// When err is set every lookup fails with it.
type fakeResolver struct {
	mu      sync.Mutex
	answers [][]net.IPAddr
	err     error
	calls   []string
}

func (f *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.answers) == 0 {
		return nil, errors.New("no scripted answers")
	}
	idx := len(f.calls) - 1
	if idx >= len(f.answers) {
		idx = len(f.answers) - 1
	}
	return f.answers[idx], nil
}

func (f *fakeResolver) lookupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// addr answers for scripted lookups.
func ips(t *testing.T, addrs ...string) []net.IPAddr {
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

// recordingDial records every raw socket dial attempt. Entries listed in
// rewire connect to a real local address instead (test-only transport — the
// SSRF policy itself only ever sees the public address it was asked for).
type recordingDial struct {
	mu     sync.Mutex
	calls  []string
	rewire map[string]string
}

func (r *recordingDial) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.calls = append(r.calls, addr)
	to, ok := r.rewire[addr]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("test dialer refused %s", addr)
	}
	return (&net.Dialer{}).DialContext(ctx, network, to)
}

func (r *recordingDial) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// ─── IP classification ───────────────────────────────────────────────────────

func TestIsPublicDestination(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// IPv4 non-public — every range required by the Phase 8D.2 spec.
		{"", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"10.255.255.255", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"169.254.169.254", false}, // cloud metadata
		{"169.254.0.1", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.0.0.1", false},
		{"192.0.2.1", false},
		{"192.168.1.1", false},
		{"192.88.99.1", false},
		{"198.18.0.1", false},
		{"198.51.100.7", false},
		{"203.0.113.9", false},
		{"224.0.0.1", false},
		{"239.255.255.250", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},

		// IPv6 non-public.
		{"::", false},
		{"::1", false},
		{"fc00::1", false},
		{"fd12:3456:789a::1", false},
		{"fe80::1", false},
		{"ff02::1", false},
		{"2001:db8::1", false},
		{"100::1", false},
		{"2001::1", false},
		{"2002::1", false},
		{"64:ff9b::10.0.0.1", false}, // NAT64 embedding private IPv4
		{"64:ff9b::7f00:1", false},   // NAT64 embedding loopback IPv4
		{"64:ff9b:1::1", false},      // NAT64 local-use

		// IPv4-mapped IPv6 forms classify exactly like their IPv4 target.
		{"::ffff:127.0.0.1", false},
		{"::ffff:10.0.0.1", false},
		{"::ffff:192.168.1.1", false},
		{"::ffff:169.254.169.254", false},
		{"::ffff:0.0.0.0", false},
		{"::ffff:8.8.8.8", true}, // mapped PUBLIC is still public

		// Public destinations are allowed.
		{"1.1.1.1", true},
		{"8.8.8.8", true},
		{"93.184.216.34", true},
		{"104.16.0.1", true},
		{"100.63.255.255", true}, // just below CGNAT space
		{"172.15.255.255", true}, // just below 172.16/12
		{"172.32.0.1", true},     // just above 172.31/16
		{"2606:4700:4700::1111", true},
		{"2001:4860:4860::8888", true},
	}
	for _, tc := range cases {
		var ip net.IP
		if tc.ip != "" {
			ip = net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
		}
		if got := IsPublicDestination(ip); got != tc.want {
			t.Errorf("IsPublicDestination(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
	// Nil slice (distinct from empty-but-valid) is default-deny.
	if IsPublicDestination(nil) {
		t.Error("IsPublicDestination(nil) = true, want false")
	}
}

// ─── URL validation (configuration / delivery time) ─────────────────────────

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want error // nil = allowed; ErrInvalidURL; ErrDestinationBlocked
	}{
		// ── Blocked private/loopback/link-local/metadata IPv4 ──
		{"loopback v4", "http://127.0.0.1/hook", ErrDestinationBlocked},
		{"private 10/8", "http://10.0.0.1/hook", ErrDestinationBlocked},
		{"private 172.16/12", "http://172.16.0.1/hook", ErrDestinationBlocked},
		{"private 192.168/16", "http://192.168.1.1/hook", ErrDestinationBlocked},
		{"link-local", "http://169.254.0.1/hook", ErrDestinationBlocked},
		{"metadata IP", "http://169.254.169.254/latest/meta-data", ErrDestinationBlocked},
		{"unspecified v4", "http://0.0.0.0/hook", ErrDestinationBlocked},

		// ── Blocked IPv6 ──
		{"v6 loopback", "http://[::1]/hook", ErrDestinationBlocked},
		{"v6 unique-local fc00", "http://[fc00::1]/hook", ErrDestinationBlocked},
		{"v6 unique-local fd00", "http://[fd00::1]/hook", ErrDestinationBlocked},
		{"v6 link-local", "http://[fe80::1]/hook", ErrDestinationBlocked},
		{"v6 unspecified", "http://[::]/hook", ErrDestinationBlocked},
		{"v6 multicast", "http://[ff02::1]/hook", ErrDestinationBlocked},

		// ── Blocked IPv4-mapped IPv6 ──
		{"mapped loopback", "http://[::ffff:127.0.0.1]/hook", ErrDestinationBlocked},
		{"mapped private", "http://[::ffff:10.0.0.1]/hook", ErrDestinationBlocked},
		{"mapped 192.168", "http://[::ffff:192.168.1.1]/hook", ErrDestinationBlocked},
		{"mapped metadata", "http://[::ffff:169.254.169.254]/hook", ErrDestinationBlocked},

		// ── Hostnames (secondary check — early feedback only) ──
		{"localhost", "http://localhost/hook", ErrDestinationBlocked},
		{"localhost trailing dot", "http://localhost./hook", ErrDestinationBlocked},
		{"localhost uppercase", "http://LOCALHOST/hook", ErrDestinationBlocked},
		{"subdomain localhost", "http://foo.localhost/hook", ErrDestinationBlocked},
		{"metadata hostname", "http://metadata.google.internal/hook", ErrDestinationBlocked},
		{"metadata short name", "http://metadata/computeMetadata/v1/", ErrDestinationBlocked},

		// ── Invalid URLs ──
		{"not a url", "notaurl", ErrInvalidURL},
		{"relative", "/relative/path", ErrInvalidURL},
		{"unsupported scheme ftp", "ftp://example.com/hook", ErrInvalidURL},
		{"unsupported scheme file", "file:///etc/passwd", ErrInvalidURL},
		{"userinfo credentials", "http://user:pass@example.com/hook", ErrInvalidURL},
		{"userinfo no password", "https://user@example.com/hook", ErrInvalidURL},
		{"missing host", "http:///nohost", ErrInvalidURL},
		{"host only colon", "http://:8080/hook", ErrInvalidURL},
		{"port too large", "http://example.com:99999/hook", ErrInvalidURL},
		{"port zero", "http://example.com:0/hook", ErrInvalidURL},
		{"empty label", "http://foo..com/hook", ErrInvalidURL},
		{"leading dot label", "http://.example.com/hook", ErrInvalidURL},
		{"decimal IP encoding", "http://2130706433/hook", ErrInvalidURL},
		{"opaque", "http:example.com", ErrInvalidURL},

		// ── Allowed public destinations ──
		{"public https", "https://hooks.example.com/hook", nil},
		{"public http", "http://hooks.example.com/hook", nil}, // https policy lives in the config service
		{"mixed case host", "https://EXAMPLE.COM/Hook", nil},
		{"public trailing dot", "https://example.com./hook", nil},
		{"explicit port", "https://example.com:8443/hook", nil},
		{"max port", "https://example.com:65535/hook", nil},
		{"public IP literal", "https://8.8.8.8/hook", nil},
		{"query string", "https://example.com/hook?src=gateway", nil},
		{"IPv6 public literal", "https://[2606:4700:4700::1111]/hook", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateURL(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateURL(%q) = %v, want %v", tc.raw, err, tc.want)
			}
			// Errors must stay safe to persist/show: no addresses or detail.
			if err != nil {
				msg := err.Error()
				for _, leak := range []string{"lookup", "10.0.", "192.168", "127.0.0.1"} {
					if strings.Contains(msg, leak) && !strings.Contains(tc.raw, leak) {
						t.Errorf("error %q leaks %q", msg, leak)
					}
				}
			}
		})
	}
}

// ─── Connection-time enforcement (the actual security boundary) ─────────────

func TestDialer_ClassifiesResolvedAddresses(t *testing.T) {
	cases := []struct {
		name      string
		addr      string
		answers   []string // scripted resolver answer for the host
		wantErr   error    // nil dial success is simulated by the recorder refusing with errConnectFailed
		wantDials []string
	}{
		{
			name:      "public only is dialled by IP literal",
			addr:      "public.test:443",
			answers:   []string{"93.184.216.34"},
			wantErr:   errConnectFailed, // recorder refuses, but the dial ATTEMPTED the validated literal
			wantDials: []string{"93.184.216.34:443"},
		},
		{
			name: "private answer blocked", addr: "intranet.test:80",
			answers: []string{"10.0.0.5"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			name: "loopback answer blocked", addr: "lp.test:80",
			answers: []string{"127.0.0.1"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			name: "link-local metadata answer blocked", addr: "metadata.test:80",
			answers: []string{"169.254.169.254"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			name: "ipv6 private answer blocked", addr: "v6.test:80",
			answers: []string{"fd00::1"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			name: "ipv4-mapped private answer blocked", addr: "mapped.test:80",
			answers: []string{"::ffff:10.0.0.1"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			// public + private → the WHOLE dial fails; no fallback dial is ever attempted.
			name: "mixed public and private blocked", addr: "mixed.test:80",
			answers: []string{"93.184.216.34", "10.0.0.5"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			name: "private first then public blocked", addr: "mixed2.test:80",
			answers: []string{"10.0.0.5", "93.184.216.34"}, wantErr: ErrDestinationBlocked, wantDials: nil,
		},
		{
			// Multiple PUBLIC addresses: fallback happens only among validated ones.
			name: "multiple public fallback stays on validated addresses", addr: "multi.test:8443",
			answers: []string{"1.1.1.1", "8.8.8.8"}, wantErr: errConnectFailed,
			wantDials: []string{"1.1.1.1:8443", "8.8.8.8:8443"},
		},
		{
			name: "no answers", addr: "empty.test:80",
			answers: []string{}, wantErr: errNoAddresses, wantDials: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeResolver{}
			if tc.answers != nil {
				fr.answers = [][]net.IPAddr{ips(t, tc.answers...)}
			}
			rd := &recordingDial{}
			d := &Dialer{Resolver: fr, Dial: rd.dial}

			conn, err := d.DialContext(context.Background(), "tcp", tc.addr)
			if conn != nil {
				t.Fatalf("unexpected conn %v", conn)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			got := rd.recorded()
			if len(got) != len(tc.wantDials) {
				t.Fatalf("dial attempts = %v, want %v", got, tc.wantDials)
			}
			for i := range got {
				if got[i] != tc.wantDials[i] {
					t.Errorf("dial attempt[%d] = %q, want %q", i, got[i], tc.wantDials[i])
				}
			}
		})
	}
}

// A resolver failure must surface a SAFE message — Go's DNSError text can
// contain internal DNS server addresses ("lookup x on 10.0.0.53:53: ...").
func TestDialer_ResolverErrorIsSanitized(t *testing.T) {
	fr := &fakeResolver{err: errors.New("lookup intranet.test on 10.0.0.53:53: server misbehaving")}
	rd := &recordingDial{}
	d := &Dialer{Resolver: fr, Dial: rd.dial}

	_, err := d.DialContext(context.Background(), "tcp", "intranet.test:443")
	if !errors.Is(err, errLookupFailed) {
		t.Fatalf("err = %v, want errLookupFailed", err)
	}
	msg := err.Error()
	for _, leak := range []string{"10.0.0.53", "misbehaving", " on "} {
		if strings.Contains(msg, leak) {
			t.Errorf("resolver error leaked %q: %q", leak, msg)
		}
	}
	if len(rd.recorded()) != 0 {
		t.Errorf("dial attempted after resolver failure: %v", rd.recorded())
	}
}

// ─── HTTP client: redirect, rebinding, public reachability ──────────────────

// newGuardedTestClient wires the PRODUCTION client constructor around a fake
// resolver + recording dialer. The rewire map lets a "public" address reach a
// local httptest server — a test transport, not a policy relaxation.
func newGuardedTestClient(timeout time.Duration, fr *fakeResolver, rd *recordingDial) *http.Client {
	return NewHTTPClient(timeout, &Dialer{Resolver: fr, Dial: rd.dial})
}

// Redirect SSRF regression: a public endpoint answers 30x towards a private
// address. The redirect must NOT be followed, and the private target must
// never be dialled.
func TestHTTPClient_RedirectToPrivateNotFollowed(t *testing.T) {
	var redirectTargetContacted bool // observed through the dial recorder below
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9/steal", http.StatusFound)
	}))
	defer srv.Close()

	fr := &fakeResolver{answers: [][]net.IPAddr{ips(t, "93.184.216.34")}}
	rd := &recordingDial{rewire: map[string]string{"93.184.216.34:80": srv.Listener.Addr().String()}}
	client := newGuardedTestClient(2*time.Second, fr, rd)

	resp, err := client.Get("http://public.test/hook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 (ErrUseLastResponse semantics)", resp.StatusCode)
	}
	for _, call := range rd.recorded() {
		if strings.Contains(call, "127.0.0.1") {
			redirectTargetContacted = true
		}
	}
	if redirectTargetContacted {
		t.Fatal("redirect target 127.0.0.1 was dialled")
	}
	if got := len(rd.recorded()); got != 1 {
		t.Fatalf("dial attempts = %d (%v), want exactly 1 (the public endpoint)", got, rd.recorded())
	}
}

// DNS rebinding regression: the SAME hostname resolves to a public address on
// the first connection and to a private/loopback address afterwards. The
// second request must be refused at the connection path — the private address
// is never dialled. This exercises the real outbound HTTP client, not just
// the validation helper.
func TestHTTPClient_DNSRebindingBlockedAtConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	defer srv.Close()

	// Lookup #1 → public; lookup #2 → loopback (the "rebound" answer).
	fr := &fakeResolver{answers: [][]net.IPAddr{
		ips(t, "93.184.216.34"),
		ips(t, "127.0.0.1"),
	}}
	rd := &recordingDial{rewire: map[string]string{"93.184.216.34:80": srv.Listener.Addr().String()}}
	client := newGuardedTestClient(2*time.Second, fr, rd)

	// Connection 1: resolves public → allowed → reaches the (test-rewired) endpoint.
	resp1, err := client.Get("http://rebind.test/hook")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp1.StatusCode)
	}
	if fr.lookupCount() != 1 {
		t.Fatalf("lookups after first request = %d, want 1", fr.lookupCount())
	}

	// Force a fresh connection so the second request must dial again.
	client.CloseIdleConnections()

	// Connection 2: hostname now resolves to 127.0.0.1 → blocked at dial time.
	_, err = client.Get("http://rebind.test/hook")
	if err == nil {
		t.Fatal("second request succeeded — rebound private address was reachable")
	}
	if !errors.Is(err, ErrDestinationBlocked) {
		t.Fatalf("second request err = %v, want wrapping ErrDestinationBlocked", err)
	}
	// The only socket ever opened was to the first (validated) public literal.
	calls := rd.recorded()
	if len(calls) != 1 || calls[0] != "93.184.216.34:80" {
		t.Fatalf("dial attempts = %v, want exactly [93.184.216.34:80]", calls)
	}
	if fr.lookupCount() != 2 {
		t.Fatalf("lookups = %d, want 2 (second answered privately)", fr.lookupCount())
	}
}

// Unsafe literal endpoints are refused without opening any socket.
func TestHTTPClient_LiteralsRejectedWithoutDialing(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:9/hook",
		"http://10.0.0.1/hook",
		"http://192.168.1.1/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:9/hook",
		"http://[fe80::1]/hook",
		"http://[::ffff:127.0.0.1]:9/hook",
	} {
		t.Run(raw, func(t *testing.T) {
			rd := &recordingDial{}
			// Real net resolver: IP literals parse without DNS → deterministic offline.
			client := NewHTTPClient(time.Second, &Dialer{Dial: rd.dial})

			_, err := client.Get(raw)
			if !errors.Is(err, ErrDestinationBlocked) {
				t.Fatalf("err = %v, want ErrDestinationBlocked", err)
			}
			if calls := rd.recorded(); len(calls) != 0 {
				t.Fatalf("socket dialled for blocked literal: %v", calls)
			}
		})
	}
}

// Public destinations remain reachable through the guarded transport.
func TestHTTPClient_ReachesPublicDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	defer srv.Close()

	fr := &fakeResolver{answers: [][]net.IPAddr{ips(t, "93.184.216.34")}}
	rd := &recordingDial{rewire: map[string]string{"93.184.216.34:80": srv.Listener.Addr().String()}}
	client := newGuardedTestClient(2*time.Second, fr, rd)

	resp, err := client.Get("http://public.test/hook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "pong" {
		t.Fatalf("status=%d body=%q, want 200 \"pong\"", resp.StatusCode, body)
	}
	if got := rd.recorded(); len(got) != 1 || got[0] != "93.184.216.34:80" {
		t.Fatalf("dial attempts = %v, want [93.184.216.34:80]", got)
	}
}

// The production client shape: no proxy (proxying would bypass destination
// validation), redirects disabled, TLS verification untouched.
func TestHTTPClient_PolicyShape(t *testing.T) {
	client := NewHTTPClient(5*time.Second, nil)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", client.Transport)
	}
	if tr.Proxy != nil {
		t.Error("Proxy is set — environment proxy must be explicitly disabled for webhook delivery")
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify is set — TLS verification must not be weakened")
	}
	if client.CheckRedirect == nil {
		t.Fatal("CheckRedirect is nil — redirects must be explicitly disabled")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
	if client.Timeout <= 0 {
		t.Error("Timeout must remain positive")
	}
}
