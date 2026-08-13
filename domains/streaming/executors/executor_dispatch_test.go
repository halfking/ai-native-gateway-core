package executors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestDispatchErrMapping locks the contract that dispatch pipeline errors are
// wrapped into *ExecuteError so the handler's Exhausted branch emits 503 (not
// 502) and goal-retry recognises the kind.
func TestDispatchErrMapping(t *testing.T) {
	cases := []struct {
		name    string
		in      error
		wantExh bool
		wantKnd errorsx.ErrorKind
	}{
		{"no-route", dispatch.ErrNoRoute, true, errorsx.KindConcurrent},
		{"overflow", dispatch.ErrOverflow, true, errorsx.KindConcurrent},
		{"deadline", context.DeadlineExceeded, true, errorsx.KindTimeout},
		{"forward-sentinel", errDispatchCircuitOpen, true, errorsx.KindTransient},
		{"generic", errors.New("boom"), true, errorsx.KindTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ee := dispatchErrToExecuteError(c.in)
			if !ee.Exhausted {
				t.Fatalf("Exhausted must be true for %q", c.name)
			}
			if ee.LastKind != c.wantKnd {
				t.Fatalf("kind = %q, want %q", ee.LastKind, c.wantKnd)
			}
			if !errors.Is(ee.LastErr, c.in) {
				t.Fatalf("LastErr does not wrap input: got %v", ee.LastErr)
			}
		})
	}
}

func TestCopyQueueTimestampsToError(t *testing.T) {
	qr := dispatch.NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	t1 := time.Now().Add(-100 * time.Millisecond)
	t6 := time.Now().Add(-10 * time.Millisecond)
	t9 := time.Now()
	qr.T1_TotalEnqueuedAt = &t1
	qr.T6_CredDequeuedAt = &t6
	qr.T9_ResponseEndAt = &t9

	ee := dispatchErrToExecuteError(dispatch.ErrNoRoute)
	copyQueueTimestampsToError(ee, qr)
	if ee.T0ArrivedAt == nil {
		t.Fatal("T0ArrivedAt missing")
	}
	if ee.T1TotalEnqueuedAt == nil || !ee.T1TotalEnqueuedAt.Equal(t1) {
		t.Fatalf("T1=%v", ee.T1TotalEnqueuedAt)
	}
	if ee.T6CredDequeuedAt == nil || !ee.T6CredDequeuedAt.Equal(t6) {
		t.Fatalf("T6=%v", ee.T6CredDequeuedAt)
	}
	if ee.T9ResponseEndAt == nil || !ee.T9ResponseEndAt.Equal(t9) {
		t.Fatalf("T9=%v", ee.T9ResponseEndAt)
	}
}
