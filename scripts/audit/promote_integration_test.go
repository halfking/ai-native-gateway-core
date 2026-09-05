//go:build integration

// Integration tests for the session_bodies / session_turns hot→partition
// promote paths against the isolated audit PostgreSQL container. These
// tests require:
//
//	TEST_AUDIT_ISOLATED_DB_URL=postgres://kxuser:audit_admin_pw_local_only@127.0.0.1:15433/llm_gateway?sslmode=disable
//
// and the kx-citus container running (see scripts/audit/start-isolated-pg.sh).
// Gated by the `integration` build tag so the default `go test ./...`
// run does not require the container.
//
// To run:
//
//	bash scripts/audit/start-isolated-pg.sh --recreate
//	bash scripts/audit/verify-promote-session-bodies-turns.sh
//
// The tests cover three scenarios called out as gaps in
// docs/audit/2026-08-31-4h-correction-audit.md §2.5:
//
//  1. promote_session_bodies_hot_to_partition moves hot rows into the
//     parent monthly partition, the unified view returns the same row
//     count, and a re-run on an empty hot is a no-op.
//  2. promote_session_turns_hot_to_partition_audit (the audit-only helper)
//     moves hot rows into the parent monthly partition; a re-run is a
//     no-op.
//  3. session_bodies_unified stays consistent across promote (the
//     subtle gotcha: writers MUST set partition_date to yesterday for
//     retention-eligible rows; the test pins that contract by seeding
//     yesterday's partition_date).
package promote

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const auditDBEnv = "TEST_AUDIT_ISOLATED_DB_URL"

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(auditDBEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", auditDBEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

func reset(t *testing.T, pool *pgxpool.Pool, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// TestPromote_SessionBodies_HotToUnifiedVisible seeds 50 rows in
// session_bodies_hot (with partition_date set to yesterday to satisfy
// the unified view's parent-side filter), calls
// promote_session_bodies_hot_to_partition, and asserts:
//
//   - the hot table is empty after promote
//   - the parent has 50 rows
//   - session_bodies_unified still returns 50 rows
//
// A re-run on the empty hot must return 0 (idempotency).
func TestPromote_SessionBodies_HotToUnifiedVisible(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	reset(t, pool,
		"TRUNCATE public.session_bodies_hot",
		"TRUNCATE public.session_bodies",
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_bodies_hot
		    (session_id, turn_no, tenant_id, request_id, ts,
		     request_delta, response_delta, partition_date)
		SELECT 'sess_' || g, g, 'tenant_prom', 'req_' || g,
		       NOW() - INTERVAL '2 days',
		       '{"role":"user"}'::jsonb, '{"role":"assistant"}'::jsonb,
		       CURRENT_DATE - INTERVAL '1 day'
		FROM generate_series(1, 50) g`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var moved int
	if err := pool.QueryRow(ctx,
		"SELECT promote_session_bodies_hot_to_partition('1 hour'::interval, 1000)",
	).Scan(&moved); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if moved != 50 {
		t.Errorf("promote moved = %d, want 50", moved)
	}

	var hot, parent, unified int
	if err := pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM public.session_bodies_hot),"+
			"(SELECT count(*) FROM public.session_bodies),"+
			"(SELECT count(*) FROM public.session_bodies_unified)",
	).Scan(&hot, &parent, &unified); err != nil {
		t.Fatalf("count: %v", err)
	}
	if hot != 0 {
		t.Errorf("hot = %d, want 0", hot)
	}
	if parent != 50 {
		t.Errorf("parent = %d, want 50", parent)
	}
	if unified != 50 {
		t.Errorf("unified = %d, want 50 (promote must preserve view visibility)", unified)
	}

	// Idempotency: second call returns 0.
	if err := pool.QueryRow(ctx,
		"SELECT promote_session_bodies_hot_to_partition('1 hour'::interval, 1000)",
	).Scan(&moved); err != nil {
		t.Fatalf("promote (idempotency): %v", err)
	}
	if moved != 0 {
		t.Errorf("idempotent moved = %d, want 0", moved)
	}
}

// TestPromote_SessionTurns_HotToParent installs the audit-only
// promote_session_turns_hot_to_partition_audit helper, seeds 30 hot
// rows, calls promote, and asserts hot=0 / parent=30 + idempotency.
//
// This is the same row-movement pattern that production migration 526
// uses against the 50+ column production schema; the audit-only helper
// trims the contract to the columns the isolated fixture has so we can
// exercise the logic without bootstrapping the full production schema.
func TestPromote_SessionTurns_HotToParent(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	reset(t, pool,
		"TRUNCATE public.session_turns_hot",
		"TRUNCATE public.session_turns",
	)
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition_audit(
		    p_retention INTERVAL DEFAULT '7 days',
		    p_batch_size INTEGER DEFAULT 5000
		)
		RETURNS BIGINT
		LANGUAGE plpgsql
		AS $func$
		DECLARE
		    v_moved BIGINT := 0;
		BEGIN
		    IF p_retention IS NULL OR p_retention <= INTERVAL '0 seconds' THEN
		        RAISE EXCEPTION 'p_retention must be a positive interval';
		    END IF;

		    WITH picked AS (
		        SELECT id FROM public.session_turns_hot
		        WHERE ts < NOW() - p_retention
		        ORDER BY ts
		        LIMIT p_batch_size
		        FOR UPDATE SKIP LOCKED
		    ), moved AS (
		        DELETE FROM public.session_turns_hot h
		        USING picked p
		        WHERE h.id = p.id
		        RETURNING h.id, h.session_id, h.turn_no, h.tenant_id, h.request_id, h.ts, h.partition_date
		    ), inserted AS (
		        INSERT INTO public.session_turns
		            (id, session_id, turn_no, tenant_id, request_id, ts, partition_date)
		        SELECT id, session_id, turn_no, tenant_id, request_id, ts, partition_date
		        FROM moved
		        RETURNING 1
		    )
		    SELECT count(*) INTO v_moved FROM inserted;

		    RETURN v_moved;
		END;
		$func$`); err != nil {
		t.Fatalf("install audit fn: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DROP FUNCTION IF EXISTS public.promote_session_turns_hot_to_partition_audit(INTERVAL, INTEGER)")
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_turns_hot
		    (session_id, turn_no, tenant_id, request_id, ts, partition_date,
		     submit_mode, model, provider, prompt_tokens, completion_tokens)
		SELECT 'sess_t_' || g, g, 'tenant_prom_t', 'req_t_' || g,
		       NOW() - INTERVAL '2 days',
		       CURRENT_DATE - INTERVAL '1 day',
		       'full', 'gpt-4o', 'openai', 100, 50
		FROM generate_series(1, 30) g`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var moved int
	if err := pool.QueryRow(ctx,
		"SELECT promote_session_turns_hot_to_partition_audit('1 hour'::interval, 1000)",
	).Scan(&moved); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if moved != 30 {
		t.Errorf("promote moved = %d, want 30", moved)
	}

	var hot, parent int
	if err := pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM public.session_turns_hot),"+
			"(SELECT count(*) FROM public.session_turns)",
	).Scan(&hot, &parent); err != nil {
		t.Fatalf("count: %v", err)
	}
	if hot != 0 {
		t.Errorf("hot = %d, want 0", hot)
	}
	if parent != 30 {
		t.Errorf("parent = %d, want 30", parent)
	}

	// Idempotency.
	if err := pool.QueryRow(ctx,
		"SELECT promote_session_turns_hot_to_partition_audit('1 hour'::interval, 1000)",
	).Scan(&moved); err != nil {
		t.Fatalf("promote (idempotency): %v", err)
	}
	if moved != 0 {
		t.Errorf("idempotent moved = %d, want 0", moved)
	}
}

// TestPromote_CandidateFailureLogs_RetentionZeroIsError guards the
// function's own precondition: a zero retention must reject before any
// rows move. Migration 628 (promote_candidate_failure_logs_hot_to_partition)
// enforces this contract; session_bodies / session_turns promote fns
// don't, so we pin the contract on the fn that does.
func TestPromote_CandidateFailureLogs_RetentionZeroIsError(t *testing.T) {
	pool := openPool(t)
	_, err := pool.Exec(context.Background(),
		"SELECT promote_candidate_failure_logs_hot_to_partition('0 seconds'::interval, 1000)")
	if err == nil {
		t.Error("promote_candidate_failure_logs_hot_to_partition must reject retention=0, got nil error")
	}
}
