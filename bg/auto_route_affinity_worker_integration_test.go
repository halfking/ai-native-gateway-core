//go:build integration

// auto_route_affinity_worker_integration_test.go — R38 self-audit: the
// aggregate() synthetic-actor guard (R37 §四 #1 closing) had only been
// verified by string inspection. R37 lessons §1 ("SQL filter fixes must
// run on a real DB") and §3 ("声明 vs 验证") apply — this file runs the
// REAL aggregate method against a real PostgreSQL with the minimum
// schema needed by aggregate():
//
//   - auto_route_selections_hot (the aggregate reads auto_route_selections_all,
//     but the hot-only path is sufficient for this test — we skip the
//     partitioned parent by never inserting into it; the view falls back
//     to the hot rows on their own)
//   - request_logs_hot carrying origin_actor (the new LEFT JOIN)
//   - session_summaries (existing aggregate() dependency)
//
// Contract under test:
//   - real traffic row (origin_actor='') → aggregated into the (task, profile,
//     canonical, tenant) bucket
//   - goal-* row stamped with a real reward (the dangerous regression: a future
//     write path that backfills reward onto a synthetic selection must NOT
//     pollute task_model_affinity) → excluded by the new predicate
//   - internal loopback row stamped with a real reward → excluded
//   - goal-* row stamped with NULL reward (abandoned) → excluded by the
//     existing reward IS NOT NULL guard regardless of origin_actor
//     (documents that the predicate and the guard are belt-and-braces)
//
// Run:
//
//	go test -tags=integration -timeout 5m -count=1 -run TestAutoRouteAffinity ./bg
package bg

import (
	"context"
	"testing"
	"time"
)

const affinityWorkerSchema = `
CREATE TABLE public.request_logs_hot (
	request_id text PRIMARY KEY,
	origin_actor text,
	ts timestamptz NOT NULL DEFAULT NOW()
);
CREATE TABLE public.auto_route_selections_hot (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	partition_date date NOT NULL DEFAULT CURRENT_DATE,
	request_id text,
	task_type text,
	profile text,
	canonical_id bigint,
	chosen_model text,
	tenant_id text,
	reward double precision,
	reward_source text,
	settled_at timestamptz,
	session_id text,
	success boolean,
	latency_ms integer,
	cost_usd double precision,
	ts timestamptz NOT NULL DEFAULT NOW()
);
CREATE OR REPLACE VIEW public.auto_route_selections_all AS
SELECT id, request_id, session_id, NULL::bigint AS task_id, tenant_id, ts,
       task_type, profile, NULL::text AS classifier, NULL::double precision AS confidence,
       canonical_id, chosen_model, NULL::int AS candidate_rank,
       NULL::double precision AS composite_score, NULL::double precision AS affinity_score,
       FALSE AS affinity_applied, FALSE AS explore, FALSE AS fallback_used,
       success, latency_ms, cost_usd, reward, reward_source, settled_at,
       partition_date, NULL::text AS experiment_id, NULL::text AS treatment,
       NULL::int AS assignment_version, NULL::text AS assignment_key_hash,
       'hot'::text AS storage_tier
FROM public.auto_route_selections_hot;
CREATE TABLE public.session_summaries (
	session_key text PRIMARY KEY,
	health_score integer,
	error_count integer,
	request_count integer
);
`

// TestAutoRouteAffinity_AggregateExcludesSyntheticActors_RealDB seeds the
// schema above with one real row and three synthetic rows (goal shadow
// with reward, loopback with reward, goal shadow abandoned), then runs
// w.aggregate() and asserts exactly the real row is returned.
func TestAutoRouteAffinity_AggregateExcludesSyntheticActors_RealDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, cleanup := DispatchPostgresContainer(t, ctx, affinityWorkerSchema)
	defer cleanup()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	// Seed request_logs_hot with origin_actor for each synthetic / real row.
	// All rows settled within the affinity window so the WHERE clause's
	// s.settled_at >= NOW() - interval doesn't drop them.
	mustExec(`INSERT INTO public.request_logs_hot (request_id, origin_actor) VALUES
		('req-real', ''),
		('req-goal', 'goal-audit'),
		('req-loop', 'auto-title-generator'),
		('req-abandoned', 'goal-model-switch')`)

	// Seed auto_route_selections_hot: each row carries a (task, profile,
	// canonical, model, tenant) bucket shared with the real row so the
	// synthetic rows, if the predicate regressed, would inflate sampleCount
	// and pollute the bucket. All settled within the window.
	mustExec(`INSERT INTO public.auto_route_selections_hot
		(request_id, task_type, profile, canonical_id, chosen_model, tenant_id,
		 reward, reward_source, settled_at, ts)
		VALUES
		('req-real',      'chat', 'p1', 100, 'gpt-5',         't1', 0.80, 'request', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day' - INTERVAL '10 minutes'),
		('req-goal',      'chat', 'p1', 100, 'gpt-5',         't1', 0.95, 'request', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day' - INTERVAL '10 minutes'),
		('req-loop',      'chat', 'p1', 100, 'gpt-5',         't1', 0.95, 'request', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day' - INTERVAL '10 minutes'),
		('req-abandoned', 'chat', 'p1', 100, 'gpt-5',         't1', NULL, 'request', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day' - INTERVAL '10 minutes')`)

	// session_summaries: real session only; synthetic rows would needlessly
	// fan out the LEFT JOIN.
	mustExec(`INSERT INTO public.session_summaries (session_key, health_score, error_count, request_count)
		VALUES ('sess-real', 80, 1, 1)`)

	w := NewAutoRouteAffinityWorker(pool)
	aggs, err := w.aggregate(ctx)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(aggs) != 1 {
		t.Fatalf("aggregate returned %d buckets, want 1 (synthetic actors must be filtered out)", len(aggs))
	}
	a := aggs[0]
	if a.taskType != "chat" || a.profile != "p1" || a.canonicalID != 100 || a.tenantID != "t1" {
		t.Errorf("aggregate bucket = %+v, want {chat, p1, 100, gpt-5, t1}", a)
	}
	// sampleCount is the SUM of COUNT(*) over the bucket — only the real
	// row should count, so sampleCount must be 1 (NOT 4 if filter regressed).
	if a.sampleCount != 1 {
		t.Errorf("sampleCount = %d, want 1 (the goal/loopback/abandoned rows must be excluded by the predicate and the reward guard)", a.sampleCount)
	}
	// avgReward is the mean of reward over the surviving rows; with only
	// the real row surviving it equals 0.80 exactly.
	if a.avgReward != 0.80 {
		t.Errorf("avgReward = %f, want 0.80 (real row only; any synthetic row would pull this away from 0.80)", a.avgReward)
	}
}