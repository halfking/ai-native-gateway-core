package ir

import "log/slog"

// SanitizeToolMessages removes orphaned tool messages that would cause
// tool_call_id_mismatch errors (MiniMax 2013, OpenAI item_reference).
//
// Problem: When a conversation has tool→assistant→user sequences (skipping
// the required assistant.tool_calls), downstream providers reject the request
// because they can't match tool.tool_call_id back to any assistant.tool_calls[].id.
//
// Fix: Walk messages and remove any tool message whose tool_call_id doesn't
// match a preceding assistant.tool_calls[].id within a valid scope.
//
// Scope rules:
//  1. tool messages must reference the immediately preceding assistant's tool_calls
//  2. Once we see a new assistant or user message, all prior tool_calls are "closed"
//  3. A tool message referencing a closed or non-existent call_id is orphaned → remove
//
// 2026-10-13: consecutive assistant messages that EACH carry tool_calls are a
// parallel tool call split across messages (ZCode/agent clients emit one
// assistant message per parallel call). Rule 2 used to close the previous
// scope on each such message, so the results of every call but the last were
// removed as "orphaned" — the sanitizer itself then produced the MiniMax
// "tool call result does not follow tool call (2013)" 400 it exists to
// prevent (raw-log evidence: request d717f89a8c79123c7ca7cd69ff2cbf9d,
// pre_messages=11 → post_messages=9 with tool results f7/f8 dropped).
// Such messages are now MERGED into the preceding assistant message — the
// canonical OpenAI parallel-call form — and their ids stay in the active
// scope so all results survive.
//
// 2026-07-12: Created to fix MiniMax "tool id not found (2013)" and similar
// errors caused by malformed conversation histories from AI SDK retries or
// client-side tool-call state bugs.
//
// 2026-07-18: Added requestID parameter so each removal is correlated to the
// gateway request that produced it. Pass "" when not in a request context
// (e.g. unit tests). All slog lines now carry "request_id" so downstream
// `journalctl _SYSTEMD_UNIT=... | grep <req_id>` works.
func SanitizeToolMessages(messages []Message, requestID string) []Message {
	if len(messages) == 0 {
		return messages
	}
	log := slog.With("request_id", requestID)

	// Track the set of valid tool_call_ids from the most recent assistant message
	activeToolCallIds := make(map[string]bool)
	out := make([]Message, 0, len(messages))
	removedCount := 0
	mergedCallCount := 0

	for i, msg := range messages {
		role := msg.Role

		switch role {
		case "assistant":
			// Parallel-call split: an assistant message carrying tool_calls
			// directly after another assistant message carrying tool_calls
			// (in the emitted stream) is one parallel call round that the
			// client split across messages. Merge it instead of closing the
			// previous scope, or the previous calls' results get orphaned.
			if len(msg.ToolCalls) > 0 && len(out) > 0 &&
				out[len(out)-1].Role == "assistant" && len(out[len(out)-1].ToolCalls) > 0 {
				last := &out[len(out)-1]
				for _, tc := range msg.ToolCalls {
					if tc.ID != "" {
						activeToolCallIds[tc.ID] = true
						// Skip duplicate ids — a provider-visible assistant
						// message with repeated tool_call ids is invalid.
						dup := false
						for _, existing := range last.ToolCalls {
							if existing.ID == tc.ID {
								dup = true
								break
							}
						}
						if dup {
							continue
						}
					}
					last.ToolCalls = append(last.ToolCalls, tc)
					mergedCallCount++
				}
				if len(last.Content) == 0 && len(msg.Content) > 0 {
					last.Content = msg.Content
				} else {
					last.Content = append(last.Content, msg.Content...)
				}
				continue
			}

			// Start a new tool-call scope
			activeToolCallIds = make(map[string]bool)

			// Collect tool_call_ids if any
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					if tc.ID != "" {
						activeToolCallIds[tc.ID] = true
					}
				}
			}
			out = append(out, msg)

		case "tool":
			// Validate tool_call_id against active scope
			toolCallID := msg.ToolCallID
			if toolCallID == "" {
				// Tool message without tool_call_id → invalid, remove
				log.Warn("sanitize_tool_messages: removing tool message without tool_call_id",
					"index", i,
					"total", len(messages),
				)
				removedCount++
				continue
			}

			if !activeToolCallIds[toolCallID] {
				// Orphaned tool message → remove
				log.Warn("sanitize_tool_messages: removing orphaned tool message",
					"tool_call_id", toolCallID,
					"index", i,
					"total", len(messages),
					"active_ids", len(activeToolCallIds),
				)
				removedCount++
				continue
			}

			// Valid tool message
			out = append(out, msg)

		case "user":
			// Close tool-call scope (no more tool messages can reference prior assistant)
			activeToolCallIds = make(map[string]bool)
			out = append(out, msg)

		case "system":
			// System messages don't affect tool-call scope
			out = append(out, msg)

		default:
			// Unknown role → pass through
			out = append(out, msg)
		}
	}

	if removedCount > 0 {
		log.Info("sanitize_tool_messages: removed orphaned tool messages",
			"removed", removedCount,
			"original", len(messages),
			"sanitized", len(out),
		)
	}
	if mergedCallCount > 0 {
		log.Info("sanitize_tool_messages: merged consecutive assistant tool_call messages",
			"merged_calls", mergedCallCount,
			"original", len(messages),
			"sanitized", len(out),
		)
	}

	return out
}

// Message interface helpers (already exist in ir package, these are just stubs for reference)
// Real implementation uses the existing Message interface methods.
