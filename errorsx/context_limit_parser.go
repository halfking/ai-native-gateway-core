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

// ParseContextLimitFromError extracts the actual context limit from an upstream
// error message body. Returns (limit, true) when found, (0, false) otherwise.
//
// This function is called in two scenarios:
//  1. Primary: extract the configured limit from "maximum context length is X"
//  2. Fallback: extract actual usage from "your messages resulted in X tokens"
//
// When both patterns match, prefer the "maximum context length" value since
// that's the authoritative limit; the "resulted in" value is just the request
// size (may be larger than the limit).
func ParseContextLimitFromError(body string) (limit int, found bool) {
	var fallbackLimit int
	var fallbackFound bool

	// Try all patterns; the last one is the "resulted in" fallback
	resultedInPattern := contextLimitPatterns[len(contextLimitPatterns)-1]

	for _, re := range contextLimitPatterns {
		matches := re.FindStringSubmatch(body)
		if len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil && val > 0 {
				// For "resulted in X tokens", treat it as a lower-priority match
				if re == resultedInPattern {
					// This is the "resulted in" pattern - save it as fallback
					if !fallbackFound {
						fallbackLimit = val
						fallbackFound = true
					}
				} else {
					// This is a primary pattern (maximum/window) - prefer it
					return val, true
				}
			}
		}
	}

	// Return fallback value if found (from "resulted in" pattern)
	return fallbackLimit, fallbackFound
}
