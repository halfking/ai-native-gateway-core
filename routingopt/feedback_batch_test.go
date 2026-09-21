package routingopt

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =============================================================================
// Stub batch executor：绝不连真实 DB，只记录 pgx.Batch 往返
// =============================================================================

// recordingBatchExecutor captures every SendBatch round trip so tests can
// assert batching behaviour and (via order) the enrich-before-insert ordering.
type recordingBatchExecutor struct {
	mu      sync.Mutex
	batches [][]*pgx.QueuedQuery // one entry per SendBatch round trip
	order   []string             // global timeline: "mark:<id>" / "insert:<id>"
}

func (e *recordingBatchExecutor) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	qs := make([]*pgx.QueuedQuery, len(b.QueuedQueries))
	copy(qs, b.QueuedQueries)
	e.mu.Lock()
	e.batches = append(e.batches, qs)
	for _, q := range qs {
		id := "?"
		if s, ok := q.Arguments[0].(string); ok {
			id = s
		}
		e.order = append(e.order, "insert:"+id)
	}
	e.mu.Unlock()
	return &stubBatchResults{n: len(qs)}
}

// note appends an event to the shared timeline (used by enrich stubs).
func (e *recordingBatchExecutor) note(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.order = append(e.order, event)
}

func (e *recordingBatchExecutor) batchCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.batches)
}

func (e *recordingBatchExecutor) totalRows() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0
	for _, b := range e.batches {
		total += len(b)
	}
	return total
}

// requestIDs returns every inserted request_id in arrival order.
func (e *recordingBatchExecutor) requestIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for _, b := range e.batches {
		for _, q := range b {
			if s, ok := q.Arguments[0].(string); ok {
				ids = append(ids, s)
			}
		}
	}
	return ids
}

func (e *recordingBatchExecutor) snapshotOrder() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.order...)
}

// firstRow returns the arguments of the very first queued INSERT.
func (e *recordingBatchExecutor) firstRow() []any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.batches[0][0].Arguments
}

// stubBatchResults is a pgx.BatchResults that succeeds exactly n times.
type stubBatchResults struct{ n int }

func (r *stubBatchResults) Exec() (pgconn.CommandTag, error) {
	if r.n <= 0 {
		return pgconn.CommandTag{}, errors.New("stubBatchResults: no queued rows left")
	}
	r.n--
	return pgconn.CommandTag{}, nil
}

func (r *stubBatchResults) Query() (pgx.Rows, error) {
	return nil, errors.New("stubBatchResults: Query not implemented")
}

func (r *stubBatchResults) QueryRow() pgx.Row { return failRow{} }

func (r *stubBatchResults) Close() error { return nil }

type failRow struct{}

func (failRow) Scan(dest ...any) error {
	return errors.New("stubBatchResults: QueryRow not implemented")
}

