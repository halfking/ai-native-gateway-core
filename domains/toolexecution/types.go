// Package toolexecution 实现工具执行追踪领域。
//
// 该域负责记录所有 LLM 工具调用的完整生命周期：
//   - Start：记录工具调用开始（pending 状态）
//   - Success：记录成功完成及结果
//   - Error：记录执行错误
//   - Timeout：记录执行超时
//
// 数据模型直接对应 migrations/134_tool_execution.sql 中的两张表：
//   - tool_executions：每次工具调用的详细记录
//   - tool_usage_stats：按天聚合的统计指标（P50/P95/P99、Top 用户等）
//
// 该包是 Hook 集成 (domains/hooks/toolexecution) 和统计聚合
// (StatsAggregator) 共同依赖的核心层。
package toolexecution

import (
	"encoding/json"
	"time"
)

// ExecutionStatus 工具执行状态。
type ExecutionStatus string

const (
	// StatusPending 调用已开始但尚未完成
	StatusPending ExecutionStatus = "pending"
	// StatusSuccess 调用成功完成
	StatusSuccess ExecutionStatus = "success"
	// StatusError 调用因错误终止
	StatusError ExecutionStatus = "error"
	// StatusTimeout 调用超时
	StatusTimeout ExecutionStatus = "timeout"
)

// Common error_type 分类（在 RecordError 中可显式指定）。
const (
	ErrorTypeNetwork        = "network_error"
	ErrorTypeTimeout        = "timeout"
	ErrorTypeInvalidArgs    = "invalid_args"
	ErrorTypeExecutionFail  = "execution_failed"
	ErrorTypeUpstreamReject = "upstream_rejected"
)

// ToolExecution 工具执行记录（对应 tool_executions 表的一行）。
type ToolExecution struct {
	ExecutionID string
	SessionID   string
	RequestID   string
	TenantID    string

	// 工具信息
	ToolName   string
	ToolCallID string          // OpenAI: tool_call_id, Anthropic: tool_use.id
	Arguments  json.RawMessage // 工具调用参数
	Result     json.RawMessage // 工具执行结果

	// 执行状态
	Status       ExecutionStatus
	ErrorMessage string
	ErrorType    string

	// 时间统计
	StartedAt   time.Time
	CompletedAt time.Time
	DurationMs  int64

	// 关联信息
	IdentityHash string
	Model        string

	CreatedAt time.Time
}

// IsTerminal 报告该记录是否已经处于终止状态（success/error/timeout）。
func (e *ToolExecution) IsTerminal() bool {
	switch e.Status {
	case StatusSuccess, StatusError, StatusTimeout:
		return true
	}
	return false
}

// Elapsed 返回自 StartedAt 起的耗时；若 CompletedAt 已设置则返回完成耗时。
// 主要用于在执行尚未完成时提供当前耗时（如超时检测）。
func (e *ToolExecution) Elapsed() time.Duration {
	end := e.CompletedAt
	if end.IsZero() {
		end = time.Now()
	}
	if e.StartedAt.IsZero() {
		return 0
	}
	return end.Sub(e.StartedAt)
}

// ComputeDuration 根据 StartedAt/CompletedAt 重算 DurationMs。
// 数据库 trigger 会自动计算，这里用于在写入前先填好字段。
func (e *ToolExecution) ComputeDuration() {
	if e.StartedAt.IsZero() || e.CompletedAt.IsZero() {
		return
	}
	e.DurationMs = e.CompletedAt.Sub(e.StartedAt).Milliseconds()
}

// ToolUsageStats 工具使用统计（对应 tool_usage_stats 表的一行）。
type ToolUsageStats struct {
	ID int64

	ToolName string
	Date     time.Time

	TotalCalls   int64
	SuccessCalls int64
	FailedCalls  int64
	TimeoutCalls int64

	AvgDurationMs float64
	P50DurationMs int64
	P95DurationMs int64
	P99DurationMs int64

	UniqueUsers    int
	UniqueSessions int

	// TopUsers 按调用次数降序，最多 TopUsersLimit 个
	TopUsers []UserUsage

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TopUsersLimit TopUsers 列表的最大长度。
const TopUsersLimit = 10

// UserUsage 单个 identity_hash 的调用统计。
type UserUsage struct {
	IdentityHash string
	CallCount    int64
}
