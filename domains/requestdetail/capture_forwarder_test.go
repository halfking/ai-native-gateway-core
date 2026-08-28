package requestdetail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// strPtr is a small helper for building telemetry entries with body pointers.
func strPtr(s string) *string { return &s }

// itoa is a tiny local helper so the test file doesn't depend on strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestCaptureForwarder_EmitDoesNotBlockWhenStoreBusy(t *testing.T) {
	// 2026-08-28 (audit follow-up, async capture): the synchronous
	// CaptureFromEntry held the Store lifecycle lock during JSON marshal +
	// tmp-file write + rename. Under a burst + slow /tmp, this could
	// stall request latency. The forwarder's emit() must complete even
	// when the consumer is mid-write, because the store lock is owned
	// by the consumer goroutine, not the emit goroutine.
	//
	// Use an isolated forwarder so this test is independent of the
	// process-global SetGlobal state.
	tmp := t.TempDir()
	store, err := NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	fwd := newCaptureForwarder(store)

	// Slow the consumer by inflating the queue. We do not start run() —
	// the queue must be saturated to make emit perform the FIFO evict
	// branch (the eviction path is part of what we want to exercise).
	burst := captureForwarderCapacity + 8
	for i := 0; i < burst; i++ {
		fwd.emit(&telemetry.RequestLogEntry{
			RequestID:   "warmup-" + itoa(i),
			TenantID:    "default",
			RequestBody: strPtr(`{"warmup":true}`),
		})
	}
	enqueued, processed, dropped, _ := fwd.Stats()
	if enqueued < uint64(burst) {
		t.Fatalf("warmup enqueued=%d, want >= %d", enqueued, burst)
	}
	if dropped == 0 {
		t.Fatalf("expected at least one FIFO eviction during warmup, got 0")
	}
	t.Logf("warmup stats: enqueued=%d processed=%d dropped=%d", enqueued, processed, dropped)

	// Now: emit MUST return promptly even while the queue is full.
	// Because the queue is already saturated, the next emit will take
	// the FIFO-evict branch; the time bound validates that path too.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 32; i++ {
			fwd.emit(&telemetry.RequestLogEntry{
				RequestID:   "live-" + itoa(i),
				TenantID:    "default",
				RequestBody: strPtr(`{"live":true}`),
			})
		}
	}()
	select {
	case <-done:
		// Expected: emit returns within a few microseconds.
	case <-time.After(2 * time.Second):
		t.Fatalf("emit() blocked for >2s — capture is no longer non-blocking")
	}
}

func TestCaptureForwarder_DrainsAndPersistsBodies(t *testing.T) {
	tmp := t.TempDir()
	store, err := NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	fwd := newCaptureForwarder(store)
	go fwd.run()
	defer fwd.stop()

	entry := &telemetry.RequestLogEntry{
		RequestID:    "captured-001",
		TenantID:     "tenant-x",
		GwSessionID:  strPtr("session-y"),
		ClientModel:  strPtr("claude-test"),
		RequestBody:  strPtr(`{"prompt":"hello"}`),
		ResponseBody: strPtr(`{"reply":"world"}`),
		OutboundBody: []byte(`{"upstream":"body"}`),
		Success:      true,
	}
	fwd.emit(entry)

	// Wait for the consumer to drain this single entry.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		enq, proc, _, _ := fwd.Stats()
		if enq > 0 && enq == proc {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	enq, proc, _, _ := fwd.Stats()
	if enq != proc || proc == 0 {
		t.Fatalf("forwarder never drained entry: enq=%d proc=%d", enq, proc)
	}

	// Confirm the body file exists on disk with the right content.
	path := filepath.Join(tmp, "captured-001.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected body file at %s: %v", path, err)
	}
	var got filePayload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("malformed body payload: %v", err)
	}
	if got.Meta.TenantID != "tenant-x" {
		t.Fatalf("tenant_id round-trip: got %q want tenant-x", got.Meta.TenantID)
	}
	if string(got.Bodies.RequestBody) != `{"prompt":"hello"}` {
		t.Fatalf("request_body round-trip: got %q", string(got.Bodies.RequestBody))
	}
	if string(got.Bodies.OutboundBody) != `{"upstream":"body"}` {
		t.Fatalf("outbound_body round-trip: got %q", string(got.Bodies.OutboundBody))
	}
}

