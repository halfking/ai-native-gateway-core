package sessionstate

import (
	"context"
	"testing"
	"time"
)

func TestSessionStateMachine_BasicTransitions(t *testing.T) {
	sm := NewSessionStateMachine("sess_123", "tenant_001")
	
	// 初始状态应该是 Initialized
	if sm.GetState() != StateInitialized {
		t.Errorf("expected initial state to be Initialized, got %s", sm.GetState())
	}
	
	// Initialized → Active (认证通过)
	err := sm.Transition(context.Background(), EventAuthenticated, "user authenticated", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateActive {
		t.Errorf("expected state to be Active, got %s", sm.GetState())
	}
	
	// Active → Pending (缓存未命中)
	err = sm.Transition(context.Background(), EventCacheMiss, "cache miss", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StatePending {
		t.Errorf("expected state to be Pending, got %s", sm.GetState())
	}
	
	// Pending → ToolExecuting (需要工具)
	err = sm.Transition(context.Background(), EventToolRequired, "tool required", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateToolExecuting {
		t.Errorf("expected state to be ToolExecuting, got %s", sm.GetState())
	}
	
	// ToolExecuting → Active (工具完成)
	err = sm.Transition(context.Background(), EventToolCompleted, "tool completed", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateActive {
		t.Errorf("expected state to be Active, got %s", sm.GetState())
	}
	
	// Active → Completed (会话结束)
	err = sm.Transition(context.Background(), EventCompleted, "session completed", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateCompleted {
		t.Errorf("expected state to be Completed, got %s", sm.GetState())
	}
	
	// 检查历史记录
	history := sm.GetHistory()
	if len(history) != 5 {
		t.Errorf("expected 5 state changes, got %d", len(history))
	}
}

func TestSessionStateMachine_ApprovalFlow(t *testing.T) {
	sm := NewSessionStateMachine("sess_456", "tenant_002")
	
	// Initialized → Active
	_ = sm.Transition(context.Background(), EventAuthenticated, "authenticated", nil)
	
	// Active → Suspended (高风险检测)
	err := sm.Transition(context.Background(), EventHighRiskDetected, "high risk", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateSuspended {
		t.Errorf("expected state to be Suspended, got %s", sm.GetState())
	}
	
	// Suspended → Active (审批通过)
	err = sm.Transition(context.Background(), EventApprovalGranted, "approved", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateActive {
		t.Errorf("expected state to be Active, got %s", sm.GetState())
	}
}

func TestSessionStateMachine_ApprovalRejection(t *testing.T) {
	sm := NewSessionStateMachine("sess_789", "tenant_003")
	
	// Initialized → Active → Suspended
	_ = sm.Transition(context.Background(), EventAuthenticated, "authenticated", nil)
	_ = sm.Transition(context.Background(), EventHighRiskDetected, "high risk", nil)
	
	// Suspended → Aborted (审批拒绝)
	err := sm.Transition(context.Background(), EventApprovalRejected, "rejected", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateAborted {
		t.Errorf("expected state to be Aborted, got %s", sm.GetState())
	}
	
	// 尝试从终态转换应该失败
	err = sm.Transition(context.Background(), EventAuthenticated, "retry", nil)
	if err == nil {
		t.Error("expected error when transitioning from terminal state, got nil")
	}
}

func TestSessionStateMachine_ErrorRecovery(t *testing.T) {
	sm := NewSessionStateMachine("sess_error", "tenant_004")
	
	// Initialized → Active → Error
	_ = sm.Transition(context.Background(), EventAuthenticated, "authenticated", nil)
	err := sm.Transition(context.Background(), EventError, "error occurred", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateError {
		t.Errorf("expected state to be Error, got %s", sm.GetState())
	}
	
	// Error → Active (恢复)
	err = sm.Transition(context.Background(), EventRecovered, "recovered", nil)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	
	if sm.GetState() != StateActive {
		t.Errorf("expected state to be Active after recovery, got %s", sm.GetState())
	}
	
	// 检查错误计数
	metrics := sm.GetMetrics()
	if metrics.ErrorCount != 1 {
		t.Errorf("expected error count to be 1, got %d", metrics.ErrorCount)
	}
}

func TestSessionStateMachine_CancellationFromAnyState(t *testing.T) {
	states := []SessionState{StateInitialized, StateActive, StatePending, StateToolExecuting, StateSuspended}
	
	for _, startState := range states {
		sm := NewSessionStateMachine("sess_cancel", "tenant_005")
		
		// 手动设置初始状态（仅用于测试）
		sm.mu.Lock()
		sm.currentState = startState
		sm.mu.Unlock()
		
		// 从任何非终态都应该能取消
		err := sm.Transition(context.Background(), EventCanceled, "user canceled", nil)
		if err != nil && !startState.IsTerminal() {
			t.Errorf("failed to cancel from state %s: %v", startState, err)
		}
	}
}

func TestSessionStateMachine_Metadata(t *testing.T) {
	sm := NewSessionStateMachine("sess_meta", "tenant_006")
	
	// 设置元数据
	sm.SetMetadata("user_id", "user_123")
	sm.SetMetadata("request_count", 5)
	
	// 获取元数据
	userID, ok := sm.GetMetadata("user_id")
	if !ok || userID != "user_123" {
		t.Errorf("expected user_id to be 'user_123', got %v", userID)
	}
	
	count, ok := sm.GetMetadata("request_count")
	if !ok || count != 5 {
		t.Errorf("expected request_count to be 5, got %v", count)
	}
	
	// 不存在的键
	_, ok = sm.GetMetadata("nonexistent")
	if ok {
		t.Error("expected nonexistent key to return false")
	}
}

func TestSessionStateMachine_Metrics(t *testing.T) {
	sm := NewSessionStateMachine("sess_metrics", "tenant_007")
	
	// 执行一系列转换
	_ = sm.Transition(context.Background(), EventAuthenticated, "auth", nil)
	time.Sleep(10 * time.Millisecond)
	_ = sm.Transition(context.Background(), EventCacheMiss, "miss", nil)
	time.Sleep(10 * time.Millisecond)
	_ = sm.Transition(context.Background(), EventToolRequired, "tool", nil)
	
	metrics := sm.GetMetrics()
	
	// 检查转换计数
	if metrics.TransitionCount != 3 {
		t.Errorf("expected 3 transitions, got %d", metrics.TransitionCount)
	}
	
	// 检查状态持续时间（应该大于0）
	if metrics.StateDurations[StateInitialized] == 0 {
		t.Error("expected non-zero duration for Initialized state")
	}
	
	if metrics.StateDurations[StateActive] == 0 {
		t.Error("expected non-zero duration for Active state")
	}
}

func TestSessionStateMachine_Snapshot(t *testing.T) {
	sm := NewSessionStateMachine("sess_snapshot", "tenant_008")
	
	// 执行一些转换
	_ = sm.Transition(context.Background(), EventAuthenticated, "auth", nil)
	sm.SetMetadata("key1", "value1")
	
	// 获取快照
	snapshot := sm.GetSnapshot()
	
	if snapshot.SessionID != "sess_snapshot" {
		t.Errorf("expected session_id to be 'sess_snapshot', got %s", snapshot.SessionID)
	}
	
	if snapshot.CurrentState != StateActive {
		t.Errorf("expected current state to be Active, got %s", snapshot.CurrentState)
	}
	
	if len(snapshot.History) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(snapshot.History))
	}
	
	if snapshot.Metadata["key1"] != "value1" {
		t.Error("expected metadata to be included in snapshot")
	}
}

func TestSessionStateMachine_CustomTransitionCondition(t *testing.T) {
	sm := NewSessionStateMachine("sess_condition", "tenant_009")
	
	// 注册一个带条件的转换
	sm.RegisterTransition(SessionTransition{
		From:  StateActive,
		To:    StateSuspended,
		Event: EventHighRiskDetected,
		Condition: func(ctx *TransitionContext) bool {
			// 只有当风险评分 > 8 时才允许挂起
			score, ok := ctx.Metadata["risk_score"].(int)
			return ok && score > 8
		},
	})
	
	_ = sm.Transition(context.Background(), EventAuthenticated, "auth", nil)
	
	// 风险评分低，不应该挂起
	err := sm.Transition(context.Background(), EventHighRiskDetected, "low risk", map[string]any{
		"risk_score": 5,
	})
	if err == nil {
		t.Error("expected transition to fail with low risk score")
	}
	
	if sm.GetState() != StateActive {
		t.Error("state should remain Active when condition not satisfied")
	}
	
	// 风险评分高，应该挂起
	err = sm.Transition(context.Background(), EventHighRiskDetected, "high risk", map[string]any{
		"risk_score": 9,
	})
	if err != nil {
		t.Errorf("transition should succeed with high risk score: %v", err)
	}
	
	if sm.GetState() != StateSuspended {
		t.Errorf("expected state to be Suspended, got %s", sm.GetState())
	}
}

func TestSessionStateMachine_CustomTransitionAction(t *testing.T) {
	sm := NewSessionStateMachine("sess_action", "tenant_010")
	
	actionExecuted := false
	
	// 注册一个带动作的转换
	sm.RegisterTransition(SessionTransition{
		From:  StateActive,
		To:    StateCompleted,
		Event: EventCompleted,
		Action: func(ctx *TransitionContext) error {
			actionExecuted = true
			return nil
		},
	})
	
	_ = sm.Transition(context.Background(), EventAuthenticated, "auth", nil)
	_ = sm.Transition(context.Background(), EventCompleted, "done", nil)
	
	if !actionExecuted {
		t.Error("expected transition action to be executed")
	}
}

func TestSessionStateMachine_PhaseTransition(t *testing.T) {
	sm := NewSessionStateMachine("sess_phase", "tenant_011")
	
	// 初始阶段应该是 PhaseUnknown
	if sm.GetPhase() != PhaseUnknown {
		t.Errorf("expected initial phase to be Unknown, got %s", sm.GetPhase())
	}
	
	// Initialized → Active 应该更新阶段为 PhaseQA
	_ = sm.Transition(context.Background(), EventAuthenticated, "auth", nil)
	
	if sm.GetPhase() != PhaseQA {
		t.Errorf("expected phase to be QA, got %s", sm.GetPhase())
	}
	
	// 手动设置阶段
	sm.SetPhase(PhasePreKnowledge)
	if sm.GetPhase() != PhasePreKnowledge {
		t.Errorf("expected phase to be PreKnowledge, got %s", sm.GetPhase())
	}
	
	// 转换到错误状态应该更新阶段为 PhaseException
	_ = sm.Transition(context.Background(), EventError, "error", nil)
	
	if sm.GetPhase() != PhaseException {
		t.Errorf("expected phase to be Exception, got %s", sm.GetPhase())
	}
}

func TestSessionStateMachine_ConcurrentAccess(t *testing.T) {
	sm := NewSessionStateMachine("sess_concurrent", "tenant_012")
	_ = sm.Transition(context.Background(), EventAuthenticated, "auth", nil)
	
	// 并发读取状态
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = sm.GetState()
				_ = sm.GetPhase()
				_ = sm.GetHistory()
				_ = sm.GetMetrics()
			}
			done <- true
		}()
	}
	
	// 等待所有goroutine完成
	for i := 0; i < 10; i++ {
		<-done
	}
}
