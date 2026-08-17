package executors

import (
	"errors"
	"sync"
	"testing"
)

func TestUpstreamAttemptBudgetCapsConcurrentConsumption(t *testing.T) {
	budget := NewUpstreamAttemptBudget(100)
	var wg sync.WaitGroup
	accepted := make(chan struct{}, 200)

	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if budget.TryConsume() {
				accepted <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(accepted)

	if got := len(accepted); got != 100 {
		t.Fatalf("accepted attempts = %d, want 100", got)
	}
	if got := budget.Used(); got != 100 {
		t.Fatalf("used attempts = %d, want 100", got)
	}
	if !budget.Exhausted() {
		t.Fatal("budget must be exhausted")
	}
}

func TestConsumeUpstreamAttemptReturnsStableLimitError(t *testing.T) {
	params := &ExecParams{UpstreamAttempts: NewUpstreamAttemptBudget(1)}
	if attempt, err := consumeUpstreamAttempt(params); err != nil || attempt != 1 {
		t.Fatalf("first attempt = %d, %v; want 1, nil", attempt, err)
	}
	if _, err := consumeUpstreamAttempt(params); !errors.Is(err, ErrUpstreamAttemptLimit) {
		t.Fatalf("second attempt error = %v, want ErrUpstreamAttemptLimit", err)
	}
}

func TestOpenAIRetryBudgetDoesNotAddHiddenDispatchRetry(t *testing.T) {
	if got := effectiveOpenAIRetryBudget(0, true); got != 0 {
		t.Fatalf("dispatch retry budget = %d, want 0", got)
	}
	if got := effectiveOpenAIRetryBudget(0, false); got != 1 {
		t.Fatalf("legacy retry budget = %d, want 1 model-not-found verification", got)
	}
}
