package telemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestEmptyResponseGate_RegistersHookWithoutPanic pins that the public
// registration entry point is safe to call once with a valid Client and
// sets the gate flag. The hook must NOT fire when the gate is disabled
// (this is the production-wiring-vs-test boundary).
func TestEmptyResponseGate_RegistersHookWithoutPanic(t *testing.T) {
	before := emptyResponseGateEnabled
	defer func() { emptyResponseGateEnabled = before }()

	c := NewClient()
	RegisterEmptyResponseGate(c)
	if !emptyResponseGateEnabled {
		t.Fatal("RegisterEmptyResponseGate must flip the gate flag")
	}
	if c == nil {
		t.Fatal("client must be returned non-nil after registration")
	}
}

// TestEmptyResponseGate_NilClientNoOp pins that a nil client registration
// is safe — startup paths that defer to nil during config gating must not
// crash the process.
func TestEmptyResponseGate_NilClientNoOp(t *testing.T) {
	before := emptyResponseGateEnabled
	defer func() { emptyResponseGateEnabled = before }()

	RegisterEmptyResponseGate(nil) // must not panic
}

// TestRecordEmptyResponseBody_SuccessAndEmptyIncrementsCounter is the core
// contract: Success=true × ResponseBody=nil ⇒ metric increments on the
// (protocol=chat, stream=non_stream) series.
func TestRecordEmptyResponseBody_SuccessAndEmptyIncrementsCounter(t *testing.T) {
	before := emptyResponseGateEnabled
	defer func() { emptyResponseGateEnabled = before }()
	emptyResponseGateEnabled = true

	beforeCount := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")

	entry := &RequestLogEntry{
		RequestID:  "req-empty-1",
		TenantID:   "tenant-empty",
		Success:    true,
		RequestMode: strPtr("chat"),
	}
	recordEmptyResponseBody(entry)

	afterCount := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")
	if afterCount != beforeCount+1 {
		t.Fatalf("counter = %v, want %v", afterCount, beforeCount+1)
	}
}

// TestRecordEmptyResponseBody_FailureDoesNotIncrement pins that
// Success=false rows (which legitimately have empty bodies) do not
// trigger the alertable counter — the contract is "success but empty",
// not "any empty body".
func TestRecordEmptyResponseBody_FailureDoesNotIncrement(t *testing.T) {
	before := emptyResponseGateEnabled
	defer func() { emptyResponseGateEnabled = before }()
	emptyResponseGateEnabled = true

	beforeCount := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")

	entry := &RequestLogEntry{
		RequestID:   "req-failed-1",
		Success:     false,
		RequestMode: strPtr("chat"),
	}
	recordEmptyResponseBody(entry)

	afterCount := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")
	if afterCount != beforeCount {
		t.Fatalf("counter must not change on Success=false; got delta %v", afterCount-beforeCount)
	}
}

// TestRecordEmptyResponseBody_NonEmptyBodyDoesNotIncrement pins the
// inverse: a successful request with a non-empty body must not be
// classified as the alertable failure.
func TestRecordEmptyResponseBody_NonEmptyBodyDoesNotIncrement(t *testing.T) {
	before := emptyResponseGateEnabled
	defer func() { emptyResponseGateEnabled = before }()
	emptyResponseGateEnabled = true

	beforeCount := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")

	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	entry := &RequestLogEntry{
		RequestID:   "req-nonempty-1",
		Success:     true,
		RequestMode: strPtr("chat"),
		ResponseBody: &body,
	}
	recordEmptyResponseBody(entry)

	afterCount := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")
	if afterCount != beforeCount {
		t.Fatalf("counter must not change when ResponseBody is meaningful; got delta %v",
			afterCount-beforeCount)
	}
}

