package trace

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/internal/probelane"
	"github.com/redis/go-redis/v9"
)

func TestProbeLaneTracePersistencePG17(t *testing.T) {
	pool := retirementPG(t)
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	rec := NewRedisRecorder(rdb)
	const id = "probe-lane-audit"
	const raw = `{"events":[{"seq":1,"stage":"request_complete","module":"handler","timestamp":"2026-11-09T00:00:00Z","status":"success"}],"final_status":"success"}`
	if _, err := pool.Exec(ctx, `INSERT INTO session_turns_probe_hot(request_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, keyFor(id), raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rec.FlushToPG(ctx, pool, id); err != nil {
		t.Fatalf("probe parent exists but trace flush failed: %v", err)
	}
	if mini.Exists(keyFor(id)) {
		t.Fatal("durable probe trace retained in Redis")
	}
	// The physical-lane switch controls new writes, not readability of traces
	// already persisted to their probe parent.
	probelane.SetPhysicalEnabledForTest(false)
	t.Cleanup(probelane.ResetPhysicalOverrideForTest)
	trace, err := LoadFromPG(ctx, pool, id)
	if err != nil || trace == nil || trace.FinalStatus != FinalSuccess || len(trace.Events) != 1 {
		t.Fatalf("persisted probe trace cannot be loaded: trace=%+v err=%v", trace, err)
	}
	var stages, business int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM request_stage_events WHERE request_id=$1),
		(SELECT count(*) FROM session_turns_hot WHERE request_id=$1)+
		(SELECT count(*) FROM session_turns WHERE request_id=$1)`, id).Scan(&stages, &business); err != nil {
		t.Fatal(err)
	}
	if stages != 1 || business != 0 {
		t.Fatalf("probe persistence stages=%d business_rows=%d, want 1/0", stages, business)
	}
	if err := rec.FlushToPG(ctx, pool, id); err != nil {
		t.Fatalf("already-flushed trace is not idempotent: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE request_stage_events RENAME TO temporarily_missing_events`); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, keyFor(id), raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rec.FlushToPG(ctx, pool, id); err == nil || !mini.Exists(keyFor(id)) {
		t.Fatalf("stage write failure must retain Redis trace: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE temporarily_missing_events RENAME TO request_stage_events`); err != nil {
		t.Fatal(err)
	}
	if err := rec.FlushToPG(ctx, pool, id); err != nil || mini.Exists(keyFor(id)) {
		t.Fatalf("stage retry did not persist and remove Redis: %v", err)
	}
	if err := rdb.Set(ctx, keyFor("missing-parent"), raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rec.FlushToPG(ctx, pool, "missing-parent"); !errors.Is(err, ErrTraceParentNotFound) || !mini.Exists(keyFor("missing-parent")) {
		t.Fatalf("missing parent must retain Redis for retry: %v", err)
	}
}

func TestProbeLaneTraceMigrationPG17(t *testing.T) {
	pool := retirementPG(t)
	ctx := context.Background()
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	apply := func(suffix string) {
		t.Helper()
		body, err := os.ReadFile("../../sql/migrations/startup/954_probe_lane_trace_events" + suffix + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, strings.ReplaceAll(string(body), "public.", schema+".")); err != nil {
			t.Fatal(err)
		}
	}
	apply(".down")
	if _, err := pool.Exec(ctx, `INSERT INTO session_turns_probe_hot(request_id) VALUES('before-migration')`); err != nil {
		t.Fatal(err)
	}
	var before, after uint32
	if err := pool.QueryRow(ctx, `SELECT relfilenode FROM pg_class WHERE oid='session_turns_probe_hot'::regclass`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	apply("")
	apply("")
	if err := pool.QueryRow(ctx, `SELECT relfilenode FROM pg_class WHERE oid='session_turns_probe_hot'::regclass`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_turns_probe_hot WHERE trace_events IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if before != after || nulls != 1 {
		t.Fatalf("954 rewrote the table or changed existing rows: relfilenode %d→%d nulls=%d", before, after, nulls)
	}
	var nullableWithoutDefault int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_attribute
		WHERE attrelid='session_turns_probe_hot'::regclass AND attname='trace_events'
		AND NOT attisdropped AND NOT attnotnull AND NOT atthasdef`).Scan(&nullableWithoutDefault); err != nil || nullableWithoutDefault != 1 {
		t.Fatalf("954 column must be nullable without a default: count=%d err=%v", nullableWithoutDefault, err)
	}
	apply(".down")
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version='954'`).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("954 rollback ledger: count=%d err=%v", versions, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_attribute WHERE attrelid='session_turns_probe_hot'::regclass
		AND attname='trace_events' AND NOT attisdropped`).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("954 rollback retained its column: count=%d err=%v", versions, err)
	}
}
