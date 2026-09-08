// Package safehttpclient provides SSRF-hardened HTTP client with defense-in-depth protections.
//
// SafeHTTPClient enforces:
//  1. Private IP blocking: Prevents requests to RFC1918, loopback, link-local, and cloud metadata endpoints
//  2. DNS rebinding defense: Re-resolves hostnames before connecting and validates resolved IPs
//  3. Allowlist support: Opt-in bypass for trusted internal services
//  4. Timeout enforcement: All requests have mandatory timeouts
//
// Design (2026-09-07, SSRF hardening work package W5):
//
// Attack vectors mitigated:
//   - Direct private IP access: http://192.168.1.1, http://10.0.0.1
//   - Cloud metadata SSRF: http://169.254.169.254/latest/meta-data/
//   - DNS rebinding: attacker.com → 8.8.8.8 (first check) → 127.0.0.1 (actual connect)
//   - IPv6 attacks: http://[::1], http://[fc00::1]
//   - URL parser bypasses: http://0x7f000001 (hex encoding), http://2130706433 (decimal)
//   - TOCTOU attacks: hostname resolves to safe IP, then attacker changes DNS before connect
//
// Allowlist design:
//   - Exact hostname match: "localhost", "host.docker.internal"
//   - CIDR blocks: "10.0.0.0/8" (for internal API gateways)
//   - Domain suffixes: "*.internal.corp" (for trusted services)
//
// Usage:
//
//	// Default client (blocks all private IPs)
//	client := safehttpclient.New(30 * time.Second)
//	resp, err := client.Get(ctx, "https://api.example.com/data")
//
//	// With allowlist (for local development)
//	client := safehttpclient.NewWithAllowlist(30*time.Second, []string{
//	    "localhost",
//	    "host.docker.internal",
//	    "10.0.0.0/8",  // internal services
//	})
//	resp, err := client.Get(ctx, "http://10.0.1.50:8080/health")
package safehttpclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SafeHTTPClient is an SSRF-hardened HTTP client with private IP blocking and DNS rebinding defense.
type SafeHTTPClient struct {
	client    *http.Client
	allowlist *Allowlist
}

// New creates a SafeHTTPClient that blocks all private IPs.
// timeout is mandatory and applies to the entire request lifecycle.
func New(timeout time.Duration) *SafeHTTPClient {
	return NewWithAllowlist(timeout, nil)
}

// NewWithAllowlist creates a SafeHTTPClient with an allowlist for trusted destinations.
// allowlistRules format:
//   - Exact hostname: "localhost", "host.docker.internal"
//   - CIDR block: "10.0.0.0/8", "172.16.0.0/12"
//   - Domain suffix: "*.internal.corp" (matches any subdomain)
func NewWithAllowlist(timeout time.Duration, allowlistRules []string) *SafeHTTPClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	allowlist := newAllowlist(allowlistRules)

	// Custom dialer that validates resolved IPs before connecting (DNS rebinding defense)
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialWithValidation(ctx, dialer, network, addr, allowlist)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &SafeHTTPClient{
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Validate redirect targets
				if err := validateURL(req.URL, allowlist); err != nil {
					return fmt.Errorf("ssrf: redirect blocked: %w", err)
				}
				if len(via) >= 10 {
					return fmt.Errorf("ssrf: too many redirects")
				}
				return nil
			},
		},
		allowlist: allowlist,
	}
}

// Get performs a GET request with SSRF protection.
func (c *SafeHTTPClient) Get(ctx context.Context, urlStr string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("ssrf: build request: %w", err)
	}
	return c.Do(req)
}

// Post performs a POST request with SSRF protection.
func (c *SafeHTTPClient) Post(ctx context.Context, urlStr, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("ssrf: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

// Do executes an HTTP request with SSRF protection.
func (c *SafeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// Pre-flight validation: check URL before DNS resolution
	if err := validateURL(req.URL, c.allowlist); err != nil {
		return nil, fmt.Errorf("ssrf: pre-flight check failed: %w", err)
	}
	return c.client.Do(req)
}

// validateURL checks if a URL is safe before making a request.
// This is the first line of defense (before DNS resolution).
func validateURL(u *url.URL, allowlist *Allowlist) error {
	if u == nil {
		return fmt.Errorf("nil URL")
	}

	// Only allow http/https schemes
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (only http/https)", u.Scheme)
	}

	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("empty hostname")
	}

	// Check allowlist first
	if allowlist.IsAllowed(hostname) {
		return nil
	}

	// If hostname is a literal IP, validate it immediately
	if ip := net.ParseIP(hostname); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("IP %s is in blocked range (private/loopback/link-local/metadata)", hostname)
		}
		return nil
	}

	// For domain names, we can't fully prevent DNS rebinding here (attacker controls DNS),
	// but we block obviously malicious patterns and rely on dialWithValidation for the real check.
	hostname = strings.ToLower(hostname)

	// Block suspicious patterns that might bypass URL parsers
	if strings.Contains(hostname, "@") {
		return fmt.Errorf("hostname contains @ character (potential parser bypass)")
	}

	return nil
}

