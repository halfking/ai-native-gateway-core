// Package errorsx — context_limit_parser.go
//
// ParseContextLimitFromError extracts the actual context limit from upstream
// error messages when the provider returns a context_length error with the
// real limit embedded in the message body.
//
// Used by the context-length recovery path (executors.handleContextLengthRecovery)
// to:
//  1. Discover the true limit when config is missing or stale
//  2. Use aggressive compression (60% target) for 4xx recovery
//  3. Optionally persist the discovered limit for future requests
package errorsx

import (
	"regexp"
	"strconv"
)

// contextLimitPatterns matches common upstream context-limit error formats.
// Examples:
//   - "This model's maximum context length is 262144 tokens"
//   - "context window is 128000"
//   - "上下文长度限制为 32768 个token"
//   - "exceeded the context window of 200000 tokens"
var contextLimitPatterns = []*regexp.Regexp{
	// English patterns - primary formats
	regexp.MustCompile(`(?i)(?:maximum\s+)?context\s+(?:window|length)\s+(?:is\s+)?(\d+)`),
	regexp.MustCompile(`(?i)context\s+(?:window|length)\s+of\s+(\d+)`),
	// "maximum context is 200000" - simpler pattern
	regexp.MustCompile(`(?i)maximum\s+context\s+is\s+(\d+)`),
	// "The context window for this model is 16384"
	regexp.MustCompile(`(?i)context\s+window\s+for\s+.{0,30}\s+is\s+(\d+)`),
	// Chinese patterns
	regexp.MustCompile(`(?:上下文|Context)(?:长度)?限制(?:为)?[\s]*(\d+)`),
	// "However, your messages resulted in 263247 tokens" - extract actual usage (lowest priority)
	regexp.MustCompile(`(?i)your\s+messages?\s+resulted\s+in\s+(\d+)\s+tokens?`),
}

// ContextLimitEvidence describes how confidently a value extracted from an
// upstream error can be treated as the model's context limit.
type ContextLimitEvidence string

const (
	ContextLimitAuthoritative ContextLimitEvidence = "authoritative_limit"
	ContextLimitObservedUsage ContextLimitEvidence = "observed_usage"
)

// ContextLimitParseResult is the structured result of parsing an upstream error.
type ContextLimitParseResult struct {
	Limit    int
	Found    bool
	Evidence ContextLimitEvidence
}

// ParseContextLimitResult extracts a context-related token count and records
// whether the provider stated a model limit or only reported request usage.
func ParseContextLimitResult(body string) ContextLimitParseResult {
	var observed int
	for i, re := range contextLimitPatterns {
		matches := re.FindStringSubmatch(body)
		if len(matches) <= 1 {
			continue
		}
		val, err := strconv.Atoi(matches[1])
		if err != nil || val <= 0 {
			continue
		}
		if i == len(contextLimitPatterns)-1 {
			if observed == 0 {
				observed = val
			}
			continue
		}
		return ContextLimitParseResult{Limit: val, Found: true, Evidence: ContextLimitAuthoritative}
	}
	if observed > 0 {
		return ContextLimitParseResult{Limit: observed, Found: true, Evidence: ContextLimitObservedUsage}
	}
	return ContextLimitParseResult{}
}

// ParseContextLimitFromError preserves the original API. Callers that persist
// a value should use ParseContextLimitResult and require authoritative evidence.
func ParseContextLimitFromError(body string) (limit int, found bool) {
	result := ParseContextLimitResult(body)
	return result.Limit, result.Found
}

// ParseContextLimitPrimaryFromError is the authoritative-limit-only variant of
// ParseContextLimitFromError. It returns (limit, true) only when one of the
// PRIMARY patterns matched — i.e. the error body states the model's actual
// ceiling ("maximum context length is 262144 tokens", "context window of
// 200000"). The "your messages resulted in X tokens" fallback is deliberately
// ignored here: that number is THIS request's token count, not the model
// limit, so callers must never persist it as a discovered context window.
//
// Callers that only need an in-memory compression target may keep using
// ParseContextLimitFromError (fallback included); callers that discover and
// persist a limit (e.g. credential_model_bindings.context_window_override)
// must gate on this function.
func ParseContextLimitPrimaryFromError(body string) (limit int, found bool) {
	// Every pattern except the trailing "resulted in" fallback is primary.
	primaryPatterns := contextLimitPatterns[:len(contextLimitPatterns)-1]
	for _, re := range primaryPatterns {
		matches := re.FindStringSubmatch(body)
		if len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil && val > 0 {
				return val, true
			}
		}
	}
	return 0, false
}
