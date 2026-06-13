package telemetry

import (
	"testing"
	"time"
)

func TestClient_Disabled(t *testing.T) {
	c := NewClient()
	if c.Enabled() {
		t.Fatal("no DB should be disabled")
	}
	c.EmitDecisionLog(&DecisionLogEntry{RequestID: "test"})
	c.EmitRequestLog(&RequestLogEntry{RequestID: "test"})
	c.Stop()
}

func TestClient_QueueFull(t *testing.T) {
	c := NewClient()
	c.queue = make(chan any, 2)

	for i := 0; i < 10; i++ {
		c.EmitDecisionLog(&DecisionLogEntry{
			RequestID: "overflow",
			Model:     "test",
			Success:   true,
		})
	}

	c.Stop()
}

func TestClient_EmitDoesNotBlock(t *testing.T) {
	c := NewClient()
	start := time.Now()
	for i := 0; i < 100; i++ {
		c.EmitDecisionLog(&DecisionLogEntry{RequestID: "bench", Model: "test", Success: true})
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("Emit should not block")
	}
	c.Stop()
}

func TestResolveRequestStatus(t *testing.T) {
	t.Parallel()
	errKind := "timeout"
	cases := []struct {
		name      string
		success   bool
		errorKind *string
		initial   bool
		want      string
	}{
		{name: "success", success: true, want: RequestStatusSuccess},
		{name: "failure", success: false, errorKind: &errKind, want: RequestStatusFailure},
		{name: "initial in progress", success: false, initial: true, want: RequestStatusInProgress},
		{name: "update without error still in progress", success: false, initial: false, want: RequestStatusInProgress},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveRequestStatus(tc.success, tc.errorKind, tc.initial); got != tc.want {
				t.Fatalf("ResolveRequestStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeRequestStatus(t *testing.T) {
	entry := &RequestLogEntry{Op: RequestLogInsert, Success: false}
	normalizeRequestStatus(entry)
	if entry.RequestStatus == nil || *entry.RequestStatus != RequestStatusInProgress {
		t.Fatalf("expected in_progress, got %#v", entry.RequestStatus)
	}
}
