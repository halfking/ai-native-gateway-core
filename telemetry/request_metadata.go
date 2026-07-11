// Package telemetry provides request metadata extraction and observability utilities
package telemetry

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// RequestMetadata contains observability fields for request tracing
type RequestMetadata struct {
	// ─── Caller Information ───
	ClientIP           string // Real IP extracted from headers
	ClientForwardedFor string // Full X-Forwarded-For chain
	AgentName          string // Agent/application name (e.g., claude-code, opencode)
	AgentType          string // Agent type: web/mobile/cli/api/bot/internal
	APIKeyFingerprint  string // First 8 chars of API key (masked)
	CustomerID         *int64 // Customer/organization ID for billing

	// ─── Provider Detail ───
	CredentialID     *int64 // Credential used (FK)
	UpstreamEndpoint string // Full upstream API URL

	// ─── Session & Task Context ───
	SessionTitle   string // Human-readable session title
	SessionSummary string // Session description
	TaskID         string // Task/work item ID (e.g., JIRA-123)
	TaskTitle      string // Task title
	TaskType       string // Task type (feature/bugfix/refactor)

	// ─── Compression & Optimization ───
	CompressionStartIndex *int     // Starting message index for compression
	CompressionEndIndex   *int     // Ending message index for compression
	CompressionRatio      *float64 // Compression ratio (0.0 - 1.0)
	CacheHit              *bool    // Whether request hit cache
	CacheTokensSaved      *int     // Tokens saved due to cache

	// ─── Security & Compliance ───
	ContentSafetyScore map[string]interface{}   // Content safety analysis (JSONB)
	DLPViolations      []map[string]interface{} // DLP violation details (JSONB array)
	SensitiveKeywords  []string                 // Matched sensitive keywords
	RateLimitStatus    string                   // under_limit/approaching_limit/exceeded/bypassed

	// ─── Protocol & Conversion Metadata ───
	ClientProtocol     string                 // Client protocol (openai/anthropic/gemini)
	UpstreamProtocol   string                 // Upstream protocol (anthropic/openai/bedrock)
	ProtocolConversion *bool                  // Whether protocol conversion occurred
	IRExtensions       map[string]interface{} // IR extension fields (JSONB)
	SanitizerMutations map[string]interface{} // Sanitizer mutations applied (JSONB)

	// ─── Vendor Metadata ───
	VendorMetadata map[string]interface{} // Vendor-specific fields (JSONB)
}

// ExtractClientIP extracts the real client IP from HTTP request headers.
// Forwarded headers (X-Real-IP, X-Forwarded-For) are trusted only when the immediate
// peer (RemoteAddr) is loopback/private or belongs to LLM_GATEWAY_TRUSTED_PROXY_CIDRS.
// Otherwise, RemoteAddr is used directly to prevent spoofing.
func ExtractClientIP(r *http.Request) string {
	remoteIP := extractRemoteIP(r.RemoteAddr)

	// Only trust forwarded headers if immediate peer is trusted
	if !isTrustedProxy(remoteIP) {
		return remoteIP
	}

	// Peer is trusted, check X-Real-IP first
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		realIP = strings.TrimSpace(realIP)
		if ip := net.ParseIP(realIP); ip != nil {
			return realIP
		}
	}

	// Fall back to first valid IP in X-Forwarded-For
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if ip := net.ParseIP(part); ip != nil {
				return part
			}
		}
	}

	// No valid forwarded header, use peer IP
	return remoteIP
}

// extractRemoteIP strips port from RemoteAddr
func extractRemoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// No port, return as-is
		return remoteAddr
	}
	return host
}

