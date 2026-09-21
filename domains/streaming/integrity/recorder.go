package integrity

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"math"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Recorder is the minimal contract needed by detector.go and the
// executor/handler integration points. Both FormatAnomalyRecorder
// (in streaming/format_anomaly_recorder.go) and the integrity
// package use the same Exec surface, so we model the contract on
// FormatAnomalyExec.
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

// Exec is the minimal SQL exec surface required to insert into
// model_integrity_events. Same shape as FormatAnomalyExec in
// streaming/format_anomaly_recorder.go:15 so the two tables can be
// written by the same pool.
type Exec interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// PoolRecorder persists Events into model_integrity_events. nil-safe:
// a nil receiver is a no-op so the call site never needs a nil check.
type PoolRecorder struct {
	pool Exec
	// sampleRatio is the per-row probability for non-critical Events.
	// 0.0 = drop everything non-critical; 1.0 = record everything.
	// Configurable via LLM_GATEWAY_INTEGRITY_SAMPLE_RATIO; default 0.1.
	sampleRatio float64
	// rng is a per-instance hash-based pseudo RNG; we don't need
	// cryptographic randomness here, just a stable per-row toss.
	rngSeed atomic.Uint64

	// counters for /metrics and unit tests. atomic.
	recorded atomic.Uint64
	sampled  atomic.Uint64
	dropped  atomic.Uint64
}

// NewPoolRecorder creates a PoolRecorder backed by a pgxpool. A nil pool
// is allowed and the returned recorder is a no-op.
//
// sampleRatio is clamped to [0, 1]. A non-positive value defaults to 0.1
// (matches the production default set in cmd/gateway/main.go).
func NewPoolRecorder(pool *pgxpool.Pool, sampleRatio float64) *PoolRecorder {
	if sampleRatio <= 0 || sampleRatio > 1 {
		sampleRatio = 0.1
	}
	r := &PoolRecorder{
		pool:        pool,
		sampleRatio: sampleRatio,
	}
	r.rngSeed.Store(uint64(time.Now().UnixNano()))
	return r
}

// NewRecorderFromExec is a variant for tests that pass a fake Exec
// (e.g. pgxmock). nil-safe.
func NewRecorderFromExec(exec Exec, sampleRatio float64) *PoolRecorder {
	if sampleRatio <= 0 || sampleRatio > 1 {
		sampleRatio = 0.1
	}
	r := &PoolRecorder{
		pool:        exec,
		sampleRatio: sampleRatio,
	}
	r.rngSeed.Store(42)
	return r
}

// recordTimeout bounds the out-of-band write so a slow DB never stalls
// or fails the request that triggered the detection. Same shape as
// streaming/data_loss_anomaly.go:41 anomalyRecordTimeout.
const recordTimeout = 3 * time.Second

