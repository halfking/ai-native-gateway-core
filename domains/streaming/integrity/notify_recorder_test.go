package integrity

import (
	"context"
	"sync/atomic"
	"testing"
)

type captureRec struct {
	n atomic.Int32
}

func (c *captureRec) Record(_ context.Context, _ Event) error {
	c.n.Add(1)
	return nil
}

func TestCredibilityAutoTriggerWorthy(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want bool
	}{
		{"mismatch_high", Event{AnomalyType: AnomalyModelMismatch, Severity: SeverityHigh}, true},
		{"mismatch_critical", Event{AnomalyType: AnomalyModelMismatch, Severity: SeverityCritical}, true},
		{"drift_high", Event{AnomalyType: AnomalyFingerprintDrift, Severity: SeverityHigh}, true},
		{"mismatch_medium", Event{AnomalyType: AnomalyModelMismatch, Severity: SeverityMedium}, false},
		{"empty_high", Event{AnomalyType: AnomalyEmptyResponse, Severity: SeverityHigh}, false},
		{"refusal_critical", Event{AnomalyType: AnomalyFinishRefusal, Severity: SeverityCritical}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CredibilityAutoTriggerWorthy(tc.ev); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestNotifyRecorder_FiresBeforeInner(t *testing.T) {
	inner := &captureRec{}
	var notified atomic.Int32
	n := &NotifyRecorder{
		Inner: inner,
		Notify: func(ev Event) {
			if ev.AnomalyType != AnomalyModelMismatch {
				t.Errorf("unexpected type %s", ev.AnomalyType)
			}
			notified.Add(1)
		},
	}
	if err := n.Record(context.Background(), Event{
		AnomalyType: AnomalyModelMismatch,
		Severity:    SeverityHigh,
	}); err != nil {
		t.Fatal(err)
	}
	if notified.Load() != 1 {
		t.Fatalf("notify = %d, want 1", notified.Load())
	}
	if inner.n.Load() != 1 {
		t.Fatalf("inner = %d, want 1", inner.n.Load())
	}
}

func TestNotifyRecorder_SkipsNonWorthy(t *testing.T) {
	inner := &captureRec{}
	var notified atomic.Int32
	n := &NotifyRecorder{
		Inner:  inner,
		Notify: func(Event) { notified.Add(1) },
	}
	_ = n.Record(context.Background(), Event{
		AnomalyType: AnomalyEmptyResponse,
		Severity:    SeverityHigh,
	})
	if notified.Load() != 0 {
		t.Fatal("must not notify for empty_response")
	}
	if inner.n.Load() != 1 {
		t.Fatal("inner must still record")
	}
}

func TestNotifyRecorder_NilSafe(t *testing.T) {
	var n *NotifyRecorder
	if err := n.Record(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
	_ = (&NotifyRecorder{}).Record(context.Background(), Event{
		AnomalyType: AnomalyModelMismatch, Severity: SeverityHigh,
	})
}
