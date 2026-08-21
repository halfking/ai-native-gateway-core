// Package goalrun — GoalRun 持久编排账本（设计 13，Wave 2-A）。
//
// GoalRun 是一次持续目标执行的持久编排账本，不是第二个 durable task owner；
// 它维护 goal_run -> step -> action 的关联与决策 projection，实际上游调用仍由
// dispatch、Executor、SurvivalCoordinator 与 durable worker 执行。
//
// 关键不变量：
//   - GoalRun 不能替代 goal_sessions、durable task、handoff confirmation、
//     approval、session v2 的权威 store；
//   - content/tool call/side-effect checkpoint 后不得透明 replay；
//   - 终态 sticky；cancel/terminal 优先于迟到 worker 或 follow-up；
//   - 一个 GoalRun 同时只有一个有效 successor（CAS/lease 保证）。
package goalrun

import "time"

// Status 是 GoalRun 执行状态机（设计 13 §6.3）。
type Status string

const (
	// StatusCreated GoalRun 已创建但尚未排队（初始化阶段）。
	StatusCreated Status = "created"
	// StatusQueued 已排队等待执行。
	StatusQueued Status = "queued"
	// StatusRunning 当前正在执行。
	StatusRunning Status = "running"
	// StatusWaitingTool 等待工具执行结果。
	StatusWaitingTool Status = "waiting_tool"
	// StatusWaitingInput 等待用户输入。
	StatusWaitingInput Status = "waiting_input"
	// StatusWaitingHandoff 等待跨会话 handoff 确认。
	StatusWaitingHandoff Status = "waiting_handoff"
	// StatusAuditing 完成候选，正在审计验证。
	StatusAuditing Status = "auditing"
	// StatusRestoring 从 handoff 恢复中。
	StatusRestoring Status = "restoring"
	// StatusCompleted 成功完成终态。
	StatusCompleted Status = "completed"
	// StatusFailed 永久失败终态。
	StatusFailed Status = "failed"
	// StatusCanceled 显式取消终态。
	StatusCanceled Status = "canceled"
	// StatusExpired deadline 到期终态。
	StatusExpired Status = "expired"
	// StatusManualRequired 需要人工介入终态。
	StatusManualRequired Status = "manual_required"
	// StatusResumeSafetyBlocked 安全阻断：越过语义 checkpoint 后无法安全续接。
	StatusResumeSafetyBlocked Status = "resume_safety_blocked"
)

// terminalStatuses 终态集合：一旦进入不可迁移回任何状态（设计 13 §6.3）。
var terminalStatuses = map[Status]struct{}{
	StatusCompleted:      {},
	StatusFailed:         {},
	StatusCanceled:       {},
	StatusExpired:        {},
	StatusManualRequired: {},
}

// IsTerminalStatus 报告 s 是否终态。resume_safety_blocked 不是终态但同样不可自动恢复。
func IsTerminalStatus(s Status) bool {
	_, ok := terminalStatuses[s]
	return ok
}

// transitions 是合法状态迁移矩阵（设计 13 §6.3）。
var transitions = buildTransitions()

func buildTransitions() map[Status]map[Status]struct{} {
	t := map[Status]map[Status]struct{}{}
	add := func(from Status, tos ...Status) {
		set, ok := t[from]
		if !ok {
			set = map[Status]struct{}{}
			t[from] = set
		}
		for _, to := range tos {
			set[to] = struct{}{}
		}
	}

	// 初始化 -> 排队 -> 执行
	add(StatusCreated, StatusQueued)
	add(StatusQueued, StatusRunning)

	// 执行中 -> 等待状态
	add(StatusRunning,
		StatusWaitingTool, StatusWaitingInput, StatusWaitingHandoff, StatusAuditing)

	// 等待状态 -> 恢复执行
	add(StatusWaitingTool, StatusQueued)
	add(StatusWaitingInput, StatusQueued)
	add(StatusWaitingHandoff, StatusRestoring)
	add(StatusRestoring, StatusQueued)

	// 任意非终态 -> 终态
	nonTerminal := []Status{
		StatusCreated, StatusQueued, StatusRunning,
		StatusWaitingTool, StatusWaitingInput, StatusWaitingHandoff,
		StatusAuditing, StatusRestoring,
	}
	for _, from := range nonTerminal {
		add(from,
			StatusCompleted, StatusFailed, StatusCanceled, StatusExpired,
			StatusManualRequired, StatusResumeSafetyBlocked)
	}

	// resume_safety_blocked 是吸态：只允许未来人工/对账迁出
	return t
}

// CanTransition 报告 from -> to 是否状态机合法。
func CanTransition(from, to Status) bool {
	if from == to {
		return true
	}
	set, ok := transitions[from]
	if !ok {
		return false
	}
	_, ok = set[to]
	return ok
}

