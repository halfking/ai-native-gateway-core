package governance

import "time"

// GovernanceState 同步治理层跨 hook 共享状态。
//
// 由 PhaseGovernance 阶段内的 hooks（安全插件、拦截引擎、工具编排）读写；
// 其他阶段的 hook 应视为只读。
//
// PR-V4-02 引入骨架；PR-V4-03 起由 security.plugins 与 interception.Engine 真正写入。
type GovernanceState struct {
	// Verdicts 由各安全插件累积；拦截引擎读取后产出 Decision。
	Verdicts []*Verdict

	// Decision 治理最终决策；拦截引擎写入，post_routing 之后读取。
	Decision *Decision

	// DecisionTrace 决策追踪步骤，用于可观测与回放。
	DecisionTrace []DecisionStep
}

// ToolState 客户端工具编排共享状态。
//
// 由 PhaseGovernance 阶段的 ToolOrchestrator 写入，post_upstream 阶段的
// streaming/audit hooks 读取。
type ToolState struct {
	// PendingPlan 当前等待客户端返回结果的执行计划；nil 表示无待执行 tool。
	PendingPlan *ToolExecutionPlan

	// LastResults 客户端工具返回结果，按执行顺序追加。
	LastResults []*ToolResult

	// Attempts 当前 plan 的往返次数；超过 1 表示曾等待/重试。
	Attempts int
}

// ToolResult 客户端单个工具的执行结果。
type ToolResult struct {
	Name    string
	Success bool
	Output  any
	Error   string
	At      time.Time
}

// PromptState 提示词体当前形态（V4 引入骨架；PR-V4-03 后由 mutate 决策真正写入）。
//
// 用法：
//
//	Original = 客户端原始请求体
//	Mutated  = 拦截引擎改写后的请求体（可能 == Original）
//	MutatedBy = 触发改写的插件名
type PromptState struct {
	Original  []byte
	Mutated   []byte
	MutatedBy string
}

// RecordVerdict 追加 verdict；nil-safe。
func (g *GovernanceState) RecordVerdict(v *Verdict) {
	if g == nil || v == nil {
		return
	}
	g.Verdicts = append(g.Verdicts, v)
}

// RecordDecision 写入决策并追加 trace 步骤；nil-safe。
func (g *GovernanceState) RecordDecision(d *Decision) {
	if g == nil || d == nil {
		return
	}
	g.Decision = d
	g.DecisionTrace = append(g.DecisionTrace, DecisionStep{
		Stage:    "governance",
		Decision: d.Kind,
		At:       time.Now(),
	})
}

// HasBlock 报告任意 verdict 是否拒绝请求；用于拦截引擎快速短路。
func (g *GovernanceState) HasBlock() bool {
	if g == nil {
		return false
	}
	for _, v := range g.Verdicts {
		if v != nil && !v.Allow {
			return true
		}
	}
	return false
}

// HighestSeverity 返回当前所有 verdict 中最高 severity；无 verdict 时返回 -1。
func (g *GovernanceState) HighestSeverity() int {
	if g == nil || len(g.Verdicts) == 0 {
		return -1
	}
	max := -1
	for _, v := range g.Verdicts {
		if v == nil {
			continue
		}
		if v.Severity > max {
			max = v.Severity
		}
	}
	return max
}