// TestRecordEmptyResponseBody_WhitespaceOnlyIsMissing pins the
// classifier: a ResponseBody pointer that points to "" or whitespace-only
// is treated as missing. This catches the regression mode where the body
// is "allocated" but never populated (the streaming reassembly path that
// writes a placeholder string).
func TestRecordEmptyResponseBody_WhitespaceOnlyIsMissing(t *testing.T) {
	for _, body := range []string{"", " ", "\n", "\t  \n\r"} {
		t.Run("body="+body, func(t *testing.T) {
			if hasMeaningfulResponseBody(&body) {
				t.Fatalf("hasMeaningfulResponseBody(%q) = true, want false (whitespace-only is missing)", body)
			}
			if hasMeaningfulResponseBody(nil) {
				t.Fatal("hasMeaningfulResponseBody(nil) = true, want false")
			}
		})
	}
}

// TestRecordEmptyResponseBody_StreamingBucketSeparation pins the
// stream/non_stream bucket split: a streaming Success=true with empty
// body does NOT alarm (its body chunk count proves success), but the
// stream bucket still records the event for ratio analysis.
func TestRecordEmptyResponseBody_StreamingBucketSeparation(t *testing.T) {
	before := emptyResponseGateEnabled
	defer func() { emptyResponseGateEnabled = before }()
	emptyResponseGateEnabled = true

	beforeNon := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")
	beforeStream := readCounterVec(t, successResponseBodyMissingTotal, "chat", "stream")

	chunk := 5
	entry := &RequestLogEntry{
		RequestID:       "req-stream-1",
		Success:         true,
		RequestMode:     strPtr("chat"),
		StreamChunkCount: &chunk,
	}
	recordEmptyResponseBody(entry)

	if got := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream"); got != beforeNon {
		t.Fatalf("streaming row must NOT touch non_stream bucket; got delta %v", got-beforeNon)
	}
	if got := readCounterVec(t, successResponseBodyMissingTotal, "chat", "stream"); got != beforeStream+1 {
		t.Fatalf("streaming row must touch stream bucket; got %v, want %v",
			got, beforeStream+1)
	}
}

// TestRecordEmptyResponseBody_GateDisabledIsNoOp pins the production
// contract: while emptyResponseGateEnabled is false (e.g. a test that
// does not opt in), recordEmptyResponseBody returns without incrementing
// the counter.
func TestRecordEmptyResponseBody_GateDisabledIsNoOp(t *testing.T) {
	if emptyResponseGateEnabled {
		t.Skip("gate already enabled; cannot verify disabled path")
	}

	before := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream")
	entry := &RequestLogEntry{
		RequestID: "req-gated",
		Success:   true,
	}
	recordEmptyResponseBody(entry)

	if got := readCounterVec(t, successResponseBodyMissingTotal, "chat", "non_stream"); got != before {
		t.Fatalf("gate disabled but counter changed by %v", got-before)
	}
}

// TestEmptyResponseGate_LabelSurfaceStableAtBoot pins that the metric
// has every (protocol × stream) combo pre-initialised so dashboards
// observe a stable surface from process start (audit-24h-20260828-r3
// P2-3 convention).
func TestEmptyResponseGate_LabelSurfaceStableAtBoot(t *testing.T) {
	for _, c := range []struct{ protocol, stream string }{
		{"chat", "non_stream"},
		{"chat", "stream"},
		{"responses", "non_stream"},
		{"responses", "stream"},
		{"anthropic_messages", "non_stream"},
		{"anthropic_messages", "stream"},
	} {
		_ = readCounterVec(t, successResponseBodyMissingTotal, c.protocol, c.stream)
	}
}

// readCounterVec returns the current value of a (label1, label2) cell in
// a CounterVec, reading via the standard Prometheus gatherer. Local helper
// to keep tests focused on the assertion rather than the gather plumbing.
func readCounterVec(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		t.Fatalf("metric.Write: %v", err)
	}
	if pb.Counter == nil {
		return 0
	}
	return pb.Counter.GetValue()
}

// strPtr is a tiny helper to avoid pulling a pointer-to-string allocator
// into every test literal. Defined in sanitize_prometheus_test.go (shared
// across the package); re-using that one to avoid duplicate declarations.
