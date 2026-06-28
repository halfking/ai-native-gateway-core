// Package plugins 包含 V4 安全插件的具体实现（PR-V4-03 引入）。
//
// 当前状态：
//   - PromptInjectionChecker：真实现，包装 v3 IntentAnalyzer + ThreatDetector
//   - 其他 5 个：占位实现，PR-V4-04 起逐步接入
package plugins

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
	legacysec "github.com/kaixuan/llm-gateway-go/domains/hooks/security"
)

// PromptInjectionChecker 提示词注入检查插件。
//
// 实现：复用 v3 IntentAnalyzer + ThreatDetector；命中规则与原 SecurityHook 一致，
// 保证 v3 → v4 切换过程中相同输入产生相同 verdict。
//
// 触发规则（v3 既有）：
//   - Intent: code / chat / harmful（含 "ignore previous" 等越狱关键词）
//   - Threat: pii_leak (severity 8) / injection (severity 9) / jailbreak (severity 10)
//   - Threat.Severity >= ThreatThreshold → verdict.Allow=false, Severity=3
type PromptInjectionChecker struct {
	IntentThreshold float64
	ThreatThreshold int
}

// NewPromptInjectionChecker 构造插件。
func NewPromptInjectionChecker() *PromptInjectionChecker {
	return &PromptInjectionChecker{
		IntentThreshold: 0.5,
		ThreatThreshold: 7,
	}
}

// Name 返回插件名。
func (p *PromptInjectionChecker) Name() string { return "prompt_injection" }

// Direction input：只在请求进入上游前运行。
func (p *PromptInjectionChecker) Direction() string { return "input" }

// Inspect 执行检查。
func (p *PromptInjectionChecker) Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error) {
	content, _ := env.Metadata["user_content"].(string)
	if content == "" {
		return &governance.Verdict{
			PluginName: p.Name(),
			Allow:      true,
			Severity:   0,
			Code:       "no_content",
			Reason:     "no user_content in metadata",
		}, nil
	}

	intent := legacysec.NewIntentAnalyzer(p.IntentThreshold)
	threat := legacysec.NewThreatDetector(p.AllowThreshold())
	_ = intent // 当前 threat 判定已足够；intent 仅作为 evidence 透传
	threats := threat.Detect(content)

	v := &governance.Verdict{
		PluginName: p.Name(),
		Allow:      true,
		Severity:   0,
		Code:       "clean",
		Evidence: map[string]any{
			"threats": threats,
		},
	}
	if threat.IsCritical(threats) {
		v.Allow = false
		v.Severity = 3
		v.Code = "prompt_injection.critical"
		v.Reason = "critical threat detected"
		v.FixAction = "abort_request"
	}
	return v, nil
}

// AllowThreshold 返回 threat 阈值（暴露给测试用）。
func (p *PromptInjectionChecker) AllowThreshold() int {
	if p.ThreatThreshold <= 0 {
		return 7
	}
	return p.ThreatThreshold
}
