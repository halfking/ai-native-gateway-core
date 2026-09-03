package telemetry

// selection_writer.go — async write of auto_route_selections rows.
//
// Ref: docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md
//
// Mirrors tuning_signal_writer.go deliberately: a separate batching goroutine
// with its own queue, so recording a routing decision can never add latency to
// the request that produced it, and a slow or dead database degrades into
// dropped telemetry rather than dropped traffic.
//
// Privacy: this row carries identifiers and numbers only — request_id,
// session_id, task_id, canonical_id plus the decision snapshot. No prompt text,
// no message content, no conversation detail. Keep it that way; the whole point
// of logging IDs is that the analysis never needs the payload.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	selectionQueueCap   = 4096
	selectionBatchSize  = 50
	selectionFlushDelay = 200 * time.Millisecond
	selectionWriteTO    = 5 * time.Second
)

// AutoSelection is one recorded `model=auto` match.
//
// Outcome fields are deliberately absent: they are backfilled later by
// AutoRouteSettleWorker once the request has settled, because latency and cost
// are not known at decision time.
type AutoSelection struct {
	RequestID string
	SessionID string
	TaskID    string
	TenantID  string

	TaskType    string
	Profile     string
	Classifier  string
	Confidence  float64
	CanonicalID int64
	ChosenModel string

	CandidateRank   int
	CompositeScore  float64
	AffinityScore   float64
	AffinityApplied bool
	Explore         bool
	FallbackUsed    bool

	// Treatment attribution is recorded only when the request enrolled in the
	// enabled rollout. Empty values represent an unenrolled request.
	ExperimentID      string
	Treatment         string
	AssignmentVersion string
	AssignmentKeyHash string
}

type selectionWriter struct {
	queue     chan AutoSelection
	wg        sync.WaitGroup
	stop      chan struct{}
	once      sync.Once
	stopOnce  sync.Once
	pool      poolExec
	dropped   atomic.Int64
	persisted atomic.Int64
}

var selectionWriterSingleton = &selectionWriter{
	queue: make(chan AutoSelection, selectionQueueCap),
	stop:  make(chan struct{}),
}

var selectionStarted atomic.Bool

// StartSelectionWriter launches the batching worker. Safe to call repeatedly.
// Reuses the same Adapter.PoolExec bridge as the tuning writer.
func StartSelectionWriter() {
	selectionWriterSingleton.once.Do(func() {
		selectionWriterSingleton.pool = adapterExec{}
		selectionWriterSingleton.wg.Add(1)
		selectionStarted.Store(true)
		go selectionWriterSingleton.run()
	})
}

// StopSelectionWriter drains the queue and stops the worker.
func StopSelectionWriter() {
	if !selectionStarted.Load() {
		return
	}
	selectionWriterSingleton.stopOnce.Do(func() {
		close(selectionWriterSingleton.stop)
	})
	selectionWriterSingleton.wg.Wait()
}

// WriteAutoSelection enqueues a selection for async batched write.
// Non-blocking: a full queue drops the row and increments a counter rather than
// stalling the request path.
func WriteAutoSelection(sel AutoSelection) {
	if sel.RequestID == "" {
		return // without a request_id the row cannot be settled or deduplicated
	}
	select {
	case selectionWriterSingleton.queue <- sel:
	default:
		selectionWriterSingleton.dropped.Add(1)
		RecordAutoSelectionDropped()
		slog.Warn("auto_route_selections queue full, dropping row",
			"request_id", sel.RequestID)
	}
}

// SelectionWriterStats reports counters for observability and tests.
func SelectionWriterStats() (persisted, dropped int64) {
	return selectionWriterSingleton.persisted.Load(),
		selectionWriterSingleton.dropped.Load()
}

func (w *selectionWriter) run() {
	defer w.wg.Done()
	batch := make([]AutoSelection, 0, selectionBatchSize)
	timer := time.NewTimer(selectionFlushDelay)
	defer timer.Stop()

	for {
		select {
		case <-w.stop:
			for {
				select {
				case s := <-w.queue:
					batch = append(batch, s)
					if len(batch) >= selectionBatchSize {
						w.flush(batch)
						batch = batch[:0]
					}
				default:
					if len(batch) > 0 {
						w.flush(batch)
					}
					return
				}
			}
		case s := <-w.queue:
			batch = append(batch, s)
			if len(batch) >= selectionBatchSize {
				w.flush(batch)
				batch = batch[:0]
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(selectionFlushDelay)
			}
		case <-timer.C:
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}
			timer.Reset(selectionFlushDelay)
		}
	}
}

func (w *selectionWriter) flush(batch []AutoSelection) {
	if w.pool == nil || len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), selectionWriteTO)
	defer cancel()

	if err := w.insertBatch(ctx, batch); err != nil {
		w.dropped.Add(int64(len(batch)))
		RecordAutoSelectionDropped()
		slog.Warn("auto_route_selections batch insert failed",
			"count", len(batch), "error", err)
		return
	}
	w.persisted.Add(int64(len(batch)))
	for _, s := range batch {
		RecordAutoSelectionWritten(s.TaskType, s.AffinityApplied, s.Explore)
	}
}

const selectionColumnCount = 20

func (w *selectionWriter) insertBatch(ctx context.Context, sels []AutoSelection) error {
	values := make([]string, 0, len(sels))
	args := make([]any, 0, len(sels)*selectionColumnCount)

	for i, s := range sels {
		base := i * selectionColumnCount
		values = append(values, fmt.Sprintf(
			"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8,
			base+9, base+10, base+11, base+12, base+13, base+14, base+15, base+16,
			base+17, base+18, base+19, base+20,
		))

		profile := s.Profile
		if profile == "" {
			profile = "smart"
		}
		classifier := s.Classifier
		if classifier == "" {
			classifier = "heuristic"
		}
		rank := s.CandidateRank
		if rank < 1 {
			rank = 1
		}

		args = append(args,
			s.RequestID,
			nullableString(s.SessionID),
			nullableString(s.TaskID),
			nullableString(s.TenantID),
			s.TaskType,
			profile,
			classifier,
			nullableFloat(s.Confidence),
			nullableInt64(s.CanonicalID),
			s.ChosenModel,
			rank,
			nullableFloat(s.CompositeScore),
			nullableFloat(s.AffinityScore),
			s.AffinityApplied,
			s.Explore,
			s.FallbackUsed,
			nullableString(s.ExperimentID),
			nullableString(s.Treatment),
			nullableString(s.AssignmentVersion),
			nullableString(s.AssignmentKeyHash),
		)

	}

	// ON CONFLICT DO NOTHING pairs with uq_ars_request (request_id,
	// partition_date): a replayed or retried write is a no-op rather than a
	// duplicate sample, which matters because duplicates would inflate
	// sample_count and skew the learned ranking.
	query := `
INSERT INTO auto_route_selections (
    request_id, session_id, task_id, tenant_id,
    task_type, profile, classifier, confidence,
    canonical_id, chosen_model, candidate_rank,
    composite_score, affinity_score, affinity_applied, explore,
    fallback_used, experiment_id, treatment, assignment_version,
    assignment_key_hash
) VALUES ` + joinStrings(values, ",") + `
ON CONFLICT DO NOTHING`

	_, err := w.pool.Exec(ctx, query, args...)
	return err
}

func nullableInt64(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}
