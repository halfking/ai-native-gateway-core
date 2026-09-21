package streaming

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAttemptCommitGate_CapacityCheckBeforeAppend verifies that the gate
// refuses oversized frames BEFORE appending, preventing bufferLen from
// transiently exceeding maxMetadata (guards 2026-08-28 audit fix).
func TestAttemptCommitGate_CapacityCheckBeforeAppend(t *testing.T) {
	f := &trackingFlusher{}
	g := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{Mode: GateModeBuffered, MaxMetadataBufferBytes: 100})

	// Write metadata frame that fits.
	if err := g.WriteFrame("event: message_start\ndata: {}\n\n"); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if g.bufferLen > 100 {
		t.Fatalf("bufferLen=%d after first frame, already exceeds cap 100", g.bufferLen)
	}

	// Write frame that would exceed cap.
	bigFrame := "event: ping\ndata: {\"pad\":\"" + strings.Repeat("x", 200) + "\"}\n\n"
	err := g.WriteFrame(bigFrame)
	if !errors.Is(err, ErrAttemptMetadataBufferExceeded) {
		t.Fatalf("oversized frame error=%v, want ErrAttemptMetadataBufferExceeded", err)
	}

	// Verify bufferLen did NOT exceed cap (old code would append first, then check).
	if g.bufferLen > 100 {
		t.Errorf("bufferLen=%d after rejected frame, exceeds cap 100 (frame was appended before check)", g.bufferLen)
	}
}
