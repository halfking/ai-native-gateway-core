// P2.2 Track B: FeedbackIntegrator 的异步批量反馈落库。
//
// 纪律（路由优先于数据）：RecordFeedback 热路径只做一次非阻塞入队；
// 写库全部由后台 worker 批量完成 —— 满 BatchSize 条或每 FlushInterval
// 用 pgx.Batch 一次往返写入。队列满时丢弃并计数，绝不阻塞调用方。
// 任何失败只 slog 增量日志 + 原子计数，不影响路由热路径。
package routingopt

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// batchExecutor abstracts the pgx batch round trip so tests can stub the
// database (tests never connect to a real DB). *pgxpool.Pool satisfies this
// interface natively.
type batchExecutor interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// feedbackBatchInsertSQL is the batched INSERT executed by
// FeedbackBatchWriter. Column-for-column identical to FeedbackLogDAO.Insert
// (dao.go), minus the RETURNING id clause: batch writes never consume the
// generated ids, so there is nothing to scan back.
const feedbackBatchInsertSQL = `
	INSERT INTO routing_feedback_log (
		request_id, task_type, predicted_provider, confidence,
		actual_latency_ms, actual_cost, success, error_type,
		profile, user_id, session_id,
		has_human_correction, correct_provider, correction_reason, annotator, annotated_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
`

// Defaults for feedbackBatchConfig (design: batch insert every 100 records
// or 5 seconds, queue bounded at 10000).
const (
	defaultFeedbackQueueCapacity = 10000
	defaultFeedbackBatchSize     = 100
	defaultFeedbackFlushInterval = 5 * time.Second
)

// feedbackBatchConfig holds the async writer tunables. Zero-valued size /
// duration fields fall back to the documented defaults; tests inject small
// values (and DisableWorker) to stay deterministic without real sleeps.
type feedbackBatchConfig struct {
	// QueueCapacity is the buffered-channel capacity (default 10000).
	QueueCapacity int

	// BatchSize is the entry count that triggers a batch INSERT (default 100).
	BatchSize int

	// FlushInterval is the periodic flush cadence (default 5s).
	FlushInterval time.Duration

	// DisableWorker keeps the background goroutine parked: entries stay in
	// the queue until Flush drains them. Tests use this to make Flush /
	// drop-count assertions fully deterministic. Production leaves it false.
	DisableWorker bool
}

// defaultFeedbackBatchConfig returns the production configuration.
func defaultFeedbackBatchConfig() feedbackBatchConfig {
	return feedbackBatchConfig{
		QueueCapacity: defaultFeedbackQueueCapacity,
		BatchSize:     defaultFeedbackBatchSize,
		FlushInterval: defaultFeedbackFlushInterval,
	}
}

// FeedbackBatchWriter queues FeedbackLog rows on a bounded channel and writes
// them in pgx.Batch round trips from a single background worker.
//
// The worker starts lazily on the first Enqueue (no goroutine for unused
// integrators) and keeps running forever — graceful shutdown uses Flush,
// which drains the queue and returns while the worker stays alive.
type FeedbackBatchWriter struct {
	cfg feedbackBatchConfig
	// exec is nil only for degenerate construction (no pool); writeBatch
	// then counts the entries as failed instead of panicking.
	exec batchExecutor
	// enrich runs the P2.1 annotation write-back (lookupAnnotation +
	// MarkHumanCorrection) for each entry BEFORE the batch INSERT, so the
	// inserted rows already carry has_human_correction. Best-effort.
	enrich func(ctx context.Context, log *FeedbackLog)

	queue     chan *FeedbackLog
	startOnce sync.Once

	// Atomic counters (read by Stats* accessors below).
	enqueued      atomic.Int64
	dropped       atomic.Int64
	flushed       atomic.Int64
	batchCount    atomic.Int64
	flushFailures atomic.Int64
}

// NewFeedbackBatchWriter wires an async batch writer. exec is typically a
// *pgxpool.Pool; enrich may be nil (no annotation write-back).
func NewFeedbackBatchWriter(exec batchExecutor, cfg feedbackBatchConfig, enrich func(context.Context, *FeedbackLog)) *FeedbackBatchWriter {
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = defaultFeedbackQueueCapacity
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultFeedbackBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultFeedbackFlushInterval
	}
	return &FeedbackBatchWriter{
		cfg:    cfg,
		exec:   exec,
		enrich: enrich,
		queue:  make(chan *FeedbackLog, cfg.QueueCapacity),
	}
}

// Enqueue queues one feedback row without ever blocking: when the queue is
// full the entry is dropped and counted (routing beats data). Also lazily
// starts the background worker on first use.
func (w *FeedbackBatchWriter) Enqueue(log *FeedbackLog) {
	if w == nil || log == nil {
		return
	}
	w.startWorker()
	select {
	case w.queue <- log:
		w.enqueued.Add(1)
	default:
		total := w.dropped.Add(1)
		// Incremental, rate-limited logging: every drop bumps the counter,
		// but only the first drop and every 1000th reach the log.
		if total == 1 || total%1000 == 0 {
			slog.WarnContext(context.Background(),
				"routingopt: feedback queue full, dropping feedback (routing first)",
				"dropped_total", total, "request_id", log.RequestID)
		}
	}
}

