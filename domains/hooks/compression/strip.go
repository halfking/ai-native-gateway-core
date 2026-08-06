// Package compressor - strip.go (v4 T1)
//
// Tool/thinking info stripper for the v4 intelligent session compression.
//
// Problem:
//
//	LLM conversations contain non-semantic overhead that wastes context:
//	  - tool_calls (assistant -> tool invocation metadata)
//	  - tool_results (tool -> assistant response data)
//	  - thinking/blocks (Anthropic extended thinking)
//	  - function_call legacy fields
//	These are essential for the LLM to process, but once a tool round
//	completes (tool_call → tool_result → user acknowledgement), the
//	tool details are no longer needed in the compressed context.
//
// Strategy (v4 smart compression):
//
//	Phase 1 (pre-compress strip):  COMPLETED tool rounds are stripped
//	from the messages array before LLM summarization. The 7-segment
//	summary (compaction.go) already captures tool outputs as "Key
//	References" — we don't need the raw tool_call/tool_result pairs.
//
//	Phase 2 (thinking strip):      Anthropic "thinking" content blocks
//	and any non-text content (image_url, input_audio) are removed.
//	Only "text" and "tool_use" / "tool_result" blocks survive.
//
// Preserved fields:
//   - user text messages
//   - assistant text messages (including summary markers)
//   - system messages (verbatim)
//   - INCOMPLETE tool rounds (tool_call without matching tool_result)
//   - the LAST completed tool round (for context continuity)

package compression

import (
	"encoding/json"
)

// StripResult describes what was removed from a body.
type StripResult struct {
	ToolCallsRemoved   int  `json:"tool_calls_removed"`
	ToolResultsRemoved int  `json:"tool_results_removed"`
	ThinkingRemoved    int  `json:"thinking_removed"`
	MessagesRemoved    int  `json:"messages_removed"`
	BytesBefore        int  `json:"bytes_before"`
	BytesAfter         int  `json:"bytes_after"`
	DidStrip           bool `json:"did_strip"`
}

// StripToolInfo removes completed tool rounds and thinking blocks from
// a messages array. Returns the stripped body and a result summary.
//
// "Completed tool round" = an assistant message carrying tool_calls
// where every call id has at least one matching tool result. Once all
// results exist the round is self-contained and can be removed from the
// compressed context — the tool outputs are captured in the LLM summary
// as "Key References". Incomplete rounds (missing results) are always
// preserved, because dropping the call would orphan the results that do
// exist and dropping the results would leave the call unanswered.
//
// 2026-08-06 fix: integrity is verified AFTER stripping. If any
// surviving tool result lacks a preceding assistant.tool_calls with a
// matching id, the strip is rejected and the original body is returned
// unchanged (fail-open). Data loss is preferable to corruption.
func StripToolInfo(body []byte, protocol string) ([]byte, *StripResult) {
	if len(body) == 0 {
		return body, &StripResult{DidStrip: false}
	}

	result := &StripResult{
		BytesBefore: len(body),
	}

	msgs, err := extractMessages(body)
	if err != nil || len(msgs) == 0 {
		return body, result
	}

	rounds := detectToolRounds(msgs)
	filtered := filterMessages(msgs, rounds, result)
	if len(filtered) == len(msgs) && result.ThinkingRemoved == 0 {
		return body, result
	}

	// Integrity guard: after filtering, every tool result must still have
	// a preceding assistant.tool_calls with a matching id. If the filter
	// produced an orphan, abandon the strip and return the original body
	// verbatim. This is the fail-open path — we never ship a body whose
	// tool chain we broke, even if it means carrying more context.
	if !toolChainIntact(filtered) {
		return body, &StripResult{BytesBefore: len(body), BytesAfter: len(body), DidStrip: false}
	}

	newMsgsRaw, err := json.Marshal(filtered)
	if err != nil {
		return body, result
	}

	newBody, ok := spliceBodyMessages(body, newMsgsRaw)
	if !ok {
		return body, result
	}

	result.BytesAfter = len(newBody)
	result.DidStrip = true
	return newBody, result
}

