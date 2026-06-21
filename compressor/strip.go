// Package compressor - strip.go (v4 T1)
//
// Tool/thinking info stripper for the v4 intelligent session compressor.
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

package compressor

import (
	"encoding/json"
)

// StripResult describes what was removed from a body.
type StripResult struct {
	ToolCallsRemoved  int `json:"tool_calls_removed"`
	ToolResultsRemoved int `json:"tool_results_removed"`
	ThinkingRemoved   int `json:"thinking_removed"`
	MessagesRemoved   int `json:"messages_removed"`
	BytesBefore       int `json:"bytes_before"`
	BytesAfter        int `json:"bytes_after"`
	DidStrip          bool `json:"did_strip"`
}

// StripToolInfo removes completed tool rounds and thinking blocks from
// a messages array. Returns the stripped body and a result summary.
//
// "Completed tool round" = tool_call (assistant) + tool_result (tool)
// + ack (user). Once all three exist, the round is considered complete
// and can be safely removed from the compressed context. The tool
// outputs are captured in the LLM summary as "Key References".
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

	// Phase 1: Identify completed tool round boundaries
	// A completed round = sequential:
	//   assistant[tool_calls] → tool[tool_result] → user[acknowledgement]
	toolRounds := detectToolRounds(msgs)

	// Phase 2: Filter messages, removing completed rounds
	// But keep the LAST completed round for context continuity
	filtered := filterMessages(msgs, toolRounds, result)
	if len(filtered) == len(msgs) && result.ThinkingRemoved == 0 {
		return body, result
	}

	// Rebuild body with filtered messages
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

// detectToolRounds scans messages for completed tool invocation rounds.
// Returns a set of message indices to remove.
func detectToolRounds(msgs []json.RawMessage) map[int]bool {
	remove := make(map[int]bool)
	i := 0
	for i < len(msgs) {
		// Look for assistant[tool_calls] → tool[tool_result] → user[ack]
		if hasToolCalls(msgs[i]) {
			// Count how many tool_calls in this assistant message
			toolIDs := extractToolCallIDs(msgs[i])
			if len(toolIDs) == 0 {
				i++
				continue
			}

			// Find matching tool_results
			resultsFound := 0
			j := i + 1
			for j < len(msgs) && resultsFound < len(toolIDs) {
				if isToolResult(msgs[j]) {
					// Check if this result matches one of our calls
					if matchesAnyToolCall(msgs[j], toolIDs) {
						resultsFound++
					}
				}
				j++
			}

			if resultsFound >= len(toolIDs) {
				// Round completed (all tool_calls have matching results)
				// Remove: assistant[tool_calls] + tool_results
				// Keep: user acknowledgement (if any)
				remove[i] = true // assistant with tool_calls
				for k := i + 1; k <= i+len(toolIDs) && k < len(msgs); k++ {
					if isToolResult(msgs[k]) {
						remove[k] = true
					}
				}
				i = j
				continue
			}
		}
		i++
	}

	return remove
}

// filterMessages applies the strip rules:
// 1. Remove completed tool rounds (but keep last one)
// 2. Remove thinking content blocks
// 3. Preserve all other messages
func filterMessages(msgs []json.RawMessage, remove map[int]bool, result *StripResult) []json.RawMessage {
	if len(remove) == 0 && !hasThinkingBlocks(msgs) {
		return msgs
	}

	// Keep track of the last completed round (for continuity)
	lastCompletedRoundEnd := 0
	for idx := range remove {
		if idx > lastCompletedRoundEnd {
			lastCompletedRoundEnd = idx
		}
	}

	// Remove last round from the removal set (keep it for continuity)
	delete(remove, lastCompletedRoundEnd)
	for k := range remove {
		if k >= lastCompletedRoundEnd-2 && k <= lastCompletedRoundEnd {
			// Keep this round too (it's near the last one)
		}
	}

	filtered := make([]json.RawMessage, 0, len(msgs))
	for i, msg := range msgs {
		if remove[i] {
			result.ToolCallsRemoved++
			result.MessagesRemoved++
			continue
		}

		// Strip thinking blocks from message content
		cleaned := stripThinkingBlocks(msg)
		if len(cleaned) > 0 {
			filtered = append(filtered, cleaned)
		}
	}

	return filtered
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
				if p.Type == "thinking" || p.Type == "image_url" || p.Type == "input_audio" {
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

	// Content is an array of blocks — filter out non-text blocks
	var parts []struct {
		Type json.RawMessage `json:"type"`
		Text string          `json:"text,omitempty"`
	}
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return raw
	}

	filtered := make([]json.RawMessage, 0, len(parts))
	changed := false
	for _, p := range parts {
		// Check type
		var typeStr string
		json.Unmarshal(p.Type, &typeStr)

		switch typeStr {
		case "text", "tool_use", "tool_result":
			filtered = append(filtered, p.Type)
		case "thinking", "image_url", "input_audio":
			changed = true
		default:
			filtered = append(filtered, p.Type)
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