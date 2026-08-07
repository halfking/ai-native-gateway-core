package cachemetrics

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDBRecorder_Record tests single event recording.
func TestDBRecorder_Record(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	dsn := os.Getenv("TEST_DB_URL")
	if dsn == "" {
		t.Skip("TEST_DB_URL not set")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	recorder := NewDBRecorder(pool)
	ctx := context.Background()

	now := time.Now()
	event := Event{
		TenantID:    "test-tenant",
		CacheLayer:  LayerSemantic,
		EventType:   EventHit,
		TokensSaved: 1200,
		SessionID:   "sess-123",
		RequestID:   "req-456",
		RecordedAt:  now,
	}

	err = recorder.Record(ctx, event)
	require.NoError(t, err)

	// Verify event was written
	var count int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cache_metrics
		WHERE tenant_id = $1 AND cache_layer = $2 AND event_type = $3
		  AND session_id = $4 AND request_id = $5
	`, event.TenantID, string(event.CacheLayer), string(event.EventType),
		event.SessionID, event.RequestID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Cleanup
	_, _ = pool.Exec(ctx, "DELETE FROM cache_metrics WHERE session_id = $1", event.SessionID)
}

// TestDBRecorder_RecordBatch tests batch event recording.
func TestDBRecorder_RecordBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	dsn := os.Getenv("TEST_DB_URL")
	if dsn == "" {
		t.Skip("TEST_DB_URL not set")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	recorder := NewDBRecorder(pool)
	ctx := context.Background()

	now := time.Now()
	events := []Event{
		{
			TenantID:    "test-tenant",
			CacheLayer:  LayerPrefix,
			EventType:   EventHit,
			TokensSaved: 500,
			SessionID:   "sess-batch-1",
			RecordedAt:  now,
		},
		{
			TenantID:    "test-tenant",
			CacheLayer:  LayerPrefix,
			EventType:   EventMiss,
			TokensSaved: 0,
			SessionID:   "sess-batch-1",
			RecordedAt:  now,
		},
	}

	err = recorder.RecordBatch(ctx, events)
	require.NoError(t, err)

	// Verify both events were written
	var count int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM cache_metrics
		WHERE tenant_id = $1 AND session_id = $2
	`, "test-tenant", "sess-batch-1").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Cleanup
	_, _ = pool.Exec(ctx, "DELETE FROM cache_metrics WHERE session_id = $1", "sess-batch-1")
}

// TestNullRecorder tests that NullRecorder is a no-op.
func TestNullRecorder(t *testing.T) {
	recorder := NullRecorder{}
	ctx := context.Background()

	err := recorder.Record(ctx, Event{
		TenantID:   "test",
		CacheLayer: LayerKV,
		EventType:  EventMiss,
	})
	assert.NoError(t, err)

	err = recorder.RecordBatch(ctx, []Event{})
	assert.NoError(t, err)
}

// TestDBRecorder_NilDB tests that DBRecorder with nil db is graceful no-op.
func TestDBRecorder_NilDB(t *testing.T) {
	recorder := NewDBRecorder(nil)
	ctx := context.Background()

	err := recorder.Record(ctx, Event{
		TenantID:   "test",
		CacheLayer: LayerDelta,
		EventType:  EventHit,
	})
	assert.NoError(t, err)
}
