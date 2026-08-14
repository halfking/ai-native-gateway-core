package streaming

import (
	"strings"
	"testing"
	"time"
)

func newGateWriter(rec *recordingWriter, cfg AttemptGateConfig) *AttemptCommitGate {
	if cfg.Protocol == "" {
		cfg.Protocol = FrameProtocolOpenAIChat
	}
	return NewAttemptCommitGate(rec, cfg)
}

// Phase 0B (doc 18 §17): with the immediate policy the gate commits at the
// exact old timing (first write), so the wire bytes are identical to the
// ungated stream.
func TestAttemptCommitGateImmediatePolicyByteIdentical(t *testing.T) {
	frames := []string{
		": keep-alive\n\n",
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}

	direct := newRecordingWriter()
	for _, f := range frames {
		_, _ = direct.Write([]byte(f))
	}

	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitImmediate})
	w := gate.ResponseWriter()
	for _, f := range frames {
		if _, err := w.Write([]byte(f)); err != nil {
			t.Fatalf("write %q: %v", f, err)
		}
	}

	if got, want := rec.snapshot(), direct.snapshot(); got != want {
		t.Fatalf("wire bytes differ:\n got %q\nwant %q", got, want)
	}
	if !gate.Committed() {
		t.Fatal("immediate policy must commit on first write")
	}
	if gate.State() != CommitStateTerminal {
		t.Fatalf("state = %q, want terminal", gate.State())
	}
	if !gate.ContentSeen() {
		t.Fatal("content should have been seen")
	}
}

// Deferred semantics: comments reach the client immediately, attempt metadata
// stays buffered, and the first content frame triggers the semantic commit,
// flushing the buffered frames in original order.
func TestAttemptCommitGateFirstSemanticBuffersMetadata(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
		t.Fatalf("comment write: %v", err)
	}
	if got, want := rec.snapshot(), ": keep-alive\n\n"; got != want {
		t.Fatalf("comment must pass through immediately, got %q", got)
	}
	if gate.State() != CommitStateNone {
		t.Fatalf("comment must not change state, got %q", gate.State())
	}

	meta := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"
	if _, err := w.Write([]byte(meta)); err != nil {
		t.Fatalf("metadata write: %v", err)
	}
	if got := rec.snapshot(); got != ": keep-alive\n\n" {
		t.Fatalf("attempt metadata must be buffered, wire = %q", got)
	}
	if gate.State() != CommitStateMetadata {
		t.Fatalf("state = %q, want metadata", gate.State())
	}
	if gate.Committed() {
		t.Fatal("metadata alone must not commit")
	}

	content := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("content write: %v", err)
	}

	if !gate.Committed() {
		t.Fatal("first content frame must trigger commit")
	}
	want := ": keep-alive\n\n" + meta + content
	if got := rec.snapshot(); got != want {
		t.Fatalf("commit must flush buffer in order:\n got %q\nwant %q", got, want)
	}
	if gate.State() != CommitStateContent {
		t.Fatalf("state = %q, want content", gate.State())
	}
}

// doc 18 §10.1: with commit_state none/metadata and no Commit(), the whole
// attempt buffer is droppable (transparent retry); comments already sent stay
// on the wire; the retried attempt starts fresh.
func TestAttemptCommitGateDiscardDropsAttemptBuffer(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	_, _ = w.Write([]byte(": keep-alive\n\n"))
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"))

	if err := gate.Discard(); err != nil {
		t.Fatalf("discard in metadata state: %v", err)
	}
	if gate.Committed() {
		t.Fatal("discard must not commit")
	}
	if got, want := rec.snapshot(), ": keep-alive\n\n"; got != want {
		t.Fatalf("buffered metadata must vanish, wire = %q", got)
	}

	// After discard the gate still works for the (retried) attempt.
	if _, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")); err != nil {
		t.Fatalf("write after discard: %v", err)
	}
	if !gate.Committed() {
		t.Fatal("content after discard must commit")
	}
	if !strings.Contains(rec.snapshot(), "ok") {
		t.Fatal("retried content must reach the wire")
	}
}

// doc 18 §10.1: after content/tool_call/terminal the attempt is no longer
// droppable — Discard must fail closed.
func TestAttemptCommitGateDiscardRejectedAfterSemanticCommit(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"))
	if err := gate.Discard(); err == nil {
		t.Fatal("discard after content must fail")
	}
	if got, want := rec.snapshot(), "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"; got != want {
		t.Fatalf("committed bytes must stay on the wire, got %q", got)
	}
}

// doc 18 §5.1: when the attempt-metadata buffer exceeds its cap the gate
// returns attempt_metadata_buffer_exceeded and forbids Commit() — memory is
// never released by force-committing.
func TestAttemptCommitGateMetadataBufferExceeded(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{
		Policy:                  AttemptCommitFirstSemantic,
		MaxAttemptMetadataBytes: 64,
	})
	w := gate.ResponseWriter()

	var lastErr error
	for i := 0; i < 20; i++ {
		_, lastErr = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"))
		if lastErr != nil {
			break
		}
	}
	if lastErr == nil {
		t.Fatal("expected attempt_metadata_buffer_exceeded")
	}
	if !strings.Contains(lastErr.Error(), "attempt_metadata_buffer_exceeded") {
		t.Fatalf("error = %v, want attempt_metadata_buffer_exceeded", lastErr)
	}
	if gate.Err() == nil {
		t.Fatal("gate.Err() must latch the overflow")
	}

	// Force-commit must be refused: nothing semantic may leak to the wire.
	if err := gate.Commit(); err == nil {
		t.Fatal("Commit() after metadata overflow must fail")
	}
	if got := rec.snapshot(); got != "" {
		t.Fatalf("overflowed attempt must not be force-committed, wire = %q", got)
	}
	if gate.Committed() {
		t.Fatal("gate must stay uncommitted after overflow")
	}

	// Discard is still allowed (that is the coordinator's escape hatch).
	if err := gate.Discard(); err != nil {
		t.Fatalf("discard after overflow must succeed: %v", err)
	}
}