func TestCaptureForwarder_DropsOldestOnOverflow(t *testing.T) {
	// Use a small capacity so the eviction logic is exercised deterministically.
	prev := captureForwarderCapacity
	SetCaptureForwarderCapacity(4)
	defer SetCaptureForwarderCapacity(prev)

	tmp := t.TempDir()
	store, err := NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Do NOT start run() — without a consumer the queue fills up so we
	// can verify FIFO eviction deterministically.
	fwd := newCaptureForwarder(store)

	emitCount := captureForwarderCapacity + 5
	for i := 0; i < emitCount; i++ {
		fwd.emit(&telemetry.RequestLogEntry{
			RequestID: "drop-test-" + itoa(i),
			TenantID:  "default",
		})
	}
	enq, _, dropped, _ := fwd.Stats()
	if dropped == 0 {
		t.Fatalf("expected FIFO drops when emitting > capacity, got enq=%d dropped=%d", enq, dropped)
	}
	if enq < uint64(captureForwarderCapacity) {
		t.Fatalf("expected at least %d enqueued (the latest ones), got %d", captureForwarderCapacity, enq)
	}
}

func TestCaptureForwarder_NilEntryIsNoop(t *testing.T) {
	tmp := t.TempDir()
	store, err := NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	fwd := newCaptureForwarder(store)
	go fwd.run()
	defer fwd.stop()
	fwd.emit(nil) // must not panic
	enq, _, _, _ := fwd.Stats()
	if enq != 0 {
		t.Fatalf("nil entry must not enqueue, got %d", enq)
	}
}

func TestCaptureFromEntrySync_StillWorks(t *testing.T) {
	// CaptureFromEntrySync is the synchronous fallback used by tests
	// that need deterministic ordering. Pin its behaviour so a future
	// refactor doesn't accidentally change it.
	tmp := t.TempDir()
	store, err := NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	SetGlobal(store)
	defer SetGlobal(nil)

	CaptureFromEntrySync(&telemetry.RequestLogEntry{
		RequestID:   "sync-001",
		TenantID:    "default",
		RequestBody: strPtr(`{"sync":true}`),
	})

	path := filepath.Join(tmp, "sync-001.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("synchronous capture did not write file: %v", err)
	}
}

// TestCaptureFromEntry_PublicHook_GoesThroughForwarder ensures the public
// CaptureFromEntry entry point used by telemetry wires through the async
// forwarder rather than doing synchronous body capture.
func TestCaptureFromEntry_PublicHook_GoesThroughForwarder(t *testing.T) {
	tmp := t.TempDir()
	store, err := NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Reset the package-global forwarder so this test owns its lifecycle.
	SetGlobal(nil)
	SetGlobal(store)
	defer SetGlobal(nil)

	StartGlobalCaptureForwarder()
	defer StopGlobalCaptureForwarder()

	CaptureFromEntry(&telemetry.RequestLogEntry{
		RequestID:   "public-hook-001",
		TenantID:    "default",
		RequestBody: strPtr(`{"public":true}`),
	})

	fwd := CaptureForwarder()
	if fwd == nil {
		t.Fatal("expected global forwarder to be wired")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		enq, proc, _, _ := fwd.Stats()
		if enq > 0 && enq == proc {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	enq, proc, _, _ := fwd.Stats()
	if enq != proc || proc == 0 {
		t.Fatalf("public hook did not drain via forwarder: enq=%d proc=%d", enq, proc)
	}
	if _, err := os.Stat(filepath.Join(tmp, "public-hook-001.json")); err != nil {
		t.Fatalf("body file not written by forwarder: %v", err)
	}
}
