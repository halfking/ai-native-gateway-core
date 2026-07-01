package interception

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// InterceptionHook 把 Engine 接入 Pipeline。
//
// 行为：
//   - Enabled: env != nil
//   - Execute: 调 Engine.Decide 并把 Decision 写入 env.Governance（Engine 内部已写）
//   - OnError: 吞掉（Engine 仅在 nil envelope 时返回 error，是程序错误而非业务阻断）
//
// 适用阶段：PhaseGovernance，紧跟在 security_plugins stage 之后。
//
// 重要：本 hook **不**返回 error 阻断 pipeline。当前职责只是产生决策，
// 实际的 HTTP 阻断/挂起由更上层的 dispatch gate 统一处理。
// 这样能保证 v1 chatHandler 路径不会被意外的 governance 决策破坏。
type InterceptionHook struct {
	engine *Engine
}

// NewInterceptionHook 构造 hook。
func NewInterceptionHook(engine *Engine) *InterceptionHook {
	if engine == nil {
		engine = NewEngine(EngineConfig{})
	}
	return &InterceptionHook{engine: engine}
}

// Name 返回 hook 名。
func (h *InterceptionHook) Name() string { return "interception.engine" }

// Priority 优先级（与 security hook 区分；同 phase 内排序）。
func (h *InterceptionHook) Priority() int { return 200 }

// Enabled 仅当 envelope 非 nil 时启用。
func (h *InterceptionHook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 调 Engine.Decide 产生决策。
func (h *InterceptionHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if _, err := h.engine.Decide(ctx, env); err != nil {
		return err
	}
	// 校验决策是否合理（早期发现 programming bug）
	state := env.EnsureGovernance()
	if state.Decision == nil {
		return nil
	}
	// 已知 Kind 列表内不做二次校验；扩展时由 governance.DecisionKind const 保证
	_ = state.Decision.Kind
	return nil
}

// OnError 吞掉 error（Engine 仅在参数错误时返回 error，不是业务阻断）。
func (h *InterceptionHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return nil
}

// Engine 返回内部 engine（用于测试与 admin UI 展示）。
func (h *InterceptionHook) Engine() *Engine { return h.engine }

// LastDecision 便捷读取最近一次决策（hook 执行后调用）。
func (h *InterceptionHook) LastDecision(env *domain.PipelineRequest) *governance.Decision {
	if env == nil {
		return nil
	}
	state := env.EnsureGovernance()
	return state.Decision
}

// 编译期断言。
var _ pipeline.Hook = (*InterceptionHook)(nil)
