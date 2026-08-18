package stats

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestEventWriterNilSafe(t *testing.T) {
	var w *EventWriter
	// Must not panic on a nil receiver.
	w.Record(nil)
	if d, f, dl := w.Stats(); d != 0 || f != 0 || dl != 0 {
		t.Fatalf("nil writer should report zero counters, got dropped=%d persistFailed=%d deadLettered=%d", d, f, dl)
	}
}

func TestEventWriterRecordsDroppedWhenQueueFull(t *testing.T) {
	// queueSize=1 plus no consumer (Start() not called) means subsequent
	// Record() calls hit the select-default branch in Record() and bump
	// the dropped counter.
	w := NewEventWriter(nil, 1)
	status := telemetry.RequestStatusSuccess
	entry := &telemetry.RequestLogEntry{
		Op: telemetry.RequestLogUpdate, RequestID: "r1", RequestStatus: &status,
	}
	for i := 0; i < 8; i++ {
		w.Record(entry)
	}
	dropped, _, _ := w.Stats()
	if dropped == 0 {
		t.Fatalf("expected dropped > 0 when queue is full, got 0")
	}
}

func TestEventWriterStopOnNeverStarted(t *testing.T) {
	// Stop() must be safe on a writer that was never Start()ed: it should
	// not block forever, since run() never launched and the done channel
	// is never closed.
	w := NewEventWriter(nil, 1)
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-timeoutAfter(t, time.Second):
		t.Fatal("Stop() blocked on a never-started writer")
	}
}

func timeoutAfter(t *testing.T, d time.Duration) <-chan time.Time {
	t.Helper()
	return time.After(d)
}
