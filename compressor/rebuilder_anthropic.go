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

package compressor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicSystemSummaryPrefix marks the gateway-injected summary inside
// the top-level system field. Mirrors CompressionSummaryPrefix style but
// adapted for Anthropic (no markdown fences, simple ASCII separator).
const AnthropicSystemSummaryPrefix = "\\n\\n--- Compressed context (gateway injection; LLM summary of prior turns) ---\\n"

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

	// Build the rebuilt messages array: B-track first user + recent tail.
	// We pass a synthetic Retained with no system messages (since A-track
	// is at the top level, not in messages[]) and splitSystemAndTail
	// handles that case by returning the first user as head and the tail
	// as the rest.
	synthRet := &Retained{FirstUser: ret.FirstUser, FirstUserIndex: ret.FirstUserIndex}
	_, tailMsgs := splitSystemAndTail(probe.Messages, synthRet, keepRecentPairs*2)
	if len(tailMsgs) == 0 {
		// No B-track first user found at expected index. Fall back: keep
		// original messages, just inject summary into system field. This
		// preserves the conversation even if the indexing went wrong.
		tailMsgs = lastN(probe.Messages, keepRecentPairs*2)
	}

	// Rebuild messages array: B-track first user (verbatim) + tail.
	merged := make([]json.RawMessage, 0, 1+len(tailMsgs))
	if ret.FirstUser != nil {
		merged = append(merged, *ret.FirstUser)
	}
	merged = append(merged, tailMsgs...)
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

// TrimAnthropicTail removes tool_result-only user messages from the tail
// to avoid Anthropic tool_use_id orphan errors after a sliding-window trim.
// Called as a post-pass after rebuildAnthropicAfterSummary (or by the
// upstream-side compaction caller when dropping a tool round mid-conversation).
//
// Returns (cleaned, droppedCount). cleaned is in original order, preserving
// non-tool-result user turns and all assistant turns.
func TrimAnthropicTail(messages []json.RawMessage) (cleaned []json.RawMessage, droppedCount int) {
	for _, m := range messages {
		if messageRole(m) == "user" && isToolResultOnly(m) {
			droppedCount++
			continue
		}
		cleaned = append(cleaned, m)
	}
	return cleaned, droppedCount
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
