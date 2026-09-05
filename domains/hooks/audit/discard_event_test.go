package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStreamCapture_MarkDiscarded_PopulatesDiscardEvents exercises the
// 2026-08-19 StreamCapture.MarkDiscarded hook end-to-end:
//
//   - one MarkDiscarded call records one event;
//   - a second call appends (does not replace);
//   - the resulting slice flows into SummaryAsMap() under the
//     "discard_events" key so the audit upsert path picks it up;
//   - the entry is JSON-marshalable to the audit envelope without
//     requiring custom marshalers (operator-facing JSONB column is just
//     `[]DiscardEvent` JSON-encoded).
func TestStreamCapture_MarkDiscarded_PopulatesDiscardEvents(t *testing.T) {
	c := NewStreamCapture()

	idx := c.MarkDiscarded(DiscardEvent{
		Reason:         "survival_attempt_discarded",
		BufferBytes:    4096,
		HoldbackHeld:   7,
		State:          "metadata",
		AttemptNumber:  1,
		ProviderID:     14,
		RawModel:       "minimax-m3",
		DecisionAction: "retry_now",
		DecisionReason: "recoverable_candidate",
	})
	if idx != 0 {
		t.Fatalf("first MarkDiscarded returned idx=%d want 0", idx)
	}

	idx2 := c.MarkDiscarded(DiscardEvent{
		Reason:        "stream_recovery_discard_and_replay",
		BufferBytes:   0,
		HoldbackHeld:  2,
		State:         "none",
		AttemptNumber: 2,
		ProviderID:    12763,
		RawModel:      "glm-5.2",
	})
	if idx2 != 1 {
		t.Fatalf("second MarkDiscarded returned idx=%d want 1", idx2)
	}

	cp := c.DiscardEventsCopy()
	if len(cp) != 2 {
		t.Fatalf("DiscardEventsCopy len=%d want 2", len(cp))
	}
	if cp[0].Reason != "survival_attempt_discarded" || cp[0].BufferBytes != 4096 ||
		cp[0].HoldbackHeld != 7 || cp[0].RawModel != "minimax-m3" {
		t.Errorf("first event mismatch: %+v", cp[0])
	}
	if cp[1].ProviderID != 12763 || cp[1].RawModel != "glm-5.2" || cp[1].DecisionAction != "" {
		t.Errorf("second event mismatch: %+v", cp[1])
	}

	summary := c.SummaryAsMap()
	raw, ok := summary["discard_events"]
	if !ok {
		t.Fatalf("SummaryAsMap missing discard_events key; got keys=%v", keysOf(summary))
	}
	// The JSON marshal round-trip exercises the same path the audit upsert
	// path uses; this is the regression test for the audit → DB column
	// contract.
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal discard_events: %v", err)
	}
	if !strings.Contains(string(encoded), "survival_attempt_discarded") ||
		!strings.Contains(string(encoded), "minimax-m3") {
		t.Fatalf("encoded discard_events missing expected fields: %s", encoded)
	}
}

// TestStreamCapture_MarkDiscarded_NilReceiver mirrors the nil-safety
// pattern used by RecordChunkSent / MarkInterrupted: optional capture
// pointers must not panic the recovery loop.
func TestStreamCapture_MarkDiscarded_NilReceiver(t *testing.T) {
	var c *StreamCapture
	if idx := c.MarkDiscarded(DiscardEvent{}); idx != -1 {
		t.Errorf("nil receiver returned idx=%d want -1", idx)
	}
	if cp := c.DiscardEventsCopy(); cp != nil {
		t.Errorf("nil receiver DiscardEventsCopy=%v want nil", cp)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