// StripThinkingBlocksOnly removes Anthropic "thinking" content blocks
// without touching tool rounds. Safe to run unconditionally (even when
// the compression window has not triggered), because it only deletes a
// non-semantic block type and never breaks tool_call/tool_result
// pairing. Returns the cleaned body and a result summary.
func StripThinkingBlocksOnly(body []byte) ([]byte, *StripResult) {
	if len(body) == 0 {
		return body, &StripResult{DidStrip: false}
	}
	result := &StripResult{BytesBefore: len(body)}
	msgs, err := extractMessages(body)
	if err != nil || len(msgs) == 0 {
		return body, result
	}
	if !hasThinkingBlocks(msgs) {
		return body, result
	}
	filtered := make([]json.RawMessage, 0, len(msgs))
	for _, msg := range msgs {
		cleaned := stripThinkingBlocks(msg)
		if cleaned == nil {
			result.ThinkingRemoved++
			continue
		}
		filtered = append(filtered, cleaned)
	}
	if len(filtered) == len(msgs) {
		return body, result
	}
	newMsgsRaw, err := json.Marshal(filtered)
	if err != nil {
		return body, result
	}
	newBody, ok := spliceBodyMessages(body, newMsgsRaw)
	if !ok {
		return body, result
	}
	result.BytesAfter = len(newBody)
	result.DidStrip = true
	return newBody, result
}

// toolChainIntact reports whether every tool result in msgs has a
// preceding assistant message whose tool_calls contain the result's
// tool_call_id. Used as a post-condition after stripping.
func toolChainIntact(msgs []json.RawMessage) bool {
	active := make(map[string]bool)
	for _, raw := range msgs {
		role := messageRole(raw)
		switch role {
		case "assistant":
			active = make(map[string]bool)
			for _, id := range extractToolCallIDs(raw) {
				if id != "" {
					active[id] = true
				}
			}
		case "user":
			active = make(map[string]bool)
		case "tool":
			id := toolCallIDOf(raw)
			if id == "" || !active[id] {
				return false
			}
		}
	}
	return true
}

// toolRound describes one assistant tool-call message together with the
// tool-result messages that resolve its calls. A round is "complete" when
// every call id has at least one matching tool result.
//
// 2026-08-06 fix: detectToolRounds previously returned a flat
// map[int]bool mixing assistant anchors and tool results from different
// rounds. filterMessages then tried to "keep the last round" by deleting
// the single highest index from that map — which kept a tool result
// whose assistant.tool_calls anchor was still deleted, producing an
// orphan that SanitizeToolMessages later removed. Structured rounds make
// preservation atomic and let the filter verify integrity.
type toolRound struct {
	anchor   int   // index of the assistant message carrying tool_calls
	results  []int // indexes of tool results matching the anchor's call ids
	complete bool  // every call id has at least one matching result
}

// detectToolRounds scans messages for assistant tool-call rounds.
//
// A round starts at an assistant message that carries tool_calls and
// spans the anchor plus every consecutive tool result whose
// tool_call_id matches one of the anchor's call ids. Parallel calls
// (multiple ids in one assistant message) form a single round, because
// the provider requires all of their results together. A round is
// complete when every call id has at least one matching result;
// incomplete rounds are never stripped.
func detectToolRounds(msgs []json.RawMessage) []toolRound {
	var rounds []toolRound
	i := 0
	for i < len(msgs) {
		if !hasToolCalls(msgs[i]) {
			i++
			continue
		}
		callIDs := extractToolCallIDs(msgs[i])
		if len(callIDs) == 0 {
			i++
			continue
		}
		need := make(map[string]bool, len(callIDs))
		for _, id := range callIDs {
			need[id] = true
		}
		r := toolRound{anchor: i}
		j := i + 1
		for j < len(msgs) {
			if !isToolResult(msgs[j]) {
				break
			}
			if matchesAnyToolCall(msgs[j], callIDs) {
				r.results = append(r.results, j)
				id := toolCallIDOf(msgs[j])
				if id != "" {
					delete(need, id)
				}
			}
			j++
		}
		r.complete = len(need) == 0
		rounds = append(rounds, r)
		i = j
	}
	return rounds
}

