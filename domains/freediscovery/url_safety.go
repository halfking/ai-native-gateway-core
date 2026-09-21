package freediscovery

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// recordURLSafetyBlocked records a URL-safety rejection in metrics.
// reason uses a low-cardinality classification: <field>_<class>, where field is in
// base_url/models_endpoint and class is derived from the rejection message
// (required/scheme/hostname/blocked_ip/userinfo/...).
func recordURLSafetyBlocked(field, msg string) {
	metrics.FreeDiscoveryURLSafetyBlockedTotal.WithLabelValues(field + "_" + classifyURLSafetyRejection(msg)).Inc()
}

// classifyURLSafetyRejection buckets human-readable validation messages into a low-cardinality
// enumeration. Unknown messages fall back to "other" so the label cardinality stays bounded.
func classifyURLSafetyRejection(msg string) string {
	switch {
	case strings.Contains(msg, "is required"):
		return "required"
	case strings.Contains(msg, "exceeds maximum length"):
		return "too_long"
	case strings.Contains(msg, "control characters"):
		return "control_chars"
	case strings.Contains(msg, "must use http"):
		return "scheme"
	case strings.Contains(msg, "must contain a hostname"):
		return "hostname"
	case strings.Contains(msg, "userinfo"):
		return "userinfo"
	case strings.Contains(msg, "fragment"):
		return "fragment"
	case strings.Contains(msg, "loopback"), strings.Contains(msg, "private"),
		strings.Contains(msg, "link-local"), strings.Contains(msg, "multicast"),
		strings.Contains(msg, "broadcast"), strings.Contains(msg, "unspecified"):
		return "blocked_ip"
	case strings.Contains(msg, "@ character"):
		return "parser_bypass"
	case strings.Contains(msg, "whitespace"):
		return "whitespace"
	case strings.Contains(msg, "relative path"):
		return "not_relative"
	case strings.Contains(msg, "scheme-relative"):
		return "scheme_relative"
	case strings.Contains(msg, "invalid"):
		return "invalid_url"
	default:
		return "other"
	}
}

// URL validation and endpoint safety utilities (2026-09-09 audit-fix).
//
// Design goals:
//   - Reject SSRF-dangerous targets (RFC1918 / loopback / link-local / IPv6 ULA / private IP literals /
//     cloud metadata, userinfo / control characters / invalid scheme / fragment);
//   - Restrict models_endpoint to a relative path; reject absolute URLs and `//host` scheme-relative URLs;
//   - Pair with the safehttpclient runtime transport to block at the validation layer first; tests
//     are allowed to inject an allowlist;
//   - Match the blocking scope of safehttpclient.NewWithAllowlist in production to avoid policy drift.
//
// Note: this package does not initiate any outbound requests; runtime outbound protection is
// provided by safehttpclient.

// maxBaseURLLen and maxEndpointLen prevent abnormally long inputs from triggering parser overhead.
const (
	maxBaseURLLen  = 2048
	maxEndpointLen = 512
)

// isValidBaseURL validates base_url: only http/https schemes; reject userinfo, fragments, and
// control characters; reject dangerous IP literals such as loopback/private/link-local/multicast/metadata.
//
// A non-empty return string means an error (kept consistent with the isValidProviderCode style).
// Each rejection also increments freediscovery_url_safety_blocked_total{reason}.
func isValidBaseURL(raw string) string {
	msg := validateBaseURL(raw)
	if msg != "" {
		recordURLSafetyBlocked("base_url", msg)
	}
	return msg
}

func validateBaseURL(raw string) string {
	if raw == "" {
		return "base_url is required"
	}
	if len(raw) > maxBaseURLLen {
		return "base_url exceeds maximum length"
	}
	// Control characters may be normalized by URL parsers, so block them up front.
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "base_url contains control characters"
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("base_url invalid: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "base_url must use http:// or https://"
	}
	host := u.Hostname()
	if host == "" {
		return "base_url must contain a hostname"
	}
	if u.User != nil {
		return "base_url must not contain userinfo (username:password@host)"
	}
	if u.Fragment != "" {
		return "base_url must not contain a fragment"
	}
	// IPv4/IPv6 literals: block private address ranges directly.
	if ip := net.ParseIP(host); ip != nil {
		if reason := blockReasonForIP(ip); reason != "" {
			return "base_url " + reason
		}
	}
	// Domain form: block common bypass patterns (safehttpclient performs further DNS validation at the dial stage).
	if strings.Contains(host, "@") {
		return "base_url hostname contains @ character (potential parser bypass)"
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return "base_url hostname contains whitespace"
	}
	return ""
}