// batchTestConfig builds a config with explicit tunables for determinism.
func batchTestConfig(capacity, size int, interval time.Duration, disableWorker bool) feedbackBatchConfig {
	return feedbackBatchConfig{
		QueueCapacity: capacity,
		BatchSize:     size,
		FlushInterval: interval,
		DisableWorker: disableWorker,
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

// =============================================================================
// 批量聚合：满 batchSize 条 → 恰好一次批量 INSERT
// =============================================================================

func TestFeedbackBatchWriter_BatchSizeThresholdFlushes(t *testing.T) {
	exec := &recordingBatchExecutor{}
	w := NewFeedbackBatchWriter(exec, batchTestConfig(100, 10, time.Hour, false), nil)

	for i := 0; i < 10; i++ {
		w.Enqueue(&FeedbackLog{RequestID: fmt.Sprintf("req-%d", i)})
	}
	waitFor(t, 2*time.Second, func() bool { return exec.totalRows() == 10 }, "10 batched inserts")

	if got := w.StatsBatchCount(); got != 1 {
		t.Fatalf("10 entries with batchSize=10 must produce exactly 1 batch INSERT, got %d", got)
	}
	if got := w.StatsFlushed(); got != 10 {
		t.Fatalf("flushed counter = %d, want 10", got)
	}
	if got := w.StatsDropped(); got != 0 {
		t.Fatalf("dropped counter = %d, want 0", got)
	}
}

// =============================================================================
// 定时刷盘：注入小间隔，批量阈值未到也按时写入
// =============================================================================

func TestFeedbackBatchWriter_TimerFlush(t *testing.T) {
	exec := &recordingBatchExecutor{}
	w := NewFeedbackBatchWriter(exec, batchTestConfig(100, 1000, 50*time.Millisecond, false), nil)

	for i := 0; i < 3; i++ {
		w.Enqueue(&FeedbackLog{RequestID: fmt.Sprintf("timer-%d", i)})
	}
	waitFor(t, 2*time.Second, func() bool { return exec.totalRows() == 3 }, "timer-driven flush of 3 entries")

	if got := w.StatsBatchCount(); got != 1 {
		t.Fatalf("timer flush should be a single batch INSERT, got %d batches", got)
	}
	if got := w.StatsFlushed(); got != 3 {
		t.Fatalf("flushed counter = %d, want 3", got)
	}
}

// =============================================================================
// Flush 零丢失：worker 未消费的队列条目全部落库后才返回
// =============================================================================

func TestFeedbackBatchWriter_FlushDrainsAll(t *testing.T) {
	exec := &recordingBatchExecutor{}
	// Worker parked: Flush is the only consumer, so zero-loss is deterministic.
	w := NewFeedbackBatchWriter(exec, batchTestConfig(100, 10, time.Hour, true), nil)

	const n = 7 // below the batch threshold
	for i := 0; i < n; i++ {
		w.Enqueue(&FeedbackLog{RequestID: fmt.Sprintf("flush-%d", i)})
	}

	w.Flush(context.Background())

	if got := exec.totalRows(); got != n {
		t.Fatalf("Flush must drain all %d queued entries, stub received %d", n, got)
	}
	if got := w.StatsFlushed(); got != n {
		t.Fatalf("flushed counter = %d, want %d", got, n)
	}
	if got := w.StatsBatchCount(); got != 1 {
		t.Fatalf("Flush should emit a single batch for %d entries, got %d batches", n, got)
	}
}

// =============================================================================
// 满丢弃计数：容量 2、不消费、连入 5 条 → dropped=3 且永不阻塞
// =============================================================================

func TestFeedbackBatchWriter_DropsWhenFullAndNeverBlocks(t *testing.T) {
	exec := &recordingBatchExecutor{}
	w := NewFeedbackBatchWriter(exec, batchTestConfig(2, 100, time.Hour, true), nil)

	start := time.Now()
	for i := 0; i < 5; i++ {
		w.Enqueue(&FeedbackLog{RequestID: fmt.Sprintf("drop-%d", i)})
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Enqueue must never block on a full queue, took %s", elapsed)
	}

	if got := w.StatsDropped(); got != 3 {
		t.Fatalf("capacity=2 with 5 enqueues must drop 3, got %d", got)
	}
	if got := w.StatsEnqueued(); got != 2 {
		t.Fatalf("enqueued counter = %d, want 2", got)
	}
	if got := w.StatsFlushed(); got != 0 {
		t.Fatalf("nothing consumed yet, flushed counter = %d, want 0", got)
	}

	// The 2 surviving entries are still flushable.
	w.Flush(context.Background())
	if got := exec.totalRows(); got != 2 {
		t.Fatalf("Flush after drops must write the 2 surviving entries, got %d", got)
	}
}

// =============================================================================
// ControlledSyncFallback=true → 旧同步路径（stub Insert 被同步调用）
// =============================================================================

func TestFeedbackIntegrator_ControlledSyncFallbackUsesLegacyInsert(t *testing.T) {
	writer := &stubFeedbackWriter{}
	affinity := &stubAffinity{}
	exec := &recordingBatchExecutor{}
	bw := NewFeedbackBatchWriter(exec, batchTestConfig(10, 10, time.Hour, true), nil)
	i := &FeedbackIntegrator{
		feedback: writer, enhancer: affinity, pool: nil,
		batch: bw, controlledSyncFallback: true,
	}

	if err := i.RecordFeedback(context.Background(), RoutingFeedback{
		RequestID: "sync-1", TaskType: "code",
		PredictedProvider: "model-a", IsSuccess: true, UserID: 7,
	}); err != nil {
		t.Fatalf("RecordFeedback failed: %v", err)
	}

	if len(writer.inserted) != 1 {
		t.Fatalf("fallback path must call Insert synchronously, got %d inserts", len(writer.inserted))
	}
	if got := exec.totalRows(); got != 0 {
		t.Fatalf("fallback path must not touch the batch writer, got %d batched rows", got)
	}
	if got := bw.StatsEnqueued(); got != 0 {
		t.Fatalf("fallback path must not enqueue, enqueued counter = %d", got)
	}
	if len(affinity.updated) != 1 || affinity.updated[0] != "7|code|model-a" {
		t.Fatalf("fallback path must keep affinity semantics, got %+v", affinity.updated)
	}
}

// =============================================================================
// 异步路径：入队非阻塞、不走同步 Insert、Flush 后行内容完整
// =============================================================================

func TestFeedbackIntegrator_AsyncEnqueueThenFlush(t *testing.T) {
	writer := &stubFeedbackWriter{}
	affinity := &stubAffinity{}
	exec := &recordingBatchExecutor{}
	bw := NewFeedbackBatchWriter(exec, batchTestConfig(10, 10, time.Hour, true), nil)
	i := &FeedbackIntegrator{feedback: writer, enhancer: affinity, pool: nil, batch: bw}

	if err := i.RecordFeedback(context.Background(), RoutingFeedback{
		RequestID: "async-1", TaskType: "chat",
		PredictedProvider: "model-b", IsSuccess: false,
		Latency: 1500 * time.Millisecond, Cost: 0.02, UserID: 42, SessionID: "sess-9",
	}); err != nil {
		t.Fatalf("RecordFeedback failed: %v", err)
	}

	if got := len(writer.inserted); got != 0 {
		t.Fatalf("async path must not call the synchronous Insert, got %d", got)
	}
	if got := bw.StatsEnqueued(); got != 1 {
		t.Fatalf("async path must enqueue exactly 1 entry, got %d", got)
	}
	if len(affinity.updated) != 1 || affinity.updated[0] != "42|chat|model-b" {
		t.Fatalf("async path must keep affinity semantics, got %+v", affinity.updated)
	}

	i.Flush(context.Background())

	if got := exec.totalRows(); got != 1 {
		t.Fatalf("Flush must persist the queued entry, stub received %d", got)
	}
	args := exec.firstRow()
	if id, ok := args[0].(string); !ok || id != "async-1" {
		t.Fatalf("request_id column = %+v, want async-1", args[0])
	}
	if success, ok := args[6].(bool); !ok || success {
		t.Fatalf("success column = %+v, want false", args[6])
	}
	if errType, ok := args[7].(*string); !ok || errType == nil || *errType != "unknown" {
		t.Fatalf("error_type column = %+v, want 'unknown' for failed requests", args[7])
	}
	if uid, ok := args[9].(*string); !ok || uid == nil || *uid != "42" {
		t.Fatalf("user_id column = %+v, want '42'", args[9])
	}
	if sess, ok := args[10].(*string); !ok || sess == nil || *sess != "sess-9" {
		t.Fatalf("session_id column = %+v, want 'sess-9'", args[10])
	}
}

// =============================================================================
// 标注回写先于 INSERT：worker 在批量刷盘前逐条 enrich
// =============================================================================

func TestFeedbackBatchWriter_EnrichRunsBeforeInsert(t *testing.T) {
	exec := &recordingBatchExecutor{}
	fb := &stubFeedbackWriter{}
	enrich := func(ctx context.Context, log *FeedbackLog) {
		// Mirrors FeedbackIntegrator.enrichFromHumanAnnotations:
		// annotation write-back on the row for this request.
		if err := fb.MarkHumanCorrection(ctx, log.RequestID, markHumanCorrectionParams{
			CorrectProvider: "human-a",
		}); err != nil {
			t.Errorf("MarkHumanCorrection failed: %v", err)
		}
		exec.note("mark:" + log.RequestID)
	}
	w := NewFeedbackBatchWriter(exec, batchTestConfig(100, 10, time.Hour, true), enrich)

	w.Enqueue(&FeedbackLog{RequestID: "enrich-a"})
	w.Enqueue(&FeedbackLog{RequestID: "enrich-b"})
	w.Flush(context.Background())

	if got := exec.totalRows(); got != 2 {
		t.Fatalf("Flush must persist both entries, stub received %d", got)
	}
	if len(fb.marked) != 2 {
		t.Fatalf("enrich must run per entry, got %d MarkHumanCorrection calls", len(fb.marked))
	}

	var insertSeen bool
	for _, ev := range exec.snapshotOrder() {
		switch {
		case ev == "mark:enrich-a" || ev == "mark:enrich-b":
			if insertSeen {
				t.Fatalf("annotation write-back must happen before any INSERT, timeline: %v", exec.snapshotOrder())
			}
		case ev == "insert:enrich-a" || ev == "insert:enrich-b":
			insertSeen = true
		}
	}
	if !insertSeen {
		t.Fatal("expected INSERT events in timeline")
	}
}

// safeAffinity is a concurrency-safe affinityUpdater stub: the async path
// calls UpdateUserAffinity from every RecordFeedback goroutine, so the
// shared single-threaded stubAffinity from optimizer_test.go cannot be used
// under -race.
type safeAffinity struct {
	mu      sync.Mutex
	updated []string
}

func (s *safeAffinity) UpdateUserAffinity(ctx context.Context, userID string, taskType string, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated = append(s.updated, userID+"|"+taskType+"|"+provider)
	return nil
}

// =============================================================================
// 并发压力：goroutine × N，-race 下零丢失、计数自洽
// =============================================================================

func TestFeedbackIntegrator_ConcurrentRecordFeedback(t *testing.T) {
	writer := &stubFeedbackWriter{}
	affinity := &safeAffinity{}
	exec := &recordingBatchExecutor{}
	bw := NewFeedbackBatchWriter(exec, batchTestConfig(10000, 100, time.Hour, true), nil)
	i := &FeedbackIntegrator{feedback: writer, enhancer: affinity, pool: nil, batch: bw}

	const goroutines, perG = 20, 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				if err := i.RecordFeedback(context.Background(), RoutingFeedback{
					RequestID:         fmt.Sprintf("conc-%d-%d", g, j),
					TaskType:          "code",
					PredictedProvider: "model-a",
					IsSuccess:         true,
					UserID:            g + 1,
				}); err != nil {
					t.Errorf("RecordFeedback failed: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	i.Flush(context.Background())

	const total = goroutines * perG
	if got := exec.totalRows(); got != total {
		t.Fatalf("zero-loss violated: stub received %d rows, want %d", got, total)
	}
	ids := exec.requestIDs()
	if len(ids) != total {
		t.Fatalf("request id count = %d, want %d", len(ids), total)
	}
	sort.Strings(ids)
	for k := 1; k < len(ids); k++ {
		if ids[k] == ids[k-1] {
			t.Fatalf("duplicate request_id %s persisted twice", ids[k])
		}
	}
	if got := bw.StatsEnqueued(); got != total {
		t.Fatalf("enqueued counter = %d, want %d", got, total)
	}
	if got := bw.StatsDropped(); got != 0 {
		t.Fatalf("dropped counter = %d, want 0 (queue large enough)", got)
	}
	if got := bw.StatsFlushed(); got != total {
		t.Fatalf("flushed counter = %d, want %d", got, total)
	}
	if got := bw.StatsBatchCount(); got != total/100 {
		t.Fatalf("batch counter = %d, want %d (chunks of 100)", got, total/100)
	}
	if got := len(writer.inserted); got != 0 {
		t.Fatalf("async path must never call the synchronous Insert, got %d", got)
	}
}

// =============================================================================
// nil 安全：未接线 / nil 写入器不 panic（Track C 指标可安全读取）
// =============================================================================

func TestFeedbackBatchWriter_NilSafety(t *testing.T) {
	var w *FeedbackBatchWriter
	w.Enqueue(&FeedbackLog{RequestID: "nil-1"}) // must not panic
	w.Flush(context.Background())               // must not panic

	i := &FeedbackIntegrator{feedback: &stubFeedbackWriter{}, enhancer: &stubAffinity{}}
	i.Flush(context.Background()) // no batch writer wired → no-op

	if got := w.StatsEnqueued() + w.StatsDropped() + w.StatsFlushed() + w.StatsBatchCount() + w.StatsFlushFailures(); got != 0 {
		t.Fatalf("nil writer counters must read 0, got %d", got)
	}
	if got := w.Counters(); got != (FeedbackCounters{}) {
		t.Fatalf("nil writer Counters snapshot must be zero, got %+v", got)
	}
}

// TestFeedbackBatchWriter_CountersProjection pins the Track C metrics seam:
// the Counters snapshot must match the Stats* accessors.
func TestFeedbackBatchWriter_CountersProjection(t *testing.T) {
	exec := &recordingBatchExecutor{}
	w := NewFeedbackBatchWriter(exec, batchTestConfig(2, 100, time.Hour, true), nil)

	for i := 0; i < 5; i++ {
		w.Enqueue(&FeedbackLog{RequestID: fmt.Sprintf("cnt-%d", i)})
	}
	w.Flush(context.Background())

	c := w.Counters()
	if c.Enqueued != 2 || c.Dropped != 3 || c.Flushed != 2 {
		t.Fatalf("counters snapshot = %+v, want {2 3 2}", c)
	}
}
