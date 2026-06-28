package governance

import "time"

// DecisionKind 治理决策类型。
type DecisionKind string

const (
	DecisionContinue  DecisionKind = "continue"
	DecisionBlock     DecisionKind = "block"
	DecisionMutate    DecisionKind = "mutate"
	DecisionSuspend   DecisionKind = "suspend"
	DecisionTerminate DecisionKind = "terminate"
)

// WaitFor 挂起原因。
type WaitFor string

const (
	WaitForTool     WaitFor = "tool"
	WaitForApproval WaitFor = "approval"
	WaitForAnalysis WaitFor = "analysis"
)

// ApprovalRequest 审批请求快照。
//
// 由 sessionaudit 创建审批记录时使用；RiskLevel 建议取值
// "low" | "medium" | "high" | "critical"，由治理引擎根据 verdict 决定。
type ApprovalRequest struct {
	Reason    string
	RiskLevel string
	RequestID string
	SessionID string
	Snapshot  any
}

// AnalysisTicket 分析工单。
//
// 挂起到 async 层时使用；WorkerName 指定目标 worker（如 "topic_summarizer"）。
type AnalysisTicket struct {
	WorkerName string
	Reason     string
	Priority   int
}

// SuspensionSpec 挂起规格。
//
// 仅在 Decision.Kind == Suspend 时填充。
type SuspensionSpec struct {
	WaitFor  WaitFor
	ToolPlan *ToolExecutionPlan
	Approval *ApprovalRequest
	Analysis *AnalysisTicket
	Timeout  time.Duration
}

// Decision 统一治理决策。
//
// 由 domains/interception.Engine 产出；调用方按 Kind 分发：
//
//	Continue  → 继续送往上游
//	Block     → 返回 4xx 并写审计
//	Mutate    → 用 MutatedBody 替换请求体后继续
//	Suspend   → 返回 202 + 轮询地址（pending/ 已有 store 可复用）
//	Terminate → 终止会话并写终态
type Decision struct {
	Kind         DecisionKind
	Reason       string
	MutatedBody  []byte
	Suspension   *SuspensionSpec
	TTL          time.Duration
	TraceID      string
	SourcePlugin string
	CreatedAt    time.Time
}
