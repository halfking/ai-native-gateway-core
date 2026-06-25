package routing

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// RoutingHook 路由决策 Hook。
//
// 行为：
//   - Enabled: env != nil && env.SelectedCredential == nil
//     （如果其他 Hook 已选中凭据，跳过）
//   - Execute: 构造 routing.Context，调用 router.Route，
//     将 Decision.Selected 翻译为 env.SelectedCredential。
//   - OnError: 路由失败必须上报（OnError 透传 error）。
//
// Candidates 来源：从 env.Metadata["candidates"] 读取（由 PhasePreRouting 的
// 候选准备 Hook 写入）。若不存在则视为空候选——不报错，由 router 决定如何
// 处理空候选。
type RoutingHook struct {
	router Router
}

// NewRoutingHook 构造一个路由 Hook。
func NewRoutingHook(router Router) *RoutingHook {
	if router == nil {
		panic("routing.NewRoutingHook: router must not be nil")
	}
	return &RoutingHook{router: router}
}

// Name 返回 Hook 名称。
func (h *RoutingHook) Name() string { return "routing.decide" }

// Priority 返回 Hook 优先级（在 Phase 内排序；100 为中等）。
func (h *RoutingHook) Priority() int { return 100 }

// Enabled 报告 Hook 是否启用。
func (h *RoutingHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.SelectedCredential == nil
}

// Execute 执行路由决策。
func (h *RoutingHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 提取候选（来自 Pre-Routing 阶段写入 metadata）
	var candidates []*Candidate
	if env.Metadata != nil {
		if raw, ok := env.Metadata["candidates"]; ok {
			if cands, ok := raw.([]*Candidate); ok {
				candidates = cands
			}
		}
	}
	if candidates == nil {
		candidates = []*Candidate{}
	}

	// 构造 routing.Context
	rc := Context{
		TenantID:   env.TenantID,
		Metadata:   env.Metadata,
		Candidates: candidates,
	}

	start := time.Now()
	decision, err := h.router.Route(rc)
	if err != nil {
		return err
	}
	if decision == nil || decision.Selected == nil {
		// 路由未决：让后续阶段处理（如回退到默认凭据）
		return nil
	}

	// 翻译为 Pipeline 视图
	env.SelectedCredential = &domain.PipelineCredential{
		ID:       decision.Selected.CredentialID,
		Provider: decision.Selected.Provider,
	}
	if decision.LatencyMs == 0 {
		env.Metadata = ensureMetadata(env.Metadata)
		env.Metadata["routing_latency_ms"] = time.Since(start).Milliseconds()
		env.Metadata["routing_strategy"] = decision.Strategy
	}
	return nil
}

// OnError 路由失败时透传 error（路由失败必须可见）。
func (h *RoutingHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

// ensureMetadata 保证 env.Metadata 非 nil。
func ensureMetadata(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// 编译期接口断言。
var _ pipeline.Hook = (*RoutingHook)(nil)
