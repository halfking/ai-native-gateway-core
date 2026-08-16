package executors

import (
	"context"
	"testing"
	"time"
)

type stateWriteContextKey struct{}

func TestStateWriteContextDetachesCanceledParent(t *testing.T) {
	parent := context.WithValue(context.Background(), stateWriteContextKey{}, "request-value")
	parent, cancelParent := context.WithCancel(parent)
	cancelParent()

	ctx, cancel := stateWriteContext(parent)
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("state write context inherited parent cancellation: %v", err)
	}
	if got := ctx.Value(stateWriteContextKey{}); got != "request-value" {
		t.Fatalf("state write context value = %v, want request-value", got)
	}
}

func TestStateWriteContextHasBoundedDeadline(t *testing.T) {
	started := time.Now()
	ctx, cancel := stateWriteContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("state write context has no deadline")
	}
	remaining := deadline.Sub(started)
	if remaining <= 0 || remaining > executionStateWriteTimeout+time.Second {
		t.Fatalf("state write context deadline = %s, want within %s", remaining, executionStateWriteTimeout)
	}
}

func TestExecutionRecorderWithoutWriterIsNoop(t *testing.T) {
	recorder := &ExecutionRecorderImpl{}
	if err := recorder.RecordOutcome(context.Background(), ExecutionOutcome{Success: true}); err != nil {
		t.Fatalf("RecordOutcome without writer returned error: %v", err)
	}
}
