package sessionsummary

import (
	"context"
	"testing"
	"time"
)

// TestUpdateHandoffMetrics_NilDB locks in the nil-safety contract: a nil db
// (e.g. when handoff runs without a configured store) must be a silent no-op,
// not a panic. This mirrors the defensive contract the handoff PGStore
// previously had inline.
func TestUpdateHandoffMetrics_NilDB(t *testing.T) {
	err := UpdateHandoffMetrics(context.Background(), nil, &HandoffMetricsSummary{
		SessionKey:        "sess-nil",
		LastHandoffAt:     time.Now(),
		TokensAtTrigger:   100,
		MessagesAtTrigger: 5,
		LastTriggerReason: "absolute_threshold:180000",
		LastTriggerAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("nil db must be a no-op, got err=%v", err)
	}
}

// TestUpdateHandoffMetrics_NilMetrics verifies the helper does not panic when
// handed a nil metrics pointer alongside a nil db (the only configuration in
// which handoff currently reaches this code path with no metrics). A non-nil
// db + nil metrics is undefined by contract and out of scope here.
func TestUpdateHandoffMetrics_NilMetrics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil db + nil metrics must not panic, got %v", r)
		}
	}()
	_ = UpdateHandoffMetrics(context.Background(), nil, nil)
}

// TestHandoffMetricsSummary_Fields documents the expected field set so that
// schema drift in session_summaries is caught at compile time across modules.
func TestHandoffMetricsSummary_Fields(t *testing.T) {
	m := HandoffMetricsSummary{
		SessionKey:        "k",
		HandoffCount:      3,
		LastHandoffAt:     time.Unix(1000, 0),
		TokensAtTrigger:   180000,
		MessagesAtTrigger: 42,
		LastTriggerReason: "percentage:0.85",
		LastTriggerAt:     time.Unix(2000, 0),
	}
	if m.SessionKey != "k" || m.HandoffCount != 3 || m.TokensAtTrigger != 180000 {
		t.Fatalf("field assignment roundtrip failed: %+v", m)
	}
}
