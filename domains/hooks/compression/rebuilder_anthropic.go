// Package compressor - rebuilder_anthropic.go (Round 47 / v7 T7)
//
// C-track rebuild for Anthropic Messages API: takes an LLM-generated summary
// and splices it into the original body via the top-level "system" field,
// while preserving:
//   - B-track first user message (verbatim, in messages[])
//   - last keepRecentPairs × 2 turns (most recent user/assistant dialog,
//     minus tool_result-only user turns to avoid Anthropic tool_use_id
//     orphan errors)
//
// Why system-field injection for Anthropic?
//   Per v7 §4.3: Anthropic's messages[] roles are strictly user/assistant.
//   A user-role "dynamic_context" message would break the wire format
//   because Anthropic validates the role semantically. Worse: it could
//   collide with the agent's actual user turn or be misinterpreted as
//   tool_result input.
//
//   Anthropic's top-level "system" field is the canonical place for
//   context the model must always see. Prepending the LLM summary to
//   system (with a `--- Compressed context (gateway injection) ---`
//   separator) keeps the wire format valid and the intent clear.
//
// v7 §6 single-level chain rule: this rebuild never emits a new request_id
// (that's executor_anthropic.go's job via parent_request_id); it just
// rewrites the body bytes.
//
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §4.3 (Anthropic
// system-field injection rationale).

package compression

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicSystemSummaryPrefix marks the gateway-injected summary inside
// the top-level system field. Mirrors CompressionSummaryPrefix style but
// adapted for Anthropic (no markdown fences, simple ASCII separator).
const AnthropicSystemSummaryPrefix = "\n\n--- Compressed context (gateway injection; LLM summary of prior turns) ---\n"

// rebuildAnthropicSystemField combines the original system prompt with the
// LLM-generated summary. Returns a single json.RawMessage suitable for the
// top-level "system" field. Handles both string and block-array shapes:
//   - string: returns "<orig>\n<prefix><summary>"
//   - blocks: appends a {"type":"text", "text": <prefix+summary>} block
//
// Empty original system + non-empty summary → returns just prefix+summary
// (still wrapped in a text block so Anthropic receives a consistent shape).
func rebuildAnthropicSystemField(origSystem json.RawMessage, summary string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return origSystem, nil
	}

	if len(origSystem) == 0 || string(origSystem) == "null" {
		// No original system. Wrap the summary in a single text block.
		return json.Marshal([]map[string]any{
			{"type": "text", "text": AnthropicSystemSummaryPrefix[2:] + trimmed}, // strip leading \n\n
		})
	}

	// Probe the shape. Trim leading whitespace from the raw probe so a
	// pure "  ...  " string isn't mis-detected as a block array.
	probe := strings.TrimSpace(string(origSystem))
	if strings.HasPrefix(probe, "[") {
		// Block-array shape. Append a new text block.
		var blocks []json.RawMessage
		if err := json.Unmarshal(origSystem, &blocks); err != nil {
			return nil, fmt.Errorf("rebuildAnthropicSystemField: parse blocks: %w", err)
		}
		newBlock, err := json.Marshal(map[string]any{
			"type": "text",
			"text": AnthropicSystemSummaryPrefix + trimmed,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, newBlock)
		return json.Marshal(blocks)
	}
	if strings.HasPrefix(probe, "{") {
		// Single block object. Wrap into an array of two.
		newBlock, err := json.Marshal(map[string]any{
			"type": "text",
			"text": AnthropicSystemSummaryPrefix + trimmed,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal([]json.RawMessage{origSystem, newBlock})
	}

	// String shape (most common). Concatenate with separator.
	// Unquote the original string literal to avoid double-escaping.
	var origStr string
	if err := json.Unmarshal(origSystem, &origStr); err != nil {
		return nil, fmt.Errorf("rebuildAnthropicSystemField: unmarshal string: %w", err)
	}
	return json.Marshal(origStr + AnthropicSystemSummaryPrefix + trimmed)
}

// RebuildAnthropicAfterSummary splices the LLM-generated summary into an
// Anthropic Messages body via the top-level "system" field.
//
// Layout produced:
//
//	{
//	  "model": ...,
//	  "system": "<orig_system>\n<prefix><summary>",  // C-track injected here
//	  "messages": [
//	    <first user message verbatim>,                 // B-track
//	    ...tail (last keepRecentPairs * 2 turns)...
//	  ],
//	  ...other top-level keys (max_tokens, tools, ...)
//	}
//
// Returns (newBody, true) on success, (origBody, false) when:
//   - body is unparseable as Anthropic Messages
//   - body has no messages
//   - ret (B-track extraction) is nil
//   - summary is empty
//
// keepRecentPairs defaults to 2 (matches LLM_GATEWAY_COMPRESSION_KEEP_RECENT_PAIRS).
func RebuildAnthropicAfterSummary(body []byte, summary string, ret *Retained, keepRecentPairs int) ([]byte, bool) {
	if ret == nil || ret.FirstUser == nil || len(summary) == 0 {
		return body, false
	}
	if keepRecentPairs <= 0 {
		keepRecentPairs = 2
	}

	// Probe wire format. We support Anthropic Messages shape:
	// {"model":"...", "system":"...", "messages":[...]}
	var probe struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, false
	}
	if len(probe.Messages) == 0 {
		return body, false
	}

	// Build the rebuilt system field (C-track injection).
	newSystem, err := rebuildAnthropicSystemField(probe.System, summary)
	if err != nil {
		return body, false
	}

	// Build the rebuilt messages array: pre-intent reminders + B-track first
	// user + recent tail. We pass a synthetic Retained with no system
	// messages (since A-track is at the top level, not in messages[]).
	synthRet := &Retained{FirstUser: ret.FirstUser, FirstUserIndex: ret.FirstUserIndex}
	headMsgs, tailMsgs := splitSystemAndTail(probe.Messages, synthRet, keepRecentPairs*2)
	if len(headMsgs) == 0 {
		// No B-track first user found at expected index. Fall back to an
		// eligible recent tail: old gateway markers are superseded by the
		// fresh top-level C-track summary, and system messages do not belong
		// in Anthropic messages[]. Keep tool rounds atomic for the same
		// reason as the normal OpenAI rebuild path.
		headMsgs = nil
		tailMsgs = recentAtomicTail(filterRebuildTail(probe.Messages), keepRecentPairs*2)
	}

	// Rebuild messages array: preserve reminders and first user verbatim, then
	// append the recent tail.
	merged := make([]json.RawMessage, 0, len(headMsgs)+len(tailMsgs))
	merged = append(merged, headMsgs...)
	merged = append(merged, tailMsgs...)
	merged, _ = TrimAnthropicTail(merged)
	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return body, false
	}

	// Splice into body: replace both "system" and "messages" while keeping
	// every other top-level key (model, max_tokens, tools, metadata, ...).
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return body, false
	}
	generic["system"] = newSystem
	generic["messages"] = newMsgs
	out, err := json.Marshal(generic)
	if err != nil {
		return body, false
	}
	return out, true
}

