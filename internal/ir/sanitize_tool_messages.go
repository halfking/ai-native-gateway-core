package ir

import (
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/internal/toolhistory"
)

// SanitizeToolMessages repairs interrupted local call batches before removing
// genuinely orphaned tool messages. A user notice between matching answers is
// kept after those answers rather than closing their batch prematurely.
// Consecutive assistant call records remain one parallel batch and are merged
// into the canonical Chat shape. Ambiguous duplicate calls/results are preserved
// for the outbound wire validator's typed rejection; no actual call is deleted.
// requestID correlates structural fixes without logging result/argument payloads.
func SanitizeToolMessages(messages []Message, requestID string) []Message {
	if len(messages) == 0 {
		return messages
	}
	log := slog.With("request_id", requestID)
	repaired, err := reconcileToolResults(messages)
	if err != nil {
		// The outbound wire validator reports this typed ambiguity. Keep every
		// actual call and answer here; deleting one would conceal the error.
		log.Warn("sanitize_tool_messages: preserving ambiguous history", "error", err)
		return messages
	}
	messages = repaired

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

// reconcileToolResults collects a whole local batch before classifying a user
// notice as a boundary. Otherwise a resize notice can orphan a later real answer.
func reconcileToolResults(messages []Message) ([]Message, error) {
	messages = expandToolResultMessages(messages)
	out := make([]Message, 0, len(messages))
	for i := 0; i < len(messages); {
		if messages[i].Role != "assistant" || len(messages[i].ToolCalls) == 0 {
			out = append(out, messages[i])
			i++
			continue
		}
		endCalls := i + 1
		for endCalls < len(messages) && messages[endCalls].Role == "assistant" && len(messages[endCalls].ToolCalls) > 0 {
			endCalls++
		}
		end := endCalls
		for end < len(messages) && messages[end].Role != "assistant" {
			end++
		}
		var ids []string
		known := map[string]bool{}
		for _, m := range messages[i:endCalls] {
			for _, c := range m.ToolCalls {
				ids = append(ids, c.ID)
				known[c.ID] = true
			}
		}
		var answers, rest []Message
		var answerIDs []string
		restSeen, interleaved := false, false
		for _, m := range messages[endCalls:end] {
			if m.Role == "tool" && known[m.ToolCallID] {
				answers = append(answers, m)
				answerIDs = append(answerIDs, m.ToolCallID)
				if restSeen {
					interleaved = true
				}
			} else {
				rest = append(rest, m)
				restSeen = true
			}
		}
		plan, err := toolhistory.Pair(ids, answerIDs)
		if err != nil {
			return nil, err
		}
		missing := false
		for _, n := range plan {
			if n < 0 {
				missing = true
			}
		}
		if !missing && !interleaved {
			out = append(out, messages[i:end]...)
			i = end
			continue
		}
		out = append(out, messages[i:endCalls]...)
		for n, id := range ids {
			if plan[n] >= 0 {
				out = append(out, answers[plan[n]])
				continue
			}
			out = append(out, Message{Role: "tool", ToolCallID: id, Content: []ContentBlock{{Type: "tool_result", ToolResult: &ToolResult{ToolUseID: id, IsError: true, Content: []ContentBlock{{Type: "text", Text: toolhistory.Interrupted}}}}}})
		}
		out = append(out, rest...)
		i = end
	}
	return out, nil
}