// Record inserts the event into model_integrity_events. Nil-safe: a nil
// receiver returns nil. Sampling is applied for non-critical severities;
// critical events are always recorded.
//
// The sample column is PII-safe by construction (see Event.Sample doc).
// We additionally truncate it to 1 KiB to keep the table small even if
// a caller misuses the field.
func (r *PoolRecorder) Record(parent context.Context, ev Event) error {
	if r == nil || r.pool == nil {
		return nil
	}
	if ev.AnomalyType == "" {
		return nil
	}
	if !shouldRecord(r.sampleRatio, r.nextRand(), ev.Severity) {
		r.dropped.Add(1)
		return nil
	}

	// Detach from the request context so client disconnects and
	// deadlined parent contexts do not abort the integrity write.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), recordTimeout)
	defer cancel()

	ev.Sample = TruncateForSample(ev.Sample, 1024)
	var contextJSON string
	if len(ev.Context) > 0 {
		b, err := json.Marshal(ev.Context)
		if err != nil {
			slog.Warn("integrity recorder: context marshal failed; dropping",
				"anomaly_type", ev.AnomalyType, "error", err)
			r.dropped.Add(1)
			return nil
		}
		if !json.Valid(b) {
			slog.Warn("integrity recorder: context marshal produced invalid JSON; dropping",
				"anomaly_type", ev.AnomalyType)
			r.dropped.Add(1)
			return nil
		}
		contextJSON = string(b)
	}

	const query = `
		INSERT INTO model_integrity_events (
			ts, request_id, tenant_id, application_id, api_key_id,
			provider_id, provider_code, credential_id,
			client_model, outbound_model, raw_model_name,
			anomaly_type, severity,
			expected_value, actual_value, sample, context
		) VALUES (
			now(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
				$11, $12,
				$13, $14, $15, $16::text::jsonb
			)
	`
	if _, err := r.pool.Exec(writeCtx, query,
		nullString(ev.RequestID),
		nullString(ev.TenantID),
		ev.ApplicationID,
		ev.APIKeyID,
		ev.ProviderID,
		nullString(ev.ProviderCode),
		ev.CredentialID,
		nullString(ev.ClientModel),
		nullString(ev.OutboundModel),
		nullString(ev.RawModel),
		string(ev.AnomalyType),
		string(ev.Severity),
		nullString(ev.ExpectedValue),
		nullString(ev.ActualValue),
		nullString(ev.Sample),
		contextJSON,
	); err != nil {
		slog.Warn("integrity recorder: insert failed",
			"anomaly_type", ev.AnomalyType, "request_id", ev.RequestID, "error", err)
		r.dropped.Add(1)
		return err
	}
	if ev.Severity == SeverityCritical {
		r.recorded.Add(1)
	} else {
		r.sampled.Add(1)
	}
	return nil
}

// Stats returns the lifetime record / sample / drop counters for the
// admin /metrics endpoint and for unit tests.
func (r *PoolRecorder) Stats() (recorded, sampled, dropped uint64) {
	if r == nil {
		return 0, 0, 0
	}
	return r.recorded.Load(), r.sampled.Load(), r.dropped.Load()
}

// nextRand returns a pseudo-random number in [0, 1). FNV-mixed with a
// per-row counter so collisions are vanishingly rare in practice.
func (r *PoolRecorder) nextRand() float64 {
	// Atomically reserve a distinct input value before hashing. The old
	// read-modify-write assignment raced under concurrent request traffic.
	seed := r.rngSeed.Add(1)
	mixed := fnvHash(seed)
	// Map uint64 -> [0, 1) by dividing by 2^63 after clearing the sign bit.
	return float64(mixed&math.MaxInt64) / float64(1<<63)
}

func fnvHash(x uint64) uint64 {
	h := fnv.New64a()
	// encoding/binary is overkill for a single uint64; use FNV's
	// own Write on a stack buffer.
	var buf [8]byte
	for i := 0; i < 8; i++ {
		buf[i] = byte(x >> (8 * i))
	}
	_, _ = h.Write(buf[:])
	return h.Sum64()
}

// shouldRecord returns true if the row should be persisted. Critical
// events are always recorded; everything else is sampled at sampleRatio.
func shouldRecord(sampleRatio float64, rand float64, sev Severity) bool {
	if sev == SeverityCritical {
		return true
	}
	return rand < sampleRatio
}

// nullString turns "" into nil for nullable text columns. The DB
// driver distinguishes "" (empty string) and NULL, and we want NULL
// for missing identifiers so dashboards and JOINs behave naturally.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TruncateForSample caps a string to maxLen runes with an ellipsis
// marker. Mirrors streaming/format_anomaly_recorder.go:289 so the
// two tables have consistent truncation behavior.
func TruncateForSample(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}

// LoadSampleRatioFromEnv reads LLM_GATEWAY_INTEGRITY_SAMPLE_RATIO and
// returns the parsed value (or the fallback). Helper so main.go doesn't
// have to know the env name.
func LoadSampleRatioFromEnv(fallback float64) float64 {
	raw := os.Getenv("LLM_GATEWAY_INTEGRITY_SAMPLE_RATIO")
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		return fallback
	}
	return v
}

// Compile-time assertion: PoolRecorder satisfies Recorder.
var _ Recorder = (*PoolRecorder)(nil)

// Ensure Exec interface is satisfied by pgxpool.Pool at compile time.
// We don't store a pgxpool here but this guards against drift.
var _ Exec = (*pgxpool.Pool)(nil)
