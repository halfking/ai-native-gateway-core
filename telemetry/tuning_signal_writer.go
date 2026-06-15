package telemetry

// tuning_signal_writer.go — async write of tuning_signals rows for the
// auto-route feedback loop.
//
// Architecture: separate batching goroutine (independent from
// request_logs) to keep the hot path zero-allocation. The writer
// maintains its own channel + worker + timer; it never blocks the
// request handler even when the DB is slow or down.
//
// Lifecycle:
//   - relay/handler.go calls WriteTuningSignal(entry) after every
//     request_logs update.
//   - The entry is queued on a 4096-cap channel, batched up to 50
//     entries or 200ms, then flushed in a single multi-row INSERT.
//   - Errors are logged and dropped (best-effort, hot path is preserved).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	tuningQueueCap   = 4096
	tuningBatchSize  = 50
	tuningFlushDelay = 200 * time.Millisecond
	tuningWriteTO    = 5 * time.Second
)

// TuningSignal is the per-request feedback signal payload.
type TuningSignal struct {
	RequestID        string
	SessionID        string
	TaskType         string
	Classifier       string
	Confidence       float64
	ChosenModel      string
	CanonicalID      int
	SuccessScore     float64
	LatencyScore     float64
	CostScore        float64
	DriftFlag        bool
	QualityScore     float64
	LatencyMs        int
	CostUSD          float64
	PromptTokens     int
	CompletionTokens int
	SignalPayload    []byte // JSONB

	// Strategy identifies which classifier strategy produced the task
	// type for A/B comparison. Optional: empty string means "unknown
	// or A/B not enabled" (treated as pattern_layered by the admin UI).
	Strategy string
}

// tuningWriter manages the batching worker for tuning_signals.
type tuningWriter struct {
	queue chan TuningSignal
	wg    sync.WaitGroup
	stop  chan struct{}
	once  sync.Once
	pool  poolExec
}

// poolExec is the minimal interface from Client we depend on, so the
// writer can be unit-tested with a fake.
type poolExec interface {
	Exec(ctx context.Context, sql string, args ...any) (commandTag, error)
}

// commandTag is a tiny shim around pgx's CommandTag.
type commandTag interface {
	RowsAffected() int64
}

// pgxPoolAdapter wraps a *pgxpool.Pool to satisfy the poolExec interface.
// We intentionally accept the pool indirectly to keep this file free of
// the pgxpool import (the existing Client has it).
type pgxPoolAdapter struct{ exec func(ctx context.Context, sql string, args ...any) (PgxTag, error) }

type PgxTag interface{ RowsAffected() int64 }

// Adapter is the bridge called from main.go to wire the pgxpool into
// the tuning writer without coupling the writer package to pgx.
var Adapter struct {
	PoolExec func(ctx context.Context, sql string, args ...any) (PgxTag, error)
}

// adapterExec satisfies poolExec via the global Adapter.
type adapterExec struct{}

func (adapterExec) Exec(ctx context.Context, sql string, args ...any) (commandTag, error) {
	if Adapter.PoolExec == nil {
		return nilTuningTag{}, errNoTuningPool
	}
	return Adapter.PoolExec(ctx, sql, args...)
}

type nilTuningTag struct{}

func (nilTuningTag) RowsAffected() int64 { return 0 }

// errNoTuningPool is the sentinel for an unconfigured writer.
var errNoTuningPool = fmt.Errorf("tuning writer pool not configured")

var tuningWriterSingleton = &tuningWriter{
	queue: make(chan TuningSignal, tuningQueueCap),
	stop:  make(chan struct{}),
}

// StartTuningWriter launches the batching worker. Safe to call multiple times.
// The pool adapter is wired via the global Adapter.PoolExec in main.go.
func StartTuningWriter() {
	tuningWriterSingleton.once.Do(func() {
		tuningWriterSingleton.pool = adapterExec{}
		tuningWriterSingleton.wg.Add(1)
		go tuningWriterSingleton.run()
	})
}

// StopTuningWriter drains the queue and stops the worker.
func StopTuningWriter() {
	select {
	case <-tuningWriterSingleton.stop:
		// already stopped
	default:
		close(tuningWriterSingleton.stop)
		tuningWriterSingleton.wg.Wait()
	}
}

// WriteTuningSignal enqueues a feedback signal for async batched write.
// Non-blocking: if the queue is full, the signal is dropped with a warning
// (preserves the hot path's bounded latency).
func WriteTuningSignal(sig TuningSignal) {
	select {
	case tuningWriterSingleton.queue <- sig:
	default:
		RecordTuningSignalDropped()
		slog.Warn("tuning_signal queue full, dropping signal",
			"request_id", sig.RequestID)
	}
}

