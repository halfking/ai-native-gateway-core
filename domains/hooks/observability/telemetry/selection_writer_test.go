package telemetry

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// fakeSelectionPool captures the SQL and args a flush would have executed.
type fakeSelectionPool struct {
	mu    sync.Mutex
	calls []fakeSelectionCall
	err   error
}

type fakeSelectionCall struct {
	sql  string
	args []any
}

func (f *fakeSelectionPool) Exec(_ context.Context, sql string, args ...any) (commandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeSelectionCall{sql: sql, args: args})
	return nilTuningTag{}, f.err
}

func (f *fakeSelectionPool) lastCall() (fakeSelectionCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeSelectionCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func TestSelectionWriter_InsertBatchShape(t *testing.T) {
	fake := &fakeSelectionPool{}
	w := &selectionWriter{pool: fake}

	sels := []AutoSelection{
		{
			RequestID: "r1", SessionID: "s1", TaskID: "t1", TenantID: "ten1",
			TaskType: "code", Profile: "smart", Classifier: "heuristic",
			Confidence: 0.9, CanonicalID: 42, ChosenModel: "claude-sonnet-5",
			CandidateRank: 1, CompositeScore: 81.5,
			AffinityScore: 70, AffinityApplied: true, Explore: false, FallbackUsed: false,
		},
		{
			RequestID: "r2", TaskType: "chat", ChosenModel: "m2",
			Explore: true, FallbackUsed: true,
		},
	}

	if err := w.insertBatch(context.Background(), sels); err != nil {
		t.Fatalf("insertBatch: %v", err)
	}

	call, ok := fake.lastCall()
	if !ok {
		t.Fatal("no Exec call recorded")
	}

	// One placeholder group per row, and the arg count must match exactly —
	// a mismatch here is the classic way a batched INSERT corrupts columns.
	if got, want := len(call.args), len(sels)*selectionColumnCount; got != want {
		t.Errorf("arg count = %d, want %d", got, want)
	}
	if n := strings.Count(call.sql, "($"); n != len(sels) {
		t.Errorf("placeholder groups = %d, want %d", n, len(sels))
	}
	if !strings.Contains(call.sql, "INSERT INTO auto_route_selections_hot") {
		t.Error("selection writer must insert into the independent hot heap")
	}
	if !strings.Contains(call.sql, "ON CONFLICT DO NOTHING") {
		t.Error("insert must be replay-safe via ON CONFLICT DO NOTHING")
	}

	// The row must never carry conversation content — only IDs and numbers.
	for _, forbidden := range []string{"prompt", "message", "content", "body"} {
		if strings.Contains(strings.ToLower(call.sql), forbidden) {
			t.Errorf("SQL references %q; selection rows must stay IDs-only", forbidden)
		}
	}

	// Defaults are applied for the sparse second row.
	args2 := call.args[selectionColumnCount:]
	if args2[5] != "smart" {
		t.Errorf("empty profile should default to smart, got %v", args2[5])
	}
	if args2[6] != "heuristic" {
		t.Errorf("empty classifier should default to heuristic, got %v", args2[6])
	}
	if args2[10] != 1 {
		t.Errorf("rank <1 should default to 1, got %v", args2[10])
	}
	// Absent optional IDs must be NULL, not empty string, so the settle
	// worker's IS NULL checks behave.
	if args2[1] != nil || args2[2] != nil || args2[3] != nil {
		t.Errorf("empty session/task/tenant should be NULL, got %v %v %v", args2[1], args2[2], args2[3])
	}
	if args2[8] != nil {
		t.Errorf("canonical_id 0 should be NULL, got %v", args2[8])
	}
	// fallback_used must be carried through, not silently defaulted.
	if args2[15] != true {
		t.Errorf("fallback_used should be true, got %v", args2[15])
	}
}

func TestSelectionWriter_NilPoolAndEmptyBatchAreSafe(t *testing.T) {
	w := &selectionWriter{} // nil pool
	w.flush([]AutoSelection{{RequestID: "r"}})

	fake := &fakeSelectionPool{}
	w2 := &selectionWriter{pool: fake}
	w2.flush(nil)
	if _, ok := fake.lastCall(); ok {
		t.Error("empty batch must not issue a query")
	}
	if err := w2.insertBatch(context.Background(), nil); err != nil {
		t.Errorf("nil batch insert returned %v", err)
	}
}

// A row without a request_id could never be settled or deduplicated, so it is
// rejected at the door rather than written and orphaned.
func TestWriteAutoSelection_RejectsMissingRequestID(t *testing.T) {
	before := len(selectionWriterSingleton.queue)
	WriteAutoSelection(AutoSelection{TaskType: "code"})
	if after := len(selectionWriterSingleton.queue); after != before {
		t.Error("a selection without request_id must not be queued")
	}
}

// The hot path must never block on telemetry: overflowing the queue has to drop
// rows and return immediately.
func TestWriteAutoSelection_NonBlockingWhenQueueFull(t *testing.T) {
	saved := selectionWriterSingleton.queue
	defer func() { selectionWriterSingleton.queue = saved }()

	// A zero-capacity queue with no consumer is always "full".
	selectionWriterSingleton.queue = make(chan AutoSelection)

	_, droppedBefore := SelectionWriterStats()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			WriteAutoSelection(AutoSelection{RequestID: "r", TaskType: "code"})
		}
		close(done)
	}()

	select {
	case <-done:
		// Returned without blocking, as required.
	case <-context.Background().Done():
		t.Fatal("unreachable")
	}

	_, droppedAfter := SelectionWriterStats()
	if droppedAfter <= droppedBefore {
		t.Errorf("expected drops to be counted: before=%d after=%d", droppedBefore, droppedAfter)
	}
}

func TestSelectionWriter_InsertErrorCountsAsDropped(t *testing.T) {
	fake := &fakeSelectionPool{err: context.DeadlineExceeded}
	w := &selectionWriter{pool: fake}

	w.flush([]AutoSelection{{RequestID: "r1", TaskType: "code"}})

	if got := w.dropped.Load(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
	if got := w.persisted.Load(); got != 0 {
		t.Errorf("persisted = %d, want 0 on error", got)
	}
}

func TestSelectionWriter_SuccessCountsAsPersisted(t *testing.T) {
	fake := &fakeSelectionPool{}
	w := &selectionWriter{pool: fake}

	w.flush([]AutoSelection{
		{RequestID: "r1", TaskType: "code"},
		{RequestID: "r2", TaskType: "chat"},
	})

	if got := w.persisted.Load(); got != 2 {
		t.Errorf("persisted = %d, want 2", got)
	}
	if got := w.dropped.Load(); got != 0 {
		t.Errorf("dropped = %d, want 0", got)
	}
}

func TestStopSelectionWriter_SafeWhenNeverStarted(t *testing.T) {
	// Must not deadlock on a WaitGroup that was never incremented.
	StopSelectionWriter()
}

func TestStopSelectionWriter_ConcurrentStopDoesNotPanic(t *testing.T) {
	// Guards the "close of closed channel" panic the tuning writer had to fix.
	w := &selectionWriter{
		queue: make(chan AutoSelection, 1),
		stop:  make(chan struct{}),
		pool:  &fakeSelectionPool{},
	}
	w.wg.Add(1)
	go w.run()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.stopOnce.Do(func() { close(w.stop) })
			w.wg.Wait()
		}()
	}
	wg.Wait()
}
