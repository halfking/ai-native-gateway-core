package telemetry

import (
	"net"
	"net/http"
	"strings"
)

// ObservabilityContext holds authoritative metadata extracted from HTTP request
// and routing decisions. Populated at request ingress and enriched during execution.
type ObservabilityContext struct {
	// Caller metadata
	ClientIP           string
	ClientForwardedFor string
	AgentName          string
	AgentType          string
	APIKeyFingerprint  string

	// Session context
	SessionTitle string
	TaskID       string

	// Routing metadata
	UpstreamEndpoint   string
	UpstreamProtocol   string
	ProtocolConversion *bool
}

// ExtractObservabilityContext extracts caller metadata from HTTP request.
// Call this at ingress (middleware or handler entry) to capture authoritative source.
func ExtractObservabilityContext(r *http.Request) *ObservabilityContext {
	return &ObservabilityContext{
		ClientIP:           extractClientIP(r),
		ClientForwardedFor: SanitizeForwardedFor(r.Header.Get("X-Forwarded-For")),
		AgentName:          extractAgentName(r),
		AgentType:          extractAgentType(r),
	}
}

// extractClientIP extracts real client IP from HTTP headers.
// Priority: X-Real-IP > X-Forwarded-For (first) > RemoteAddr
func extractClientIP(r *http.Request) string {
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return strings.TrimSpace(realIP)
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// extractAgentName extracts agent name from headers.
// Priority: X-Agent-Name > User-Agent pattern matching > "unknown"
func extractAgentName(r *http.Request) string {
	if agentName := r.Header.Get("X-Agent-Name"); agentName != "" {
		return agentName
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	if ua == "" {
		return "unknown"
	}
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

// extractAgentType determines agent type from headers and patterns.
// Returns: web/mobile/cli/api/bot/internal/unknown
func extractAgentType(r *http.Request) string {
	if agentType := r.Header.Get("X-Agent-Type"); agentType != "" {
		return agentType
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "curl") || strings.Contains(ua, "wget") || strings.Contains(ua, "httpie"):
		return "cli"
	case strings.Contains(ua, "claude-code") || strings.Contains(ua, "opencode") ||
		strings.Contains(ua, "cursor") || strings.Contains(ua, "vscode") || strings.Contains(ua, "jetbrains"):
		return "cli"
	case strings.Contains(ua, "postman") || strings.Contains(ua, "insomnia"):
		return "api"
	case strings.Contains(ua, "python") || strings.Contains(ua, "go-http-client") ||
		strings.Contains(ua, "java") || strings.Contains(ua, "okhttp") || strings.Contains(ua, "axios"):
		return "api"
	case strings.Contains(ua, "bot") || strings.Contains(ua, "crawler") || strings.Contains(ua, "spider"):
		return "bot"
	case strings.Contains(ua, "android") || strings.Contains(ua, "iphone") ||
		strings.Contains(ua, "ipad") || strings.Contains(ua, "mobile"):
		return "mobile"
	case strings.Contains(ua, "mozilla") || strings.Contains(ua, "chrome") ||
		strings.Contains(ua, "safari") || strings.Contains(ua, "firefox"):
		return "web"
	default:
		return "unknown"
	}
}

// MaskAPIKeyFingerprint masks an API key, keeping first 8 chars visible.
// Example: sk-1234abcd5678efgh -> sk-1234ab
func MaskAPIKeyFingerprint(key string) string {
	if len(key) == 0 {
		return ""
	}
	if len(key) < 8 {
		return "***"
	}
	// Return first 8 chars only (no *** suffix to save space in bounded VARCHAR(16) column)
	return key[:8]
}

// SanitizeForwardedFor bounds X-Forwarded-For header to 512 chars to prevent
// DoS via unlimited proxy chain injection. Matches VARCHAR(512) column bound.
func SanitizeForwardedFor(fwd string) string {
	if len(fwd) <= 512 {
		return fwd
	}
	return fwd[:512]
}

// EnrichRequestLogWithContext copies observability metadata from context to RequestLogEntry.
// Call this before persisting the entry to ensure metadata fields are populated.
func EnrichRequestLogWithContext(entry *RequestLogEntry, ctx *ObservabilityContext) {
	if ctx == nil {
		return
	}
	if ctx.ClientIP != "" {
		entry.ClientIP = &ctx.ClientIP
	}
	if ctx.ClientForwardedFor != "" {
		entry.ClientForwardedFor = &ctx.ClientForwardedFor
	}
	if ctx.AgentName != "" {
		entry.AgentName = &ctx.AgentName
	}
	if ctx.AgentType != "" {
		entry.AgentType = &ctx.AgentType
	}
	if ctx.APIKeyFingerprint != "" {
		entry.APIKeyFingerprint = &ctx.APIKeyFingerprint
	}
	if ctx.SessionTitle != "" {
		entry.SessionTitle = &ctx.SessionTitle
	}
	if ctx.TaskID != "" {
		entry.TaskID = &ctx.TaskID
	}
	if ctx.UpstreamEndpoint != "" {
		entry.UpstreamEndpoint = &ctx.UpstreamEndpoint
	}
	if ctx.UpstreamProtocol != "" {
		entry.UpstreamProtocol = &ctx.UpstreamProtocol
	}
	if ctx.ProtocolConversion != nil {
		entry.ProtocolConversion = ctx.ProtocolConversion
	}
}
