package sessionstate

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SessionStateMachine 会话状态机
//
// 职责：
//   - 管理会话的状态转换
//   - 验证转换的合法性
//   - 记录状态变更历史
//   - 执行转换时的副作用动作
//
// 线程安全：使用 sync.RWMutex 保护所有状态字段
type SessionStateMachine struct {
	sessionID    string
	tenantID     string
	currentState SessionState
	currentPhase SessionPhase
	transitions  map[SessionState][]SessionTransition
	history      []StateChange
	metadata     map[string]any
	metrics      *SessionMetrics
	mu           sync.RWMutex
	createdAt    time.Time
}

// NewSessionStateMachine 创建会话状态机
func NewSessionStateMachine(sessionID, tenantID string) *SessionStateMachine {
	sm := &SessionStateMachine{
		sessionID:    sessionID,
		tenantID:     tenantID,
		currentState: StateInitialized,
		currentPhase: PhaseUnknown,
		transitions:  make(map[SessionState][]SessionTransition),
		history:      make([]StateChange, 0),
		metadata:     make(map[string]any),
		metrics:      NewSessionMetrics(),
		createdAt:    time.Now(),
	}
	
	// 注册默认转换规则
	sm.registerDefaultTransitions()
	
	// 记录初始状态
	sm.metrics.StateEnterTime[StateInitialized] = time.Now()
	
	return sm
}

// registerDefaultTransitions 注册默认状态转换规则
func (sm *SessionStateMachine) registerDefaultTransitions() {
	// Initialized → Active: 认证通过
	sm.RegisterTransition(SessionTransition{
		From:  StateInitialized,
		To:    StateActive,
		Event: EventAuthenticated,
	})
	
	// Active → Pending: 缓存未命中
	sm.RegisterTransition(SessionTransition{
		From:  StateActive,
		To:    StatePending,
		Event: EventCacheMiss,
	})
	
	// Pending → Active: 缓存命中（从其他来源）
	sm.RegisterTransition(SessionTransition{
		From:  StatePending,
		To:    StateActive,
		Event: EventCacheHit,
	})
	
	// Pending → ToolExecuting: 需要工具执行
	sm.RegisterTransition(SessionTransition{
		From:  StatePending,
		To:    StateToolExecuting,
		Event: EventToolRequired,
	})
	
	// ToolExecuting → Pending: 工具执行完成，等待匹配
	sm.RegisterTransition(SessionTransition{
		From:  StateToolExecuting,
		To:    StatePending,
		Event: EventToolCompleted,
	})
	
	// ToolExecuting → Active: 工具执行完成，直接继续
	sm.RegisterTransition(SessionTransition{
		From:  StateToolExecuting,
		To:    StateActive,
		Event: EventToolCompleted,
	})
	
	// ToolExecuting → Error: 工具执行失败
	sm.RegisterTransition(SessionTransition{
		From:  StateToolExecuting,
		To:    StateError,
		Event: EventToolFailed,
	})
	
	// Pending → Completed: 从Pending直接完成
	sm.RegisterTransition(SessionTransition{
		From:  StatePending,
		To:    StateCompleted,
		Event: EventCompleted,
	})
	
	// Active → Suspended: 高风险检测，需要审批
	sm.RegisterTransition(SessionTransition{
		From:  StateActive,
		To:    StateSuspended,
		Event: EventHighRiskDetected,
	})
	
	// Suspended → Active: 审批通过
	sm.RegisterTransition(SessionTransition{
		From:  StateSuspended,
		To:    StateActive,
		Event: EventApprovalGranted,
	})
	
	// Suspended → Aborted: 审批拒绝
	sm.RegisterTransition(SessionTransition{
		From:  StateSuspended,
		To:    StateAborted,
		Event: EventApprovalRejected,
	})
	
	// Active → Completed: 会话正常结束
	sm.RegisterTransition(SessionTransition{
		From:  StateActive,
		To:    StateCompleted,
		Event: EventCompleted,
	})
	
	// Active → Error: 处理异常
	sm.RegisterTransition(SessionTransition{
		From:  StateActive,
		To:    StateError,
		Event: EventError,
	})
	
	// Error → Active: 错误恢复
	sm.RegisterTransition(SessionTransition{
		From:  StateError,
		To:    StateActive,
		Event: EventRecovered,
	})
	
	// 任何状态 → Aborted: 用户取消或超时
	allStates := []SessionState{
		StateInitialized, StateActive, StatePending, StateToolExecuting,
		StateSuspended, StateTerminating, StateError,
	}
	for _, state := range allStates {
		sm.RegisterTransition(SessionTransition{
			From:  state,
			To:    StateAborted,
			Event: EventCanceled,
		})
		sm.RegisterTransition(SessionTransition{
			From:  state,
			To:    StateAborted,
			Event: EventTimeout,
		})
	}
	
	// 任何非终态 → Terminating
	for _, state := range allStates {
		if !state.IsTerminal() {
			sm.RegisterTransition(SessionTransition{
				From:  state,
				To:    StateTerminating,
				Event: EventCanceled,
			})
		}
	}
	
	// Terminating → Aborted
	sm.RegisterTransition(SessionTransition{
		From:  StateTerminating,
		To:    StateAborted,
		Event: EventCompleted,
	})
}

