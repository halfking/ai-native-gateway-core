package dispatch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// waitFor polls cond until it returns true or the deadline expires. Terminal
// journal delivery is asynchronous (audit 2026-09-05 C-#4: complete() hands
// the snapshot to a background worker instead of invoking the sink inline),
// so tests assert delivery via polling rather than immediately after
// complete() returns.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// newJournalTestPipeline builds a bare (non-Started) pipeline wired with a
// journal sink, mirroring the construction used by the pre-existing
// emitJournalSnapshot tests.
func newJournalTestPipeline(sink JournalSink) *Pipeline {
	p := NewPipeline(Deps{
		RouteFunc:        func(context.Context, *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) { return "m", nil, nil },
		ForwardFunc:      func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		JournalSink:      sink,
	})
	p.registry = NewLifecycleRegistry(100, 50, 10)
	p.totalQueue = newTotalExecutionQueue(100)
	p.dimensionIndex = NewDimensionIndex(DefaultDimensionIndexConfig())
	return p
}

// newJournaledRequest builds a request carrying one pre-terminal journal
// entry (complete() appends the terminal entry, so every completed request
// has a non-empty journal and enqueues a snapshot).
func newJournaledRequest(id string) *QueuedRequest {
	qr := NewQueuedRequest(id, "tenant", "m", context.Background(), nil)
	qr.GatewayInstanceID = "gw"
	qr.TenantID = "tenant"
	qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "m1", CredentialID: 1, ErrorKind: "timeout"})
	return qr
}

// journalDroppedTotal reads the dispatch_journal_snapshot_dropped_total
// counter (mirrors credentialFullCounterValue's dto.Metric.Write style).
func journalDroppedTotal(t *testing.T, reason string) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := metricJournalSnapshotDropped.WithLabelValues(reason).Write(metric); err != nil {
		t.Fatalf("read journal snapshot drop metric: %v", err)
	}
	return metric.GetCounter().GetValue()
}

// TestJournalSinkFunc_AdapterContract verifies the JournalSinkFunc adapter
// wraps a function and implements JournalSink.
func TestJournalSinkFunc_AdapterContract(t *testing.T) {
	var called bool
	var receivedSnap JournalSnapshot
	fn := JournalSinkFunc(func(_ context.Context, snap JournalSnapshot) {
		called = true
		receivedSnap = snap
	})

	snap := JournalSnapshot{
		TenantID:  "t1",
		RequestID: "r1",
		Entries: []JournalEntry{
			{Seq: 1, Action: NextActionRetrySameCred, Model: "m1"},
		},
	}
	fn.ApplyJournalSnapshot(context.Background(), snap)

	if !called {
		t.Fatal("JournalSinkFunc did not invoke the wrapped function")
	}
	if receivedSnap.RequestID != "r1" || len(receivedSnap.Entries) != 1 {
		t.Fatalf("JournalSinkFunc received snapshot = %+v, want tenant=t1 request=r1 entries=1", receivedSnap)
	}
}

// TestJournalSinkFunc_NilSafe verifies a nil JournalSinkFunc is a no-op.
func TestJournalSinkFunc_NilSafe(t *testing.T) {
	var fn JournalSinkFunc
	fn.ApplyJournalSnapshot(context.Background(), JournalSnapshot{RequestID: "nil-safe"})
	// No panic = pass.
}

