// Package cachemetrics provides unified cache observability (docs/omni-ref3 D2).
//
// All cache layers (semantic/prefix/delta/kv/session_state) report hit/miss
// events to a unified CacheMetricsRecorder, which writes to cache_metrics table.
// Enables answering "overall cache hit rate" and "tokens saved" without
// scattered in-memory counters.
package cachemetrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CacheLayer identifies which cache layer emitted the metric.
type CacheLayer string

const (
	LayerSemantic     CacheLayer = "semantic"
	LayerPrefix       CacheLayer = "prefix"
	LayerDelta        CacheLayer = "delta"
	LayerKV           CacheLayer = "kv"
	LayerSessionState CacheLayer = "session_state"
)

// EventType identifies cache hit or miss.
type EventType string

const (
	EventHit  EventType = "hit"
	EventMiss EventType = "miss"
)

// Event is a single cache hit/miss event.
type Event struct {
	TenantID    string
	CacheLayer  CacheLayer
	EventType   EventType
	TokensSaved int    // tokens saved on hit (0 on miss)
	SessionID   string // optional context
	RequestID   string // optional context
	RecordedAt  time.Time
}

// Recorder writes cache metrics to cache_metrics table.
type Recorder interface {
	Record(ctx context.Context, event Event) error
	RecordBatch(ctx context.Context, events []Event) error
}

// NullRecorder is a no-op recorder (for tests or when metrics disabled).
type NullRecorder struct{}

func (NullRecorder) Record(ctx context.Context, event Event) error         { return nil }
func (NullRecorder) RecordBatch(ctx context.Context, events []Event) error { return nil }

// DBRecorder writes to cache_metrics table.
type DBRecorder struct {
	db *pgxpool.Pool
}

// NewDBRecorder creates a new DB-backed recorder.
func NewDBRecorder(db *pgxpool.Pool) *DBRecorder {
	return &DBRecorder{db: db}
}

// Record writes a single event.
func (r *DBRecorder) Record(ctx context.Context, event Event) error {
	if r.db == nil {
		return nil // graceful no-op when db is nil
	}

	_, err := r.db.Exec(ctx, `
		INSERT INTO cache_metrics (
			tenant_id, cache_layer, event_type, tokens_saved,
			session_id, request_id, recorded_at, partition_date
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		event.TenantID,
		string(event.CacheLayer),
		string(event.EventType),
		event.TokensSaved,
		nullIfEmpty(event.SessionID),
		nullIfEmpty(event.RequestID),
		event.RecordedAt,
		event.RecordedAt.Format("2006-01-02"),
	)
	return err
}

// RecordBatch writes multiple events in a single transaction (future optimization).
func (r *DBRecorder) RecordBatch(ctx context.Context, events []Event) error {
	if r.db == nil || len(events) == 0 {
		return nil
	}

	// Simple implementation: loop (future: use COPY or batch insert)
	for _, e := range events {
		if err := r.Record(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
