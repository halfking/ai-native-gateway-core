package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionStateMachine_Transition(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session",
		RequestID: "test_request",
		State:     StateInitial,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}

	// Test successful transition
	err := sm.Transition(context.Background(), sc, StateReceivingFromClient, "test_transition")
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if sc.State != StateReceivingFromClient {
		t.Errorf("Expected state %s, got %s", StateReceivingFromClient, sc.State)
	}

	if len(sc.Transitions) != 1 {
		t.Errorf("Expected 1 transition, got %d", len(sc.Transitions))
	}

	transition := sc.Transitions[0]
	if transition.From != StateInitial {
		t.Errorf("Expected from state %s, got %s", StateInitial, transition.From)
	}
	if transition.To != StateReceivingFromClient {
		t.Errorf("Expected to state %s, got %s", StateReceivingFromClient, transition.To)
	}
	if transition.Reason != "test_transition" {
		t.Errorf("Expected reason 'test_transition', got '%s'", transition.Reason)
	}
}

func TestSessionStateMachine_Callbacks(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session",
		State:     StateInitial,
		Metadata:  make(map[string]any),
	}

	callbackInvoked := false
	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		callbackInvoked = true
		sc.SetMetadata("callback_executed", true)
		return nil
	})

	err := sm.Transition(context.Background(), sc, StateReceivingFromClient, "test")
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if !callbackInvoked {
		t.Error("Callback was not invoked")
	}

	if val, ok := sc.GetMetadata("callback_executed"); !ok || val != true {
		t.Error("Callback did not set metadata correctly")
	}
}

func TestSessionStateMachine_CallbackError(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session",
		State:     StateInitial,
		Metadata:  make(map[string]any),
	}

	expectedErr := errors.New("callback error")
	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		return expectedErr
	})

	err := sm.Transition(context.Background(), sc, StateReceivingFromClient, "test")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("Expected error to contain %v, got %v", expectedErr, err)
	}

	// State should still be updated even if callback fails
	if sc.State != StateReceivingFromClient {
		t.Errorf("Expected state to be updated despite callback error")
	}
}

func TestSessionStateMachine_MultipleCallbacks(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session",
		State:     StateInitial,
		Metadata:  make(map[string]any),
	}

	callOrder := []int{}

	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		callOrder = append(callOrder, 1)
		return nil
	})

	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		callOrder = append(callOrder, 2)
		return nil
	})

	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		callOrder = append(callOrder, 3)
		return nil
	})

	err := sm.Transition(context.Background(), sc, StateReceivingFromClient, "test")
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(callOrder) != 3 {
		t.Fatalf("Expected 3 callbacks, got %d", len(callOrder))
	}

	for i, v := range callOrder {
		if v != i+1 {
			t.Errorf("Callback %d was called out of order", i)
		}
	}
}

func TestSessionStateMachine_NilContext(t *testing.T) {
	sm := NewStateMachine()

	err := sm.Transition(context.Background(), nil, StateReceivingFromClient, "test")
	if err == nil {
		t.Error("Expected error when SessionContext is nil")
	}
}

func TestSessionStateMachine_GetCallbackCount(t *testing.T) {
	sm := NewStateMachine()

	if count := sm.GetCallbackCount(StateReceivingFromClient); count != 0 {
		t.Errorf("Expected 0 callbacks, got %d", count)
	}

	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		return nil
	})

	if count := sm.GetCallbackCount(StateReceivingFromClient); count != 1 {
		t.Errorf("Expected 1 callback, got %d", count)
	}

	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		return nil
	})

	if count := sm.GetCallbackCount(StateReceivingFromClient); count != 2 {
		t.Errorf("Expected 2 callbacks, got %d", count)
	}
}

func TestSessionStateMachine_Clear(t *testing.T) {
	sm := NewStateMachine()

	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		return nil
	})

	if count := sm.GetCallbackCount(StateReceivingFromClient); count != 1 {
		t.Errorf("Expected 1 callback before clear, got %d", count)
	}

	sm.Clear()

	if count := sm.GetCallbackCount(StateReceivingFromClient); count != 0 {
		t.Errorf("Expected 0 callbacks after clear, got %d", count)
	}
}