// Flush drains every entry still sitting in the queue and writes them in
// batches of cfg.BatchSize, returning once the queue is empty. The background
// worker keeps running afterwards, which keeps tests and repeated flushes
// simple. Safe to call concurrently with the worker (and with itself); the
// entries the worker already took are written by the worker, so nothing is
// lost either way.
func (w *FeedbackBatchWriter) Flush(ctx context.Context) {
	if w == nil {
		return
	}
	pending := make([]*FeedbackLog, 0, w.cfg.BatchSize)
	for {
		select {
		case e := <-w.queue:
			if e == nil {
				continue
			}
			pending = append(pending, e)
			if len(pending) >= w.cfg.BatchSize {
				w.writeBatch(ctx, pending)
				pending = pending[:0]
			}
		default:
			w.writeBatch(ctx, pending)
			return
		}
	}
}

// startWorker lazily launches the background flush loop on first enqueue.
func (w *FeedbackBatchWriter) startWorker() {
	if w.cfg.DisableWorker {
		return
	}
	w.startOnce.Do(func() { go w.run() })
}

// run is the background worker: write when BatchSize entries accumulate or
// on every FlushInterval tick. Runs forever; errors are logged and counted,
// never propagated. It uses a detached context: batch writes outlive the
// request that produced the feedback, and a slow DB must never take the
// routing hot path down with it (the bounded queue + drop counter is the
// backpressure story).
func (w *FeedbackBatchWriter) run() {
	ctx := context.Background()
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	buf := make([]*FeedbackLog, 0, w.cfg.BatchSize)
	for {
		select {
		case e := <-w.queue:
			if e == nil {
				continue
			}
			buf = append(buf, e)
			if len(buf) >= w.cfg.BatchSize {
				w.writeBatch(ctx, buf)
				buf = buf[:0]
			}
		case <-ticker.C:
			if len(buf) > 0 {
				w.writeBatch(ctx, buf)
				buf = buf[:0]
			}
		}
	}
}

// writeBatch executes one pgx.Batch round trip for the given entries.
// Order of operations matters: the P2.1 annotation write-back runs FIRST so
// the INSERTed rows already carry has_human_correction/correct_provider.
// Failures are counted + logged per entry; they never panic and never block.
func (w *FeedbackBatchWriter) writeBatch(ctx context.Context, entries []*FeedbackLog) {
	if len(entries) == 0 {
		return
	}
	// 1. Annotation write-back before INSERT (best-effort, per entry).
	if w.enrich != nil {
		for _, e := range entries {
			w.enrich(ctx, e)
		}
	}
	// 2. Degenerate construction (no executor): count as failures.
	if w.exec == nil {
		w.flushFailures.Add(int64(len(entries)))
		slog.WarnContext(ctx, "routingopt: feedback batch writer has no executor",
			"failed", len(entries))
		return
	}

	// 3. One round trip for the whole batch.
	batch := &pgx.Batch{}
	for _, e := range entries {
		batch.Queue(feedbackBatchInsertSQL,
			e.RequestID, e.TaskType, e.PredictedProvider, e.Confidence,
			e.ActualLatencyMs, e.ActualCost, e.Success, e.ErrorType,
			e.Profile, e.UserID, e.SessionID,
			e.HasHumanCorrection, e.CorrectProvider, e.CorrectionReason, e.Annotator, e.AnnotatedAt,
		)
	}
	w.batchCount.Add(1)

	br := w.exec.SendBatch(ctx, batch)
	for _, e := range entries {
		if _, err := br.Exec(); err != nil {
			w.flushFailures.Add(1)
			slog.WarnContext(ctx, "routingopt: batch feedback insert failed",
				"err", err, "request_id", e.RequestID)
			continue
		}
		w.flushed.Add(1)
	}
	if err := br.Close(); err != nil {
		// Rows were already accounted above; Close errors mean the
		// connection had trouble — log so the increments are explainable.
		slog.WarnContext(ctx, "routingopt: batch feedback close failed", "err", err)
	}
}

// =============================================================================
// Atomic counters (exported for P2.2 Track C: these will be taken over by
// Prometheus metrics — routingopt_feedback_{enqueued,dropped,flushed,batches,
// failures}_total — keep names stable when that lands).
// =============================================================================

// StatsEnqueued returns the number of entries accepted into the queue.
func (w *FeedbackBatchWriter) StatsEnqueued() int64 {
	if w == nil {
		return 0
	}
	return w.enqueued.Load()
}

// StatsDropped returns the number of entries shed because the queue was full.
func (w *FeedbackBatchWriter) StatsDropped() int64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// StatsFlushed returns the number of entries successfully INSERTed in batches.
func (w *FeedbackBatchWriter) StatsFlushed() int64 {
	if w == nil {
		return 0
	}
	return w.flushed.Load()
}

// StatsBatchCount returns the number of pgx.Batch round trips issued.
func (w *FeedbackBatchWriter) StatsBatchCount() int64 {
	if w == nil {
		return 0
	}
	return w.batchCount.Load()
}

// StatsFlushFailures returns the number of entries whose INSERT failed.
func (w *FeedbackBatchWriter) StatsFlushFailures() int64 {
	if w == nil {
		return 0
	}
	return w.flushFailures.Load()
}

// Counters returns the Track C snapshot projection consumed by
// metrics.AttachFeedbackCounters — wiring is a one-liner:
//
//	routingopt.AttachFeedbackCounters(writer.Counters)
//
// Concurrency-safe and non-blocking (atomic reads only). Zero value when the
// receiver is nil.
func (w *FeedbackBatchWriter) Counters() FeedbackCounters {
	if w == nil {
		return FeedbackCounters{}
	}
	return FeedbackCounters{
		Enqueued: uint64(w.enqueued.Load()),
		Dropped:  uint64(w.dropped.Load()),
		Flushed:  uint64(w.flushed.Load()),
	}
}
