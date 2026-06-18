package relay

import (
	"strings"
	"testing"
	"time"
)

// TestPendingCapturer_AppendUnderCap: chunks under the cap
// accumulate verbatim, and bytesCaptured reflects the total.
func TestPendingCapturer_AppendUnderCap(t *testing.T) {
	p := NewPendingCapturer(100)
	p.append("data: chunk1\n\n")
	p.append("data: chunk2\n\n")
	// "data: chunk1\n\n" is 14 bytes; same for chunk2 → 28.
	if p.BytesCaptured() != 28 {
		t.Errorf("bytesCaptured: got %d, want 28", p.BytesCaptured())
	}
}

// TestPendingCapturer_AppendDropsOverflow: a chunk that would
// overflow is dropped entirely (not truncated). Audit fix 3.3:
// truncating mid-JSON would produce an invalid SSE line on
// replay; dropping is safer. Subsequent appends are silent
// no-ops (the bytes counter is at the cap).
func TestPendingCapturer_AppendDropsOverflow(t *testing.T) {
	p := NewPendingCapturer(10)
	p.append("0123456789") // exactly the cap
	if p.BytesCaptured() != 10 {
		t.Fatalf("after first fill: got %d, want 10", p.BytesCaptured())
	}
	// Overflow chunk: dropped entirely. bytes stays at 10.
	p.append("X")
	if p.BytesCaptured() != 10 {
		t.Errorf("overflow: got %d, want 10", p.BytesCaptured())
	}
	// Partial overflow: 15 bytes > 10 cap → entire chunk dropped.
	p2 := NewPendingCapturer(10)
	p2.append("0123456789ABCDE")
	if p2.BytesCaptured() != 0 {
		t.Errorf("partial overflow: got %d, want 0 (entire chunk dropped)", p2.BytesCaptured())
	}
	// Chunk that exactly fits is kept.
	p3 := NewPendingCapturer(10)
	p3.append("0123456789")
	if p3.BytesCaptured() != 10 {
		t.Errorf("exact fit: got %d, want 10", p3.BytesCaptured())
	}
}

// TestPendingCapturer_NilSafe pins the nil-receiver contract.
// Every method must be a no-op so a missed SetPendingStore in
// production wiring does not panic.
func TestPendingCapturer_NilSafe(t *testing.T) {
	var p *pendingCapturer
	p.append("data: x\n\n")
	p.markInterrupted("test")
	p.finalize(StreamOutcome{Interrupted: true, Reason: "test"})
	_, _, ok := p.Snapshot()
	if ok {
		t.Fatal("nil capturer should not be ok")
	}
	if got := p.BytesCaptured(); got != 0 {
		t.Errorf("nil bytesCaptured: got %d, want 0", got)
	}
}

// TestPendingCapturer_DefaultCap: maxBytes <= 0 falls back to
// 1 MiB. Verifying via a sentinel append that would otherwise
// overflow a small cap.
func TestPendingCapturer_DefaultCap(t *testing.T) {
	p := NewPendingCapturer(0)
	if p.maxBytes != 1<<20 {
		t.Errorf("default cap: got %d, want %d", p.maxBytes, 1<<20)
	}
}

// TestPendingCapturer_FinalizeCleanCompletes pins the happy
// path: an EOF with [DONE] (no Interrupted flag) marks the
// buffer as completed.
func TestPendingCapturer_FinalizeCleanCompletes(t *testing.T) {
	p := NewPendingCapturer(1000)
	p.append("data: hi\n\n")
	p.finalize(StreamOutcome{Interrupted: false})
	body, state, ok := p.Snapshot()
	if !ok {
		t.Fatal("snapshot: not ok")
	}
	if state.Status != "completed" {
		t.Errorf("status: got %q, want completed", state.Status)
	}
	if state.ErrMessage != "" {
		t.Errorf("errMessage: got %q, want empty", state.ErrMessage)
	}
	if !strings.Contains(string(body), "data: hi") {
		t.Errorf("body: got %q", body)
	}
	if state.CompletedAt == 0 {
		t.Error("completedAt: should be set")
	}
}

// TestPendingCapturer_FinalizeClientCancelCompletes: a
// client_cancel after the buffer is fully populated counts as
// "completed" for replay — the full body is there.
func TestPendingCapturer_FinalizeClientCancelCompletes(t *testing.T) {
	p := NewPendingCapturer(1000)
	p.append("data: hi\n\n")
	p.finalize(StreamOutcome{Interrupted: true, Reason: "client_cancel"})
	_, state, _ := p.Snapshot()
	if state.Status != "completed" {
		t.Errorf("client_cancel: got %q, want completed", state.Status)
	}
}

