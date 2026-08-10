package bg

import "testing"

// TestCredentialSelfcheck_TaskID_StableAcrossLifecycle pins the 2026-08-12
// second-pass audit fix: publishSelfcheck now takes a runID (the
// self_check_runs DB row id) and builds the SSE task ID from it, so the
// in-flight and terminal transitions emitted inside runOne collapse into one
// dashboard tile. Previously each call minted a fresh ns-timestamp id, so the
// two publishes forked into two unrelated tiles.
func TestCredentialSelfcheck_TaskID_StableAcrossLifecycle(t *testing.T) {
	sink := &captureSink{}
	w := &CredentialSelfcheckWorker{probeSink: sink}

	const credID = 42
	const runID = int64(777)

	// Simulate runOne's two publishes with the same runID.
	w.publishSelfcheck(credID, runID, "in-flight")
	w.publishSelfcheck(credID, runID, "ok")

	if sink.count() != 2 {
		t.Fatalf("expected 2 events, got %d", sink.count())
	}
	first := sink.events[0]
	second := sink.events[1]
	if first.ID != second.ID {
		t.Fatalf("in-flight and terminal must share ID for lifecycle collapse:\n in-flight=%s\n terminal =%s", first.ID, second.ID)
	}
	wantID := "selfcheck:42:777"
	if first.ID != wantID {
		t.Errorf("ID = %q, want %q", first.ID, wantID)
	}
	// Different run → different ID (each daily run is its own tile).
	w.publishSelfcheck(credID, 778, "in-flight")
	third := sink.events[2]
	if third.ID == first.ID {
		t.Fatalf("different runID must produce different task ID")
	}
}
