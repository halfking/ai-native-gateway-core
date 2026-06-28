package security

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// SecurityHook 把 Registry 接入 Pipeline。
//
// 行为：
//   - Enabled: env != nil
//   - Execute: 调 Registry.RunAll 并把所有 verdict 写入 env.EnsureGovernance()
//   - OnError: 吞掉 error（verdict 已写入，interception 引擎接管决策；
//     若某 plugin 直接 return error，Registry.RunAll 已把错误转 verdict）
//
// 适用阶段：PhaseGovernance。
type SecurityHook struct {
	registry *Registry
	scope    Scope
}

// NewSecurityHook 构造 hook。
func NewSecurityHook(registry *Registry, scope Scope) *SecurityHook {
	if registry == nil {
		registry = NewRegistry()
	}
	return &SecurityHook{registry: registry, scope: scope}
}

// Name 返回 hook 名（用于日志/调试）。
func (h *SecurityHook) Name() string { return "security.plugins" }

// Priority 优先级（与 v3 同名 hook 一致；同 phase 内排序使用）。
func (h *SecurityHook) Priority() int { return 100 }

// Enabled 报告 hook 是否启用。
func (h *SecurityHook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 调 Registry.RunAll 并写入 env.Governance。
func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	verdicts, err := h.registry.RunAll(ctx, env, h.scope)
	if err != nil {
		return err
	}
	state := env.EnsureGovernance()
	for _, v := range verdicts {
		state.RecordVerdict(v)
	}
	return nil
}

// OnError 吞掉错误（verdict 已写入；interception 引擎在后续阶段做决策）。
func (h *SecurityHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return nil
}

// Registry 返回内部 registry（用于测试与 telemetry）。
func (h *SecurityHook) Registry() *Registry { return h.registry }

// 编译期断言。
var _ pipeline.Hook = (*SecurityHook)(nil)
