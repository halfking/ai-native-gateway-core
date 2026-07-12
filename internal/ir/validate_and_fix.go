package ir

import (
	"log/slog"
	"strings"
)

// ValidateAndFixRequest performs comprehensive validation and automatic fixes
// on an InternalRequest before serialization to upstream providers.
//
// Fixes applied:
//  1. Tool message sanitization (removes orphaned tool messages)
//  2. Message sequence validation (enforces role alternation rules)
//  3. Empty content cleanup (removes messages with no substantive content)
//  4. Tool call consistency (ensures tool_calls have valid IDs and names)
//  5. System message placement (moves misplaced system messages to the front)
//
// 2026-07-12: Created to prevent upstream rejections from malformed requests.
func ValidateAndFixRequest(req *InternalRequest) *InternalRequest {
	if req == nil {
		return req
	}

	original := len(req.Messages)
	req.Messages = validateAndFixMessages(req.Messages)
	fixed := original - len(req.Messages)

	if fixed > 0 {
		slog.Info("validate_and_fix_request: applied automatic fixes",
			"original_messages", original,
			"fixed_messages", len(req.Messages),
			"removed_count", fixed,
		)
	}

	return req
}

// validateAndFixMessages applies all validation and fix rules to message array.
func validateAndFixMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	// Step 1: Remove orphaned tool messages (existing SanitizeToolMessages)
	messages = SanitizeToolMessages(messages)

	// Step 2: Fix empty content
	messages = removeEmptyMessages(messages)

	// Step 3: Fix tool call structure
	messages = fixToolCallStructure(messages)

	// Step 4: Enforce role alternation (if needed)
	messages = enforceRoleAlternation(messages)

	// Step 5: Move system messages to front
	messages = normalizeSystemMessages(messages)

	return messages
}

// removeEmptyMessages removes messages with no substantive content.
// Empty content is defined as:
//   - No Content blocks
//   - All Content blocks are empty text
//   - tool role without tool_call_id
//   - assistant role without content or tool_calls
func removeEmptyMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	removedCount := 0

	for i, msg := range messages {
		isEmpty := false

		switch msg.Role {
		case "tool":
			// Tool message must have tool_call_id
			if msg.ToolCallID == "" {
				isEmpty = true
			}

		case "assistant":
			// Assistant must have either content or tool_calls
			if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
				isEmpty = true
			} else if len(msg.ToolCalls) == 0 {
				// Check if all content blocks are empty
				allEmpty := true
				for _, block := range msg.Content {
					if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
						allEmpty = false
						break
					}
					if block.Type != "text" {
						allEmpty = false
						break
					}
				}
				isEmpty = allEmpty
			}

		case "user":
			// User message must have content
			if len(msg.Content) == 0 {
				isEmpty = true
			} else {
				// Check if all content blocks are empty
				allEmpty := true
				for _, block := range msg.Content {
					if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
						allEmpty = false
						break
					}
					if block.Type != "text" {
						allEmpty = false
						break
					}
				}
				isEmpty = allEmpty
			}

		case "system":
			// System message must have content
			if len(msg.Content) == 0 {
				isEmpty = true
			} else {
				allEmpty := true
				for _, block := range msg.Content {
					if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
						allEmpty = false
						break
					}
					if block.Type != "text" {
						allEmpty = false
						break
					}
				}
				isEmpty = allEmpty
			}
		}

		if isEmpty {
			slog.Warn("validate_and_fix: removing empty message",
				"role", msg.Role,
				"index", i,
			)
			removedCount++
			continue
		}

		out = append(out, msg)
	}

	if removedCount > 0 {
		slog.Info("validate_and_fix: removed empty messages",
			"removed", removedCount,
		)
	}

	return out
}

// fixToolCallStructure ensures all tool_calls have valid IDs and function names.
func fixToolCallStructure(messages []Message) []Message {
	fixedCount := 0

	for i := range messages {
		if messages[i].Role != "assistant" || len(messages[i].ToolCalls) == 0 {
			continue
		}

		for j := range messages[i].ToolCalls {
			tc := &messages[i].ToolCalls[j]

			// Ensure ID is not empty
			if tc.ID == "" {
				tc.ID = generateToolCallID(j)
				slog.Warn("validate_and_fix: generated missing tool_call_id",
					"message_index", i,
					"tool_call_index", j,
					"generated_id", tc.ID,
				)
				fixedCount++
			}

			// Ensure function name is not empty
			if tc.Function.Name == "" {
				tc.Function.Name = "__unknown_tool__"
				slog.Warn("validate_and_fix: fixed empty tool function name",
					"message_index", i,
					"tool_call_index", j,
					"tool_call_id", tc.ID,
				)
				fixedCount++
			}

			// Ensure type is set
			if tc.Type == "" {
				tc.Type = "function"
				fixedCount++
			}
		}
	}

	if fixedCount > 0 {
		slog.Info("validate_and_fix: fixed tool_call structure",
			"fixes_applied", fixedCount,
		)
	}

	return messages
}

// enforceRoleAlternation ensures proper role alternation (user/assistant).
// This is a soft enforcement - we only warn, don't remove messages.
func enforceRoleAlternation(messages []Message) []Message {
	if len(messages) < 2 {
		return messages
	}

	violations := 0
	lastRole := ""

	for i, msg := range messages {
		if msg.Role == "system" || msg.Role == "tool" {
			continue // System and tool messages don't affect alternation
		}

		if lastRole != "" && lastRole == msg.Role && msg.Role != "tool" {
			slog.Warn("validate_and_fix: role alternation violation detected",
				"index", i,
				"role", msg.Role,
				"previous_role", lastRole,
			)
			violations++
		}

		lastRole = msg.Role
	}

	if violations > 0 {
		slog.Info("validate_and_fix: role alternation violations detected (not auto-fixed)",
			"violations", violations,
		)
	}

	return messages
}

// normalizeSystemMessages moves all system messages to the beginning.
// Most providers require system messages to be at the start.
func normalizeSystemMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	systemMsgs := []Message{}
	otherMsgs := []Message{}

	for _, msg := range messages {
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			otherMsgs = append(otherMsgs, msg)
		}
	}

	// If no system messages or they're already at the front, no change needed
	if len(systemMsgs) == 0 {
		return messages
	}

	// Check if first N messages are all system messages
	alreadyNormalized := true
	if len(systemMsgs) > 0 {
		for i := 0; i < len(systemMsgs) && i < len(messages); i++ {
			if messages[i].Role != "system" {
				alreadyNormalized = false
				break
			}
		}
	}

	if alreadyNormalized {
		return messages
	}

	slog.Info("validate_and_fix: normalized system message placement",
		"system_messages", len(systemMsgs),
		"moved_to_front", true,
	)

	return append(systemMsgs, otherMsgs...)
}

// generateToolCallID generates a deterministic tool_call_id.
func generateToolCallID(index int) string {
	return string([]byte{'c', 'a', 'l', 'l', '_', 'g', 'e', 'n', 'e', 'r', 'a', 't', 'e', 'd', '_', byte('0' + (index % 10))})
}
