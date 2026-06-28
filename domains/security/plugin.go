// Package security 是 V4 治理平台的安全插件域（PR-V4-03 引入）。
//
// 与 domains/hooks/security（v3 内嵌实现）并行存在：
//   - v3 单体 SecurityHook 仍挂载在 PreRouting 阶段，作为粗粒度预筛。
//   - v4 本包提供可插拔的 Plugin + Registry，挂载在 PhaseGovernance 阶段。
//
// 两套机制并存直到 PR-V4-04 之后再统一收口；本期不修改任何 v3 代码。
package security

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

// Direction 插件运行方向。
const (
	DirectionInput  = "input"  // 请求进入上游前
	DirectionOutput = "output" // 响应回客户端前
	DirectionBoth   = "both"
)

// Scope 插件启停作用域。
//
// 任意字段为空切片表示"全匹配"（不是"全拒绝"）。
//   - TenantIDs: 空 = 所有租户
//   - ModelIDs:  空 = 所有模型
//   - Phases:    空 = 所有 phase（实际由 Pipeline 调度保证）
type Scope struct {
	TenantIDs []string
	ModelIDs  []string
	Phases    []string
}

// Plugin 安全检查插件接口。
//
// 与 pipeline.Hook 不同：
//   - 不负责阶段/Pipeline 调度
//   - 只产出 1 个 *governance.Verdict，由 Registry 收集后写入 PipelineRequest.Governance
//
// Verdict 字段语义见 domain/governance/verdict.go。
type Plugin interface {
	Name() string
	Direction() string
	Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error)
}

// _ 抑制 unused import 误报（governance 包由 registry/hook 间接使用）。
var _ governance.DecisionKind
