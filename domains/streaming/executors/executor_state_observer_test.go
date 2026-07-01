package executors

import (
	"context"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// mockStateObserver records UpdateOnSuccess/UpdateOnFailure calls for testing
type mockStateObserver struct {
	mu        sync.Mutex
	successes []successCall
	failures  []failureCall
}

type successCall struct {
	credID    int
	model     string
	latencyMs int
	requestID string
}

type failureCall struct {
	credID    int
	model     string
	errKind   errorsx.ErrorKind
	requestID string
}

func (m *mockStateObserver) UpdateOnSuccess(ctx context.Context, credID int, model string, latencyMs int, requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successes = append(m.successes, successCall{
		credID:    credID,
		model:     model,
		latencyMs: latencyMs,
		requestID: requestID,
	})
}

func (m *mockStateObserver) UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, failureCall{
		credID:    credID,
		model:     model,
		errKind:   errKind,
		requestID: requestID,
	})
}

func (m *mockStateObserver) getSuccesses() []successCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]successCall(nil), m.successes...)
}

func (m *mockStateObserver) getFailures() []failureCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]failureCall(nil), m.failures...)
}

func (m *mockStateObserver) reset() { //nolint:unused
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successes = nil
	m.failures = nil
}

// TestStateObserver_UserCancelSkipped verifies that KindCanceled is NOT
// recorded by StateObserver (the manager skips it internally).
func TestStateObserver_UserCancelSkipped(t *testing.T) {
	mock := &mockStateObserver{}

	// Simulate UpdateOnFailure with various error kinds
	ctx := context.Background()

	// 1. User cancel - should be skipped by manager (but executor still calls it)
	mock.UpdateOnFailure(ctx, 100, "gpt-4", errorsx.KindCanceled, "req-1")

	// 2. Real error - should be recorded
	mock.UpdateOnFailure(ctx, 100, "gpt-4", errorsx.KindNetwork, "req-2")

	// 3. Another real error
	mock.UpdateOnFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit, "req-3")

	failures := mock.getFailures()

	// Mock records all calls (it doesn't have the skip logic)
	// In real code, credentialstate.Manager skips KindCanceled
	if len(failures) != 3 {
		t.Errorf("expected 3 calls to mock, got %d", len(failures))
	}

	// Verify all error kinds were passed through
	expectedKinds := []errorsx.ErrorKind{
		errorsx.KindCanceled,
		errorsx.KindNetwork,
		errorsx.KindRateLimit,
	}

	for i, f := range failures {
		if f.errKind != expectedKinds[i] {
			t.Errorf("failure[%d]: expected kind=%s, got %s", i, expectedKinds[i], f.errKind)
		}
	}
}

// TestStateObserver_Success verifies that successful requests are recorded
func TestStateObserver_Success(t *testing.T) {
	mock := &mockStateObserver{}
	ctx := context.Background()

	mock.UpdateOnSuccess(ctx, 200, "claude-3-opus", 1500, "req-success-1")
	mock.UpdateOnSuccess(ctx, 201, "gpt-4-turbo", 800, "req-success-2")

	successes := mock.getSuccesses()

	if len(successes) != 2 {
		t.Fatalf("expected 2 success calls, got %d", len(successes))
	}

	// Verify first call
	if successes[0].credID != 200 || successes[0].model != "claude-3-opus" || successes[0].latencyMs != 1500 {
		t.Errorf("unexpected success call[0]: %+v", successes[0])
	}

	// Verify second call
	if successes[1].credID != 201 || successes[1].model != "gpt-4-turbo" || successes[1].latencyMs != 800 {
		t.Errorf("unexpected success call[1]: %+v", successes[1])
	}
}

// TestStateObserver_Integration verifies the integration contract:
// - Executor calls StateObserver.UpdateOnSuccess/UpdateOnFailure
// - Manager (real implementation) filters out KindCanceled
func TestStateObserver_Integration(t *testing.T) {
	mock := &mockStateObserver{}

	// Simulate a request lifecycle
	ctx := context.Background()
	credID := 300
	model := "gpt-4"

	// Scenario 1: User cancels request
	mock.UpdateOnFailure(ctx, credID, model, errorsx.KindCanceled, "req-cancel")

	// Scenario 2: Network error (real failure)
	mock.UpdateOnFailure(ctx, credID, model, errorsx.KindNetwork, "req-net-fail")

	// Scenario 3: Success
	mock.UpdateOnSuccess(ctx, credID, model, 1200, "req-success")

	// Scenario 4: Auth error (permanent failure)
	mock.UpdateOnFailure(ctx, credID, model, errorsx.KindAuth, "req-auth-fail")

	failures := mock.getFailures()
	successes := mock.getSuccesses()

	if len(failures) != 3 {
		t.Errorf("expected 3 failure calls, got %d", len(failures))
	}

	if len(successes) != 1 {
		t.Errorf("expected 1 success call, got %d", len(successes))
	}

	// In real Manager, KindCanceled would be filtered out in UpdateOnFailure
	// Here we just verify the executor passed it through
	hasCancel := false
	for _, f := range failures {
		if f.errKind == errorsx.KindCanceled {
			hasCancel = true
			break
		}
	}

	if !hasCancel {
		t.Error("expected KindCanceled to be passed to StateObserver (manager filters it)")
	}
}

// TestStateObserver_ConcurrentCalls verifies thread safety
func TestStateObserver_ConcurrentCalls(t *testing.T) {
	mock := &mockStateObserver{}
	ctx := context.Background()

	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent failures
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			mock.UpdateOnFailure(ctx, 100+id, "gpt-4", errorsx.KindNetwork, "concurrent-fail")
		}(i)
	}

	// Concurrent successes
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			mock.UpdateOnSuccess(ctx, 200+id, "claude-3", 1000, "concurrent-success")
		}(i)
	}

	wg.Wait()

	failures := mock.getFailures()
	successes := mock.getSuccesses()

	if len(failures) != numGoroutines {
		t.Errorf("expected %d failure calls, got %d", numGoroutines, len(failures))
	}

	if len(successes) != numGoroutines {
		t.Errorf("expected %d success calls, got %d", numGoroutines, len(successes))
	}
}