// doc 18 §10.1: unknown/malformed frames fail closed — they count as content
// so the attempt can never be transparently dropped.
func TestAttemptCommitGateUnknownFrameFailsClosed(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	_, _ = w.Write([]byte("data: total garbage\n\n"))
	if gate.State() != CommitStateContent {
		t.Fatalf("unknown frame must fail closed to content, state = %q", gate.State())
	}
	if !gate.Committed() {
		t.Fatal("unknown frame must trigger semantic commit")
	}
}

// Frames may arrive split across arbitrary Write boundaries and may use CRLF
// terminators; the gate must reassemble frames without altering any bytes.
func TestAttemptCommitGateFrameSplittingAndCRLF(t *testing.T) {
	frames := []string{
		"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\r\n\r\n",
		"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
	}
	joined := strings.Join(frames, "")

	direct := newRecordingWriter()
	_, _ = direct.Write([]byte(joined))

	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{
		Protocol: FrameProtocolAnthropic,
		Policy:   AttemptCommitFirstSemantic,
	})
	w := gate.ResponseWriter()

	// Feed in odd-sized chunks to exercise reassembly.
	raw := []byte(joined)
	for i := 0; i < len(raw); i += 7 {
		end := i + 7
		if end > len(raw) {
			end = len(raw)
		}
		if _, err := w.Write(raw[i:end]); err != nil {
			t.Fatalf("chunked write: %v", err)
		}
	}

	if got, want := rec.snapshot(), direct.snapshot(); got != want {
		t.Fatalf("wire bytes differ after reassembly:\n got %q\nwant %q", got, want)
	}
	if gate.State() != CommitStateTerminal {
		t.Fatalf("state = %q, want terminal", gate.State())
	}
}

// A terminal-only stream (no content at all, e.g. upstream immediately ends)
// commits on the terminator, but the gate must expose that NO content was
// seen so the caller cannot misreport an empty stream as a success.
func TestAttemptCommitGateTerminalOnlyStreamNotContent(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if !gate.Committed() {
		t.Fatal("terminal frame must commit")
	}
	if gate.State() != CommitStateTerminal {
		t.Fatalf("state = %q, want terminal", gate.State())
	}
	if gate.ContentSeen() {
		t.Fatal("terminal-only stream must not count as content seen")
	}
	if gate.ToolCallSeen() {
		t.Fatal("terminal-only stream must not count as tool call seen")
	}
}

// doc 18 §16.1 #10: SSE comments never trigger semantic commit, even in bulk.
func TestAttemptCommitGateCommentsNeverCommit(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(": gateway-status: waiting_for_provider\n\n")); err != nil {
			t.Fatalf("comment write: %v", err)
		}
	}
	if gate.Committed() {
		t.Fatal("comments must never commit")
	}
	if gate.State() != CommitStateNone {
		t.Fatalf("comments must not change state, got %q", gate.State())
	}
}

// A failed write detaches the gate; further writes are refused without
// touching the underlying writer, and the detachment is observable so the
// coordinator stops trying to render anything on this connection.
func TestAttemptCommitGateDetachesOnWriteFailure(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{Policy: AttemptCommitFirstSemantic})
	w := gate.ResponseWriter()

	rec.failNext = true
	if _, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")); err == nil {
		t.Fatal("expected write error on commit flush")
	}
	if !gate.Detached() {
		t.Fatal("gate must latch detached")
	}

	rec.failNext = false
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err == nil {
		t.Fatal("write after detach must fail")
	}
	if err := gate.Commit(); err == nil {
		t.Fatal("Commit on detached gate must fail")
	}
}

// doc 18 §5.1 caps the attempt metadata buffer by bytes AND time. The time
// cap is enforced lazily on the next buffered metadata write.
func TestAttemptCommitGateMetadataAgeCap(t *testing.T) {
	rec := newRecordingWriter()
	gate := newGateWriter(rec, AttemptGateConfig{
		Policy:                AttemptCommitFirstSemantic,
		MaxAttemptMetadataAge: 20 * time.Millisecond,
	})
	w := gate.ResponseWriter()

	meta := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"
	if _, err := w.Write([]byte(meta)); err != nil {
		t.Fatalf("first metadata write: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := w.Write([]byte(meta)); err == nil {
		t.Fatal("expected attempt_metadata_buffer_exceeded after age cap")
	} else if !strings.Contains(err.Error(), "attempt_metadata_buffer_exceeded") {
		t.Fatalf("error = %v, want attempt_metadata_buffer_exceeded", err)
	}
	if err := gate.Commit(); err == nil {
		t.Fatal("Commit after age overflow must fail")
	}
	if got := rec.snapshot(); got != "" {
		t.Fatalf("overflowed attempt must not reach the wire, got %q", got)
	}
}
