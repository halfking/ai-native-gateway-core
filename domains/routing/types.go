// Package routing 实现请求路由领域。
//
// 阶段划分（与 pipeline.Phase 对应）：
//   - PhasePreRouting    准备候选凭据（Pre-Routing Hooks 填充 ctx.Candidates）
//   - PhaseRouting       路由决策（粘性 / 轮询 / 评分等 Strategy 模式）
//   - PhasePostRouting   后置处理（记录选择、通知上游等）
//
// 本包是 routing/（生产代码）的领域抽象子集：
//   - 不依赖旧 routing 包（保证可独立编译 + 可与旧实现共存）
//   - 不复刻 executor_*.go 等 Provider 特定逻辑
//   - 只暴露"路由决策"这个领域核心契约（Router/Decision/Candidate）
package routing

import "time"

// Candidate 候选凭据（router 评估后填充）。
//
// 这是 routing 领域的最小数据契约：上游 Candidate（provider.Candidate）
// 由 PhasePreRouting 的 Hook 翻译为该结构，避免领域抽象依赖具体 Provider。
type Candidate struct {
	// CredentialID 凭据唯一标识（来自底层 credential 存储）
	CredentialID string
	// Provider Provider 名称（"openai" / "anthropic" / "zhipu" / ...）
	Provider string
	// Model 模型标识
	Model string
	// Score 评分（由 Pre-Routing Hook 计算；越大越优，可为 0 表示未评分）
	Score float64
	// Reason 评分理由（调试用）
	Reason string
}

// Decision 路由决策。
//
// 一次路由调用应产生一个 Decision；Selected 为 nil 表示路由未决
// （如没有可用 candidate），Pipeline 应继续后续阶段。
type Decision struct {
	// Selected 选中的凭据（nil = 路由未决）
	Selected *Candidate
	// Alternatives 备选凭据（按优先级排序）
	Alternatives []*Candidate
	// Strategy 命中的策略名（"sticky" / "round_robin" / "score" / ...）
	Strategy string
	// LatencyMs 决策耗时
	LatencyMs int64
	// DecidedAt 决策时间
	DecidedAt time.Time
}

// Router 路由接口（Strategy 模式）。
//
// 一个 Router 只做一件事：从候选中选一个（或不选）。
// 多种 Router 可串联（StickyRouter -> RoundRobinRouter -> ScoreRouter），
// 上一级未命中时回退到下一级。
type Router interface {
	// Route 从 ctx.Candidates 中挑选凭据；返回 nil 表示未决（继续后续 Router）。
	Route(ctx Context) (*Decision, error)
}

// Context 路由上下文。
//
// 注意：Context 是值类型而非 context.Context，避免与 Go 标准库 Context 混淆。
// Pipeline 内的 Go context 走 Phase.Execute 的第一个参数。
type Context struct {
	// TenantID 租户 ID（用于多租户粘性）
	TenantID string
	// Model 目标模型（用户请求的模型名）
	Model string
	// Metadata 跨阶段元数据（粘性偏好等）
	Metadata map[string]any
	// Candidates 候选凭据（由 Pre-Routing Hook 准备）
	Candidates []*Candidate
}
