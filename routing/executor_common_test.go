package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/limiter"
)

// newCircuitManagerForTest returns a circuit.Manager with no DB /
// external dependencies. circuit.NewManager is in-memory and works in
// tests without further config.
func newCircuitManagerForTest() *circuit.Manager {
	return circuit.NewManager()
}

// newLimiterForTest returns a limiter.Limiter with default 4-layer
// concurrency buckets. The recovery loop runs in a background goroutine;
// tests that don't want it can call lim.Stop().
func newLimiterForTest() *limiter.Limiter {
	return limiter.New()
}

func TestCommonExecutor_RetryOnTransient(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	ce := &CommonExecutor{
		Circuit:              cm,
		Limiter:              lim,
		UpstreamTimeout:      5 * time.Second,
		StreamTimeout:        30 * time.Second,
		StreamRetryThreshold: 5,
		providerID:           1,
		credentialID:         1,
	}
	attempts := 0
	err := ce.RunWithCredential(context.Background(), func(attempt int) error {
		attempts++
		if attempt < 2 {
			return &retryableError{err: errors.New("transient")}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithCredential: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2 transient + 1 success), got %d", attempts)
	}
}

func TestCommonExecutor_NoRetryOnFatal(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	ce := &CommonExecutor{
		Circuit:         cm,
		Limiter:         lim,
		UpstreamTimeout: 5 * time.Second,
		providerID:      1,
		credentialID:    1,
	}
	attempts := 0
	err := ce.RunWithCredential(context.Background(), func(attempt int) error {
		attempts++
		return errors.New("fatal 4xx")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Errorf("fatal should not retry, got %d attempts", attempts)
	}
}