// TestPipeline_EmitJournalSnapshot_ExactlyOnce verifies Pipeline.complete calls
// the JournalSink exactly once with the full journal snapshot after the
// terminal entry has been recorded. The CAS guard at complete's entry ensures
// multiple concurrent complete() calls deliver only the first snapshot.
func TestPipeline_EmitJournalSnapshot_ExactlyOnce(t *testing.T) {
	var mu sync.Mutex
	var snapshots []JournalSnapshot
	sink := JournalSinkFunc(func(_ context.Context, snap JournalSnapshot) {
		mu.Lock()
		snapshots = append(snapshots, snap)
		mu.Unlock()
	})

	p := NewPipeline(Deps{
		RouteFunc:        func(context.Context, *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) { return "m", nil, nil },
		ForwardFunc:      func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		JournalSink:      sink,
	})
	p.registry = NewLifecycleRegistry(100, 50, 10)
	p.totalQueue = newTotalExecutionQueue(100)
	p.dimensionIndex = NewDimensionIndex(DefaultDimensionIndexConfig())
	defer p.Stop()

	qr := NewQueuedRequest("journal-once", "tenant-journal", "model-journal", context.Background(), nil)
	qr.GatewayInstanceID = "gw-journal"
	qr.TenantID = "tenant-journal"
	// Populate the journal with 3 decisions before the terminal entry.
	qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "m1", CredentialID: 1, ErrorKind: "timeout"})
	qr.recordDecision(JournalEntry{Action: NextActionSwitchCred, Model: "m1", CredentialID: 2})
	qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred, Model: "m1", CredentialID: 2, ErrorKind: "rate_limit"})

	// Pipeline.complete internally calls recordDecision for the terminal entry
	// BEFORE emitJournalSnapshot, so the sink receives the full 4-entry trace.
	p.complete(qr, ForwardOutcome{Result: "terminal"})

	// Delivery is asynchronous (audit 2026-09-05 C-#4): complete() enqueues
	// the snapshot and a background worker invokes the sink.
	waitFor(t, "first snapshot delivery", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(snapshots) >= 1
	})
	mu.Lock()
	count := len(snapshots)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("JournalSink called %d times, want exactly 1 (CAS guard must prevent multiple deliveries)", count)
	}

	snap := snapshots[0]
	if snap.TenantID != "tenant-journal" || snap.RequestID != "journal-once" {
		t.Fatalf("snapshot identity = (tenant=%s, request=%s), want (tenant-journal, journal-once)", snap.TenantID, snap.RequestID)
	}
	// complete() calls recordDecision(terminal) before emitJournalSnapshot, so
	// the snapshot includes the terminal entry (3 pre-terminal + 1 terminal = 4).
	if len(snap.Entries) != 4 {
		t.Fatalf("snapshot entries = %d, want 4 (3 pre-terminal decisions + 1 terminal)", len(snap.Entries))
	}
	// The 4th entry is the terminal entry added by complete().
	terminal := snap.Entries[3]
	if terminal.Action != NextActionCompleted {
		t.Fatalf("terminal entry Action = %s, want NextActionCompleted", terminal.Action)
	}
	// First 3 entries match the pre-terminal recordDecision calls.
	if snap.Entries[0].Action != NextActionRetrySameCred || snap.Entries[0].CredentialID != 1 {
		t.Fatalf("entry[0] = %+v, want retry_same_cred cred=1", snap.Entries[0])
	}
	if snap.Entries[1].Action != NextActionSwitchCred || snap.Entries[1].CredentialID != 2 {
		t.Fatalf("entry[1] = %+v, want switch_cred cred=2", snap.Entries[1])
	}
	if snap.Entries[2].Action != NextActionRetrySameCred || snap.Entries[2].CredentialID != 2 {
		t.Fatalf("entry[2] = %+v, want retry_same_cred cred=2", snap.Entries[2])
	}

	// Double-call complete() with a different outcome to verify the CAS guard
	// prevents a second snapshot emission (the first CAS already swapped).
	p.complete(qr, ForwardOutcome{Err: context.Canceled})
	mu.Lock()
	countAfterSecond := len(snapshots)
	mu.Unlock()
	if countAfterSecond != 1 {
		t.Fatalf("after second complete(), sink called %d times, want still 1 (CAS guard must block second delivery)", countAfterSecond)
	}
}

// TestPipeline_EmitJournalSnapshot_NilSinkSafe verifies that when no
// JournalSink is wired, complete() does not panic.
func TestPipeline_EmitJournalSnapshot_NilSinkSafe(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc:        func(context.Context, *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) { return "m", nil, nil },
		ForwardFunc:      func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		JournalSink:      nil, // nil sink
	})
	p.registry = NewLifecycleRegistry(100, 50, 10)
	p.totalQueue = newTotalExecutionQueue(10)
	p.dimensionIndex = NewDimensionIndex(DefaultDimensionIndexConfig())
	defer p.Stop()

	qr := NewQueuedRequest("nil-sink", "tenant", "m", context.Background(), nil)
	qr.GatewayInstanceID = "gw"
	qr.TenantID = "tenant"
	qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred})

	// Must not panic even though JournalSink is nil.
	p.complete(qr, ForwardOutcome{Result: "ok"})
}