// filterMessages applies the strip rules:
//  1. Remove COMPLETED tool rounds, keeping the last `keepLastRounds`.
//  2. Remove thinking content blocks from otherwise-preserved messages.
//
// A round is removed as an atomic unit: the assistant anchor and every
// one of its result messages. Keeping a round keeps both the anchor and
// all of its results, so no tool result is ever left without its call.
//
// 2026-08-06 fix: the previous implementation removed the assistant
// anchor of the last round but kept one of its tool results, creating
// an orphan tool_call_id that downstream sanitisation silently deleted.
func filterMessages(msgs []json.RawMessage, rounds []toolRound, result *StripResult) []json.RawMessage {
	keepLast := keepLastRounds
	if keepLast > len(rounds) {
		keepLast = len(rounds)
	}
	remove := make(map[int]bool)
	completed := 0
	for idx, r := range rounds {
		if !r.complete {
			continue
		}
		completed++
		// Keep the last `keepLast` completed rounds verbatim (anchor +
		// results) so the most recent tool exchange stays intact.
		if completed > len(rounds)-keepLast && idx >= len(rounds)-keepLast {
			continue
		}
		remove[r.anchor] = true
		for _, ri := range r.results {
			remove[ri] = true
		}
	}

	if len(remove) == 0 && !hasThinkingBlocks(msgs) {
		return msgs
	}

	filtered := make([]json.RawMessage, 0, len(msgs))
	for i, msg := range msgs {
		if remove[i] {
			result.MessagesRemoved++
			if isToolResult(msg) {
				result.ToolResultsRemoved++
			} else {
				result.ToolCallsRemoved++
			}
			continue
		}
		cleaned := stripThinkingBlocks(msg)
		if cleaned == nil {
			result.ThinkingRemoved++
			continue
		}
		filtered = append(filtered, cleaned)
	}
	return filtered
}

// keepLastRounds is the number of completed tool rounds preserved for
// context continuity when stripping older rounds. Two (not one) keeps
// the immediately previous exchange plus its predecessor, which is what
// the model needs to continue a multi-step task without re-asking.
const keepLastRounds = 2

// hasAnyToolCallsAfter is unused but kept for future use.
func hasAnyToolCallsAfter(msgs []json.RawMessage, start int) bool { //nolint:unused
	for k := start; k < len(msgs); k++ {
		if hasToolCalls(msgs[k]) {
			return true
		}
	}
	return false
}

// toolCallIDOf returns the tool_call_id of a tool message, or "".
func toolCallIDOf(raw json.RawMessage) string {
	var m struct {
		ToolCallID string `json:"tool_call_id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ToolCallID
}

// hasToolCalls checks if an assistant message contains tool_calls.
func hasToolCalls(raw json.RawMessage) bool {
	var m struct {
		Role      string `json:"role"`
		ToolCalls any    `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return m.Role == "assistant" && m.ToolCalls != nil
}

// extractToolCallIDs extracts tool_call IDs from an assistant message.
func extractToolCallIDs(raw json.RawMessage) []string {
	var m struct {
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	ids := make([]string, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		if tc.ID != "" {
			ids = append(ids, tc.ID)
		}
	}
	return ids
}

// isToolResult checks if a message is a tool result.
func isToolResult(raw json.RawMessage) bool {
	var m struct {
		Role       string `json:"role"`
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return m.Role == "tool" && m.ToolCallID != ""
}

// matchesAnyToolCall checks if a tool_result matches one of the given call IDs.
func matchesAnyToolCall(raw json.RawMessage, callIDs []string) bool {
	var m struct {
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	for _, id := range callIDs {
		if m.ToolCallID == id {
			return true
		}
	}
	return false
}

// hasThinkingBlocks checks if any message contains Anthropic "thinking" blocks.
func hasThinkingBlocks(msgs []json.RawMessage) bool {
	for _, msg := range msgs {
		var m struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(msg, &m); err != nil {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				if p.Type == "thinking" {
					return true
				}
			}
		}
	}
	return false
}

// stripThinkingBlocks removes "thinking" and non-text content blocks.
// Returns the cleaned message, or nil if the entire message should be dropped.
func stripThinkingBlocks(raw json.RawMessage) json.RawMessage {
	var m struct {
		Content json.RawMessage `json:"content"`
		Role    string          `json:"role"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}

	// Check if content is a string (simple text) — no blocks to strip
	var simpleContent string
	if json.Unmarshal(m.Content, &simpleContent) == nil {
		return raw
	}

	// Content is an array of blocks. Keep every block except thinking.
	var parts []json.RawMessage
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return raw
	}

	filtered := make([]json.RawMessage, 0, len(parts))
	changed := false
	for _, p := range parts {
		var block struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(p, &block); err != nil {
			filtered = append(filtered, p)
			continue
		}

		switch block.Type {
		case "thinking":
			changed = true
		default:
			filtered = append(filtered, p)
		}
	}

	if !changed {
		return raw
	}

	if len(filtered) == 0 {
		return nil
	}

	// Build new message with filtered content
	type cleanedMsg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	newParts, _ := json.Marshal(filtered)
	cleaned, _ := json.Marshal(cleanedMsg{
		Role:    m.Role,
		Content: newParts,
	})
	return cleaned
}