// TrimAnthropicTail removes messages that would leave a dangling tool_use
// reference after a sliding-window rebuild. Called as a post-pass after
// rebuildAnthropicAfterSummary (or by the upstream-side compaction caller
// when dropping a tool round mid-conversation).
//
// 2026-10-01 R74 P0 CORRECTION. The previous implementation dropped
// tool_result-only USER messages, on the stated theory that they "orphan" a
// tool_use. That is backwards, and it turned a recoverable trim into a
// guaranteed upstream rejection:
//
//	old: drop the tool_result   → assistant.tool_use survives with no result
//	                            → Anthropic 400s the whole request
//
// Anthropic requires every assistant tool_use to be answered by a
// tool_result, and rejects a tool_result whose tool_use is gone. A sliding
// rebuild can orphan EITHER half depending on where the cut lands:
// keepRecentPairs=1 leaves the result behind, keepRecentPairs>=2 leaves the
// use behind. So both directions are reconciled here — a tool_result with no
// surviving use is dropped, and an assistant anchor whose every declared
// tool_use is unanswered is dropped.
//
// This is symmetric with the OpenAI rebuild path, which keeps tool rounds
// atomic by walking the left boundary BACK to the assistant anchor
// (recentAtomicTail) rather than by cutting results.
//
// Returns (cleaned, droppedCount). cleaned is in original order.
func TrimAnthropicTail(messages []json.RawMessage) (cleaned []json.RawMessage, droppedCount int) {
	declared := make(map[string]bool)
	answered := make(map[string]bool)
	for _, m := range messages {
		switch messageRole(m) {
		case "assistant":
			for _, id := range anthropicToolUseIDs(m) {
				declared[id] = true
			}
		case "user":
			for _, id := range anthropicToolResultIDs(m) {
				answered[id] = true
			}
		}
	}
	for _, m := range messages {
		switch messageRole(m) {
		case "user":
			ids := anthropicToolResultIDs(m)
			if len(ids) == 0 {
				// Plain text user turn — never a tool artifact.
				break
			}
			// Mixed text+tool_result messages are left alone: the user turn
			// carries content the model still needs.
			if !isToolResultOnly(m) {
				break
			}
			// Drop a tool_result-only turn whose tool_use did not survive
			// the rebuild.
			keep := false
			for _, id := range ids {
				if declared[id] {
					keep = true
					break
				}
			}
			if !keep {
				droppedCount++
				continue
			}
		case "assistant":
			// Drop the anchor when EVERY tool_use it declares is
			// unanswered. A partially answered anchor is also malformed, but
			// cutting it would additionally discard the results that do
			// exist, so keep it and let the upstream speak.
			if ids := anthropicToolUseIDs(m); len(ids) > 0 {
				allUnanswered := true
				for _, id := range ids {
					if answered[id] {
						allUnanswered = false
						break
					}
				}
				if allUnanswered {
					droppedCount++
					continue
				}
			}
		}
		cleaned = append(cleaned, m)
	}
	return cleaned, droppedCount
}

