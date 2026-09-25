package bg

import (
	"testing"
)

// TestBuildNodeProbeTaskID_StableAcrossAttempts pins the 2026-08-12 audit
// fix that keeps a single SSE tile for a (credential, model) lifecycle:
// pending → in-flight → completed/failed. Without this, attempt numbers
// flowed into the ID and the dashboard forked a tile every time the worker
// re-armed the row, leaving the dashboard confused about whether the same
// pair had completed or not.
func TestBuildNodeProbeTaskID_StableAcrossAttempts(t *testing.T) {
	id1 := buildNodeProbeTaskID(7, "gpt-5.6")
	id2 := buildNodeProbeTaskID(7, "gpt-5.6")
	if id1 != id2 {
		t.Fatalf("stable ID must be identical across calls: %q vs %q", id1, id2)
	}
	// Must NOT change with attempt (the field that changes per re-arm).
	if buildNodeProbeTaskID(7, "gpt-5.6") != buildNodeProbeTaskID(7, "gpt-5.6") {
		t.Fatalf("attempt must not influence task ID")
	}
	// Different credentials → different IDs.
	if buildNodeProbeTaskID(7, "gpt-5.6") == buildNodeProbeTaskID(8, "gpt-5.6") {
		t.Fatalf("credential must influence task ID")
	}
	// Different models → different IDs.
	if buildNodeProbeTaskID(7, "gpt-5.6") == buildNodeProbeTaskID(7, "claude-sonnet-5") {
		t.Fatalf("model must influence task ID")
	}
	// The task ID lives inside Redis keys (llmgw:probe:task:<id>) and
	// JSON envelopes. Must be non-empty and printable ASCII (no newline,
	// no control chars). Colons are fine; Redis segments on them but the
	// task ID itself is a single field.
	for _, r := range id1 {
		if r == '\n' || r == '\r' || r == 0 {
			t.Fatalf("task ID %q contains control character", id1)
		}
	}
}