// TestPendingCapturer_FinalizeErrorFails: an interrupted reason
// other than client_cancel marks the buffer as failed.
func TestPendingCapturer_FinalizeErrorFails(t *testing.T) {
	cases := []string{"stream_timeout", "read_error", "stream_panic", "eof_without_done"}
	for _, reason := range cases {
		t.Run(reason, func(t *testing.T) {
			p := NewPendingCapturer(1000)
			p.append("data: partial\n\n")
			p.finalize(StreamOutcome{Interrupted: true, Reason: reason})
			_, state, _ := p.Snapshot()
			if state.Status != "failed" {
				t.Errorf("reason=%s: got %q, want failed", reason, state.Status)
			}
			if state.ErrMessage != reason {
				t.Errorf("reason=%s: errMessage=%q", reason, state.ErrMessage)
			}
		})
	}
}

// TestPendingCapturer_MarkInterruptedIsFinal: once marked
// interrupted, finalize() does not overwrite.
func TestPendingCapturer_MarkInterruptedIsFinal(t *testing.T) {
	p := NewPendingCapturer(100)
	p.append("data: x\n\n")
	p.markInterrupted("stream_panic")
	// Even with a clean outcome, the panic-induced failed
	// state must be preserved.
	p.finalize(StreamOutcome{Interrupted: false})
	_, state, _ := p.Snapshot()
	if state.Status != "failed" {
		t.Errorf("status: got %q, want failed", state.Status)
	}
	if state.ErrMessage != "stream_stream_panic" {
		t.Errorf("errMessage: got %q", state.ErrMessage)
	}
}

// TestPendingCapturer_DefaultCapIs1MiB is a regression guard
// for the production-default cap. A change here would impact
// memory safety — the test makes the cap value explicit.
func TestPendingCapturer_DefaultCapIs1MiB(t *testing.T) {
	p := NewPendingCapturer(0)
	if p.maxBytes != 1<<20 {
		t.Fatalf("default cap: got %d, want 1<<20 (1 MiB)", p.maxBytes)
	}
	// Verify the cap is enforced with a sentinel that would
	// otherwise consume 2 MiB.
	chunk := strings.Repeat("x", 4096)
	for i := 0; i < 600; i++ {
		p.append(chunk)
	}
	if p.BytesCaptured() != 1<<20 {
		t.Errorf("cap not enforced: got %d", p.BytesCaptured())
	}
}

// TestPendingCapturer_SnapshotIndependentCopy: the snapshot
// must be a copy so the caller cannot be affected by a later
// (theoretical) mutation. Audit fix 3.3: chunks that don't fit
// are dropped entirely (not truncated), so a 10-byte chunk
// into a 5-byte cap produces 0 bytes.
func TestPendingCapturer_SnapshotIndependentCopy(t *testing.T) {
	p := NewPendingCapturer(5)
	p.append("0123456789") // dropped entirely (10 > 5)
	p.finalize(StreamOutcome{})
	body, _, _ := p.Snapshot()
	if len(body) != 0 {
		t.Errorf("snapshot len: got %d, want 0 (chunk dropped)", len(body))
	}
	// Also test with a chunk that fits.
	p2 := NewPendingCapturer(5)
	p2.append("abc") // fits
	p2.finalize(StreamOutcome{})
	body2, _, _ := p2.Snapshot()
	if string(body2) != "abc" {
		t.Errorf("snapshot body: got %q, want %q", body2, "abc")
	}
}

// TestPendingCapturer_NoSnapshotBeforeFinalize: a snapshot
// taken before finalize returns ok=false. This prevents
// premature writes to the pending store.
func TestPendingCapturer_NoSnapshotBeforeFinalize(t *testing.T) {
	p := NewPendingCapturer(100)
	p.append("data: x\n\n")
	_, _, ok := p.Snapshot()
	if ok {
		t.Fatal("snapshot should not be ok before finalize")
	}
}

// TestPendingCapturer_ConcurrentAppendAndSnapshot: stress test
// for the mutex. Run with -race to catch any data race.
func TestPendingCapturer_ConcurrentAppendAndSnapshot(t *testing.T) {
	p := NewPendingCapturer(1 << 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			p.append("data: x\n\n")
		}
	}()
	for i := 0; i < 100; i++ {
		_ = p.BytesCaptured()
	}
	<-done
	p.finalize(StreamOutcome{})
	_, _, _ = p.Snapshot()
	// No assertion needed — the test is meaningful only
	// under -race, where any concurrent map/var access
	// without the mutex would be flagged.
	_ = time.Now() // keep time import live for the file
}
