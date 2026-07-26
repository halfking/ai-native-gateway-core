// Package telemetry provides request metadata extraction and observability utilities
package telemetry

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// ─── Agent system prompt pattern registry ───
//
// Agents identify themselves in system prompts (e.g. "You are Claude Code
// by Anthropic"). The registry maps canonical agent names to substrings
// that trigger detection. Extensible at runtime via RegisterAgentPattern.
//
// Detection priority follows registration order — more-specific patterns
// registered first take precedence over generic matches.
type agentPatternEntry struct {
	name     string
	patterns []string
}

var (
	agentPatternsMu sync.RWMutex
	agentPatterns   = defaultAgentPatterns()
)

func defaultAgentPatterns() []agentPatternEntry {
	return []agentPatternEntry{
		// Specific agent names first (they may co-occur with generic ones in
		// prompts like "You are ZCode (Claude Code)" or "OpenCode powered by Claude").
		{"zcode", []string{"zcode"}},
		{"opencode", []string{"opencode"}},
		{"codex", []string{"openai codex", "codex cli"}},
		{"claude-code", []string{"claude code", "claude-code"}},
		{"cursor", []string{"you are an ai assistant in cursor", "cursor ide"}},
		{"vscode", []string{"visual studio code", "vscode"}},
	}
}

// RegisterAgentPattern registers additional agent identity patterns for
// system-prompt-based detection. This is safe for concurrent access and
// intended to be called at startup (e.g. in an init() function or from main).
//
// Patterns are lowercased internally. Duplicate registrations for the
// same name are additive — subsequent calls append patterns rather than
// replace.
//
// Example:
//
//	telemetry.RegisterAgentPattern("my-custom-agent", "my agent", "custom cli")
func RegisterAgentPattern(name string, patterns ...string) {
	if name == "" || len(patterns) == 0 {
		return
	}
	lower := make([]string, len(patterns))
	for i, p := range patterns {
		lower[i] = strings.ToLower(p)
	}
	agentPatternsMu.Lock()
	defer agentPatternsMu.Unlock()
	agentPatterns = append(agentPatterns, agentPatternEntry{name: name, patterns: lower})
}

// ResetAgentPatterns restores the default built-in agent patterns.
// Used in tests to isolate registrations between test cases.
func ResetAgentPatterns() {
	agentPatternsMu.Lock()
	defer agentPatternsMu.Unlock()
	agentPatterns = defaultAgentPatterns()
}

// DetectAgentFromSystemPrompt attempts to identify the agent/client from
// the system prompt content. Returns the canonical agent name, or ""
// when no agent is identified.
//
// This is a lightweight heuristic — it lowercases the prompt and checks
// for known identity substrings. It is not a full NLP classifier.
func DetectAgentFromSystemPrompt(systemPrompt string) string {
	if systemPrompt == "" {
		return ""
	}
	lower := strings.ToLower(systemPrompt)
	agentPatternsMu.RLock()
	defer agentPatternsMu.RUnlock()
	for _, entry := range agentPatterns {
		for _, p := range entry.patterns {
			if strings.Contains(lower, p) {
				return entry.name
			}
		}
	}
	return ""
}

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

// ExtractClientIP extracts the real client IP from HTTP request headers
// Priority: X-Real-IP > X-Forwarded-For (first) > RemoteAddr
func ExtractClientIP(r *http.Request) string {
	// X-Real-IP is most reliable if set by trusted proxy
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return strings.TrimSpace(realIP)
	}

	// X-Forwarded-For: first IP is the original client
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	// Fallback to RemoteAddr (strip port)
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// ExtractForwardedFor extracts the full X-Forwarded-For header chain
func ExtractForwardedFor(r *http.Request) string {
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

// APIKeyFingerprint returns a stable, non-reversible fingerprint for an
// raw API key: SHA-256 hex truncated to 16 chars (8 bytes).
//
// 2026-07-15: Unified the convention previously split across
// `cmd/gateway-v2` (SHA-256[:16]) and the observability migration comment
// ("first 8 chars"). We pick SHA-256[:16] for collision-resistance while
// staying short enough to fit request_context_attrs.api_key_fingerprint
// VARCHAR(16) without truncation variance.
//
// Empty input returns "" — callers distinguish "no key" from a real fingerprint.
func APIKeyFingerprint(rawKey string) string {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawKey))
	return fmt.Sprintf("%x", sum[:8]) // 8 bytes = 16 hex chars
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
	case strings.Contains(ua, "zcode"):
		return "zcode"
	case strings.Contains(ua, "codex"):
		return "codex"
	case strings.Contains(ua, "cursor"):
		return "cursor"
	case strings.Contains(ua, "vscode"):
		return "vscode"
	case strings.Contains(ua, "roocode"), strings.Contains(ua, "roo-code"):
		return "roocode"
	case strings.Contains(ua, "windsurf"):
		return "windsurf"
	case strings.Contains(ua, "zed"):
		return "zed"
	case strings.Contains(ua, "jetbrains"):
		return "jetbrains"
	case strings.Contains(ua, "github-copilot"), strings.Contains(ua, "copilot"):
		return "copilot"
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
		strings.Contains(ua, "zcode") ||
		strings.Contains(ua, "codex") ||
		strings.Contains(ua, "cursor") ||
		strings.Contains(ua, "vscode") ||
		strings.Contains(ua, "roocode") ||
		strings.Contains(ua, "windsurf") ||
		strings.Contains(ua, "zed") ||
		strings.Contains(ua, "github-copilot") ||
		strings.Contains(ua, "copilot") ||
		strings.Contains(ua, "jetbrains") {
		return "cli"
	}

	// API clients
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

// EnrichAgentNameFromSystemPrompt merges header-based agent detection with
// system-prompt-based semantic detection. Priority:
//
//  1. Header detection (if we got a name other than "unknown") — keep it
//  2. System prompt semantic detection — prefer if header was "unknown"
//  3. No detection — return ""
//
// This is designed to be called later in the request lifecycle, after the
// body has been parsed and the system prompt is available, to backfill
// agent_name for session-level statistics.
func EnrichAgentNameFromSystemPrompt(headerName, systemPrompt string) string {
	if headerName != "" && headerName != "unknown" {
		return headerName
	}
	if name := DetectAgentFromSystemPrompt(systemPrompt); name != "" {
		return name
	}
	return ""
}
