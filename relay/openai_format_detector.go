package relay

import (
	"strings"
)

// isOpenAIFormatData performs a coarse string-based check to detect
// OpenAI-format data that should not appear in an Anthropic SSE stream.
// This is a fast pre-filter that runs before JSON parsing.
//
// Used in Q3 path (OpenAI client -> Anthropic upstream) to catch
// upstreams that leak OpenAI-format chunks into their Anthropic streams.
// See anthropic_to_openai_stream.go for the call site.
//
// Checks for OpenAI-specific fields that should never appear in Anthropic
// Messages streaming events:
//   - "choices" - OpenAI chat completions field
//   - "created" - OpenAI timestamp field (Anthropic uses ISO8601 strings)
//   - "object":"chat.completion" - OpenAI object type
//
// Returns true if the data appears to be OpenAI format and should be dropped.
func isOpenAIFormatData(data []byte) bool {
	dataStr := string(data)
	
	// Quick exit: if data is too short, it can't be a valid event
	if len(dataStr) < 10 {
		return false
	}
	
	// Check 1: Contains "choices" field as a top-level key (most common OpenAI indicator)
	// Use more specific pattern to avoid false positives when "choices" appears in text content
	if strings.Contains(dataStr, `"choices":[`) || strings.Contains(dataStr, `"choices": [`) {
		return true
	}
	
	// Check 2: Contains both "object" and "chat.completion" 
	// (OpenAI chat completion signature)
	if strings.Contains(dataStr, `"object"`) && 
	   strings.Contains(dataStr, `"chat.completion`) {
		return true
	}
	
	// Check 3: Contains "created" as a numeric field
	// Anthropic uses "created_at" with ISO8601 strings, not unix timestamps
	// Pattern: "created":1234567890 (numeric follows)
	if strings.Contains(dataStr, `"created":`) {
		// Look for the pattern with a digit after the colon
		idx := strings.Index(dataStr, `"created":`)
		if idx >= 0 && idx+10 < len(dataStr) {
			// Check if there's a digit after "created":
			afterColon := strings.TrimSpace(dataStr[idx+10:])
			if len(afterColon) > 0 && afterColon[0] >= '0' && afterColon[0] <= '9' {
				return true
			}
		}
	}
	
	return false
}

// truncateForLog truncates a string for logging, adding "..." if truncated.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
