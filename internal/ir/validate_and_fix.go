package ir

import (
	"log/slog"
	"os"
	"strconv"
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
//
// 2026-07-18: Added requestID parameter so each fix is correlated to the
// gateway request that produced it. Pass "" when not in a request context
// (e.g. unit tests). Without this, every "removed_count" log line is
// orphan — operators cannot tell which mini-max-m3 retry chain it
// belongs to.
func ValidateAndFixRequest(req *InternalRequest, requestID string) *InternalRequest {
	if req == nil {
		return req
	}
	log := slog.With("request_id", requestID)

	original := len(req.Messages)
	req.Messages = validateAndFixMessages(req.Messages, requestID)
	fixed := original - len(req.Messages)

	if fixed > 0 {
		log.Info("validate_and_fix_request: applied automatic fixes",
			"original_messages", original,
			"fixed_messages", len(req.Messages),
			"removed_count", fixed,
		)
	}

	return req
}

// validateAndFixMessages applies all validation and fix rules to message array.
func validateAndFixMessages(messages []Message, requestID string) []Message {
	if len(messages) == 0 {
		return messages
	}

	// Step 1: Remove empty messages FIRST.
	//
	// 2026-08-06 audit fix: this step previously ran AFTER SanitizeToolMessages.
	// When an empty user message sat between tool results (a pattern some SDKs
	// emit, e.g. Vercel AI SDK), SanitizeToolMessages saw the user message
	// first and closed the tool-call scope, wrongly removing subsequent tool
	// results that referenced a still-valid call id. By removing empty
	// messages first, the sanitize step sees the cleaned sequence and the
	// tool results survive.
	//
	// Safety: removeEmptyMessages only deletes messages with no content AND
	// no tool_calls — it never touches a message carrying tool_call_id or
	// tool_calls. An empty user message has no semantic role as a scope
	// delimiter; it's SDK noise that should be cleaned before analysis.
	messages = removeEmptyMessages(messages)

	// Step 2: Remove orphaned tool messages (existing SanitizeToolMessages)
	messages = SanitizeToolMessages(messages, requestID)

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
				tc.ID = generateToolCallID(i, j)
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
//
// Behavior is controlled by the LLM_GATEWAY_FIX_ROLE_ALTERNATION environment
// variable (docs/omni-ref3 E3):
//   - "false" or unset (default): soft enforcement — warn only, do not modify
//   - "true": merge consecutive same-role messages (excluding system/tool)
//
// When merging is enabled, consecutive messages with the same role (user or
// assistant) are combined by concatenating their Content arrays. This ensures
// compatibility with providers (e.g., some OpenAI models) that require strict
// user/assistant alternation.
func enforceRoleAlternation(messages []Message) []Message {
	if len(messages) < 2 {
		return messages
	}

	// Check if auto-fix is enabled (E3: default false, opt-in per provider)
	shouldFix := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION") == "true"

	if !shouldFix {
		// Legacy behavior: warn only, do not modify
		return warnRoleAlternationViolations(messages)
	}

	// E3: merge consecutive same-role messages
	return mergeConsecutiveSameRoleMessages(messages)
}

// warnRoleAlternationViolations detects role alternation violations and logs
// warnings without modifying the message array. This is the default behavior
// (E3: soft enforcement).
func warnRoleAlternationViolations(messages []Message) []Message {
	violations := 0
	lastRole := ""

	for i, msg := range messages {
		if msg.Role == "system" || msg.Role == "tool" {
			continue // System and tool messages don't affect alternation
		}

		if lastRole != "" && lastRole == msg.Role {
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

// mergeConsecutiveSameRoleMessages combines consecutive messages with the same
// role (user or assistant) by concatenating their Content arrays. System and
// tool messages are never merged (docs/omni-ref3 E3).
//
// Example:
//
//	[user: "A", user: "B", assistant: "C"]
//	→ [user: ["A", "B"], assistant: "C"]
//
// This ensures compliance with providers that enforce strict role alternation.
func mergeConsecutiveSameRoleMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	result := []Message{messages[0]}
	mergedCount := 0

	for i := 1; i < len(messages); i++ {
		lastMsg := &result[len(result)-1]
		currMsg := messages[i]

		// Never merge system/tool messages or messages carrying tool metadata.
		// Merging assistant tool calls would otherwise silently discard calls.
		if currMsg.Role == "system" || currMsg.Role == "tool" ||
			lastMsg.Role == "system" || lastMsg.Role == "tool" ||
			(currMsg.Role != "user" && currMsg.Role != "assistant") ||
			(lastMsg.Role != "user" && lastMsg.Role != "assistant") ||
			len(currMsg.ToolCalls) > 0 || len(lastMsg.ToolCalls) > 0 ||
			currMsg.ToolCallID != "" || lastMsg.ToolCallID != "" {
			result = append(result, currMsg)
			continue
		}

		// Merge only the explicitly supported alternating roles.
		if lastMsg.Role == currMsg.Role {
			// Concatenate Content arrays
			lastMsg.Content = append(lastMsg.Content, currMsg.Content...)
			mergedCount++
		} else {
			result = append(result, currMsg)
		}
	}

	if mergedCount > 0 {
		slog.Info("validate_and_fix: merged consecutive same-role messages",
			"count", mergedCount,
		)
	}

	return result
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

// generateToolCallID generates a deterministic, collision-resistant tool_call_id
// for assistant tool_calls that arrived without one.
//
// The previous implementation reduced the index modulo 10 and rendered a single
// digit, so it could only ever produce 10 distinct ids ("call_generated_0"..
// "call_generated_9"). With more than 10 tool_calls — or two assistant messages
// whose first tool_call was both missing an id — distinct calls collapsed onto
// the same id, which providers reject or silently mis-pair with tool results.
//
// The id is keyed on (messageIndex, toolCallIndex) so it is unique across the
// whole request while remaining stable for a given input (call sites rely on
// determinism for log correlation). It is not intended to match any real
// provider-assigned id; it only needs to be non-empty and internally unique.
func generateToolCallID(messageIndex, toolCallIndex int) string {
	return "call_generated_" + strconv.Itoa(messageIndex) + "_" + strconv.Itoa(toolCallIndex)
}