// anthropicToolUseIDs returns the tool_use block IDs declared by an
// assistant message (empty for non-assistant or string-content messages).
func anthropicToolUseIDs(raw json.RawMessage) []string {
	return anthropicBlockIDs(raw, "assistant", "tool_use", "")
}

// anthropicToolResultIDs returns the tool_use_id values a user message
// answers via tool_result blocks.
func anthropicToolResultIDs(raw json.RawMessage) []string {
	return anthropicBlockIDs(raw, "user", "tool_result", "tool_use_id")
}

func anthropicBlockIDs(raw json.RawMessage, role, blockType, idField string) []string {
	if messageRole(raw) != role {
		return nil
	}
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Content) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(probe.Content))
	if !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		TUID string `json:"tool_use_id"`
	}
	if err := json.Unmarshal(probe.Content, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type != blockType {
			continue
		}
		id := b.ID
		if idField != "" {
			id = b.TUID
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// isToolResultOnly reports whether a user-role message's content is purely
// a tool_result block (no plain text content). Anthropic rejects such
// messages when their preceding tool_use_id is no longer in the conversation.
func isToolResultOnly(raw json.RawMessage) bool {
	if messageRole(raw) != "user" {
		return false
	}
	// Probe content shape. Anthropic user content can be:
	//   - string (always safe - user said something)
	//   - array of blocks (could be text + tool_result mixed)
	//   - array of single tool_result (orphan-prone)
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Content) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(string(probe.Content))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	if !strings.HasPrefix(trimmed, "[") {
		// String content. Definitely not tool_result-only.
		return false
	}
	// Array shape. Check whether ALL blocks are tool_result.
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(probe.Content, &blocks); err != nil {
		return false
	}
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}
