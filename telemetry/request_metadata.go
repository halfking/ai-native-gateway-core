// Package telemetry provides request metadata extraction and observability utilities
package telemetry

import (
	"crypto/sha256"
	"encoding/json"
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
		// 2026-07-27: 补齐 8 类智能体语义识别 (合并自 admin/auto_title_generator.go)
		// + 增加 roocode/windsurf/zed/copilot/cline/aider/continue/kiro 模式
		// + 增加 bare "you are claude" 兜底。
		//
		// 2026-09-03 (audit closure): tightened the bare-substring patterns
		// for zcode/opencode to "you are ..." and explicit token variants.
		// The bare tokens "zcode" and "opencode" would also fire on any
		// sentence that happens to mention the project name (e.g. "running
		// inside ZCode CLI" inside a Cursor session), causing the registry
		// to mis-rank Cursor as ZCode. The "you are" prefix is the canonical
		// self-description shape that all three top-tier agents emit.
		//
		// Order matters: more-specific patterns first. Generic phrases like
		// "you are claude" can co-occur with "you are claude code" / "you are
		// opencode" / "you are claude in zcode", so concrete agent names go
		// before generic ones. Each pattern is lower-cased at match time.
		{"zcode", []string{"you are zcode", "zcode cli", "zcode-interactive"}},
		{"opencode", []string{"you are opencode", "opencode cli"}},
		{"codex", []string{"openai codex", "codex cli", "you are codex"}},
		{"claude-code", []string{"claude code", "claude-code", "you are claude code"}},
		{"roocode", []string{"roocode", "roo-code", "you are roo code", "you are roocode"}},
		{"windsurf", []string{"windsurf", "you are windsurf"}},
		{"zed", []string{"zed editor", "you are zed"}},
		{"copilot", []string{"github copilot", "you are copilot", "copilot cli"}},
		{"cline", []string{"you are cline", "cline cli", "cline coding"}},
		{"aider", []string{"you are aider", "aider chat"}},
		{"continue", []string{"you are continue", "continue dev"}},
		{"kiro", []string{"you are kiro", "kiro ide"}},
		{"cursor", []string{"you are an ai assistant in cursor", "cursor ide", "you are cursor", "operate in cursor"}},
		{"vscode", []string{"visual studio code", "vscode"}},
		// 2026-09-21 audit: domestic coding-agent clients. Place these
		// BEFORE the bare Claude fallback so that a MiniMax Code system
		// prompt that mentions Claude (it powers MiniMax Code under the
		// hood) still resolves to "minimax-code".
		{"minimax-code", []string{
			"you are minimax code",
			"minimax-code cli",
			"minimax code by anthropic",
			"minimax-code interactive",
			"minimax's official cli",
		}},
		{"deepseek-code", []string{
			"you are deepseek code",
			"deepseek-code cli",
			"deepseek ide coding",
			"deepseek chat coding mode",
			// R52：裸词锚定（09-03 zcode/opencode 同款教训）——裸
			// "deepseek-coder"/"deepseek-v3 coding" 会在任何提及这些模型
			// 名的会话里误命中（如用户自定义指令"输出风格参考
			// deepseek-coder"），须锚定自述形态。
			"you are deepseek-coder",
			"deepseek-coder cli",
			"you are deepseek-v3 coding",
		}},
		// Bare Claude / Anthropic fallback — only fires when no more-specific
		// agent above matched. Useful for custom Claude-API clients that embed
		// Claude as the model and self-identify as plain Claude.
		{"claude", []string{"you are claude"}},
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
	name = strings.TrimSpace(name)
	if name == "" || len(patterns) == 0 {
		return
	}
	lower := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			lower = append(lower, p)
		}
	}
	if len(lower) == 0 {
		return
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
	ClientForwardedFor string // Full X-Forwarded-For header chain
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
//
// SECURITY: this function trusts the inbound headers unconditionally.
// Production code paths that take an explicit allowlist MUST use
// ExtractClientIPTrusted instead.
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

// ExtractForwardedFor extracts the full X-Forwarded-For header chain.
//
// SECURITY: this function trusts the inbound header unconditionally.
// Production code paths that take an explicit allowlist MUST use
// ExtractForwardedForTrusted instead.
func ExtractForwardedFor(r *http.Request) string {
	return r.Header.Get("X-Forwarded-For")
}

// ExtractClientIPTrusted returns the client IP using the standard
// precedence (X-Real-IP > XFF[0] > RemoteAddr) but ONLY honours the
// X-Real-IP / X-Forwarded-For headers when the immediate TCP peer
// matches one of the supplied trusted CIDRs. When the allowlist is
// empty/nil or the peer is untrusted, the function falls back to
// RemoteAddr — preventing public clients from spoofing an arbitrary
// source IP (HIGH security, 2026-08-29).
//
// `trusted` may be nil or empty; in that case the function behaves like
// ExtractClientIP with no header trust (RemoteAddr only).
func ExtractClientIPTrusted(r *http.Request, trusted []*net.IPNet) string {
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteHost == "" {
		remoteHost = r.RemoteAddr
	}
	if len(trusted) == 0 {
		return remoteHost
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP == nil {
		return remoteHost
	}
	peerTrusted := false
	for _, cidr := range trusted {
		if cidr.Contains(remoteIP) {
			peerTrusted = true
			break
		}
	}
	if !peerTrusted {
		return remoteHost
	}
	// Peer is on the allowlist — honour the headers.
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	return remoteHost
}

// ExtractForwardedForTrusted returns the full X-Forwarded-For header
// chain when the immediate peer is on the supplied trusted allowlist,
// otherwise returns the remote host (so the chain is never populated
// from spoofed headers).
func ExtractForwardedForTrusted(r *http.Request, trusted []*net.IPNet) string {
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteHost == "" {
		remoteHost = r.RemoteAddr
	}
	if len(trusted) == 0 {
		return remoteHost
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP == nil {
		return remoteHost
	}
	for _, cidr := range trusted {
		if cidr.Contains(remoteIP) {
			return strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		}
	}
	return remoteHost
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
	return ExtractAgentNameFromRequest(r, nil)
}

// normalizeAgentName 收敛客户端可控的 X-Agent-Name 自由字符串（R52）：
// 去首尾空白 + 剥控制字符（日志/维度注入卫生）+ 截断到 255 rune
// （request_logs.agent_name 为 VARCHAR(255)，超长即 22001 使整行 INSERT
// 失败）。刻意不过 clienttype.Normalize 白名单——自定义 agent 名是合法
// 用法，白名单会把它们全部塌缩成 unknown；维度基数治理登记为后续专项。
func normalizeAgentName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	out := make([]rune, 0, 256)
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
		if len(out) == 255 {
			break
		}
	}
	return string(out)
}

// ExtractAgentNameFromRequest is the body-aware variant of ExtractAgentName.
// Detection order:
//
//  1. X-Agent-Name header (explicit override)
//  2. User-Agent pattern matching → if the result is a *specific* agent
//     (claude-code / cursor / minimax-code / …) return it
//  3. If the UA-derived name is "weak" (unknown / go-client / python-client
//     / curl / postman / insomnia) OR no UA at all, fall through to
//     body-marker detection — catches domestic coding-agent clients whose
//     User-Agent is generic but whose body carries a
//     metadata.{zcode_version,client_type,deepseek_session_id} field,
//     a model name prefix, or tool-name conventions
//  4. If body-marker is empty, return the weak UA-derived name (so callers
//     still see the underlying client lib)
//
// When body is nil or empty, step 3 collapses and the function returns the
// weak UA-derived name as before.
//
// R52：X-Agent-Name 是客户端可控自由字符串，返回前过 clienttype.Normalize
// 白名单收敛 + 截断——此前原样透传，超 255 字节即 22001 使 request_logs
// 整行 INSERT 失败，且任意值会撑大 stats_minute_rollup 的 agent 维度基数。
//
// 2026-09-21 audit (P1: client detection parity for domestic coding agents).
func ExtractAgentNameFromRequest(r *http.Request, body []byte) string {
	// 1. Custom header for explicit agent identification
	if r != nil {
		if agentName := r.Header.Get("X-Agent-Name"); agentName != "" {
			return normalizeAgentName(agentName)
		}
	}

	// 2. User-Agent matching.
	ua := ""
	if r != nil {
		ua = r.Header.Get("User-Agent")
	}
	uaDerived := ""
	if ua != "" {
		uaLower := strings.ToLower(ua)
		switch {
		case strings.Contains(uaLower, "claude-code"):
			return "claude-code"
		case strings.Contains(uaLower, "opencode"):
			return "opencode"
		case strings.Contains(uaLower, "zcode"):
			return "zcode"
		case strings.Contains(uaLower, "codex"):
			return "codex"
		case strings.Contains(uaLower, "cursor"):
			return "cursor"
		case strings.Contains(uaLower, "vscode"):
			return "vscode"
		case strings.Contains(uaLower, "roocode"), strings.Contains(uaLower, "roo-code"):
			return "roocode"
		case strings.Contains(uaLower, "windsurf"):
			return "windsurf"
		case strings.Contains(uaLower, "zed"):
			return "zed"
		case strings.Contains(uaLower, "jetbrains"):
			return "jetbrains"
		case strings.Contains(uaLower, "github-copilot"), strings.Contains(uaLower, "copilot"):
			return "copilot"
		// 2026-09-21 audit: domestic coding-agent clients.
		case strings.Contains(uaLower, "minimax-code"):
			return "minimax-code"
		case strings.Contains(uaLower, "deepseek-code"), strings.Contains(uaLower, "deepseek-ide"),
			strings.Contains(uaLower, "deepseek-cli"):
			return "deepseek-code"
		case strings.Contains(uaLower, "postman"):
			uaDerived = "postman"
		case strings.Contains(uaLower, "insomnia"):
			uaDerived = "insomnia"
		case strings.Contains(uaLower, "python"):
			uaDerived = "python-client"
		case strings.Contains(uaLower, "go-http-client"):
			uaDerived = "go-client"
		case strings.Contains(uaLower, "curl"):
			uaDerived = "curl"
		default:
			uaDerived = "unknown"
		}
	}

	// 3. Body-level marker detection — only fires when the UA-derived
	//    name is weak or absent. Reads metadata.* fields, model name
	//    prefix, tool-name namespace, and the X-Code-Session-Id header
	//    (MiniMax Code session marker).
	if len(body) > 0 || (r != nil && r.Header.Get("X-Code-Session-Id") != "") {
		var xCode string
		if r != nil {
			xCode = r.Header.Get("X-Code-Session-Id")
		}
		if ct := ExtractClientTypeFromBody(body, xCode); ct != "" {
			return ct
		}
	}

	// 4. No UA match and no body marker: return whatever the UA-derived
	//    weak name was so the caller still sees the underlying client lib
	//    (curl / python-client / etc.) rather than losing it. If there
	//    was no UA at all, fall back to "unknown" for backwards
	//    compatibility with the legacy ExtractAgentName contract.
	if uaDerived == "" {
		return "unknown"
	}
	return uaDerived
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
		// R52：国内编程客户端注册补齐——缺这里会使 source_channel 落空。
		strings.Contains(ua, "minimax-code") ||
		strings.Contains(ua, "deepseek-code") ||
		strings.Contains(ua, "deepseek-ide") ||
		strings.Contains(ua, "deepseek-cli") ||
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

// ExtractSystemPromptFromBody pulls the system-prompt text out of an
// already-buffered JSON request body. It supports the three shapes the
// gateway accepts:
//
//   - OpenAI /v1/chat/completions: messages[].role == "system"
//   - Anthropic /v1/messages:       top-level "system" string
//   - OpenAI /v1/responses:         top-level "instructions" string
//
// The path argument is informational (used to disambiguate when the body
// shape doesn't carry an obvious hint). Pass "" when ambiguous — the
// function will try each known field.
//
// Returns "" if body is empty, not JSON, or no system-prompt field is
// present. The function is best-effort: malformed input is treated as
// "no system prompt" rather than an error, so callers can safely fall
// back to header-only detection.
//
// 2026-07-27: Added so that fillAttemptMeta can recover the agent name
// from the system prompt when the User-Agent doesn't expose it (most
// real-world AI agent traffic shows up as "unknown" otherwise).
func ExtractSystemPromptFromBody(body []byte, path string) string {
	if len(body) == 0 {
		return ""
	}
	// Try the three known shapes in priority order. For Anthropic and
	// Responses, the field is a top-level string. For OpenAI chat, it's
	// nested in the messages array. We attempt all three — each parser is
	// cheap and the body is small (<= ~1 MB).
	if sys := extractAnthropicSystem(body); sys != "" {
		return sys
	}
	if sys := extractResponsesInstructions(body); sys != "" {
		return sys
	}
	if sys := extractGeminiSystemInstruction(body); sys != "" {
		return sys
	}
	if sys := extractChatSystemMessages(body); sys != "" {
		return sys
	}
	_ = path // reserved for future path-driven disambiguation
	return ""
}

func extractAnthropicSystem(body []byte) string {
	var s struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return ""
	}
	return extractTextContent(s.System)
}

func extractResponsesInstructions(body []byte) string {
	var s struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(body, &s); err == nil && strings.TrimSpace(s.Instructions) != "" {
		return s.Instructions
	}
	return ""
}

func extractGeminiSystemInstruction(body []byte) string {
	var s struct {
		Instruction json.RawMessage `json:"systemInstruction"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return ""
	}
	var instruction struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(s.Instruction, &instruction); err != nil {
		return ""
	}
	var buf strings.Builder
	for _, part := range instruction.Parts {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(part.Text)
	}
	return buf.String()
}

func extractTextContent(raw json.RawMessage) string {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if strings.TrimSpace(str) == "" {
			return ""
		}
		return str
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var buf strings.Builder
	for _, part := range parts {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(part.Text)
	}
	return buf.String()
}

func extractChatSystemMessages(body []byte) string {
	var s struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &s); err != nil || len(s.Messages) == 0 {
		return ""
	}
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(s.Messages, &msgs); err != nil {
		return ""
	}
	var buf strings.Builder
	for _, m := range msgs {
		if !strings.EqualFold(m.Role, "system") {
			continue
		}
		// Content may be a string or an array of parts; concatenate either way.
		var str string
		if err := json.Unmarshal(m.Content, &str); err == nil {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(str)
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &parts); err == nil {
			for _, p := range parts {
				if buf.Len() > 0 {
					buf.WriteByte('\n')
				}
				buf.WriteString(p.Text)
			}
		}
	}
	return buf.String()
}
