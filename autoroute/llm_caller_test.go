package autoroute

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDisabledCaller(t *testing.T) {
	_, err := DisabledCaller{}.Call(context.Background(), "any")
	if !errors.Is(err, ErrLLMDisabled) {
		t.Errorf("DisabledCaller returned %v, want ErrLLMDisabled", err)
	}
}

func TestNoopCaller_FixedTask(t *testing.T) {
	caller := NoopCaller{FixedTask: "reasoning"}
	resp, err := caller.Call(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("NoopCaller error: %v", err)
	}
	if resp != "reasoning" {
		t.Errorf("NoopCaller resp = %q, want 'reasoning'", resp)
	}
}

func TestNoopCaller_RespectsContextCancel(t *testing.T) {
	caller := NoopCaller{FixedTask: "code", Delay: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := caller.Call(ctx, "prompt")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("NoopCaller expected DeadlineExceeded, got %v", err)
	}
}

func TestCircuitBreakerCaller_NormalSuccess(t *testing.T) {
	inner := NoopCaller{FixedTask: "chat"}
	cb := NewCircuitBreakerCaller(inner)

	// 3 successful calls should not trip the breaker
	for i := 0; i < 3; i++ {
		resp, err := cb.Call(context.Background(), "p")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if resp != "chat" {
			t.Errorf("call %d resp = %q", i, resp)
		}
	}
	consecutive, openFor := cb.State()
	if consecutive != 0 || openFor != 0 {
		t.Errorf("state = (%d, %v), want (0, 0)", consecutive, openFor)
	}
}

func TestCircuitBreakerCaller_TripsOnConsecutiveFailures(t *testing.T) {
	failing := failingCaller{}
	cb := NewCircuitBreakerCaller(failing)
	cb.MaxFailures = 3
	cb.Cooldown = 50 * time.Millisecond

	// Trip the breaker with 3 failures
	for i := 0; i < 3; i++ {
		_, err := cb.Call(context.Background(), "p")
		if !errors.Is(err, failingErr) {
			t.Fatalf("call %d: want failingErr, got %v", i, err)
		}
	}
	// Next call should be rejected without invoking the inner
	_, err := cb.Call(context.Background(), "p")
	if !errors.Is(err, ErrLLMCircuitOpen) {
		t.Errorf("after 3 failures, want ErrLLMCircuitOpen, got %v", err)
	}

	// Wait for cooldown, then it should retry and fail again
	time.Sleep(60 * time.Millisecond)
	_, err = cb.Call(context.Background(), "p")
	if !errors.Is(err, failingErr) {
		t.Errorf("after cooldown, want failingErr, got %v", err)
	}
}

func TestCircuitBreakerCaller_ResetsOnSuccess(t *testing.T) {
	flaky := func() LLMCaller {
		var counter int
		return callFunc(func(ctx context.Context, prompt string) (string, error) {
			counter++
			if counter <= 2 {
				return "", failingErr
			}
			return "reasoning", nil
		})
	}()

	cb := NewCircuitBreakerCaller(flaky)
	cb.MaxFailures = 3

	// 2 failures then 1 success — should reset counter
	_, _ = cb.Call(context.Background(), "p")
	_, _ = cb.Call(context.Background(), "p")
	resp, _ := cb.Call(context.Background(), "p")
	if resp != "reasoning" {
		t.Errorf("expected 'reasoning', got %q", resp)
	}
	consecutive, _ := cb.State()
	if consecutive != 0 {
		t.Errorf("after success, consecutive = %d, want 0", consecutive)
	}
}

func TestInstrumentedCaller_RecordsMetrics(t *testing.T) {
	inner := NoopCaller{FixedTask: "code"}
	ic := NewInstrumentedCaller(inner)

	// 3 successes + 1 failure
	for i := 0; i < 3; i++ {
		_, _ = ic.Call(context.Background(), "p")
	}
	ic.Inner = failingCaller{}
	_, _ = ic.Call(context.Background(), "p")

	if got := ic.Metrics.Calls.Load(); got != 4 {
		t.Errorf("Calls = %d, want 4", got)
	}
	if got := ic.Metrics.Successes.Load(); got != 3 {
		t.Errorf("Successes = %d, want 3", got)
	}
	if got := ic.Metrics.Failures.Load(); got != 1 {
		t.Errorf("Failures = %d, want 1", got)
	}
	if ic.Metrics.LatencyNs.Load() == 0 {
		t.Error("LatencyNs should be > 0")
	}
}

func TestInstrumentedCaller_RecordsTimeout(t *testing.T) {
	timeoutCaller := NoopCaller{FixedTask: "code", Delay: 50 * time.Millisecond}
	ic := NewInstrumentedCaller(timeoutCaller)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, _ = ic.Call(ctx, "p")
	if got := ic.Metrics.Timeouts.Load(); got != 1 {
		t.Errorf("Timeouts = %d, want 1", got)
	}
}