func TestSessionState_String(t *testing.T) {
	tests := []struct {
		state    SessionState
		expected string
	}{
		{StateInitial, "INITIAL"},
		{StateReceivingFromClient, "RECEIVING_FROM_CLIENT"},
		{StatePendingToLLM, "PENDING_TO_LLM"},
		{StatePendingApproval, "PENDING_APPROVAL"},
		{StateApprovalRequested, "APPROVAL_REQUESTED"},
		{StateApprovalApproved, "APPROVAL_APPROVED"},
		{StateApprovalRejected, "APPROVAL_REJECTED"},
		{StateSendingToLLM, "SENDING_TO_LLM"},
		{StateReceivingFromLLM, "RECEIVING_FROM_LLM"},
		{StatePendingToClient, "PENDING_TO_CLIENT"},
		{StateSendingToClient, "SENDING_TO_CLIENT"},
		{StateCompleted, "COMPLETED"},
		{StateError, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("State.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func BenchmarkStateMachine_Transition(b *testing.B) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "bench_session",
		State:     StateInitial,
		Metadata:  make(map[string]any),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc.State = StateInitial
		sm.Transition(context.Background(), sc, StateReceivingFromClient, "bench")
	}
}

func BenchmarkStateMachine_TransitionWithCallbacks(b *testing.B) {
	sm := NewStateMachine()
	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		return nil
	})
	sm.RegisterCallback(StateReceivingFromClient, func(ctx context.Context, sc *SessionContext) error {
		return nil
	})

	sc := &SessionContext{
		SessionID: "bench_session",
		State:     StateInitial,
		Metadata:  make(map[string]any),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc.State = StateInitial
		sm.Transition(context.Background(), sc, StateReceivingFromClient, "bench")
	}
}

// TestApprovalFlow_HappyPath 测试审批流程的正常路径
func TestApprovalFlow_HappyPath(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session_approval",
		RequestID: "test_request_approval",
		State:     StateInitial,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}

	// 模拟正常流程：PENDING_TO_LLM → PENDING_APPROVAL → APPROVAL_REQUESTED → APPROVAL_APPROVED → SENDING_TO_LLM
	steps := []struct {
		toState SessionState
		reason  string
	}{
		{StatePendingToLLM, "request_parsed"},
		{StatePendingApproval, "approval_check_triggered"},
		{StateApprovalRequested, "approval_created"},
		{StateApprovalApproved, "approval_granted"},
		{StateSendingToLLM, "resume_execution"},
	}

	for i, step := range steps {
		err := sm.Transition(context.Background(), sc, step.toState, step.reason)
		if err != nil {
			t.Fatalf("Step %d: Transition to %s failed: %v", i, step.toState, err)
		}

		if sc.State != step.toState {
			t.Errorf("Step %d: Expected state %s, got %s", i, step.toState, sc.State)
		}
	}

	// 验证转换历史
	if len(sc.Transitions) != len(steps) {
		t.Errorf("Expected %d transitions, got %d", len(steps), len(sc.Transitions))
	}

	// 验证最终状态
	if sc.State != StateSendingToLLM {
		t.Errorf("Expected final state %s, got %s", StateSendingToLLM, sc.State)
	}
}

// TestApprovalFlow_RejectionPath 测试审批被拒绝的流程
func TestApprovalFlow_RejectionPath(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session_reject",
		RequestID: "test_request_reject",
		State:     StatePendingToLLM,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}

	// 流程：PENDING_TO_LLM → PENDING_APPROVAL → APPROVAL_REQUESTED → APPROVAL_REJECTED
	err := sm.Transition(context.Background(), sc, StatePendingApproval, "approval_check_triggered")
	if err != nil {
		t.Fatalf("Transition to PENDING_APPROVAL failed: %v", err)
	}

	err = sm.Transition(context.Background(), sc, StateApprovalRequested, "approval_created")
	if err != nil {
		t.Fatalf("Transition to APPROVAL_REQUESTED failed: %v", err)
	}

	err = sm.Transition(context.Background(), sc, StateApprovalRejected, "approval_denied_by_admin")
	if err != nil {
		t.Fatalf("Transition to APPROVAL_REJECTED failed: %v", err)
	}

	if sc.State != StateApprovalRejected {
		t.Errorf("Expected state %s, got %s", StateApprovalRejected, sc.State)
	}

	// 验证转换历史
	if len(sc.Transitions) != 3 {
		t.Errorf("Expected 3 transitions, got %d", len(sc.Transitions))
	}
}