// RegisterTransition 注册状态转换规则
// 注意：相同的 From+Event 组合会被覆盖（后注册的优先）
func (sm *SessionStateMachine) RegisterTransition(t SessionTransition) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.transitions[t.From] == nil {
		sm.transitions[t.From] = make([]SessionTransition, 0)
	}
	
	// 检查是否已存在相同的 From+Event 组合，如果存在则替换
	replaced := false
	for i, existing := range sm.transitions[t.From] {
		if existing.Event == t.Event {
			sm.transitions[t.From][i] = t
			replaced = true
			break
		}
	}
	
	if !replaced {
		sm.transitions[t.From] = append(sm.transitions[t.From], t)
	}
}

// Transition 执行状态转换
//
// 参数：
//   - ctx: Go 上下文
//   - event: 触发事件
//   - reason: 转换原因（可选）
//   - metadata: 附加元数据（可选）
//
// 返回：
//   - error: 转换失败时返回错误
func (sm *SessionStateMachine) Transition(ctx context.Context, event TransitionEvent, reason string, metadata map[string]any) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	currentState := sm.currentState
	
	// 检查是否为终态
	if currentState.IsTerminal() {
		return fmt.Errorf("cannot transition from terminal state: %s", currentState)
	}
	
	// 查找匹配的转换规则
	transitions, ok := sm.transitions[currentState]
	if !ok {
		return fmt.Errorf("no transitions defined for state: %s", currentState)
	}
	
	var matchedTransition *SessionTransition
	for i := range transitions {
		if transitions[i].Event == event {
			matchedTransition = &transitions[i]
			break
		}
	}
	
	if matchedTransition == nil {
		return fmt.Errorf("no transition found for event %s in state %s", event, currentState)
	}
	
	// 构建转换上下文
	transCtx := &TransitionContext{
		SessionID: sm.sessionID,
		TenantID:  sm.tenantID,
		From:      currentState,
		To:        matchedTransition.To,
		Event:     string(event),
		Metadata:  metadata,
		Timestamp: time.Now(),
		StateMeta: sm.metadata,
	}
	
	// 检查转换条件
	if matchedTransition.Condition != nil {
		if !matchedTransition.Condition(transCtx) {
			return fmt.Errorf("transition condition not satisfied for %s -> %s on event %s",
				currentState, matchedTransition.To, event)
		}
	}
	
	// 执行转换动作
	if matchedTransition.Action != nil {
		if err := matchedTransition.Action(transCtx); err != nil {
			return fmt.Errorf("transition action failed: %w", err)
		}
	}
	
	// 更新指标
	now := time.Now()
	if enterTime, ok := sm.metrics.StateEnterTime[currentState]; ok {
		duration := now.Sub(enterTime)
		sm.metrics.StateDurations[currentState] += duration
	}
	sm.metrics.StateEnterTime[matchedTransition.To] = now
	sm.metrics.TransitionCount++
	
	if matchedTransition.To == StateError {
		sm.metrics.ErrorCount++
	}
	
	// 记录状态变更
	change := StateChange{
		From:      currentState,
		To:        matchedTransition.To,
		Event:     string(event),
		Reason:    reason,
		Timestamp: now,
		Metadata:  metadata,
	}
	sm.history = append(sm.history, change)
	
	// 更新当前状态
	sm.currentState = matchedTransition.To
	
	// 根据状态更新阶段
	sm.updatePhase(matchedTransition.To)
	
	return nil
}