func (w *tuningWriter) run() {
	defer w.wg.Done()
	batch := make([]TuningSignal, 0, tuningBatchSize)
	timer := time.NewTimer(tuningFlushDelay)
	defer timer.Stop()

	for {
		select {
		case <-w.stop:
			// Drain remaining
			for {
				select {
				case s := <-w.queue:
					batch = append(batch, s)
					if len(batch) >= tuningBatchSize {
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
			if len(batch) >= tuningBatchSize {
				w.flush(batch)
				batch = batch[:0]
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(tuningFlushDelay)
			}
		case <-timer.C:
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}
			timer.Reset(tuningFlushDelay)
		}
	}
}

func (w *tuningWriter) flush(batch []TuningSignal) {
	if w.pool == nil {
		return
	}
	RecordTuningSignalBatch(len(batch))
	ctx, cancel := context.WithTimeout(context.Background(), tuningWriteTO)
	defer cancel()

	if err := w.insertBatch(ctx, batch); err != nil {
		slog.Warn("tuning_signals batch insert failed",
			"count", len(batch), "error", err)
		return
	}
	RecordSuccessfulBatch(batch)
}

// RecordSuccessfulBatch emits per-signal metrics after a successful
// flush, including the per-strategy A/B gauge.
func RecordSuccessfulBatch(signals []TuningSignal) {
	for _, s := range signals {
		RecordTuningSignalWritten(s.TaskType, s.Classifier, s.QualityScore)
		RecordStrategySignal(s.Strategy, s.TaskType, s.QualityScore, s.SuccessScore >= 1.0)
	}
}

// insertBatch writes a batch of signals in a single multi-row INSERT.
func (w *tuningWriter) insertBatch(ctx context.Context, signals []TuningSignal) error {
	if len(signals) == 0 {
		return nil
	}

	values := make([]string, 0, len(signals))
	args := make([]any, 0, len(signals)*17)
	for i, s := range signals {
		base := i * 17
		values = append(values, fmt.Sprintf(
			"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7,
			base+8, base+9, base+10, base+11, base+12, base+13, base+14,
			base+15, base+16, base+17,
		))
		var payloadJSON any
		if len(s.SignalPayload) > 0 {
			payloadJSON = string(s.SignalPayload)
		}
		args = append(args,
			s.RequestID,
			nullableString(s.SessionID),
			s.TaskType,
			s.Classifier,
			nullableFloat(s.Confidence),
			nullableString(s.ChosenModel),
			nullableInt(s.CanonicalID),
			s.SuccessScore,
			s.LatencyScore,
			s.CostScore,
			s.DriftFlag,
			s.QualityScore,
			nullableInt(s.LatencyMs),
			nullableFloat(s.CostUSD),
			nullableInt(s.PromptTokens),
			nullableInt(s.CompletionTokens),
			payloadJSON,
		)
	}

	query := `
INSERT INTO tuning_signals (
    request_id, session_id, task_type, classifier, confidence,
    chosen_model, canonical_id, success_score, latency_score, cost_score,
    drift_flag, quality_score, latency_ms, cost_usd, prompt_tokens,
    completion_tokens, signal_payload
) VALUES ` + joinStrings(values, ",") + `
ON CONFLICT DO NOTHING`

	_, err := w.pool.Exec(ctx, query, args...)
	return err
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func nullableInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
}

// MarshalTuningPayload serialises a payload to JSON. Returns nil,nil for
// nil input.
func MarshalTuningPayload(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// ComputeTuningSignalQuality is a self-contained copy of the autoroute
// feedback formula to avoid an import cycle (telemetry -> autoroute would
// invert the existing dependency graph). Keep weights in sync with
// autoroute.DefaultFeedbackWeights().
//
//  quality = 0.4*success + 0.3*latency_score + 0.2*cost_score + 0.1*(1-drift)
//  latency_score = 1 - clamp(latency/p95, 0, 1)  (default 0.5 if p95==0)
//  cost_score    = 1 - clamp(cost/p75, 0, 1)     (default 0.5 if p75==0)
func ComputeTuningSignalQuality(success bool, latencyMs, p95BaselineMs int, costUSD, p75Baseline float64, drift bool) float64 {
	successF := 0.0
	if success {
		successF = 1.0
	}
	latencyScore := 0.5
	if p95BaselineMs > 0 {
		ratio := float64(latencyMs) / float64(p95BaselineMs)
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		latencyScore = 1.0 - ratio
	}
	costScore := 0.5
	if p75Baseline > 0 {
		ratio := costUSD / p75Baseline
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		costScore = 1.0 - ratio
	}
	driftContrib := 0.0
	if !drift {
		driftContrib = 1.0
	}
	q := 0.4*successF + 0.3*latencyScore + 0.2*costScore + 0.1*driftContrib
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	return q
}
