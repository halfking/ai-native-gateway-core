// Package sanitize - system_prompt.go
//
// SANITIZE_SYSTEM_PROMPT_ENABLED 功能门控。
//
// 历史注记：本文件曾包含 System Prompt 占位符保护指令
// （PlaceholderProtectionPrompt 常量 + InjectPlaceholderProtection 注入函数，
// Phase 2 Task 2.3），该特性从未接线进脱敏主链路，唯一调用方
// SanitizeInputMiddleware.injectPlaceholderProtection 同为死链。
// 两者已于 R36 冗余清理批（2026-09-17）删除；若需恢复该特性，
// 从 git 历史取回，并重新实现注入点。
package sanitize

import (
	"os"
)

// IsSanitizeSystemPromptEnabled 检查是否启用 System Prompt 占位符保护
//
// 通过环境变量 SANITIZE_SYSTEM_PROMPT_ENABLED 控制（默认启用）。
// 当前生产代码暂无调用方（特性注入点未接线），保留门控读取器供接线时复用。
func IsSanitizeSystemPromptEnabled() bool {
	env := os.Getenv("SANITIZE_SYSTEM_PROMPT_ENABLED")
	if env == "" {
		return true // 默认启用
	}
	return env == "true" || env == "1" || env == "yes"
}