// isValidModelsEndpoint validates models_endpoint: must start with a single slash (relative path);
// reject schemes, `//host` scheme-relative URLs, absolute URLs with userinfo, and control characters.
// Each rejection also increments freediscovery_url_safety_blocked_total{reason}.
func isValidModelsEndpoint(raw string) string {
	msg := validateModelsEndpoint(raw)
	if msg != "" {
		recordURLSafetyBlocked("models_endpoint", msg)
	}
	return msg
}

func validateModelsEndpoint(raw string) string {
	if raw == "" {
		// The caller fills in the default /models; an empty string is allowed (caller supplies the default).
		return ""
	}
	if len(raw) > maxEndpointLen {
		return "models_endpoint exceeds maximum length"
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "models_endpoint contains control characters"
		}
	}
	// Must start with a single '/' (relative path); reject '//' (scheme-relative URL) and any scheme.
	if !strings.HasPrefix(raw, "/") {
		return "models_endpoint must be a relative path starting with /"
	}
	if strings.HasPrefix(raw, "//") {
		return "models_endpoint must not be a scheme-relative URL (//host/path)"
	}
	// Use url.Parse to further confirm it does not resolve to a new host.
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("models_endpoint invalid: %v", err)
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil {
		return "models_endpoint must be a relative path (no scheme/host/userinfo)"
	}
	return ""
}

// blockReasonForIP returns the rejection reason when an IP matches a block policy; empty string means allowed.
//
// Reuses the common blocking scope of safehttpclient: loopback, private (RFC1918 / IPv6 ULA),
// link-local (169.254/169.239, fe80::/10), cloud metadata (169.254.169.254), IPv4 multicast
// (224/4), IPv6 multicast (ff00::/8), unspecified addresses, and broadcast.
func blockReasonForIP(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if ip.IsUnspecified() {
		return "host is unspecified address (0.0.0.0 / ::)"
	}
	if ip.IsLoopback() {
		return "host is loopback (127.0.0.0/8 or ::1)"
	}
	if ip.IsLinkLocalUnicast() {
		// 169.254.0.0/16 (IPv4) and fe80::/10 (IPv6); includes cloud metadata 169.254.169.254.
		return "host is link-local (incl. cloud metadata 169.254.169.254)"
	}
	if ip.IsLinkLocalMulticast() {
		return "host is link-local multicast"
	}
	if ip.IsPrivate() {
		return "host is private (RFC1918 or IPv6 ULA fc00::/7)"
	}
	if ip.IsMulticast() {
		return "host is multicast"
	}
	if ip.Equal(net.IPv4bcast) {
		return "host is broadcast (255.255.255.255)"
	}
	return ""
}

// joinBaseAndEndpoint safely joins base and endpoint; endpoint must already have passed isValidModelsEndpoint.
//
// The base path segment is preserved (e.g. https://a.com/v1 + /models -> https://a.com/v1/models).
// url.ResolveReference treats an absolute-path reference as a replacement rather than an append,
// so we instead concatenate base.Path with endpoint and re-serialize the whole URL. If endpoint
// does not start with "/", one is added.
func joinBaseAndEndpoint(base, endpoint string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	if endpoint == "" {
		return baseURL.String(), nil
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	ep, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid models_endpoint: %w", err)
	}
	if ep.Scheme != "" || ep.Host != "" || ep.User != nil {
		return "", fmt.Errorf("models_endpoint must be relative")
	}
	// Concatenation: base.Path (with leading /) + ep.Path (already ensured to start with /), then merge query.
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + ep.Path
	if ep.RawQuery != "" {
		baseURL.RawQuery = ep.RawQuery
	}
	return baseURL.String(), nil
}
