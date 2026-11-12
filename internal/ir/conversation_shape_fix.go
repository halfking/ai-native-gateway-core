package ir

// conversation_shape_fix.go —— 会话级形态修复器（2026-11-11 审计第二轮 · 批 3.3）。
//
// ── SRP 边界 ─────────────────────────────────────────────────────────────
// 「多条消息之间的关系」形态修复（role 交替）集中于此；单条消息内的
// 字段级修正在 client_shape_fix.go；管线编排（五步顺序）在
// validate_and_fix.go。三者由 ValidateAndFixRequest 串联，各自可独立
// 测试与演进 —— 客户端智能体的会话形态适配（ZCode/MiniMaxCode 的
// 非严格交替）是独立的变化点（架构档变化点 D）。
//
// 环境开关（LLM_GATEWAY_FIX_ROLE_ALTERNATION）：2026-11-11 审计第二轮
// 用户裁决默认翻转为合并（unset/true ⇒ merge，"false" ⇒ warn-only）；
// 211 实测 6h 725 次告警、单请求最高 235 处。

import (
	"log/slog"
	"os"
)

// enforceRoleAlternation ensures proper role alternation (user/assistant).
//
// Behavior is controlled by the LLM_GATEWAY_FIX_ROLE_ALTERNATION environment
// variable (docs/omni-ref3 E3):
//   - unset or "true" (default, 2026-11-11 审计第二轮 · 用户裁决)：merge
//     consecutive same-role messages（合并 + WARN 留痕）—— 211 实测 6h 内
//     725 次告警、单请求最高 235 处，客户端智能体（ZCode/MiniMaxCode）的
//     会话形态就是非严格交替；OpenAI 系上游多接受，严格交替上游需要修复
//   - "false": soft enforcement — warn only, do not modify（旧默认，可回退）
//
// When merging is enabled, consecutive messages with the same role (user or
// assistant) are combined by concatenating their Content arrays. This ensures
// compatibility with providers (e.g., some OpenAI models) that require strict
// user/assistant alternation.
func enforceRoleAlternation(messages []Message) []Message {
	if len(messages) < 2 {
		return messages
	}

	// 2026-11-11 审计第二轮：默认翻转 —— 合并默认开启，env=false 显式回退
	// 旧的 warn-only 行为（升级路径可平滑退回）。
	shouldFix := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION") != "false"

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
//
// 2026-09-15 (245 audit noise reduction): agent-framework payloads trigger
// this on nearly every request — 90k+ WARN lines per day on 245, one line per
// violating message pair. The per-request result is now aggregated into a
// single WARN (violations count + first offending index); the per-index
// detail stays available at Debug level. Message content is never modified.
func warnRoleAlternationViolations(messages []Message) []Message {
	violations := 0
	firstIndex := -1
	firstRole := ""
	lastRole := ""

	for i, msg := range messages {
		if msg.Role == "system" || msg.Role == "tool" {
			continue // System and tool messages don't affect alternation
		}

		if lastRole != "" && lastRole == msg.Role {
			if firstIndex < 0 {
				firstIndex = i
				firstRole = msg.Role
			}
			violations++
			slog.Debug("validate_and_fix: role alternation violation detail",
				"index", i,
				"role", msg.Role,
				"previous_role", lastRole,
			)
		}

		lastRole = msg.Role
	}

	if violations > 0 {
		slog.Warn("validate_and_fix: role alternation violations detected (not auto-fixed)",
			"violations", violations,
			"first_index", firstIndex,
			"first_role", firstRole,
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
