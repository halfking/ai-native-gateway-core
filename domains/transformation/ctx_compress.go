// Package transform — ctx_compress.go
//
// Client-side context window enforcement for OpenAI chat (Q1/Q2/Q3) and
// Anthropic Messages (Q4) paths. OpenAI chat trimming lives in
// CompressMessagesIfNeeded; Anthropic passthrough trimming lives in
// CompressAnthropicMessagesIfNeeded (wired from routing/executor_anthropic.go).
//
// Strategy: drop-oldest (truncate). We never rewrite message content; the
// oldest non-system message is removed in pairs (user/assistant) so the
// conversation stays well-formed for upstream chat APIs.
//
// Token estimation: heuristic 1 token ≈ 3.5 chars. This is the same rule of
// thumb used by rel/usage_estimator.go:30-61 for billing fallback and is
// intentionally conservative (slightly over-counts, so we trim a bit more
// rather than risk pushing past the upstream limit).
package transformation

import (
	"encoding/json"
	"log/slog"
)

// defaultTriggerFraction is the provider-context fraction that triggers
// proactive compression. Compression then targets a lower limit (see
// providerCompressionTargetTokens).
const defaultTriggerFraction = 0.80

// defaultSoftLimitFraction is retained as the public threshold fallback for
// byte-threshold helpers. Provider-aware trimming uses a 60% target.
const defaultSoftLimitFraction = defaultTriggerFraction

// providerCompressionTargetTokens returns the target prompt size after a
// provider-aware compression pass. Small-context models need a larger reserve:
// for a 1M window, 200K is safer than the general 60% target.
func providerCompressionTargetTokens(contextWindow int) int {
	if contextWindow <= 0 {
		return 0
	}
	target := int(float64(contextWindow) * 0.60)
	if contextWindow <= 1_000_000 && target > 200_000 {
		return 200_000
	}
	return target
}

func targetFractionForWindow(contextWindow int) float64 {
	if contextWindow <= 0 {
		return 0
	}
	return float64(providerCompressionTargetTokens(contextWindow)) / float64(contextWindow)
}

// aggressiveSoftLimitFraction is used for 4xx context-length recovery scenarios.
// We compress to 60% of the context window to:
//  1. Leave sufficient space for response generation (max_tokens)
//  2. Avoid boundary errors from token estimation inaccuracy
//  3. Reserve growth headroom for subsequent conversation turns
//
// This aggressive target handles cases where requests exceed the limit by a
// small margin (e.g. 0.4%) but the default 80% compression would still be
// too close to the boundary.
const aggressiveSoftLimitFraction = 0.60

// charsPerToken is the heuristic used by the trim estimator. Calibrated to
// be a touch conservative (over-estimate) so we err on the safe side and
// never push past the upstream limit.
const charsPerToken = 3.5

// CompressMessagesIfNeeded trims messages from the oldest non-system pair when
// the estimated prompt exceeds 80% of the provider window. The post-compression
// target is 60% of that window, capped at 200K tokens for windows up to 1M.
//
// Returns the (possibly modified) body bytes. If bodyBytes is not a
// recognisable chat-style body (e.g. not JSON, no "messages" array, or no
// contextWindow provided), it is returned unchanged.
//
// Q4 (anthropic-messages) must NEVER call this — pass cand.Protocol == "anthropic-messages"
// upstream and skip the call.
// CompressAnthropicMessagesIfNeeded applies the same provider-aware 80% trigger
// and 60% (or 200K small-window) target to Anthropic Messages bodies. The system
// field (string or array) is always preserved.
func CompressAnthropicMessagesIfNeeded(bodyBytes []byte, contextWindow int) []byte {
	if contextWindow <= 0 || estimatePromptTokens(bodyBytes) <= int(float64(contextWindow)*defaultTriggerFraction) {
		return bodyBytes
	}
	return compressAnthropicMessagesWithTarget(bodyBytes, contextWindow, targetFractionForWindow(contextWindow), "provider_window")
}

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

	triggerLimit := int(float64(contextWindow) * defaultTriggerFraction)
	estimated := estimatePromptTokens(bodyBytes)
	if estimated <= triggerLimit {
		return bodyBytes
	}
	softLimit := providerCompressionTargetTokens(contextWindow)

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

	estimatedAfter := estimatePromptTokens(out)
	slog.Info("context_compress: trimmed messages",
		"reason", "default",
		"original_count", len(req.Messages),
		"trimmed_count", len(trimmed),
		"dropped_count", len(req.Messages)-len(trimmed),
		"context_window", contextWindow,
		"soft_limit", softLimit,
		"estimated_tokens_before", estimated,
		"estimated_tokens_after", estimatedAfter,
		"bytes_before", len(bodyBytes),
		"bytes_after", len(out),
	)
	return out
}

