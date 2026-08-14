package streaming

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// SR-W1 AttemptCommitGate (doc 18 §5.1/§9.3/§10.1): the bridge writes into an
// attempt-local buffer and records commit state. Before Commit() the real
// connection only receives keepalive/status comments. The first semantic
// frame (content/tool_call/terminal) triggers the normal semantic commit;
// recoverable failures before that may discard the whole attempt buffer and
// retry transparently. Metadata buffering is bounded — exceeding the limit
// must surface attempt_metadata_buffer_exceeded, never a forced Commit().

func newGateForTest(mode GateMode) (*AttemptCommitGate, *trackingFlusher) {
	f := &trackingFlusher{}
	g := NewAttemptCommitGate(ProtocolAnthropic, NewSerializedStreamWriter(f),
		GateOptions{Mode: mode, MaxMetadataBufferBytes: 1024})
	return g, f
}

func TestAttemptCommitGateBuffersMetadataUntilSemanticCommit(t *testing.T) {
	g, f := newGateForTest(GateModeBuffered)
	meta := "event: message_start\ndata: {\"message\":{}}\n\n"
	if err := g.WriteFrame(meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if g.State() != CommitStateMetadata {
		t.Fatalf("state = %v, want CommitStateMetadata", g.State())
	}
	if f.buf.Len() != 0 {
		t.Fatalf("metadata must not reach the wire before commit, got %q", f.buf.String())
	}

	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	if err := g.WriteFrame(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if g.State() != CommitStateContent {
		t.Fatalf("state = %v, want CommitStateContent", g.State())
	}
	// First semantic frame triggers the normal commit: buffered frames are
	// flushed to the wire in original order, metadata first.
	want := meta + content
	if f.buf.String() != want {
		t.Fatalf("wire = %q, want %q", f.buf.String(), want)
	}
	if !g.Committed() {
		t.Fatal("gate should be committed after first semantic frame")
	}
}

func TestAttemptCommitGateDiscardAllowedOnlyBeforeSemanticCommit(t *testing.T) {
	g, f := newGateForTest(GateModeBuffered)
	if err := g.WriteFrame("event: message_start\ndata: {}\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := g.Discard(); err != nil {
		t.Fatalf("discard of metadata-only attempt: %v", err)
	}
	if f.buf.Len() != 0 {
		t.Fatalf("discarded frames leaked to wire: %q", f.buf.String())
	}
	if g.State() != CommitStateNone {
		t.Fatalf("state after discard = %v, want CommitStateNone", g.State())
	}

	// A discarded gate is terminal: writes fail fast so the bridge loop
	// stops instead of silently producing output for a dead attempt. The
	// coordinator builds a new gate for the next attempt.
	if err := g.WriteFrame("event: message_start\ndata: {}\n\n"); !errors.Is(err, ErrAttemptDiscarded) {
		t.Fatalf("write after discard = %v, want ErrAttemptDiscarded", err)
	}

	// On a fresh gate, discarding after semantic commit would duplicate
	// client-visible output — must be refused.
	g2, _ := newGateForTest(GateModeBuffered)
	if err := g2.WriteFrame("event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := g2.Discard(); !errors.Is(err, ErrAttemptAlreadyCommitted) {
		t.Fatalf("discard after commit = %v, want ErrAttemptAlreadyCommitted", err)
	}
}

func TestAttemptCommitGateMetadataBufferLimitNeverForcesCommit(t *testing.T) {
	g, f := newGateForTest(GateModeBuffered)
	big := "event: message_start\ndata: {\"pad\":\"" + strings.Repeat("x", 600) + "\"}\n\n"
	if err := g.WriteFrame(big); err != nil {
		t.Fatal(err)
	}
	err := g.WriteFrame(big)
	if !errors.Is(err, ErrAttemptMetadataBufferExceeded) {
		t.Fatalf("second oversized metadata write = %v, want ErrAttemptMetadataBufferExceeded", err)
	}
	// The limit must surface the error, never flush the buffer to free
	// memory: nothing reached the wire and the attempt stays discardable.
	if f.buf.Len() != 0 {
		t.Fatalf("metadata limit must not force commit, wire = %q", f.buf.String())
	}
	if g.Committed() {
		t.Fatal("gate must not be committed on buffer overflow")
	}
	if err := g.Discard(); err != nil {
		t.Fatalf("overflowed metadata attempt should stay discardable: %v", err)
	}
}

func TestAttemptCommitGateKeepalivePassthroughBeforeCommit(t *testing.T) {
	g, f := newGateForTest(GateModeBuffered)
	if err := g.WriteFrame(": keep-alive\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFrame("event: ping\ndata: {}\n\n"); err != nil {
		t.Fatal(err)
	}
	// Keepalive is transport-level: it flows to the wire immediately, is not
	// buffered into the attempt, and does not advance commit state.
	if f.buf.String() != ": keep-alive\n\n"+"event: ping\ndata: {}\n\n" {
		t.Fatalf("keepalive passthrough wire = %q", f.buf.String())
	}
	if g.State() != CommitStateNone {
		t.Fatalf("keepalive must not advance commit state, got %v", g.State())
	}
}

func TestAttemptCommitGateUnknownFrameFailsClosedAsContent(t *testing.T) {
	g, f := newGateForTest(GateModeBuffered)
	if err := g.WriteFrame("event: mystery\ndata: {}\n\n"); err != nil {
		t.Fatal(err)
	}
	// Unknown frames must be treated as semantic content: force the commit
	// so the frame can never be silently dropped on a later discard.
	if g.State() != CommitStateContent || !g.Committed() {
		t.Fatalf("unknown frame: state=%v committed=%v, want content+committed", g.State(), g.Committed())
	}
	if f.buf.Len() == 0 {
		t.Fatal("unknown frame should have been committed to the wire")
	}
}

func TestAttemptCommitGateStateIsMonotonic(t *testing.T) {
	g, _ := newGateForTest(GateModeBuffered)
	g.WriteFrame("event: message_start\ndata: {}\n\n") // metadata
	g.WriteFrame("event: content_block_delta\ndata: {\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n")
	if g.State() != CommitStateToolCall {
		t.Fatalf("state = %v, want CommitStateToolCall", g.State())
	}
	// A later (weaker) frame must not regress the state.
	g.WriteFrame("event: content_block_stop\ndata: {}\n\n")
	if g.State() != CommitStateToolCall {
		t.Fatalf("state regressed after content_block_stop: %v", g.State())
	}
}

// TestAttemptCommitGateImmediateModeIsByteIdentical — Phase 0B: with the gate
// in immediate mode every frame is written to the wire as it arrives, in
// order, exactly like the legacy write path. This is what lets us wire the
// gate into the bridges now and verify the line protocol is unchanged before
// flipping to buffered mode in W2.
func TestAttemptCommitGateImmediateModeIsByteIdentical(t *testing.T) {
	g, f := newGateForTest(GateModeImmediate)
	frames := []string{
		"event: message_start\ndata: {\"message\":{}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"b\"}}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	var want bytes.Buffer
	for _, fr := range frames {
		if err := g.WriteFrame(fr); err != nil {
			t.Fatalf("write %q: %v", fr, err)
		}
		want.WriteString(fr)
	}
	if f.buf.String() != want.String() {
		t.Fatalf("immediate mode wire differs:\ngot  %q\nwant %q", f.buf.String(), want.String())
	}
	if g.State() != CommitStateTerminal {
		t.Fatalf("state = %v, want CommitStateTerminal", g.State())
	}
}
