// Package goal provides internationalized prompts for goal mode.
package goal

import (
	"fmt"
	"strings"
)

// Locale represents a language/region setting.
type Locale string

const (
	LocaleZhCN Locale = "zh-CN" // Simplified Chinese
	LocaleEnUS Locale = "en-US" // English (US)
)

// Prompts contains all internationalized prompt templates.
type Prompts struct {
	locale Locale
}

// NewPrompts creates a prompts instance for the given locale.
func NewPrompts(locale Locale) *Prompts {
	if locale == "" {
		locale = LocaleZhCN // Default to Chinese
	}
	return &Prompts{locale: locale}
}

// ContinueNextStep returns the prompt for continuing to next step.
func (p *Prompts) ContinueNextStep() string {
	switch p.locale {
	case LocaleEnUS:
		return "Please continue with the next step"
	case LocaleZhCN:
		return "请继续下一步"
	default:
		return "请继续下一步"
	}
}

// HandoffPrompt returns the handoff trigger prompt.
func (p *Prompts) HandoffPrompt(reason string) string {
	switch p.locale {
	case LocaleEnUS:
		return "Context limit reached (" + reason + "). Please use /handoff to continue in a new session."
	case LocaleZhCN:
		return "上下文已达限制（" + reason + "），请使用 /handoff 继续到新会话。"
	default:
		return "上下文已达限制（" + reason + "），请使用 /handoff 继续到新会话。"
	}
}

// GoalDetected returns the prompt when goal mode is detected.
func (p *Prompts) GoalDetected() string {
	switch p.locale {
	case LocaleEnUS:
		return "Goal mode activated. I will automatically track progress and continue until completion."
	case LocaleZhCN:
		return "已启动目标模式。我会自动跟踪进度并持续执行直到完成。"
	default:
		return "已启动目标模式。我会自动跟踪进度并持续执行直到完成。"
	}
}

// GoalCompleted returns the prompt when goal is completed.
func (p *Prompts) GoalCompleted() string {
	switch p.locale {
	case LocaleEnUS:
		return "Goal completed successfully."
	case LocaleZhCN:
		return "目标已成功完成。"
	default:
		return "目标已成功完成。"
	}
}

// AuditStarted returns the prompt when audit begins.
func (p *Prompts) AuditStarted() string {
	switch p.locale {
	case LocaleEnUS:
		return "Starting code audit..."
	case LocaleZhCN:
		return "开始代码审计..."
	default:
		return "开始代码审计..."
	}
}

// AuditPassed returns the prompt when audit passes.
func (p *Prompts) AuditPassed() string {
	switch p.locale {
	case LocaleEnUS:
		return "Audit passed. No issues found."
	case LocaleZhCN:
		return "审计通过，未发现问题。"
	default:
		return "审计通过，未发现问题。"
	}
}

// AuditFailed returns the prompt when audit fails.
func (p *Prompts) AuditFailed(issueCount int) string {
	switch p.locale {
	case LocaleEnUS:
		return fmt.Sprintf("Audit found %d issue(s). Review and fix recommended.", issueCount)
	case LocaleZhCN:
		return fmt.Sprintf("审计发现 %d 个问题，建议审查和修复。", issueCount)
	default:
		return fmt.Sprintf("审计发现 %d 个问题，建议审查和修复。", issueCount)
	}
}

// DetectLocaleFromRequest attempts to detect locale from request context.
// Falls back to zh-CN if unable to determine.
func DetectLocaleFromRequest(acceptLanguage string) Locale {
	if acceptLanguage == "" {
		return LocaleZhCN
	}

	// Simple detection based on Accept-Language header
	lower := strings.ToLower(acceptLanguage)
	if strings.Contains(lower, "en") {
		return LocaleEnUS
	}
	if strings.Contains(lower, "zh") {
		return LocaleZhCN
	}

	return LocaleZhCN // Default
}