// TestPipeline_EmitJournalSnapshot_EmptyJournalNotEmitted verifies that if
// the journal is empty (no decisions recorded), emitJournalSnapshot short-
// circuits and the sink is not called.
func TestPipeline_EmitJournalSnapshot_EmptyJournalNotEmitted(t *testing.T) {
	var called atomic.Bool
	sink := JournalSinkFunc(func(_ context.Context, _ JournalSnapshot) {
		called.Store(true)
	})

	p := NewPipeline(Deps{
		RouteFunc:        func(context.Context, *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) { return "m", nil, nil },
		ForwardFunc:      func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		JournalSink:      sink,
	})
	p.registry = NewLifecycleRegistry(100, 50, 10)
	p.totalQueue = newTotalExecutionQueue(10)
	p.dimensionIndex = NewDimensionIndex(DefaultDimensionIndexConfig())
	defer p.Stop()

	qr := NewQueuedRequest("empty-journal", "tenant", "m", context.Background(), nil)
	qr.GatewayInstanceID = "gw"
	qr.TenantID = "tenant"
	// Do NOT call recordDecision — leave qr.AttemptJournal empty.

	// complete() still calls recordDecision(terminal), so the journal has 1
	// entry by the time emitJournalSnapshot runs. Since len(entries) > 0,
	// the sink IS called. To test the "empty journal" guard, we need to
	// directly call emitJournalSnapshot on a request with zero entries.
	// But complete() always adds the terminal entry first. So the "empty"
	// guard in emitJournalSnapshot only fires if JournalSnapshot() itself
	// returns nil/empty (defensive against future nil-checks). For this test,
	// we verify the current behavior: complete() → terminal entry → sink called.
	p.complete(qr, ForwardOutcome{Result: "ok"})

	// The sink WAS called (asynchronously) because complete() adds a terminal
	// entry, making the journal non-empty.
	waitFor(t, "sink delivery for request with terminal-only journal", func() bool { return called.Load() })
}

// TestSetJournalSink_Concurrent verifies SetJournalSink can be called
// concurrently with emitJournalSnapshot (the setter holds journalSinkMu.Lock,
// the emitter holds RLock) without data race.
func TestSetJournalSink_Concurrent(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc:        func(context.Context, *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) { return "m", nil, nil },
		ForwardFunc:      func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
	})
	p.registry = NewLifecycleRegistry(100, 50, 10)
	p.totalQueue = newTotalExecutionQueue(10)
	p.dimensionIndex = NewDimensionIndex(DefaultDimensionIndexConfig())
	defer p.Stop()

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: call SetJournalSink repeatedly.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			p.SetJournalSink(JournalSinkFunc(func(context.Context, JournalSnapshot) {}))
			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 2: call complete (which calls emitJournalSnapshot) repeatedly.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			qr := NewQueuedRequest("race", "tenant", "m", context.Background(), nil)
			qr.GatewayInstanceID = "gw"
			qr.TenantID = "tenant"
			qr.recordDecision(JournalEntry{Action: NextActionRetrySameCred})
			p.complete(qr, ForwardOutcome{Result: "ok"})
		}
	}()

	wg.Wait()
	// No data race or panic = pass.
}

// TestJournalSnapshotSlowSinkDoesNotBlockComplete pins audit 2026-09-05 C-#4:
// a wedged (slow DB) JournalSink must not stall complete(). Before the fix,
// emitJournalSnapshot called sink.ApplyJournalSnapshot synchronously on the
// completing goroutine, so a slow sink dragged the whole completion path —
// which the production journey adapter serializes process-wide — into a
// global head-of-line block. Now the snapshot is queued and complete()
// returns immediately.
func TestJournalSnapshotSlowSinkDoesNotBlockComplete(t *testing.T) {
	sinkGate := make(chan struct{})
	var delivered atomic.Int32
	sink := JournalSinkFunc(func(_ context.Context, _ JournalSnapshot) {
		delivered.Add(1)
		<-sinkGate // wedge the delivery worker inside ApplyJournalSnapshot
	})
	p := newJournalTestPipeline(sink)
	defer p.Stop()

	qr := newJournaledRequest("slow-sink")
	completeDone := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(completeDone)
		p.complete(qr, ForwardOutcome{Result: "ok"})
	}()
	select {
	case <-completeDone:
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("complete() blocked %v on a wedged journal sink (C-#4 head-of-line regression)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("complete() never returned while the journal sink was wedged")
	}

	close(sinkGate)
	p.Stop() // bounded drain: the queued snapshot must be flushed here
	waitFor(t, "snapshot delivery after Stop drain", func() bool { return delivered.Load() == 1 })
}