// updatePhase 根据状态更新阶段
func (sm *SessionStateMachine) updatePhase(state SessionState) {
	switch state {
	case StateInitialized:
		sm.currentPhase = PhasePreKnowledge
	case StateActive, StatePending, StateToolExecuting:
		// 如果之前是 PhaseUnknown，进入活跃状态后更新为 PhaseQA
		if sm.currentPhase == PhaseUnknown || sm.currentPhase == PhasePreKnowledge {
			sm.currentPhase = PhaseQA
		}
	case StateError:
		sm.currentPhase = PhaseException
	case StateSuspended:
		// 保持当前阶段
	case StateCompleted, StateAborted, StateTerminating:
		// 终态不改变阶段
	}
}

// GetState 获取当前状态
func (sm *SessionStateMachine) GetState() SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentState
}

// GetPhase 获取当前阶段
func (sm *SessionStateMachine) GetPhase() SessionPhase {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentPhase
}

// SetPhase 手动设置阶段（通常由意图识别等模块调用）
func (sm *SessionStateMachine) SetPhase(phase SessionPhase) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.currentPhase = phase
}

// GetHistory 获取状态变更历史（返回副本）
func (sm *SessionStateMachine) GetHistory() []StateChange {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	history := make([]StateChange, len(sm.history))
	copy(history, sm.history)
	return history
}

// GetMetrics 获取会话指标（返回副本）
func (sm *SessionStateMachine) GetMetrics() SessionMetrics {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	metrics := SessionMetrics{
		StateEnterTime:  make(map[SessionState]time.Time),
		StateDurations:  make(map[SessionState]time.Duration),
		TransitionCount: sm.metrics.TransitionCount,
		ErrorCount:      sm.metrics.ErrorCount,
	}
	
	for k, v := range sm.metrics.StateEnterTime {
		metrics.StateEnterTime[k] = v
	}
	for k, v := range sm.metrics.StateDurations {
		metrics.StateDurations[k] = v
	}
	
	return metrics
}

// SetMetadata 设置元数据
func (sm *SessionStateMachine) SetMetadata(key string, value any) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.metadata[key] = value
}

// GetMetadata 获取元数据
func (sm *SessionStateMachine) GetMetadata(key string) (any, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	val, ok := sm.metadata[key]
	return val, ok
}

// IsTerminal 判断当前状态是否为终态
func (sm *SessionStateMachine) IsTerminal() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentState.IsTerminal()
}

// SessionID 获取会话ID
func (sm *SessionStateMachine) SessionID() string {
	return sm.sessionID
}

// TenantID 获取租户ID
func (sm *SessionStateMachine) TenantID() string {
	return sm.tenantID
}

// CreatedAt 获取创建时间
func (sm *SessionStateMachine) CreatedAt() time.Time {
	return sm.createdAt
}

// Snapshot 获取当前状态快照
type Snapshot struct {
	SessionID    string                `json:"session_id"`
	TenantID     string                `json:"tenant_id"`
	CurrentState SessionState          `json:"current_state"`
	CurrentPhase SessionPhase          `json:"current_phase"`
	History      []StateChange         `json:"history"`
	Metrics      SessionMetrics        `json:"metrics"`
	Metadata     map[string]any        `json:"metadata"`
	CreatedAt    time.Time             `json:"created_at"`
	SnapshotAt   time.Time             `json:"snapshot_at"`
}

// GetSnapshot 获取状态机快照
func (sm *SessionStateMachine) GetSnapshot() Snapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	history := make([]StateChange, len(sm.history))
	copy(history, sm.history)
	
	metadata := make(map[string]any)
	for k, v := range sm.metadata {
		metadata[k] = v
	}
	
	return Snapshot{
		SessionID:    sm.sessionID,
		TenantID:     sm.tenantID,
		CurrentState: sm.currentState,
		CurrentPhase: sm.currentPhase,
		History:      history,
		Metrics:      sm.GetMetrics(),
		Metadata:     metadata,
		CreatedAt:    sm.createdAt,
		SnapshotAt:   time.Now(),
	}
}
