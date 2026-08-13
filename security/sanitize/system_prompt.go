// Package sanitize - system_prompt.go
//
// Phase 2 Task 2.3: System Prompt 占位符保护指令
//
// 向 LLM 的 System Prompt 注入占位符保护指令，防止 LLM 改写或泄露占位符。
package sanitize

import (
	"os"
	"strings"
)

// PlaceholderProtectionPrompt 是注入到 System Prompt 的占位符保护指令
//
// 作用：
//  1. 告知 LLM 占位符的存在和格式
//  2. 要求 LLM 保持占位符不变
//  3. 禁止 LLM 泄露占位符格式或推测真实值
const PlaceholderProtectionPrompt = `
IMPORTANT: The user's message may contain placeholders in the format {SENSITIVE:type:index} (e.g., {SENSITIVE:phone:0}, {SENSITIVE:email:1}).

You MUST:
- Preserve these placeholders EXACTLY as they appear in the user's message
- Do NOT modify, remove, or replace these placeholders with actual values
- Do NOT explain or reveal the format of these placeholders to the user
- Reference placeholders naturally in your response when needed

Example:
User: "My phone is {SENSITIVE:phone:0}"
✓ Correct: "Your phone number {SENSITIVE:phone:0} has been recorded."
✗ WRONG: "Your phone number 13800138000 has been recorded."
✗ WRONG: "I see a placeholder {SENSITIVE:phone:0}, which seems to be a phone number."
`

// IsSanitizeSystemPromptEnabled 检查是否启用 System Prompt 占位符保护
//
// 通过环境变量 SANITIZE_SYSTEM_PROMPT_ENABLED 控制（默认启用）
func IsSanitizeSystemPromptEnabled() bool {
	env := os.Getenv("SANITIZE_SYSTEM_PROMPT_ENABLED")
	if env == "" {
		return true // 默认启用
	}
	return env == "true" || env == "1" || env == "yes"
}

// InjectPlaceholderProtection 向现有 System Prompt 注入占位符保护指令
//
// 参数：
//   - existingPrompt: 现有的 System Prompt（可能为空）
//
// 返回：
//   - 注入后的 System Prompt
//
// 行为：
//   - 如果功能关闭（SANITIZE_SYSTEM_PROMPT_ENABLED=false），返回原 prompt
//   - 如果 existingPrompt 已包含占位符保护指令，不重复注入
//   - 否则，在 existingPrompt 末尾追加保护指令
func InjectPlaceholderProtection(existingPrompt string) string {
	if !IsSanitizeSystemPromptEnabled() {
		return existingPrompt
	}

	// 检查是否已包含保护指令（避免重复注入）
	if strings.Contains(existingPrompt, "{SENSITIVE:type:index}") {
		return existingPrompt
	}

	// 注入保护指令
	if existingPrompt == "" {
		return strings.TrimSpace(PlaceholderProtectionPrompt)
	}

	return existingPrompt + "\n\n" + strings.TrimSpace(PlaceholderProtectionPrompt)
}
