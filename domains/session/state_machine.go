package session

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SessionState 定义会话的生命周期状态
type SessionState string

const (
	StateInitial             SessionState = "INITIAL"
	StateReceivingFromClient SessionState = "RECEIVING_FROM_CLIENT"
	StatePendingToLLM        SessionState = "PENDING_TO_LLM"
	StateSendingToLLM        SessionState = "SENDING_TO_LLM"
	StateReceivingFromLLM    SessionState = "RECEIVING_FROM_LLM"
	StatePendingToClient     SessionState = "PENDING_TO_CLIENT"
	StateSendingToClient     SessionState = "SENDING_TO_CLIENT"
	StateCompleted           SessionState = "COMPLETED"
	StateError               SessionState = "ERROR"
)

// String 返回状态的字符串表示
func (s SessionState) String() string {
	return string(s)
}

// StateTransition 记录状态转换历史
type StateTransition struct {
	From      SessionState
	To        SessionState
	Timestamp time.Time
	Reason    string
}

// StateCallback 状态转换时的回调函数
// 返回 error 会中断请求处理
type StateCallback func(ctx context.Context, sc *SessionContext) error

// SessionStateMachine 管理会话状态转换和回调
type SessionStateMachine struct {
	// callbacks 存储每个状态对应的回调函数列表
	callbacks map[SessionState][]StateCallback
	mu        sync.RWMutex
}

// NewStateMachine 创建新的状态机实例
func NewStateMachine() *SessionStateMachine {
	return &SessionStateMachine{
		callbacks: make(map[SessionState][]StateCallback),
	}
}

// RegisterCallback 注册状态转换回调
// 同一状态可以注册多个回调，按注册顺序执行
func (sm *SessionStateMachine) RegisterCallback(state SessionState, cb StateCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.callbacks[state] = append(sm.callbacks[state], cb)
}

// Transition 执行状态转换并触发回调
// 这是状态机的核心方法，负责：
//  1. 更新 SessionContext 的状态
//  2. 记录状态转换历史
//  3. 按顺序执行注册的回调函数
func (sm *SessionStateMachine) Transition(ctx context.Context, sc *SessionContext, to SessionState, reason string) error {
	if sc == nil {
		return fmt.Errorf("session context is nil")
	}

	from := sc.State
	sc.State = to
	sc.Transitions = append(sc.Transitions, StateTransition{
		From:      from,
		To:        to,
		Timestamp: time.Now(),
		Reason:    reason,
	})

	// 执行注册的回调
	sm.mu.RLock()
	callbacks := sm.callbacks[to]
	sm.mu.RUnlock()

	for i, cb := range callbacks {
		if err := cb(ctx, sc); err != nil {
			return fmt.Errorf("callback %d failed for state %s: %w", i, to, err)
		}
	}

	return nil
}

// GetCallbackCount 返回指定状态的回调函数数量（测试用）
func (sm *SessionStateMachine) GetCallbackCount(state SessionState) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.callbacks[state])
}

// Clear 清除所有回调（测试用）
func (sm *SessionStateMachine) Clear() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.callbacks = make(map[SessionState][]StateCallback)
}
