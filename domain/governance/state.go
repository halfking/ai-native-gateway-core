// Package governance 定义 V4 治理平台同步治理层共享的纯类型契约。
//
// 本包只放决策对象、状态机、工具执行计划等；与 domain/analysis 同样无外部依赖。
// 同步治理引擎 domains/interception/* 与 sessionaudit/* 都会引用本包。
package governance

// SessionState 会话治理状态。
//
// 状态机在 domain/governance/state.go 中通过 CanTransitionTo 校验。
type SessionState string

const (
	StateNew             SessionState = "new"
	StateStreaming       SessionState = "streaming"
	StatePendingTool     SessionState = "pending_tool"
	StatePendingApproval SessionState = "pending_approval"
	StatePendingAnalysis SessionState = "pending_analysis"
	StateMutated         SessionState = "mutated"
	StateBlocked         SessionState = "blocked"
	StateContinued       SessionState = "continued"
	StateTerminated      SessionState = "terminated"
)

// IsTerminal 报告该状态是否为终态（blocked / terminated）。
func (s SessionState) IsTerminal() bool {
	return s == StateBlocked || s == StateTerminated
}

// CanTransitionTo 状态机迁移校验。
//
// 规则：
//   - 终态不可迁移
//   - new          → streaming | mutated | blocked | terminated
//   - streaming    → pending_* | continued | blocked | terminated
//   - pending_*    → continued | mutated | blocked | terminated
//   - mutated      → streaming
//   - continued    → streaming | terminated
func (s SessionState) CanTransitionTo(target SessionState) bool {
	if s.IsTerminal() {
		return false
	}
	switch s {
	case StateNew:
		return target == StateStreaming ||
			target == StateMutated ||
			target == StateBlocked ||
			target == StateTerminated
	case StateStreaming:
		return target == StatePendingTool ||
			target == StatePendingApproval ||
			target == StatePendingAnalysis ||
			target == StateContinued ||
			target == StateBlocked ||
			target == StateTerminated
	case StatePendingTool, StatePendingApproval, StatePendingAnalysis:
		return target == StateContinued ||
			target == StateMutated ||
			target == StateBlocked ||
			target == StateTerminated
	case StateMutated:
		return target == StateStreaming
	case StateContinued:
		return target == StateStreaming ||
			target == StateTerminated
	}
	return false
}

// AllSessionStates 返回所有非空状态（用于 UI 列表展示 / 状态过滤）。
func AllSessionStates() []SessionState {
	return []SessionState{
		StateNew, StateStreaming,
		StatePendingTool, StatePendingApproval, StatePendingAnalysis,
		StateMutated, StateBlocked, StateContinued, StateTerminated,
	}
}