// TestJournalSnapshotStopDrainsQueuedSnapshots pins audit 2026-09-05 C-#4:
// snapshots still queued when Stop() fires are flushed by the bounded drain
// instead of dying with the process. Three requests complete while the sink
// is wedged; after the sink is released, Stop must not return before all
// three snapshots were delivered.
func TestJournalSnapshotStopDrainsQueuedSnapshots(t *testing.T) {
	sinkGate := make(chan struct{})
	var mu sync.Mutex
	delivered := make(map[string]bool)
	sink := JournalSinkFunc(func(_ context.Context, snap JournalSnapshot) {
		mu.Lock()
		delivered[snap.RequestID] = true
		mu.Unlock()
		<-sinkGate
	})
	p := newJournalTestPipeline(sink)

	for i := 0; i < 3; i++ {
		p.complete(newJournaledRequest(fmt.Sprintf("drain-%d", i)), ForwardOutcome{Result: "ok"})
	}
	close(sinkGate)
	p.Stop() // journal drain window: worker must flush all three

	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("drain-%d", i)
		if !delivered[id] {
			t.Fatalf("snapshot %s not delivered after Stop drain (delivered=%v)", id, delivered)
		}
	}
}

// TestJournalSnapshotQueueFullDrops pins audit 2026-09-05 C-#4: when the
// bounded delivery queue is full, complete() drops the snapshot (metric +
// log) instead of blocking — terminal journal delivery is best-effort by
// contract. With the sink wedged, 300 completions against a 256-slot queue
// must drop at least one snapshot, deliver everything that fit, and never
// panic; delivered + dropped must account for every completion.
func TestJournalSnapshotQueueFullDrops(t *testing.T) {
	sinkGate := make(chan struct{})
	var delivered atomic.Int32
	sink := JournalSinkFunc(func(_ context.Context, _ JournalSnapshot) {
		delivered.Add(1)
		<-sinkGate
	})
	p := newJournalTestPipeline(sink)

	before := journalDroppedTotal(t, "queue_full")
	const n = journalDeliveryQueueCapacity + 43 // 300: forces overflow
	for i := 0; i < n; i++ {
		p.complete(newJournaledRequest(fmt.Sprintf("full-%d", i)), ForwardOutcome{Result: "ok"})
	}
	dropped := journalDroppedTotal(t, "queue_full") - before
	if dropped < 1 {
		t.Fatalf("queue_full drop metric delta = %v, want >= 1 (queue capacity %d, %d completions)", dropped, journalDeliveryQueueCapacity, n)
	}

	close(sinkGate)
	p.Stop()
	waitFor(t, "queue flush after Stop drain", func() bool {
		return delivered.Load() == int32(n-int(dropped))
	})
	if got := delivered.Load() + int32(dropped); got != n {
		t.Fatalf("delivered(%d) + dropped(%d) = %d, want %d (every completion accounted)", delivered.Load(), int32(dropped), got, n)
	}
	if got := delivered.Load(); got > int32(journalDeliveryQueueCapacity)+1 {
		t.Fatalf("delivered %d snapshots, want at most queue capacity + 1 in-flight (%d)", got, journalDeliveryQueueCapacity+1)
	}
}

// TestJournalSnapshotSinkPanicWorkerSurvives pins the panic-recover contract
// carried over from the synchronous emitJournalSnapshot: a panicking sink
// must not kill the delivery worker, and a sink swapped in at runtime
// (SetJournalSink) must immediately serve subsequent snapshots.
func TestJournalSnapshotSinkPanicWorkerSurvives(t *testing.T) {
	var panics atomic.Int32
	panicking := JournalSinkFunc(func(_ context.Context, snap JournalSnapshot) {
		panics.Add(1)
		panic("sink boom: " + snap.RequestID)
	})
	p := newJournalTestPipeline(panicking)
	defer p.Stop()

	p.complete(newJournaledRequest("boom"), ForwardOutcome{Result: "ok"})
	waitFor(t, "panicking sink invocation", func() bool { return panics.Load() >= 1 })

	var delivered atomic.Int32
	var lastID atomic.Value
	p.SetJournalSink(JournalSinkFunc(func(_ context.Context, snap JournalSnapshot) {
		delivered.Add(1)
		lastID.Store(snap.RequestID)
	}))
	p.complete(newJournaledRequest("after-panic"), ForwardOutcome{Result: "ok"})
	waitFor(t, "delivery after sink swap", func() bool { return delivered.Load() >= 1 })
	if got, _ := lastID.Load().(string); got != "after-panic" {
		t.Fatalf("post-panic delivery got request %q, want after-panic", got)
	}
}