// TestApprovalFlow_WithCallbacks 测试审批流程中的回调执行
func TestApprovalFlow_WithCallbacks(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session_callbacks",
		RequestID: "test_request_callbacks",
		State:     StatePendingToLLM,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}

	callbackExecuted := false
	approvalRequestCreated := false

	// 注册 PENDING_APPROVAL 回调
	sm.RegisterCallback(StatePendingApproval, func(ctx context.Context, sc *SessionContext) error {
		callbackExecuted = true
		sc.SetMetadata("approval_check_time", time.Now())
		return nil
	})

	// 注册 APPROVAL_REQUESTED 回调
	sm.RegisterCallback(StateApprovalRequested, func(ctx context.Context, sc *SessionContext) error {
		approvalRequestCreated = true
		sc.ApprovalRequestID = "approval_req_123"
		sc.ApprovalStatus = "pending"
		return nil
	})

	// 执行转换
	err := sm.Transition(context.Background(), sc, StatePendingApproval, "check_approval")
	if err != nil {
		t.Fatalf("Transition to PENDING_APPROVAL failed: %v", err)
	}

	if !callbackExecuted {
		t.Error("PENDING_APPROVAL callback was not executed")
	}

	err = sm.Transition(context.Background(), sc, StateApprovalRequested, "create_request")
	if err != nil {
		t.Fatalf("Transition to APPROVAL_REQUESTED failed: %v", err)
	}

	if !approvalRequestCreated {
		t.Error("APPROVAL_REQUESTED callback was not executed")
	}

	// 验证回调设置的数据
	if sc.ApprovalRequestID != "approval_req_123" {
		t.Errorf("Expected ApprovalRequestID 'approval_req_123', got '%s'", sc.ApprovalRequestID)
	}

	if sc.ApprovalStatus != "pending" {
		t.Errorf("Expected ApprovalStatus 'pending', got '%s'", sc.ApprovalStatus)
	}

	if _, ok := sc.GetMetadata("approval_check_time"); !ok {
		t.Error("approval_check_time metadata was not set")
	}
}

// TestApprovalFlow_ErrorHandling 测试审批流程中的错误处理
func TestApprovalFlow_ErrorHandling(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session_error",
		RequestID: "test_request_error",
		State:     StatePendingToLLM,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}

	expectedErr := ErrApprovalPending
	sm.RegisterCallback(StatePendingApproval, func(ctx context.Context, sc *SessionContext) error {
		return expectedErr
	})

	err := sm.Transition(context.Background(), sc, StatePendingApproval, "trigger_approval")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("Expected error to be %v, got %v", expectedErr, err)
	}

	// 状态应该已更新，即使回调失败
	if sc.State != StatePendingApproval {
		t.Errorf("Expected state to be %s, got %s", StatePendingApproval, sc.State)
	}
}

// TestApprovalFlow_TimeoutScenario 测试审批超时场景
func TestApprovalFlow_TimeoutScenario(t *testing.T) {
	sm := NewStateMachine()
	sc := &SessionContext{
		SessionID: "test_session_timeout",
		RequestID: "test_request_timeout",
		State:     StateApprovalRequested,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
		ApprovalRequestID: "req_timeout_test",
		ApprovalStatus:    "pending",
	}

	// 注册超时处理回调
	sm.RegisterCallback(StateApprovalRejected, func(ctx context.Context, sc *SessionContext) error {
		if sc.ApprovalStatus == "timeout" {
			sc.Error = ErrApprovalTimeout
		}
		return nil
	})

	// 模拟超时，设置审批状态为 timeout
	sc.ApprovalStatus = "timeout"

	err := sm.Transition(context.Background(), sc, StateApprovalRejected, "approval_timeout")
	if err != nil {
		t.Fatalf("Transition to APPROVAL_REJECTED failed: %v", err)
	}

	if sc.Error != ErrApprovalTimeout {
		t.Errorf("Expected error to be %v, got %v", ErrApprovalTimeout, sc.Error)
	}
}

// TestApprovalFlow_SessionContextFields 测试 SessionContext 的审批字段
func TestApprovalFlow_SessionContextFields(t *testing.T) {
	sc := &SessionContext{
		SessionID: "test_session_fields",
		RequestID: "test_request_fields",
		State:     StateApprovalRequested,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}

	// 设置审批相关字段
	sc.ApprovalRequestID = "approval_req_456"
	sc.ApprovalStatus = "pending"
	sc.ApprovalResult = map[string]any{
		"approver":    "admin@example.com",
		"approved_at": time.Now(),
		"note":        "approved after review",
	}

	// 验证字段设置
	if sc.ApprovalRequestID != "approval_req_456" {
		t.Errorf("Expected ApprovalRequestID 'approval_req_456', got '%s'", sc.ApprovalRequestID)
	}

	if sc.ApprovalStatus != "pending" {
		t.Errorf("Expected ApprovalStatus 'pending', got '%s'", sc.ApprovalStatus)
	}

	if sc.ApprovalResult == nil {
		t.Fatal("ApprovalResult should not be nil")
	}

	if approver, ok := sc.ApprovalResult["approver"].(string); !ok || approver != "admin@example.com" {
		t.Errorf("Expected approver 'admin@example.com', got '%v'", sc.ApprovalResult["approver"])
	}
}
