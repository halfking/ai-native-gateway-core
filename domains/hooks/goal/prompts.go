// Package goal provides internationalized prompts for goal mode.
package goal

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/i18n"
)

// Locale is an alias for i18n.Locale so existing call sites keep compiling
// while locale resolution is unified through the i18n package.
type Locale = i18n.Locale

// Locale constants mirror the historical names; they map onto the canonical
// i18n.Locale values.
const (
	LocaleZhCN = i18n.ZhCN // Simplified Chinese
	LocaleEnUS = i18n.En   // English (US)
)

// Prompts contains all internationalized prompt templates.
type Prompts struct {
	locale i18n.Locale
}

// NewPrompts creates a prompts instance for the given locale.
func NewPrompts(locale Locale) *Prompts {
	if locale == "" {
		locale = LocaleZhCN // Default to Chinese
	}
	return &Prompts{locale: locale}
}

// NewPromptsFromContext resolves the locale from the request context (set by
// the locale middleware) and builds a Prompts instance. This is the preferred
// constructor for hooks that receive the request's context.Context.
func NewPromptsFromContext(ctx context.Context) *Prompts {
	return &Prompts{locale: i18n.LocaleFromContext(ctx)}
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

// DetectLocaleFromRequest is retained for backward compatibility but now
// delegates to i18n's matcher-based detection. New code should use
// NewPromptsFromContext (which reads the locale the middleware stored on the
// request context) rather than calling this directly.
func DetectLocaleFromRequest(acceptLanguage string) Locale {
	if acceptLanguage == "" {
		return LocaleZhCN
	}
	// Construct a throwaway request so i18n.Detect can parse the header. This
	// preserves the original signature while reusing the shared matcher.
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", acceptLanguage)
	loc := i18n.Detect(r, string(LocaleZhCN))
	if loc == "" {
		return LocaleZhCN
	}
	return loc
}
