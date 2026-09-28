// Package ssrf implements the Phase 8D.2 outbound HTTP destination policy
// (SSRF protection) for merchant webhook delivery.
//
// The policy is defence in depth:
//
//  1. ValidateURL — configuration/delivery-time string checks. These give
//     early feedback and catch obvious cases (IP literals, userinfo,
//     malformed hosts), but they are NOT the security boundary.
//  2. Dialer — CONNECTION-time enforcement. Every address a hostname resolves
//     to is classified immediately before the socket is opened, and the socket
//     is dialled to the validated address literal, so the HTTP stack can never
//     re-resolve the hostname to an unchecked address (DNS rebinding).
//
// Production policy: public Internet destinations are allowed, everything
// else is denied — loopback, RFC1918, link-local (cloud metadata), CGNAT,
// multicast, reserved/unspecified, documentation/benchmarking ranges, IPv6
// unique-local/link-local/multicast, and IPv6 forms that embed non-public
// IPv4 (IPv4-mapped, NAT64, 6to4, Teredo).
//
// There is intentionally NO configuration switch to disable or bypass this
// policy. Local tests inject a test resolver/dialer through Dialer instead of
// weakening the production rules.
package ssrf

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Validation errors. Both sentinels are deliberately safe to persist in
// delivery diagnostics and to show to merchants: they carry no resolved
// addresses, no resolver output, and no internal network detail.
var (
	// ErrInvalidURL — the URL itself is malformed or uses a disallowed form
	// (scheme, userinfo/credentials, empty host, bad port, malformed hostname).
	ErrInvalidURL = errors.New("invalid webhook url")
	// ErrDestinationBlocked — the destination is not a permitted public
	// address or hostname (SSRF destination policy violation).
	ErrDestinationBlocked = errors.New("webhook destination blocked by security policy")
)

// Dial-time errors. These replace raw resolver/dialer errors so that DNS
// server addresses, infrastructure detail, and resolved internal IPs can
// never leak into delivery diagnostics or logs.
var (
	errBadAddress    = errors.New("malformed destination address")
	errLookupFailed  = errors.New("webhook destination lookup failed")
	errNoAddresses   = errors.New("webhook destination lookup returned no addresses")
	errConnectFailed = errors.New("could not connect to webhook destination")
)

// nonPublicIPv4 covers IPv4 ranges that netip's built-in predicates do NOT
// classify as non-global (IsGlobalUnicast is true for several of these) but
// that are not public destinations per the IANA special-purpose registry.
// Loopback (127/8), link-local (169.254/16), private (10/8, 172.16/12,
// 192.168/16), multicast (224/4), and unspecified (0.0.0.0 exactly) are
// already rejected by the stdlib predicates in IsPublicDestination.
var nonPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network" (unspecified range)
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT (shared address space)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1 (documentation)
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast (deprecated, RFC 7526)
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking (RFC 2544)
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2 (documentation)
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3 (documentation)
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast (explicit; stdlib also rejects)
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved (includes 255.255.255.255 broadcast)
}

// nonPublicIPv6 covers IPv6 ranges outside netip's built-in predicates
// (loopback ::1, unspecified ::, link-local fe80::/10, multicast ff00::/10,
// and unique-local fc00::/7 via IsPrivate are all rejected by the stdlib
// checks). The entries here are documentation/reserved ranges and — most
// importantly — ranges that EMBED arbitrary IPv4 addresses which could point
// at loopback/private/link-local/metadata destinations.
var nonPublicIPv6 = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 well-known prefix (embeds IPv4)
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use prefix (embeds IPv4)
	netip.MustParsePrefix("100::/64"),       // discard-only address block
	netip.MustParsePrefix("2001::/32"),      // Teredo tunnelling (embeds IPv4)
	netip.MustParsePrefix("2001:2::/48"),    // benchmarking (RFC 4472)
	netip.MustParsePrefix("2001:db8::/32"),  // documentation (RFC 3849)
	netip.MustParsePrefix("2002::/16"),      // 6to4 (embeds IPv4 in bytes 2-5)
}

// IsPublicDestination reports whether ip is a permitted OUTBOUND webhook
// destination under the Phase 8D.2 SSRF policy.
//
// The policy is public-destination enforcement, not merely RFC1918 blocking:
// anything that is not demonstrably public global-unicast space returns
// false. IPv4-mapped IPv6 addresses (::ffff:127.0.0.1) are unmapped first so
// they classify exactly like their IPv4 form.
//
// A nil, empty, or unparseable address is never public (default deny).
func IsPublicDestination(ip net.IP) bool {
	if len(ip) == 0 {
		return false
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	// IPv4-mapped (::ffff:a.b.c.d) and IPv4-compatible forms classify as IPv4.
	a = a.Unmap()

	// Built-in classification: unspecified, loopback, link-local unicast,
	// multicast, and (for IPv4) everything not global-unicast is rejected
	// here. Note IsGlobalUnicast alone is NOT sufficient — it is true for
	// RFC1918 and for several reserved ranges, hence the checks below.
	if !a.IsValid() || !a.IsGlobalUnicast() {
		return false
	}
	if a.IsUnspecified() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsPrivate() {
		return false
	}

	// Registry ranges the stdlib predicates do not cover.
	extra := nonPublicIPv4
	if a.Is6() {
		extra = nonPublicIPv6
	}
	for _, n := range extra {
		if n.Contains(a) {
			return false
		}
	}
	return true
}

// blockedHostname is the SECONDARY, early-feedback hostname check used by
// ValidateURL. It is deliberately short: hostname string comparison is never
// the primary control — every address the hostname actually resolves to is
// classified again at dial time by Dialer.
func blockedHostname(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true // RFC 6761 reserved localhost names (any dot-localhost)
	}
	if host == "metadata" || host == "metadata.google.internal" {
		return true // cloud metadata DNS names (also blocked by IP at dial time)
	}
	return false
}

