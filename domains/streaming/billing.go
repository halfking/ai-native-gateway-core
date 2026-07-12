package streaming

import "strings"

// shouldChargeUsage defines the billing boundary after request processing.
// Token usage proves that the upstream request was admitted and produced
// billable work. Client cancellation therefore remains billable when usage
// exists, while gateway failures before upstream admission do not.
func shouldChargeUsage(
	success bool,
	failureStage *string,
	errorKind *string,
	promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int,
	streamChunkCount int,
) bool {
	if promptTokens <= 0 && completionTokens <= 0 && cacheReadTokens <= 0 && cacheWriteTokens <= 0 {
		return false
	}
	if errorKind != nil && strings.EqualFold(strings.TrimSpace(*errorKind), "empty_response") {
		return false
	}
	if errorKind != nil {
		switch strings.ToLower(strings.TrimSpace(*errorKind)) {
		case "client_cancel", "client_disconnected":
			return streamChunkCount > 0
		}
	}
	if failureStage != nil {
		switch strings.ToLower(strings.TrimSpace(*failureStage)) {
		case "auth", "rate_limit", "routing", "validation", "precheck", "gateway":
			return false
		}
	}
	if success {
		return true
	}
	return failureStage != nil && strings.EqualFold(strings.TrimSpace(*failureStage), "upstream")
}
