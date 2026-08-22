package streaming

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// 2026-08-19 observability: AttemptCommitGate.Discard used to be silent.
// After this change it emits one debug-level slog per discard with the
// buffer bytes / state / holdback chunk count, so post-mortem can
// reconstruct "how much was thrown away" without a Prometheus round-trip.

func TestAttemptCommitGate_DiscardEmitsLog(t *testing.T) {
	// Capture slog output into a buffer via a JSON handler.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	g, _ := newGateForTest(GateModeBuffered)
	// Stuff some metadata into the buffer so Discard has something to log.
	if err := g.WriteFrame("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"); err != nil {
		t.Fatalf("seed buffer: %v", err)
	}
	if _, _, state := g.Snapshot(); state != CommitStateMetadata {
		t.Fatalf("expected gate state metadata after seed, got %s", state)
	}

	if err := g.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	// 2-second window so the json-handler flushed all lines.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"attempt_buffer_discarded"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), `"attempt_buffer_discarded"`) {
		t.Fatalf("expected attempt_buffer_discarded slog line; got: %s", buf.String())
	}

	// Decode the first matching JSON line and assert the fields the
	// recovery loop relies on for post-mortem.
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			continue
		}
		if rec["msg"] == "attempt_buffer_discarded" {
			if rec["protocol"] != ProtocolAnthropic.String() {
				t.Errorf("protocol=%v want %s", rec["protocol"], ProtocolAnthropic.String())
			}
			if rec["state"] != CommitStateMetadata.String() {
				t.Errorf("state=%v want %s", rec["state"], CommitStateMetadata.String())
			}
			// buffer_bytes is the JSON number; presence alone is enough
			// since the seeded frame is small.
			if _, ok := rec["buffer_bytes"]; !ok {
				t.Errorf("missing buffer_bytes in log line: %v", rec)
			}
			if _, ok := rec["holdback_held"]; !ok {
				t.Errorf("missing holdback_held in log line: %v", rec)
			}
			return
		}
	}
	t.Fatal("attempt_buffer_discarded line not parsed from log buffer")
}

// TestAttemptCommitGate_DiscardSnapshotExposesBufferBytes pins the
// Snapshot helper used by the survival coordinator to log the bytes that
// WERE about to be discarded (before Discard zeroes them).
func TestAttemptCommitGate_DiscardSnapshotExposesBufferBytes(t *testing.T) {
	g, _ := newGateForTest(GateModeBuffered)
	// Use an Anthropic message_start frame (attempt metadata, not
	// keepalive) so the gate advances to CommitStateMetadata and the
	// buffer actually grows. Ping frames are SSE comments and classify
	// as keepalive — they never enter the per-attempt metadata buffer.
	if err := g.WriteFrame("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"x\"}}\n\n"); err != nil {
		t.Fatalf("seed frame: %v", err)
	}
	bufferBytes, holdbackHeld, state := g.Snapshot()
	if bufferBytes == 0 {
		t.Errorf("buffer_bytes=0 after seeding; expected >0")
	}
	if state != CommitStateMetadata {
		t.Errorf("state=%s want %s", state, CommitStateMetadata.String())
	}
	if holdbackHeld != 0 {
		t.Errorf("holdback_held=%d want 0 (no semantic frames in metadata state)", holdbackHeld)
	}
}