// ValidateURL performs the string-level part of the destination policy:
//
//   - absolute URL (net/url ParseRequestURI);
//   - http or https scheme only;
//   - no userinfo (user:pass@) credentials;
//   - non-empty hostname;
//   - numeric port within 1..65535 when present;
//   - IP literals (including IPv6 with zone, IPv4-mapped forms) must be
//     public destinations;
//   - localhost/metadata hostnames rejected;
//   - malformed hostnames (empty labels, all-numeric hosts such as decimal
//     IP encodings like http://2130706433/) rejected.
//
// It performs NO DNS resolution — Dialer enforces the address policy again at
// connection time, which is the authoritative check (DNS may change after
// validation).
func ValidateURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return ErrInvalidURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return ErrInvalidURL
	}
	if u.User != nil {
		return ErrInvalidURL // credentials in the URL
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ErrInvalidURL
	}
	// Strip IPv6 zone ("fe80::1%eth0") so the address part classifies.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
		if host == "" {
			return ErrInvalidURL
		}
	}
	// Normalise a single trailing dot (FQDN root): "localhost." == "localhost".
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ErrInvalidURL
	}
	// net/url accepts any digit run as a port; enforce the valid range.
	// (Non-numeric ports like ":abc" already fail ParseRequestURI.)
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return ErrInvalidURL
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return ErrInvalidURL // "host:" — colon with no port
	}

	if ip := net.ParseIP(host); ip != nil {
		if !IsPublicDestination(ip) {
			return ErrDestinationBlocked
		}
		return nil
	}

	// Hostname checks (secondary — dial time re-checks every resolved IP).
	if blockedHostname(host) {
		return ErrDestinationBlocked
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return ErrInvalidURL // empty label: "foo..bar", ".example"
		}
	}
	// RFC 1123 hostnames must contain a letter. This rejects all-numeric
	// hosts such as decimal IPv4 encodings (http://2130706433/) that some
	// resolvers (CGO getaddrinfo) would otherwise parse as an address —
	// those are still classified by IsPublicDestination at dial time.
	if !strings.ContainsFunc(host, func(r rune) bool {
		return r >= 'a' && r <= 'z'
	}) {
		return ErrInvalidURL
	}
	return nil
}

// Resolver abstracts DNS lookup so tests can inject deterministic answers
// (rebinding, multi-address, failure) without touching live DNS.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DialFunc matches net.Dialer.DialContext — the RAW socket dial underneath
// the guard. Tests inject a recording/rewiring dialer here.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Dialer is the guarded dialer: it resolves the target host, classifies EVERY
// resolved address, refuses the connection if ANY address violates the
// policy, and connects only to a validated address literal (the HTTP stack
// never re-resolves the hostname itself — DNS rebinding defence).
//
// A nil Resolver uses net.DefaultResolver; a nil Dial uses a net.Dialer.
type Dialer struct {
	Resolver Resolver
	Dial     DialFunc
}

func (d *Dialer) resolver() Resolver {
	if d != nil && d.Resolver != nil {
		return d.Resolver
	}
	return net.DefaultResolver
}

func (d *Dialer) rawDial() DialFunc {
	if d != nil && d.Dial != nil {
		return d.Dial
	}
	return (&net.Dialer{}).DialContext
}

// DialContext implements the connection-time destination policy.
//
// addr is "host:port" as supplied by the HTTP transport. The lookup happens
// HERE, immediately before the socket opens, and the socket is opened to the
// validated IP literal — so an address change between configuration time and
// connection time cannot reach an unchecked address.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errBadAddress
	}
	// net.Resolver parses IP literals directly (no DNS); hostnames resolve
	// here. Resolver errors are dropped, not wrapped: Go's DNSError text can
	// contain internal DNS server addresses.
	resolved, err := d.resolver().LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errLookupFailed
	}
	// Policy: ANY non-public address in the answer fails the WHOLE dial —
	// there is no fallback from a blocked address to a remaining one, so a
	// "public + private" answer can never silently use the private address.
	candidates := make([]net.IPAddr, 0, len(resolved))
	for _, r := range resolved {
		if !IsPublicDestination(r.IP) {
			return nil, ErrDestinationBlocked
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return nil, errNoAddresses
	}
	// Connect only to validated address literals. Multiple public addresses
	// are tried in resolver order (fallback between addresses that ALL
	// passed the policy).
	for _, c := range candidates {
		conn, err := d.rawDial()(ctx, network, net.JoinHostPort(c.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		// The raw socket error is deliberately dropped (infrastructure
		// detail) — only a generic message is surfaced.
	}
	return nil, errConnectFailed
}

// NewHTTPClient builds the outbound webhook HTTP client with the Phase 8D.2
// policy applied at the network boundary:
//
//   - destination validation in DialContext (DNS-rebinding safe, see Dialer);
//   - Proxy explicitly disabled — an HTTP(S)_PROXY would resolve and connect
//     to the target OUTSIDE this dialer and bypass destination validation,
//     so outbound proxying is deliberately NOT supported for webhooks;
//   - redirects never followed (CheckRedirect → http.ErrUseLastResponse), so
//     a public endpoint cannot bounce delivery at a private address;
//   - TLS verification untouched (transport is cloned from
//     http.DefaultTransport — normal certificate and hostname verification);
//   - timeout bounds the whole request as before.
//
// dialer may be nil (production default: system resolver + net.Dialer).
func NewHTTPClient(timeout time.Duration, dialer *Dialer) *http.Client {
	if dialer == nil {
		dialer = &Dialer{}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // explicit: no environment proxy — see above.
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
