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
