// Package secretmask redacts provider API keys and common bearer-token /
// credential shapes from text before it is sent to the summarizer LLM.
//
// Context (docs/omni-ref3 C2): compression and session-summary paths build a
// plain-text conversation digest and hand it to a "summary" model. That digest
// is derived from the user's raw message bodies, which can carry pasted API
// keys (OpenAI sk-..., Anthropic sk-ant-..., AWS AKIA..., generic Bearer tokens,
// Authorization headers). Without masking, those secrets leave the data plane
// for whatever model backs summary generation.
//
// Scope: this is a best-effort, pattern-based masker for the summarizer input
// only. It does NOT touch the request body forwarded to the upstream model —
// the outbound body is byte-faithful by design. The mask is one-way (the
// placeholder is not reversible), which is correct for summary input (the LLM
// only needs to know "a credential was present", not its value).
//
// False-negative risk: pattern-based masking can miss non-canonical secret
// shapes; this layer reduces, not eliminates, leakage. False-positive risk is
// bounded: the patterns are anchored to provider-specific prefixes/suffixes or
// well-known header forms, so legitimate prose is unlikely to match.
package secretmask

import "regexp"

// Placeholder replaces any matched secret. Stable and grep-friendly.
const Placeholder = "[REDACTED]"

// secretPatterns matches canonical provider credential shapes. Order is not
// significant; matches do not overlap across these patterns.
//
// These mirror the detection patterns in safety/rules.go but are intentionally
// a separate, dependency-free set scoped to the summarizer use case — coupling
// summary masking to the (DB-backed, hot-reloaded) safety Rule system would
// add operational surface for a one-way text transform.
var secretPatterns = []*regexp.Regexp{
	// OpenAI: sk-...T3BlbkFJ... (the base64-decoded middle is constant).
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20}T3BlbkFJ[a-zA-Z0-9]{20}`),
	// Anthropic: sk-ant-...
	regexp.MustCompile(`sk-ant-[a-zA-Z0-9\-]{20,}`),
	// Generic OpenAI-style sk- prefix with enough trailing entropy to avoid
	// clobbering common words; kept lenient to catch Project/Service keys too.
	regexp.MustCompile(`sk-[a-zA-Z0-9]{32,}`),
	// AWS access key id.
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// Google API key.
	regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`),
	// Bearer / Authorization header values (case-insensitive scheme), up to the
	// next whitespace or quote — the token itself, not the surrounding header.
	regexp.MustCompile(`(?i)(bearer|authorization:\s*bearer)\s+[a-zA-Z0-9\-_.=]{20,}`),
	// x-api-key: <value> header form (Anthropic and others).
	regexp.MustCompile(`(?i)x-api-key:\s*[a-zA-Z0-9\-_]{20,}`),
}

// MaskSecrets returns a copy of text with every recognized credential shape
// replaced by Placeholder. It is safe for concurrent use (the regexps are
// compiled once and never mutated). Returns the original text if it is empty.
func MaskSecrets(text string) string {
	if text == "" {
		return text
	}
	out := text
	for _, re := range secretPatterns {
		out = re.ReplaceAllString(out, Placeholder)
	}
	return out
}
