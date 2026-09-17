//go:build integration

// auto_route_settle_worker_integration_test.go — R37 self-audit: the two
// R35-R2 synthetic-actor SQL filters shipped in bg/auto_route_settle_worker.go
// (loadTaskBaselines + the settleBatch mr LATERAL) had only been verified by
// string inspection — they were never executed against a real planner. This
// file runs the REAL worker methods against a real PostgreSQL:
//
//   - loadTaskBaselines must exclude gateway-synthetic rows (goal-% shadow
//     rounds / internal loopbacks) from the cohort p95/p75 baselines;
//   - settleBatch's mr LATERAL must exclude synthetic rows from the per-session
//     model_reqs/retry_count aggregation. Made observable through the session
//     attribution contract: with a 2-request session (1 real + 1 synthetic)
//     the real share is 0.5 < SessionAttributionThreshold(0.8) → the selection
//     must settle with reward_source='request'; a filter regression yields
//     model_reqs=2 → 1.0 ≥ 0.8 → 'session'.
//
// Run:
//
//	go test -tags=integration -timeout 5m -count=1 -run TestAutoRouteSettle ./bg
package bg

import (
	"context"
	"testing"
	"time"
)

const settleWorkerSchema = `
CREATE TABLE public.request_logs_hot (
	request_id text,
	success boolean,
	latency_ms integer,
	cost_usd double precision,
	canonical_id bigint,
	tenant_id text,
	gw_session_id text,
	task_type text,
	is_auto_request boolean,
	origin_actor text,
	routing_attempts jsonb,
	ts timestamptz NOT NULL DEFAULT NOW()
);
CREATE TABLE public.auto_route_selections_hot (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	partition_date date NOT NULL DEFAULT CURRENT_DATE,
	request_id text,
	task_type text,
	canonical_id bigint,
	ts timestamptz NOT NULL DEFAULT NOW(),
	session_id text,
	settled_at timestamptz,
	success boolean,
	latency_ms integer,
	cost_usd double precision,
	reward double precision,
	reward_source text,
	tenant_id text
);
CREATE TABLE public.session_summaries (
	session_key text PRIMARY KEY,
	health_score integer,
	error_count integer,
	request_count integer
);
`

func TestAutoRouteSettleBaselinesExcludeSyntheticActors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, cleanup := DispatchPostgresContainer(t, ctx, settleWorkerSchema)
	defer cleanup()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	// A real row with sane signals next to a synthetic row with extreme
	// signals: if the origin_actor filter regresses, the p95/p75 cohort
	// baselines jump to the synthetic values.
	mustExec(`INSERT INTO public.request_logs_hot
		(request_id, success, latency_ms, cost_usd, task_type, is_auto_request, origin_actor, ts)
		VALUES
		('req-real-b', TRUE, 1000, 1.0, 'code', TRUE, 'user-app', NOW()),
		('req-goal-b', TRUE, 100000, 50.0, 'code', TRUE, 'goal-audit', NOW())`)

	w := NewAutoRouteSettleWorker(pool)
	baselines, err := w.loadTaskBaselines(ctx)
	if err != nil {
		t.Fatalf("loadTaskBaselines: %v", err)
	}
	base, ok := baselines["code"]
	if !ok {
		t.Fatal("baseline for task_type 'code' missing")
	}
	if base.P95LatencyMs != 1000 {
		t.Errorf("p95 latency = %d, want 1000 (the goal-audit row's 100000 must be excluded from the cohort)", base.P95LatencyMs)
	}
	if base.P75CostUSD != 1.0 {
		t.Errorf("p75 cost = %f, want 1.0 (synthetic 50.0 must be excluded)", base.P75CostUSD)
	}
}

func TestAutoRouteSettleBatchMrLateralExcludesSyntheticActors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, cleanup := DispatchPostgresContainer(t, ctx, settleWorkerSchema)
	defer cleanup()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	// One settled selection whose session contains exactly two logged turns:
	// the real one and a goal-audit shadow round sharing gw_session_id +
	// canonical_id. The mr LATERAL must count only the real turn.
	mustExec(`INSERT INTO public.request_logs_hot
		(request_id, success, latency_ms, cost_usd, canonical_id, tenant_id, gw_session_id, task_type, is_auto_request, origin_actor, routing_attempts, ts)
		VALUES
		('req-sel', TRUE, 800, 0.5, 7, 't1', 'gs_sess1', 'code', TRUE, 'user-app', '[{"a":1}]', NOW()),
		('req-goal', TRUE, 900, 0.6, 7, 't1', 'gs_sess1', 'code', TRUE, 'goal-audit', '[{"a":1},{"a":2}]', NOW())`)
	// Session summary claims BOTH turns: only with the filter does the real
	// model carry 1/2 = 0.5 < 0.8 (no session attribution).
	mustExec(`INSERT INTO public.session_summaries (session_key, health_score, error_count, request_count)
		VALUES ('gs_sess1', 100, 0, 2)`)
	mustExec(`INSERT INTO public.auto_route_selections_hot
		(request_id, task_type, canonical_id, ts, session_id)
		VALUES ('req-sel', 'code', 7, NOW() - INTERVAL '10 minutes', 'gs_sess1')`)

	w := NewAutoRouteSettleWorker(pool)
	baselines, err := w.loadTaskBaselines(ctx)
	if err != nil {
		t.Fatalf("loadTaskBaselines: %v", err)
	}
	settled, _, err := w.settleBatch(ctx, baselines)
	if err != nil {
		t.Fatalf("settleBatch: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want 1", settled)
	}

	var reward float64
	var source string
	if err := pool.QueryRow(ctx, `SELECT reward, reward_source FROM public.auto_route_selections_hot WHERE request_id='req-sel'`).Scan(&reward, &source); err != nil {
		t.Fatalf("read settled row: %v", err)
	}
	if source != "request" {
		t.Errorf("reward_source = %q, want %q — %q means the mr LATERAL counted the goal-audit row (model_reqs 2/2 ≥ 0.8 threshold); the synthetic-actor filter regressed",
			source, "request", "session")
	}
	if reward <= 0 || reward > 1 {
		t.Errorf("reward = %f, want within (0,1]", reward)
	}
	// The synthetic turn itself must NOT have produced a settled selection.
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM public.auto_route_selections_hot WHERE request_id='req-goal'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("selection rows for the synthetic request appeared unexpectedly: %d", n)
	}
}