// StepStatus 是 GoalRun step 的执行状态。
type StepStatus string

const (
	// StepStatusPending step 已创建但尚未开始。
	StepStatusPending StepStatus = "pending"
	// StepStatusRunning step 正在执行。
	StepStatusRunning StepStatus = "running"
	// StepStatusCompleted step 已成功完成。
	StepStatusCompleted StepStatus = "completed"
	// StepStatusFailed step 失败。
	StepStatusFailed StepStatus = "failed"
	// StepStatusCanceled step 被取消。
	StepStatusCanceled StepStatus = "canceled"
)

// ActionType 是 GoalRun action 的类型（设计 13 §6.2）。
type ActionType string

const (
	// ActionTypeContinue 创建同 session 的 successor request。
	ActionTypeContinue ActionType = "continue"
	// ActionTypeHandoff 跨 session handoff proposal。
	ActionTypeHandoff ActionType = "handoff"
	// ActionTypeModelSwitch Goal loop 级别模型切换。
	ActionTypeModelSwitch ActionType = "model_switch"
	// ActionTypeAudit 完成审计验证。
	ActionTypeAudit ActionType = "audit"
	// ActionTypeTerminate 显式终止。
	ActionTypeTerminate ActionType = "terminate"
)

// ActionStatus 是 action 执行状态。
type ActionStatus string

const (
	// ActionStatusPending action 已创建等待执行。
	ActionStatusPending ActionStatus = "pending"
	// ActionStatusRunning action 正在执行。
	ActionStatusRunning ActionStatus = "running"
	// ActionStatusCompleted action 已成功完成。
	ActionStatusCompleted ActionStatus = "completed"
	// ActionStatusFailed action 失败。
	ActionStatusFailed ActionStatus = "failed"
	// ActionStatusCanceled action 被取消。
	ActionStatusCanceled ActionStatus = "canceled"
)

// GoalRun 是持久编排账本的投影（设计 13 §6.2）。
type GoalRun struct {
	ID                       string
	TenantID                 string
	APIKeyID                 string
	RootGoalID               string
	RootSessionID            string
	CurrentSessionID         string
	RootRequestID            string
	LastRequestID            string
	LastDurableTaskID        string
	Status                   Status
	PolicyVersion            int
	PolicySnapshot           []byte
	InstructionHash          string
	RedactedInstructionSummary string
	TurnCount                int
	FollowUpCount            int
	RetryCount               int
	ModelSwitchCount         int
	HandoffCount             int
	TokensUsed               int64
	LastProgressHash         string
	DeadlineAt               time.Time
	LeaseOwner               string
	LeaseUntil               time.Time
	Version                  int64
	TerminalReason           string
	CreatedAt                time.Time
	UpdatedAt                time.Time
	CompletedAt              time.Time
}

// GoalRunStep 是 GoalRun 内一次单调序号的请求/恢复/审计/handoff 动作（设计 13 §6.2）。
type GoalRunStep struct {
	GoalRunID       string
	Sequence        int
	RequestID       string
	ParentRequestID string
	SessionID       string
	DurableTaskID   string
	Action          ActionType
	Status          StepStatus
	ResponseHash    string
	ResultVersion   int64
	CreatedAt       time.Time
	CompletedAt     time.Time
}

// GoalRunAction 是可幂等、可重试的编排命令（设计 13 §6.2）。
type GoalRunAction struct {
	ActionID        string
	GoalRunID       string
	CausationID     string
	ActionType      ActionType
	IdempotencyKey  string
	ExpectedVersion int64
	Status          ActionStatus
	RetryAt         time.Time
	Attempts        int
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewGoalRunInput 是创建 GoalRun 的输入（设计 13 §6.1/§6.2，W2-B）。
type NewGoalRunInput struct {
	TenantID                   string
	APIKeyID                   string
	RootGoalID                 string
	RootSessionID              string
	RootRequestID              string
	PolicyVersion              int
	PolicySnapshot             []byte
	InstructionHash            string
	RedactedInstructionSummary string
	DeadlineAt                 time.Time
	LeaseOwner                 string
	LeaseUntil                 time.Time
}

// Validate 校验创建输入的必填字段。
func (n NewGoalRunInput) Validate() error {
	switch {
	case n.TenantID == "":
		return ErrTenantIDRequired
	case n.APIKeyID == "":
		return ErrAPIKeyIDRequired
	case n.RootSessionID == "":
		return ErrRootSessionIDRequired
	case n.RootRequestID == "":
		return ErrRootRequestIDRequired
	case n.InstructionHash == "":
		return ErrInstructionHashRequired
	case n.DeadlineAt.IsZero():
		return ErrDeadlineRequired
	case n.LeaseOwner == "":
		return ErrLeaseOwnerRequired
	case n.LeaseUntil.IsZero():
		return ErrLeaseUntilRequired
	}
	return nil
}