// trimOldestPairs drops oldest non-system messages from the front until the
// total estimated token count fits under softLimit. Returns the (possibly
// modified) slice; if no trim was needed, returns the input unchanged.
//
// Tool-integrity aware (fix for MiniMax error 2013 "tool result's tool id not
// found"): a tool-call round is treated as an ATOMIC unit. Dropping an
// assistant tool_calls/tool_use message while keeping its matching tool result
// would orphan the tool_use_id and cause the upstream (MiniMax/Anthropic) to
// reject the request. To avoid that, when the front of `rest` is the head of a
// tool round (an assistant carrying tool_calls/tool_use, or a stray tool
// result whose origin was already dropped), the ENTIRE round is dropped
// together — the assistant call message plus every consecutive tool-result
// message (OpenAI role:"tool" and Anthropic tool_result blocks) plus the
// single trailing user turn that acknowledges the results. Non-tool messages
// are still dropped in pairs as before.
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
	// Drop from the front until we fit. Each iteration drops either an atomic
	// tool round (>=2 messages) or a plain pair (2 messages), but never leaves
	// a tool result orphaned.
	for len(rest) >= 2 {
		// Account for system message overhead in the budget.
		if current+estimate(system) <= softLimit {
			break
		}
		n := dropExtent(rest)
		// Guard against zero-progress (malformed message): fall back to a
		// plain pair-drop so the loop always terminates.
		if n < 1 {
			n = 2
			if n > len(rest) {
				n = len(rest)
			}
		}
		// Never drop everything — keep at least the most recent message.
		if n >= len(rest) {
			break
		}
		dropped := 0
		for i := 0; i < n; i++ {
			dropped += estimateMessageTokens(rest[i])
		}
		rest = rest[n:]
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

func countNonSystemMessages(messages []json.RawMessage) int {
	count := 0
	for _, message := range messages {
		if !isSystemMessage(message) {
			count++
		}
	}
	return count
}

func dropOldestMessageUnit(messages []json.RawMessage) []json.RawMessage {
	if len(messages) == 0 {
		return messages
	}
	var system, rest []json.RawMessage
	for _, message := range messages {
		if isSystemMessage(message) {
			system = append(system, message)
		} else {
			rest = append(rest, message)
		}
	}
	if len(rest) <= 1 {
		return messages
	}
	n := dropExtent(rest)
	if n <= 0 || n >= len(rest) {
		return messages
	}
	out := make([]json.RawMessage, 0, len(system)+len(rest)-n)
	out = append(out, system...)
	out = append(out, rest[n:]...)
	return out
}

// dropExtent returns how many leading messages of `rest` should be dropped as
// one atomic unit. The unit is:
//
//   - A whole tool round: an assistant message carrying tool_calls/tool_use,
//     followed by every consecutive tool-result message (OpenAI role:"tool"
//     and Anthropic tool_result blocks) and the single trailing user turn
//     that acknowledges them. Returns the full extent (>=2).
//   - A stray tool result at the front whose origin assistant was already
//     dropped in a prior pass: returns the extent of the remaining result
//     chain + trailing user ack (>=1).
//   - Otherwise (plain user/assistant): starts at 2 (a pair), then EXTENDS
//     forward if the resulting boundary would land inside a tool round — i.e.
//     if rest[n] is a tool result whose matching tool_use would be dropped.
//     This is the case that caused MiniMax 2013: dropping user+assistant in
//     a user,assistant(tool_calls),tool(result),... sequence orphans the
//     tool_result. Extending past the dangling result chain keeps every
//     surviving tool_result matched to a surviving tool_use.
//
// The goal is that after a drop the next surviving message is never a tool
// result whose matching tool_use was removed.
func dropExtent(rest []json.RawMessage) int {
	if len(rest) == 0 {
		return 0
	}
	// Case 1: head is a tool round (assistant with tool_calls / tool_use).
	if isToolRoundHead(rest[0]) {
		return toolRoundExtent(rest)
	}
	// Case 2: head is itself a tool result (OpenAI role:"tool" or Anthropic
	// tool_result block) whose origin was already dropped. Drop the whole
	// dangling result chain so nothing references a missing tool_use_id.
	if isToolResultMessage(rest[0]) {
		return danglingResultExtent(rest)
	}
	// Case 3: plain messages — start with a pair (keep parity with legacy
	// behaviour which dropped two at a time)...
	n := 2
	if n > len(rest) {
		n = len(rest)
	}
	// ...then extend forward if the boundary lands on a tool result whose
	// matching tool_use is being dropped. Consume the dangling result chain
	// (and optional trailing user ack) so the surviving head is clean.
	for n < len(rest) && isToolResultMessage(rest[n]) {
		n++
	}
	if n < len(rest) && messageRole(rest[n]) == "user" && !isToolResultMessage(rest[n]) {
		// Only swallow the ack if something remains after it.
		if n+1 < len(rest) {
			n++
		}
	}
	return n
}

// isToolRoundHead reports whether msg is an assistant message that initiates a
// tool round (carries OpenAI tool_calls or an Anthropic tool_use block).
func isToolRoundHead(msg json.RawMessage) bool {
	role := messageRole(msg)
	if role != "assistant" {
		return false
	}
	return messageHasToolCalls(msg) || messageHasAnthropicToolUse(msg)
}

// toolRoundExtent returns the count of leading messages that form one tool
// round, starting at the assistant tool-call message: the head plus every
// consecutive tool-result message, plus the single trailing user message that
// acknowledges those results (if any). The head MUST be a tool round head.
func toolRoundExtent(rest []json.RawMessage) int {
	n := 1 // the assistant tool-call head
	// Consume all consecutive tool results (OpenAI role:"tool" or Anthropic
	// tool_result-bearing user messages).
	for n < len(rest) && isToolResultMessage(rest[n]) {
		n++
	}
	// Optionally consume a single trailing user turn that acknowledges the
	// results. This is the common Cursor/RooCode shape: after the tool
	// results the client posts a user turn with the next instruction.
	if n < len(rest) && messageRole(rest[n]) == "user" && !isToolResultMessage(rest[n]) {
		// Only swallow it if there is something after it (we never want to
		// eat the final user turn the model must answer).
		if n+1 < len(rest) {
			n++
		}
	}
	return n
}

// danglingResultExtent returns the count of leading tool-result messages
// (plus a single trailing user ack) whose origin assistant was already
// dropped. Used when a prior pass removed the tool-call head but the result
// chain remains at the front.
func danglingResultExtent(rest []json.RawMessage) int {
	n := 0
	for n < len(rest) && isToolResultMessage(rest[n]) {
		n++
	}
	if n < len(rest) && messageRole(rest[n]) == "user" && !isToolResultMessage(rest[n]) {
		if n+1 < len(rest) {
			n++
		}
	}
	if n == 0 {
		n = 1
	}
	return n
}

// isToolResultMessage reports whether msg carries a tool result — either an
// OpenAI role:"tool" message (with tool_call_id) or a user message whose
// content array contains an Anthropic tool_result block.
func isToolResultMessage(msg json.RawMessage) bool {
	switch messageRole(msg) {
	case "tool":
		return true
	case "user":
		return messageHasAnthropicToolResult(msg)
	}
	return false
}

// messageRole returns the message role string, or "" if unparseable.
func messageRole(raw json.RawMessage) string {
	var probe struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Role
}

// messageHasToolCalls reports whether an OpenAI-format assistant message has a
// non-empty tool_calls array.
func messageHasToolCalls(raw json.RawMessage) bool {
	var probe struct {
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return len(probe.ToolCalls) > 0 && string(probe.ToolCalls) != "null"
}

// messageHasAnthropicToolUse reports whether a message's content array
// contains an Anthropic tool_use block.
func messageHasAnthropicToolUse(raw json.RawMessage) bool {
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(probe.Content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "tool_use" {
			return true
		}
	}
	return false
}

// messageHasAnthropicToolResult reports whether a message's content array
// contains an Anthropic tool_result block.
func messageHasAnthropicToolResult(raw json.RawMessage) bool {
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(probe.Content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "tool_result" {
			return true
		}
	}
	return false
}

// estimatePromptTokens is a coarse estimate of the total token count in the
// body, using a chars/3.5 heuristic applied to the full body length. Used
// for both pre-request trim trigger and post-drop re-check.
func estimatePromptTokens(bodyBytes []byte) int {
	return int(float64(len(bodyBytes)) / charsPerToken)
}

// EstimateTokens is the public version of estimatePromptTokens. Used by
// compressor/ (compression v7 Round 47) for the mode=1 auto_threshold
// pre-request check. Same heuristic (chars/3.5) — calibrated conservative
// so the threshold is over- rather than under-counted, which is the safe
// direction for a "should we compress?" gate.
//
// Exposed so callers outside this package can compute the token estimate
// without re-implementing the chars/3.5 rule. Pass the raw body bytes;
// the function takes care of JSON framing cost (it just counts chars, no
// parsing). For per-message precision, use estimateMessageTokens (still
// package-private).
//
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §2 (mode=1
// dynamic threshold = cand.ContextWindow × fraction × charsPerToken).
func EstimateTokens(bodyBytes []byte) int {
	return estimatePromptTokens(bodyBytes)
}

// ThresholdBytes converts a model context window into the body-byte size
// that triggers compression. Returns 0 when window is non-positive
// (caller is expected to skip compression for unknown / unset windows).
//
//	threshold = context_window × fraction × charsPerToken
//
// This is the inverse of the pre-request check: caller compares
// len(bodyBytes) against threshold to decide whether to compress.
//
// Default fraction (0.80) matches defaultSoftLimitFraction for in-place
// pre-request trim; callers may pass a configured fraction when needed.
//
// Examples:
//
//	ThresholdBytes(128000, 0.8) → 358400  (cand.ContextWindow=128000)
//	ThresholdBytes( 64000, 0.8) → 179200
//	ThresholdBytes(256000, 0.8) → 716800
//	ThresholdBytes(     0, _)   → 0      (skip)
func ThresholdBytes(contextWindow int, fraction float64) int {
	if contextWindow <= 0 {
		return 0
	}
	if fraction <= 0 {
		fraction = defaultSoftLimitFraction
	}
	return int(float64(contextWindow) * fraction * charsPerToken)
}

// NeedsCompression is the convenience pre-request gate. Returns true when
// len(bodyBytes) exceeds the threshold for the given context window.
// Returns false when contextWindow is non-positive (model window unknown
// — caller's signal to fall through to the existing 4xx recovery path
// instead of guessing).
//
// Used by compressor/ (v7 Round 47) as the mode=1 (auto_threshold) trigger.
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.4 / §5.
func NeedsCompression(bodyBytes []byte, contextWindow int, fraction float64) bool {
	threshold := ThresholdBytes(contextWindow, fraction)
	if threshold <= 0 {
		return false
	}
	return len(bodyBytes) > threshold
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

// CompressMessagesAggressively is an aggressive version of CompressMessagesIfNeeded,
// used specifically for context-length 4xx recovery scenarios. It compresses to
// contextWindow * 0.60 (instead of 0.80) to provide a larger safety margin for:
//   - Response generation tokens (max_tokens parameter)
//   - Token estimation inaccuracy (chars/3.5 is a heuristic)
//   - Future conversation growth
//
// This handles cases where a request exceeds the context limit by a small margin
// (e.g. 0.4%) and the default 80% compression would still be too close to the
// boundary, risking another 4xx on retry.
//
// Use this for OpenAI chat-style bodies. For Anthropic Messages API, use
// CompressAnthropicMessagesAggressively instead.
func CompressMessagesAggressively(bodyBytes []byte, contextWindow int) []byte {
	return compressMessagesWithTarget(bodyBytes, contextWindow, aggressiveTargetFraction(contextWindow), "aggressive")
}

// CompressAnthropicMessagesAggressively is the aggressive version for Anthropic
// Messages API bodies. Compresses to 60% of context window for 4xx recovery.
func CompressAnthropicMessagesAggressively(bodyBytes []byte, contextWindow int) []byte {
	return compressAnthropicMessagesWithTarget(bodyBytes, contextWindow, aggressiveTargetFraction(contextWindow), "aggressive")
}

func aggressiveTargetFraction(contextWindow int) float64 {
	if contextWindow <= 0 {
		return aggressiveSoftLimitFraction
	}
	return float64(providerCompressionTargetTokens(contextWindow)) / float64(contextWindow)
}

// compressMessagesWithTarget is the internal implementation used by both
// CompressMessagesIfNeeded (80% target) and CompressMessagesAggressively (60% target).
func compressMessagesWithTarget(bodyBytes []byte, contextWindow int, targetFraction float64, reason string) []byte {
	if contextWindow <= 0 {
		return bodyBytes
	}

	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil || len(req.Messages) == 0 {
		return bodyBytes
	}

	softLimit := int(float64(contextWindow) * targetFraction)
	estimated := estimatePromptTokens(bodyBytes)

	if estimated <= softLimit {
		return bodyBytes
	}

	var generic map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &generic); err != nil {
		return bodyBytes
	}
	buildBody := func(messages []json.RawMessage) ([]byte, error) {
		rawTrimmed, err := json.Marshal(messages)
		if err != nil {
			return nil, err
		}
		generic["messages"] = rawTrimmed
		return json.Marshal(generic)
	}

	// Reserve the serialized envelope and non-message fields before asking the
	// message trimmer for a budget. This keeps large tools/response schemas from
	// consuming the target after message-only trimming has already stopped.
	emptyBody, err := buildBody(nil)
	if err != nil {
		return bodyBytes
	}
	messageBudget := softLimit - estimatePromptTokens(emptyBody)
	if messageBudget < 0 {
		messageBudget = 0
	}
	trimmed := trimOldestPairs(req.Messages, messageBudget)
	if len(trimmed) == len(req.Messages) {
		return bodyBytes
	}

	out, err := buildBody(trimmed)
	if err != nil {
		return bodyBytes
	}
	// Message/tool atomicity can leave the estimate slightly above the target;
	// continue dropping complete oldest units until the full reconstructed body
	// fits or only the newest message remains.
	for estimatePromptTokens(out) > softLimit && countNonSystemMessages(trimmed) > 1 {
		next := dropOldestMessageUnit(trimmed)
		if len(next) >= len(trimmed) {
			break
		}
		trimmed = next
		candidate, candidateErr := buildBody(trimmed)
		if candidateErr != nil {
			break
		}
		out = candidate
	}

	estimatedAfter := estimatePromptTokens(out)
	slog.Info("context_compress: trimmed messages",
		"reason", reason,
		"original_count", len(req.Messages),
		"trimmed_count", len(trimmed),
		"dropped_count", len(req.Messages)-len(trimmed),
		"context_window", contextWindow,
		"target_fraction", targetFraction,
		"soft_limit", softLimit,
		"estimated_tokens_before", estimated,
		"estimated_tokens_after", estimatedAfter,
		"bytes_before", len(bodyBytes),
		"bytes_after", len(out),
	)
	return out
}

// compressAnthropicMessagesWithTarget is the Anthropic version of compressMessagesWithTarget.
func compressAnthropicMessagesWithTarget(bodyBytes []byte, contextWindow int, targetFraction float64, reason string) []byte {
	if contextWindow <= 0 {
		return bodyBytes
	}

	var req struct {
		Messages []json.RawMessage `json:"messages"`
		System   json.RawMessage   `json:"system"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil || len(req.Messages) == 0 {
		return bodyBytes
	}

	softLimit := int(float64(contextWindow) * targetFraction)
	systemTokens := estimateMessageTokens(req.System)
	estimated := estimatePromptTokens(bodyBytes)
	if estimated <= softLimit {
		return bodyBytes
	}

	trimmed := trimOldestPairs(req.Messages, softLimit-systemTokens)
	if len(trimmed) == len(req.Messages) {
		return bodyBytes
	}

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

	estimatedAfter := estimatePromptTokens(out)
	slog.Info("context_compress: trimmed anthropic messages",
		"reason", reason,
		"original_count", len(req.Messages),
		"trimmed_count", len(trimmed),
		"dropped_count", len(req.Messages)-len(trimmed),
		"context_window", contextWindow,
		"target_fraction", targetFraction,
		"soft_limit", softLimit,
		"estimated_tokens_before", estimated,
		"estimated_tokens_after", estimatedAfter,
		"bytes_before", len(bodyBytes),
		"bytes_after", len(out),
	)
	return out
}