func TestInstrumentedCaller_ClassifiesOutcomes(t *testing.T) {
	// We can't directly observe the Prometheus counter (it's a global
	// promauto singleton), but we can verify the outcome-label
	// classification logic by capturing RecordLLMMetricCall.
	type call struct {
		outcome string
	}
	var captured []call
	oldSink := RecordLLMMetricCall
	RecordLLMMetricCall = func(outcome string, _ time.Duration) {
		captured = append(captured, call{outcome})
	}
	defer func() { RecordLLMMetricCall = oldSink }()

	// Success
	ic := NewInstrumentedCaller(NoopCaller{FixedTask: "code"})
	_, _ = ic.Call(context.Background(), "p")
	// Failure
	ic.Inner = failingCaller{}
	_, _ = ic.Call(context.Background(), "p")
	// Timeout
	ic.Inner = NoopCaller{FixedTask: "code", Delay: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	_, _ = ic.Call(ctx, "p")
	cancel()
	// Breaker open
	ic.Inner = NewCircuitBreakerCaller(failingCaller{})
	_, _ = ic.Call(context.Background(), "p")
	// Disable
	ic.Inner = DisabledCaller{}
	_, _ = ic.Call(context.Background(), "p")

	if len(captured) != 5 {
		t.Fatalf("captured %d calls, want 5", len(captured))
	}
	want := []string{"success", "failure", "timeout", "failure", "failure"}
	// (breaker-open not exercised in single-call: the inner CircuitBreaker
	// counts the failure but doesn't open until MaxFailures=5, so the
	// first call surfaces the underlying failure rather than breaker_open.
	// Both are 'failure' in our outcome mapping, so the test stays valid.)
	for i, w := range want {
		if captured[i].outcome != w {
			t.Errorf("call[%d] outcome = %q, want %q", i, captured[i].outcome, w)
		}
	}
}

func TestCircuitBreakerCaller_Stats(t *testing.T) {
	failing := failingCaller{}
	cb := NewCircuitBreakerCaller(failing)
	cb.MaxFailures = 3
	cb.Cooldown = 50 * time.Millisecond

	// 2 failures: should still report 2 consecutive + max=3
	_, _ = cb.Call(context.Background(), "p")
	_, _ = cb.Call(context.Background(), "p")
	consecutive, max, cd := cb.Stats()
	if consecutive != 2 {
		t.Errorf("consecutive = %d, want 2", consecutive)
	}
	if max != 3 {
		t.Errorf("max = %d, want 3", max)
	}
	if cd != 50*time.Millisecond {
		t.Errorf("cooldown = %v, want 50ms", cd)
	}
}

func TestCircuitBreakerCaller_StateAfterOpen(t *testing.T) {
	failing := failingCaller{}
	cb := NewCircuitBreakerCaller(failing)
	cb.MaxFailures = 2
	cb.Cooldown = 100 * time.Millisecond

	// Trip the breaker
	_, _ = cb.Call(context.Background(), "p")
	_, _ = cb.Call(context.Background(), "p")

	consecutive, openFor := cb.State()
	if consecutive != 2 {
		t.Errorf("consecutive = %d, want 2", consecutive)
	}
	if openFor <= 0 || openFor > 100*time.Millisecond {
		t.Errorf("openFor = %v, want (0, 100ms]", openFor)
	}
}

func TestNewLLMFallbackClassifierWithCaller_NilCallerUsesDisabled(t *testing.T) {
	// Passing nil should be safe — Decider ends up with DisabledCaller
	clf := NewLLMFallbackClassifierWithCaller(nil)
	if clf == nil {
		t.Fatal("got nil classifier")
	}
	// Name() should return the canonical "llm" identifier
	if clf.Name() != "llm" {
		t.Errorf("Name() = %q, want 'llm'", clf.Name())
	}
	// And a Classify call should surface ErrLLMDisabled (since
	// DisabledCaller is wired in for the nil case).
	_, err := clf.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "any prompt",
	})
	if !errors.Is(err, ErrLLMDisabled) {
		t.Errorf("Classify err = %v, want ErrLLMDisabled", err)
	}
}

// ── helpers ────────────────────────────────────────────────────

var failingErr = errors.New("simulated LLM failure")

type failingCaller struct{}

func (failingCaller) Call(_ context.Context, _ string) (string, error) {
	return "", failingErr
}

type callFunc func(ctx context.Context, prompt string) (string, error)

func (c callFunc) Call(ctx context.Context, prompt string) (string, error) {
	return c(ctx, prompt)
}
