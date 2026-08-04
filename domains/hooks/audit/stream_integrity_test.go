package audit

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// fakeTextObserver is a minimal StreamTextObserver for testing the capture's
// wiring (latch, reason propagation, reset) without importing the integrity
// package (which imports audit — that would be an import cycle).
type fakeTextObserver struct {
	observeText func(s string) bool
	reason      string
	hash        string
	hits        int
	blockSize   int
	blocksTotal int
	ok          bool
	resetCalls  int
}

func (f *fakeTextObserver) ObserveText(s string) bool {
	if f.observeText != nil {
		return f.observeText(s)
	}
	return false
}
func (f *fakeTextObserver) BreachReason() string { return f.reason }
func (f *fakeTextObserver) RepeatedContentHash() (string, int, int, int, bool) {
	return f.hash, f.hits, f.blockSize, f.blocksTotal, f.ok
}
func (f *fakeTextObserver) Reset() { f.resetCalls++ }

// ObserveChunk routes delta content through appendText → notifyTextObserver,
// latching the first breach returned by the observer.
func TestStreamCapture_ObserveChunk_LatchesIntegrityBreach(t *testing.T) {
	capture := NewStreamCapture()
	breach := false
	obs := &fakeTextObserver{
		reason: "integrity_repeated_content",
		observeText: func(s string) bool {
			// Breach on the second observation.
			if strings.Contains(s, "LOOP") {
				breach = true
			}
			return breach
		},
	}
	capture.SetTextObserver(obs)

	if capture.IntegrityBreached() {
		t.Fatal("breached before any chunk")
	}

	// First delta: no breach yet.
	capture.ObserveChunk(&ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "ok"},
	})
	if capture.IntegrityBreached() {
		t.Fatal("breached on a non-loop chunk")
	}

	// Second delta trips the observer.
	capture.ObserveChunk(&ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "LOOP"},
	})
	if !capture.IntegrityBreached() {
		t.Fatal("expected breach after loop chunk")
	}
	if got := capture.IntegrityBreachReason(); got != "integrity_repeated_content" {
		t.Fatalf("breach reason = %q", got)
	}

	// Further chunks must not overwrite the latched reason even if the
	// observer keeps returning true.
	obs.reason = "integrity_other"
	capture.ObserveChunk(&ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "LOOP2"},
	})
	if got := capture.IntegrityBreachReason(); got != "integrity_repeated_content" {
		t.Fatalf("latched reason changed to %q", got)
	}
}

// ObservePayload also feeds the observer (via appendText/extractDeltaText).
func TestStreamCapture_ObservePayload_LatchesBreach(t *testing.T) {
	capture := NewStreamCapture()
	calls := 0
	capture.SetTextObserver(&fakeTextObserver{
		reason: "integrity_repeated_content",
		observeText: func(s string) bool {
			calls++
			return calls >= 2
		},
	})
	capture.ObservePayload(`{"choices":[{"delta":{"content":"a"}}]}`, "", false)
	if capture.IntegrityBreached() {
		t.Fatal("breached after one payload")
	}
	capture.ObservePayload(`{"choices":[{"delta":{"content":"b"}}]}`, "", false)
	if !capture.IntegrityBreached() {
		t.Fatal("expected breach after second payload")
	}
}

// IncrementalRepeatedContent surfaces the observer's finding.
func TestStreamCapture_IncrementalRepeatedContent(t *testing.T) {
	capture := NewStreamCapture()
	obs := &fakeTextObserver{
		hash:        "abc123",
		hits:        3,
		blockSize:   256,
		blocksTotal: 3,
		ok:          true,
	}
	capture.SetTextObserver(obs)

	hash, hits, size, blocks, ok := capture.IncrementalRepeatedContent()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if hash != "abc123" || hits != 3 || size != 256 || blocks != 3 {
		t.Fatalf("unexpected finding: hash=%s hits=%d size=%d blocks=%d", hash, hits, size, blocks)
	}

	// No observer → ok=false.
	bare := NewStreamCapture()
	if _, _, _, _, ok := bare.IncrementalRepeatedContent(); ok {
		t.Fatal("expected ok=false with no observer")
	}
}

// Reset clears the latched breach and resets the observer (failover reuse).
func TestStreamCapture_ResetClearsIntegrityState(t *testing.T) {
	capture := NewStreamCapture()
	obs := &fakeTextObserver{
		reason:      "integrity_repeated_content",
		observeText: func(string) bool { return true },
	}
	capture.SetTextObserver(obs)
	// Trip a breach via a chunk.
	capture.ObserveChunk(&ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "x"},
	})
	if !capture.IntegrityBreached() {
		t.Fatal("precondition: expected breached")
	}

	capture.Reset()
	if capture.IntegrityBreached() {
		t.Fatal("breach survived Reset")
	}
	if capture.IntegrityBreachReason() != "" {
		t.Fatal("reason survived Reset")
	}
	if obs.resetCalls != 1 {
		t.Fatalf("observer Reset called %d times, want 1", obs.resetCalls)
	}
}

// Nil-safe accessors never panic. (Reset is intentionally NOT nil-safe — it
// takes sc.mu — so it is not exercised here.)
func TestStreamCapture_IntegrityNilSafe(t *testing.T) {
	var sc *StreamCapture
	if sc.IntegrityBreached() {
		t.Fatal("nil capture breached")
	}
	if sc.IntegrityBreachReason() != "" {
		t.Fatal("nil capture has reason")
	}
	if _, _, _, _, ok := sc.IncrementalRepeatedContent(); ok {
		t.Fatal("nil capture flagged")
	}
	sc.SetTextObserver(nil) // must not panic
}

// SetTextObserver(nil) detaches; subsequent text does not latch a breach.
func TestStreamCapture_SetTextObserverNilDetaches(t *testing.T) {
	capture := NewStreamCapture()
	capture.SetTextObserver(&fakeTextObserver{
		observeText: func(string) bool { return true },
	})
	capture.SetTextObserver(nil)
	capture.ObserveChunk(&ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: "anything"},
	})
	if capture.IntegrityBreached() {
		t.Fatal("breached after observer detached")
	}
}
