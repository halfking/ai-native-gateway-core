// Package durable — durable 持久接管的存储层（doc 18 §11/§12.2，SR-08/09/10）。
//
// 本包（Wave A）只做存储层：schema（migration 516）、DurableTaskStore
// （CreateAndClaim / claim / lease / fencing / write-ahead checkpoint /
// 原子终态 / reapers）与 PendingStore CAS 投影。RecoveryWorker（SR-11）、
// Handler 接线（SR-12）与指标（SR-13）属 Wave B。
//
// 开关：全部行为由 config.RequestSurvivalDurableEnabled（默认 false，
// env LLM_GATEWAY_REQUEST_SURVIVAL_DURABLE_ENABLED）在装配层门控；
// 本包自身不读取配置，flag 关闭时不构造、不读写任何新表。
package durable

// Status 是 durable 任务调度状态机（doc 18 §11.1/§11.3）。
type Status string

const (
	// StatusRunning 已被某 owner 持有合法 lease 并正在执行 attempt。
	StatusRunning Status = "running"
	// StatusWaitingRecovery 等待恢复（连接属性），可被 claim。
	StatusWaitingRecovery Status = "waiting_recovery"
	// StatusRetryScheduled 退避后重试，可被 claim。
	StatusRetryScheduled Status = "retry_scheduled"
	// StatusCompleted 成功终态（携带加密结果）。
	StatusCompleted Status = "completed"
	// StatusFailed 永久失败终态。
	StatusFailed Status = "failed"
	// StatusExpired deadline reaper 形成的过期终态。
	StatusExpired Status = "expired"
	// StatusCanceled 显式取消终态。
	StatusCanceled Status = "canceled"
	// StatusResumeSafetyBlocked 安全阻断吸态：越过语义 checkpoint 后崩溃，
	// 无法无副作用续接，不得重新执行（doc 18 §11.3）。
	StatusResumeSafetyBlocked Status = "resume_safety_blocked"
)

// terminalStatuses 终态集合：一旦进入不可迁移回任何状态。
var terminalStatuses = map[Status]struct{}{
	StatusCompleted: {},
	StatusFailed:    {},
	StatusExpired:   {},
	StatusCanceled:  {},
}

// IsTerminalStatus 报告 s 是否终态（completed/failed/expired/canceled）。
// resume_safety_blocked 不是终态，但同样不可被普通 claim。
func IsTerminalStatus(s Status) bool {
	_, ok := terminalStatuses[s]
	return ok
}

// CommitState 是 write-ahead 语义提交门禁（doc 18 §11.1/§11.3）。
type CommitState string

const (
	// CommitStateNone 尚无语义输出，可安全重放重试。
	CommitStateNone CommitState = "none"
	// CommitStateMetadata 只有协议 metadata（可丢弃）。
	CommitStateMetadata CommitState = "metadata"
	// CommitStateContent 已写语义 content 帧，禁止透明重放。
	CommitStateContent CommitState = "content"
	// CommitStateToolCall 已写 tool call，重放有副作用，必须阻断。
	CommitStateToolCall CommitState = "tool_call"
	// CommitStateTerminal 已形成终态。
	CommitStateTerminal CommitState = "terminal"
)

// commitStateRanks 单调序：checkpoint 只允许前进，不允许回退。
var commitStateRanks = map[CommitState]int{
	CommitStateNone:     0,
	CommitStateMetadata: 1,
	CommitStateContent:  2,
	CommitStateToolCall: 3,
	CommitStateTerminal: 4,
}

// CommitStateRank 返回 commit_state 的单调序值；未知值 -1（fail closed）。
func CommitStateRank(cs CommitState) int {
	if r, ok := commitStateRanks[cs]; ok {
		return r
	}
	return -1
}

// IsRunnableCommitState 是所有普通 execution claim / reschedule 的全局
// 门禁（doc 18 §11.3）：只有 none/metadata 可安全重新执行。
func IsRunnableCommitState(cs CommitState) bool {
	return cs == CommitStateNone || cs == CommitStateMetadata
}

// transitions 是合法状态迁移矩阵。构建时间静态表，CanTransition O(1)。
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
	// claim
	add(StatusRetryScheduled, StatusRunning)
	add(StatusWaitingRecovery, StatusRunning)
	// 前台/worker reschedule 与断连等待
	add(StatusRunning, StatusRetryScheduled, StatusWaitingRecovery)
	// 终态
	add(StatusRunning,
		StatusCompleted, StatusFailed, StatusExpired, StatusCanceled,
		StatusResumeSafetyBlocked)
	add(StatusRetryScheduled,
		StatusExpired, StatusCanceled, StatusResumeSafetyBlocked)
	add(StatusWaitingRecovery,
		StatusExpired, StatusCanceled, StatusResumeSafetyBlocked)
	// resume_safety_blocked 是吸态：只允许未来的人工/对账迁出，
	// 普通状态机不允许其回到 runnable。
	return t
}

// CanTransition 报告 from -> to 是否状态机合法。终态（以及
// resume_safety_blocked）永不回到 runnable（doc 18 §11.3）。
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
