// Package transform — ctx_compress.go
//
// Client-side context window enforcement for the OpenAI chat (Q1/Q2/Q3) path.
// Anthropic-Messages passthrough (Q4) is intentionally NOT compressed here:
// Q4 forwards request body bytes verbatim per the bytes-level passthrough
// contract documented in relay/messages.go:169.
//
// Strategy: drop-oldest (truncate). We never rewrite message content; the
// oldest non-system message is removed in pairs (user/assistant) so the
// conversation stays well-formed for upstream chat APIs.
//
// Token estimation: heuristic 1 token ≈ 3.5 chars. This is the same rule of
// thumb used by rel/usage_estimator.go:30-61 for billing fallback and is
// intentionally conservative (slightly over-counts, so we trim a bit more
// rather than risk pushing past the upstream limit).
package transform

import (
	"encoding/json"
	"log/slog"
)

// defaultSoftLimitFraction is the fraction of context_window below which we
// stop trimming. We pick 0.85 so there's headroom for the upstream's
// response generation (max_tokens) plus the model's own internal overhead.
const defaultSoftLimitFraction = 0.85

// charsPerToken is the heuristic used by the trim estimator. Calibrated to
// be a touch conservative (over-estimate) so we err on the safe side and
// never push past the upstream limit.
const charsPerToken = 3.5

// CompressMessagesIfNeeded trims messages from the oldest non-system pair
// until the estimated prompt token count fits under
// contextWindow * defaultSoftLimitFraction.
//
// Returns the (possibly modified) body bytes. If bodyBytes is not a
// recognisable chat-style body (e.g. not JSON, no "messages" array, or no
// contextWindow provided), it is returned unchanged.
//
// Q4 (anthropic-messages) must NEVER call this — pass cand.Protocol == "anthropic-messages"
// upstream and skip the call.
func CompressMessagesIfNeeded(bodyBytes []byte, contextWindow int) []byte {
	if contextWindow <= 0 {
		return bodyBytes
	}

	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil || len(req.Messages) == 0 {
		return bodyBytes
	}

	softLimit := int(float64(contextWindow) * defaultSoftLimitFraction)
	estimated := estimatePromptTokens(bodyBytes)
	if estimated <= softLimit {
		return bodyBytes
	}

	// Walk from the start, drop in pairs (user+assistant or assistant+user)
	// until the body estimate fits under softLimit. We keep at least one
	// non-system message (the most recent user turn) so the upstream
	// doesn't see an empty conversation.
	trimmed := trimOldestPairs(req.Messages, softLimit)
	if len(trimmed) == len(req.Messages) {
		return bodyBytes
	}

	// Reassemble body with trimmed messages. Preserve all other fields verbatim
	// so we don't break request body shape (tools, temperature, stream, etc).
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &generic); err != nil {
		return bodyBytes
	}
	rawTrimmed, err := json.Marshal(trimmed)
	if err != nil {
		return bodyBytes
	}
	generic["messages"] = rawTrimmed

	out, err := json.Marshal(generic)
	if err != nil {
		return bodyBytes
	}

	slog.Info("context_compress: trimmed messages",
		"original_count", len(req.Messages),
		"trimmed_count", len(trimmed),
		"dropped_count", len(req.Messages)-len(trimmed),
		"context_window", contextWindow,
		"soft_limit", softLimit,
		"estimated_tokens_before", estimated,
	)
	return out
}

// trimOldestPairs drops oldest non-system messages in pairs (user+assistant
// preferred; system messages are always preserved) until the total estimated
// token count fits under softLimit. Returns the (possibly modified) slice;
// if no trim was needed, returns the input unchanged.
//
// Algorithm: O(n) per pass; we re-estimate after each drop until we fit.
func trimOldestPairs(messages []json.RawMessage, softLimit int) []json.RawMessage {
	if len(messages) == 0 {
		return messages
	}
	// Always-preserve system messages: split into (system, rest).
	var system []json.RawMessage
	var rest []json.RawMessage
	for _, m := range messages {
		if isSystemMessage(m) {
			system = append(system, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(rest) <= 1 {
		// Nothing meaningful to drop (we keep at least the most recent
		// non-system message).
		return messages
	}

	// Estimate current total. We re-estimate after each drop.
	estimate := func(s []json.RawMessage) int {
		total := 0
		for _, m := range s {
			total += estimateMessageTokens(m)
		}
		return total
	}
	current := estimate(rest)
	// Drop from the front in pairs until we fit or we run out of pairs.
	for len(rest) >= 2 {
		// Account for system message overhead in the budget.
		if current+estimate(system) <= softLimit {
			break
		}
		// Drop two oldest.
		dropped := estimateMessageTokens(rest[0]) + estimateMessageTokens(rest[1])
		rest = rest[2:]
		current -= dropped
	}
	if len(rest) == len(messages)-len(system) {
		// Nothing changed.
		return messages
	}
	// Reassemble: system messages first, then the trimmed rest.
	out := make([]json.RawMessage, 0, len(system)+len(rest))
	out = append(out, system...)
	out = append(out, rest...)
	return out
}

// estimatePromptTokens is a coarse estimate of the total token count in the
// body, using a chars/3.5 heuristic applied to the full body length. Used
// for both pre-request trim trigger and post-drop re-check.
func estimatePromptTokens(bodyBytes []byte) int {
	return int(float64(len(bodyBytes)) / charsPerToken)
}

// estimateMessageTokens is a per-message estimate. Strips JSON framing
// ("role", "content" keys) before measuring.
func estimateMessageTokens(raw json.RawMessage) int {
	return int(float64(len(raw)) / charsPerToken)
}

// isSystemMessage returns true if the message has role=system. We look at
// the raw JSON rather than re-marshalling to keep this O(1) per call.
func isSystemMessage(raw json.RawMessage) bool {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Role == "system"
}
