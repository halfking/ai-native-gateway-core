package telemetry

import (
	requesttelemetry "github.com/kaixuan/llm-gateway-go/telemetry"
	"net/http"
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
		ClientIP:           requesttelemetry.ExtractClientIP(r),
		ClientForwardedFor: SanitizeForwardedFor(requesttelemetry.ExtractForwardedFor(r)),
		AgentName:          requesttelemetry.ExtractAgentName(r),
		AgentType:          requesttelemetry.ExtractAgentType(r),
	}
}

// MaskAPIKeyFingerprint masks an API key, keeping first 8 chars visible.
// Example: sk-1234abcd5678efgh -> sk-1234ab
func MaskAPIKeyFingerprint(key string) string {
	return requesttelemetry.MaskAPIKey(key)
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
