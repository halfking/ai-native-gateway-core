// Package sessionstate 实现会话状态机，用于跟踪和管理会话的生命周期。
//
// 核心能力：
//   - 明确的会话状态定义（初始化、活跃、挂起、完成等）
//   - 会话阶段识别（预知、问答、未知、异常）
//   - 状态转换规则和验证
//   - 状态历史记录和审计
//
// 设计原则：
//   - 线程安全：使用 sync.RWMutex 保护状态
//   - 可追溯：完整记录状态变更历史
//   - 可扩展：支持自定义转换条件和动作
package sessionstate

import (
	"time"
)

// SessionState 会话状态
type SessionState string

const (
	// StateInitialized 初始化：收到第一条消息，尚未认证
	StateInitialized SessionState = "initialized"

	// StateActive 活跃：正常交互中
	StateActive SessionState = "active"

	// StatePending 待处理：缓存查找中或等待某个异步操作
	StatePending SessionState = "pending"

	// StateToolExecuting 工具执行中：正在执行工具调用
	StateToolExecuting SessionState = "tool_executing"

	// StateSuspended 挂起：等待审批或人工干预
	StateSuspended SessionState = "suspended"

	// StateTerminating 终止中：正在清理资源
	StateTerminating SessionState = "terminating"

	// StateCompleted 已完成：会话正常结束
	StateCompleted SessionState = "completed"

	// StateAborted 已中断：会话被取消或超时
	StateAborted SessionState = "aborted"

	// StateError 异常：处理过程中发生错误
	StateError SessionState = "error"
)

// IsTerminal 判断状态是否为终态（不可再转换）
func (s SessionState) IsTerminal() bool {
	return s == StateCompleted || s == StateAborted
}

// SessionPhase 会话阶段（与状态正交，描述会话的业务意图）
type SessionPhase string

const (
	// PhasePreKnowledge 预知阶段：系统识别用户意图
	PhasePreKnowledge SessionPhase = "pre_knowledge"

	// PhaseQA 问答阶段：正常对话
	PhaseQA SessionPhase = "qa"

	// PhaseUnknown 未知阶段：需要更多信息才能判断
	PhaseUnknown SessionPhase = "unknown"

	// PhaseException 异常阶段：错误处理
	PhaseException SessionPhase = "exception"
)

// StateChange 状态变更记录
type StateChange struct {
	From      SessionState   `json:"from"`
	To        SessionState   `json:"to"`
	Event     string         `json:"event"`  // 触发事件名称
	Reason    string         `json:"reason"` // 转换原因
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"` // 附加信息
}

// TransitionEvent 状态转换事件
type TransitionEvent string

const (
	// EventAuthenticated 认证通过
	EventAuthenticated TransitionEvent = "authenticated"

	// EventCacheMiss 缓存未命中
	EventCacheMiss TransitionEvent = "cache_miss"

	// EventCacheHit 缓存命中
	EventCacheHit TransitionEvent = "cache_hit"

	// EventToolRequired 需要工具执行
	EventToolRequired TransitionEvent = "tool_required"

	// EventToolCompleted 工具执行完成
	EventToolCompleted TransitionEvent = "tool_completed"

	// EventToolFailed 工具执行失败
	EventToolFailed TransitionEvent = "tool_failed"

	// EventHighRiskDetected 检测到高风险
	EventHighRiskDetected TransitionEvent = "high_risk_detected"

	// EventApprovalGranted 审批通过
	EventApprovalGranted TransitionEvent = "approval_granted"

	// EventApprovalRejected 审批拒绝
	EventApprovalRejected TransitionEvent = "approval_rejected"

	// EventCompleted 会话完成
	EventCompleted TransitionEvent = "completed"

	// EventError 发生错误
	EventError TransitionEvent = "error"

	// EventRecovered 错误恢复
	EventRecovered TransitionEvent = "recovered"

	// EventCanceled 用户取消
	EventCanceled TransitionEvent = "canceled"

	// EventTimeout 超时
	EventTimeout TransitionEvent = "timeout"
)

// TransitionCondition 转换条件函数
// 返回 true 表示允许转换，false 表示拒绝
type TransitionCondition func(ctx *TransitionContext) bool

// TransitionAction 转换动作函数
// 在状态转换时执行，用于副作用操作（如记录日志、发送通知等）
type TransitionAction func(ctx *TransitionContext) error

// TransitionContext 转换上下文
type TransitionContext struct {
	SessionID string
	TenantID  string
	From      SessionState
	To        SessionState
	Event     string
	Metadata  map[string]any
	Timestamp time.Time
	StateMeta map[string]any // 状态机的元数据
}

// SessionTransition 状态转换规则
type SessionTransition struct {
	From      SessionState        // 源状态
	To        SessionState        // 目标状态
	Event     TransitionEvent     // 触发事件
	Condition TransitionCondition // 转换条件（可选）
	Action    TransitionAction    // 转换动作（可选）
}

// SessionMetrics 会话指标
type SessionMetrics struct {
	StateEnterTime  map[SessionState]time.Time     // 进入每个状态的时间
	StateDurations  map[SessionState]time.Duration // 在每个状态的持续时间
	TransitionCount int                            // 状态转换次数
	ErrorCount      int                            // 错误次数
}

// NewSessionMetrics 创建会话指标
func NewSessionMetrics() *SessionMetrics {
	return &SessionMetrics{
		StateEnterTime: make(map[SessionState]time.Time),
		StateDurations: make(map[SessionState]time.Duration),
	}
}