// isTrustedProxy returns true if the IP is loopback, private, or in custom trusted CIDRs
func isTrustedProxy(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Check loopback
	if ip.IsLoopback() {
		return true
	}

	// Check private ranges (RFC 1918, RFC 4193 for IPv6)
	if ip.IsPrivate() {
		return true
	}

	// Check custom trusted CIDRs from env
	customCIDRs := os.Getenv("LLM_GATEWAY_TRUSTED_PROXY_CIDRS")
	if customCIDRs == "" {
		return false
	}

	for _, cidrStr := range strings.Split(customCIDRs, ",") {
		cidrStr = strings.TrimSpace(cidrStr)
		if cidrStr == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(cidrStr)
		if err != nil {
			// Invalid CIDR, skip safely
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}

	return false
}

// ExtractForwardedFor extracts the full X-Forwarded-For header chain.
// Only returns the chain if the immediate peer is a trusted proxy; otherwise empty.
func ExtractForwardedFor(r *http.Request) string {
	remoteIP := extractRemoteIP(r.RemoteAddr)
	if !isTrustedProxy(remoteIP) {
		return ""
	}
	return r.Header.Get("X-Forwarded-For")
}

// MaskAPIKey masks an API key, keeping first 8 chars visible
// Example: sk-1234abcd5678efgh -> sk-1234ab***
func MaskAPIKey(key string) string {
	if len(key) == 0 {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:8] + "***"
}

// ExtractAgentName extracts agent name from User-Agent or custom headers
// Priority: X-Agent-Name > User-Agent parsing > "unknown"
func ExtractAgentName(r *http.Request) string {
	// Custom header for explicit agent identification
	if agentName := r.Header.Get("X-Agent-Name"); agentName != "" {
		return agentName
	}

	// Parse User-Agent
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		return "unknown"
	}

	// Detect common agent patterns
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "claude-code"):
		return "claude-code"
	case strings.Contains(ua, "opencode"):
		return "opencode"
	case strings.Contains(ua, "cursor"):
		return "cursor"
	case strings.Contains(ua, "vscode"):
		return "vscode"
	case strings.Contains(ua, "jetbrains"):
		return "jetbrains"
	case strings.Contains(ua, "postman"):
		return "postman"
	case strings.Contains(ua, "insomnia"):
		return "insomnia"
	case strings.Contains(ua, "python"):
		return "python-client"
	case strings.Contains(ua, "go-http-client"):
		return "go-client"
	case strings.Contains(ua, "curl"):
		return "curl"
	default:
		return "unknown"
	}
}

// ExtractAgentType determines agent type from headers and patterns
// Returns: web/mobile/cli/api/bot/internal
func ExtractAgentType(r *http.Request) string {
	// Custom header for explicit type
	if agentType := r.Header.Get("X-Agent-Type"); agentType != "" {
		return agentType
	}

	ua := strings.ToLower(r.Header.Get("User-Agent"))

	// CLI tools
	if strings.Contains(ua, "curl") ||
		strings.Contains(ua, "wget") ||
		strings.Contains(ua, "httpie") {
		return "cli"
	}

	// Code editors / IDE agents
	if strings.Contains(ua, "claude-code") ||
		strings.Contains(ua, "opencode") ||
		strings.Contains(ua, "cursor") ||
		strings.Contains(ua, "vscode") ||
		strings.Contains(ua, "jetbrains") {
		return "cli"
	}

	// API clients
	if strings.Contains(ua, "postman") || strings.Contains(ua, "insomnia") {
		return "api"
	}
	if strings.Contains(ua, "python") ||
		strings.Contains(ua, "go-http-client") ||
		strings.Contains(ua, "java") ||
		strings.Contains(ua, "okhttp") ||
		strings.Contains(ua, "axios") {
		return "api"
	}

	// Bots
	if strings.Contains(ua, "bot") ||
		strings.Contains(ua, "crawler") ||
		strings.Contains(ua, "spider") {
		return "bot"
	}

	// Mobile
	if strings.Contains(ua, "android") ||
		strings.Contains(ua, "iphone") ||
		strings.Contains(ua, "ipad") ||
		strings.Contains(ua, "mobile") {
		return "mobile"
	}

	// Web browsers
	if strings.Contains(ua, "mozilla") ||
		strings.Contains(ua, "chrome") ||
		strings.Contains(ua, "safari") ||
		strings.Contains(ua, "firefox") {
		return "web"
	}

	return "unknown"
}

// NewRequestMetadata creates a RequestMetadata from HTTP request
func NewRequestMetadata(r *http.Request) *RequestMetadata {
	return &RequestMetadata{
		ClientIP:           ExtractClientIP(r),
		ClientForwardedFor: ExtractForwardedFor(r),
		AgentName:          ExtractAgentName(r),
		AgentType:          ExtractAgentType(r),
	}
}