// dialWithValidation is the DNS rebinding defense layer.
// It resolves the hostname at connection time and validates all resolved IPs,
// then dials a validated IP directly so the connection cannot race a second
// DNS resolution (classic rebinding TOCTOU). TLS SNI and certificate
// verification still use the original hostname because http.Transport derives
// them from the request target, not from the dialed address.
func dialWithValidation(ctx context.Context, dialer *net.Dialer, network, addr string, allowlist *Allowlist) (net.Conn, error) {
	// addr format: "hostname:port"
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf: invalid address %q: %w", addr, err)
	}

	// Check allowlist
	if allowlist.IsAllowed(host) {
		return dialer.DialContext(ctx, network, addr)
	}

	// If host is already an IP, validate it
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return nil, fmt.Errorf("ssrf: IP %s is in blocked range", host)
		}
		return dialer.DialContext(ctx, network, addr)
	}

	// For hostnames: resolve DNS and validate ALL resolved IPs
	// This prevents DNS rebinding where attacker controls DNS to return different IPs
	resolver := &net.Resolver{}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf: DNS lookup failed for %s: %w", host, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("ssrf: no IPs resolved for %s", host)
	}

	// Validate that NONE of the resolved IPs are blocked
	for _, ipAddr := range ips {
		if isBlockedIP(ipAddr.IP) {
			return nil, fmt.Errorf("ssrf: hostname %s resolves to blocked IP %s", host, ipAddr.IP)
		}
	}

	// All IPs are safe; dial a validated IP of the requested address family
	// directly instead of handing the hostname back to the dialer, which would
	// resolve DNS a second time and reopen the rebinding window.
	chosen, err := pickValidatedIP(ips, network)
	if err != nil {
		return nil, fmt.Errorf("ssrf: %s has no validated IP matching network %q: %w", host, network, err)
	}
	return dialer.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
}

// pickValidatedIP returns the first resolved IP compatible with the dial
// network ("tcp" accepts any; "tcp4"/"tcp6" require the matching family).
func pickValidatedIP(ips []net.IPAddr, network string) (net.IP, error) {
	wantV4 := strings.HasSuffix(network, "4")
	wantV6 := strings.HasSuffix(network, "6")
	for _, ipAddr := range ips {
		isV4 := ipAddr.IP.To4() != nil
		if (wantV4 && isV4) || (wantV6 && !isV4) || (!wantV4 && !wantV6) {
			return ipAddr.IP, nil
		}
	}
	return nil, fmt.Errorf("no compatible validated IP")
}

// isBlockedIP checks if an IP is in a blocked range.
// Blocked ranges:
//   - Loopback: 127.0.0.0/8, ::1/128
//   - Private: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 (RFC1918)
//   - CGNAT: 100.64.0.0/10 (includes Aliyun metadata 100.100.100.200)
//   - Private IPv6: fc00::/7 (covers fd00::/8 ULA)
//   - Link-local: 169.254.0.0/16 (AWS metadata), fe80::/10
//   - Multicast: 224.0.0.0/4, ff00::/8
//   - Documentation: 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24
//   - Benchmarking: 198.18.0.0/15
//   - Broadcast: 255.255.255.255/32
//   - Unspecified: 0.0.0.0/32, ::/128
func isBlockedIP(ip net.IP) bool {
	// Check against all blocked CIDR ranges
	for _, block := range blockedCIDRs {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// blockedCIDRs are network ranges that should never be accessed via SafeHTTPClient.
var blockedCIDRs = func() []*net.IPNet {
	ranges := []string{
		// IPv4 loopback
		"127.0.0.0/8",
		// IPv4 private (RFC1918)
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		// IPv4 link-local (AWS/GCP metadata)
		"169.254.0.0/16",
		// IPv4 CGNAT (Aliyun metadata 100.100.100.200 lives here)
		"100.64.0.0/10",
		// IPv4 benchmarking (inter-network routing used by some VPN tunnels)
		"198.18.0.0/15",
		// IPv4 multicast
		"224.0.0.0/4",
		// IPv4 documentation
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.0/24",
		// IPv4 broadcast
		"255.255.255.255/32",
		// IPv4 unspecified
		"0.0.0.0/32",
		// IPv6 loopback
		"::1/128",
		// IPv6 ULA (private)
		"fc00::/7",
		"fd00::/8",
		// IPv6 link-local
		"fe80::/10",
		// IPv6 multicast
		"ff00::/8",
		// IPv6 unspecified
		"::/128",
	}

	var blocks []*net.IPNet
	for _, r := range ranges {
		_, block, err := net.ParseCIDR(r)
		if err != nil {
			panic(fmt.Sprintf("safehttpclient: invalid CIDR %q: %v", r, err))
		}
		blocks = append(blocks, block)
	}
	return blocks
}()
