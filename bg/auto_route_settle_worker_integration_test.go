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
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const settleWorkerSchema = `
CREATE TABLE request_logs_hot (
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
CREATE TABLE auto_route_selections_hot (
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
CREATE TABLE session_summaries (
	session_key text PRIMARY KEY,
	health_score integer,
	error_count integer,
	request_count integer
);
-- §9.44: the session family had NO table in this fixture, which is precisely
-- why §9.43's session branch shipped without ever being executed — the one
-- harness that could have run it could not even name the table. Column types
-- mirror the real table where the settle SQL touches them (verified against
-- the live DB: cost_usd is numeric(14,8) here but double precision in the
-- request_logs_hot fixture above, so the parity test also proves the SQL is
-- insensitive to that difference).
CREATE TABLE session_turns_hot (
	session_id text NOT NULL,
	turn_no integer NOT NULL,
	tenant_id text NOT NULL,
	request_id text NOT NULL,
	success boolean,
	latency_ms integer,
	cost_usd numeric(14,8),
	canonical_id bigint,
	task_type text,
	is_auto_request boolean,
	origin_actor text,
	routing_attempts jsonb,
	ts timestamptz NOT NULL DEFAULT NOW()
);
`

func TestAutoRouteSettleBaselinesExcludeSyntheticActors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, cleanup := DispatchPostgresContainer(t, ctx, settleWorkerSchema)
	defer cleanup()

	// The three tables below are written UNQUALIFIED on purpose. The fixture
	// DDL (settleWorkerSchema / affinityWorkerSchema) is unqualified too, so it
	// lands in this test's per-test schema, and the code under test reads these
	// tables unqualified as well — measured 2026-10-02:
	//
	//	bg/auto_route_settle_worker.go   FROM request_logs_hot, JOIN request_logs_hot,
	//	                                 FROM auto_route_selections_hot,
	//	                                 UPDATE auto_route_selections_hot
	//	bg/auto_route_affinity_worker.go FROM request_logs_hot, JOIN request_logs_hot
	//
	// These statements used to say `public.…`, which sent them to the PRODUCTION
	// table on the gate database instead of the fixture — measured as
	// "null value in column "tenant_id" of relation "request_logs_hot"
	// violates not-null constraint" (23502), a constraint the fixture table does
	// not have. De-qualifying is the fix, and it is NOT a vacuous one: the
	// product queries resolve through search_path to the same per-test schema
	// the fixture was created in.
	//
	// Do NOT copy this to a test whose code under test hardcodes `public.`.
	// domains/dispatch/policy_publisher.go:224 is `FROM public.credentials`, so
	// de-qualifying bg/policy_publisher_e2e_test.go would seed a table the
	// product never reads — a green that proves nothing.
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	// A real row with sane signals next to a synthetic row with extreme
	// signals: if the origin_actor filter regresses, the p95/p75 cohort
	// baselines jump to the synthetic values.
	mustExec(`INSERT INTO request_logs_hot
		(request_id, success, latency_ms, cost_usd, task_type, is_auto_request, origin_actor, ts)
		VALUES
		('req-real-b', TRUE, 1000, 1.0, 'code', TRUE, 'user-app', NOW()),
		('req-goal-b', TRUE, 100000, 50.0, 'code', TRUE, 'goal-audit', NOW())`)

	w := NewAutoRouteSettleWorker(pool)
	baselines, _, err := w.loadTaskBaselines(ctx)
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
	mustExec(`INSERT INTO request_logs_hot
		(request_id, success, latency_ms, cost_usd, canonical_id, tenant_id, gw_session_id, task_type, is_auto_request, origin_actor, routing_attempts, ts)
		VALUES
		('req-sel', TRUE, 800, 0.5, 7, 't1', 'gs_sess1', 'code', TRUE, 'user-app', '[{"a":1}]', NOW()),
		('req-goal', TRUE, 900, 0.6, 7, 't1', 'gs_sess1', 'code', TRUE, 'goal-audit', '[{"a":1},{"a":2}]', NOW())`)
	// Session summary claims BOTH turns: only with the filter does the real
	// model carry 1/2 = 0.5 < 0.8 (no session attribution).
	mustExec(`INSERT INTO session_summaries (session_key, health_score, error_count, request_count)
		VALUES ('gs_sess1', 100, 0, 2)`)
	mustExec(`INSERT INTO auto_route_selections_hot
		(request_id, task_type, canonical_id, ts, session_id)
		VALUES ('req-sel', 'code', 7, NOW() - INTERVAL '10 minutes', 'gs_sess1')`)

	w := NewAutoRouteSettleWorker(pool)
	baselines, _, err := w.loadTaskBaselines(ctx)
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
	if err := pool.QueryRow(ctx, `SELECT reward, reward_source FROM auto_route_selections_hot WHERE request_id='req-sel'`).Scan(&reward, &source); err != nil {
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
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auto_route_selections_hot WHERE request_id='req-goal'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("selection rows for the synthetic request appeared unexpectedly: %d", n)
	}
}

func TestAutoRouteSettleBatchSkipsSyntheticActorSelections(t *testing.T) {
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

	// R38: synthetic actor selections (goal-*/auto-*-generator/session-summary)
	// must not settle into rewards. The filter occurs AFTER the LEFT JOIN so
	// that rows without request_logs yet can still reach the abandon path.
	//
	// Insert three selections: one real user request, one goal shadow round,
	// one internal loopback. Each has a logged request_log with success=TRUE.
	mustExec(`INSERT INTO request_logs_hot
		(request_id, success, latency_ms, cost_usd, canonical_id, tenant_id, task_type, is_auto_request, origin_actor, ts)
		VALUES
		('req-real', TRUE, 1000, 1.0, 42, 't1', 'chat', TRUE, '', NOW()),
		('req-goal', TRUE, 1100, 1.1, 43, 't1', 'chat', TRUE, 'goal-model-switch', NOW()),
		('req-loop', TRUE, 1200, 1.2, 44, 't1', 'chat', TRUE, 'auto-title-generator', NOW())`)

	mustExec(`INSERT INTO auto_route_selections_hot
		(request_id, task_type, canonical_id, ts, session_id)
		VALUES
		('req-real', 'chat', 42, NOW() - INTERVAL '10 minutes', NULL),
		('req-goal', 'chat', 43, NOW() - INTERVAL '10 minutes', NULL),
		('req-loop', 'chat', 44, NOW() - INTERVAL '10 minutes', NULL)`)

	w := NewAutoRouteSettleWorker(pool)
	baselines, _, err := w.loadTaskBaselines(ctx)
	if err != nil {
		t.Fatalf("loadTaskBaselines: %v", err)
	}

	settled, abandoned, err := w.settleBatch(ctx, baselines)
	if err != nil {
		t.Fatalf("settleBatch: %v", err)
	}

	// Only the real user request should settle with a reward; the two
	// synthetic actors should be abandoned (settled_at stamped, reward=NULL).
	if settled != 1 {
		t.Errorf("settled = %d, want 1 (only the real request)", settled)
	}
	if abandoned != 2 {
		t.Errorf("abandoned = %d, want 2 (goal + loopback)", abandoned)
	}

	// Verify the real request got a reward
	var realReward *float64
	if err := pool.QueryRow(ctx, `SELECT reward FROM auto_route_selections_hot WHERE request_id='req-real'`).Scan(&realReward); err != nil {
		t.Fatalf("read real reward: %v", err)
	}
	if realReward == nil {
		t.Error("real request reward is NULL, want numeric reward")
	} else if *realReward <= 0 || *realReward > 1 {
		t.Errorf("real reward = %f, want within (0,1]", *realReward)
	}

	// Verify the synthetic actors were abandoned (settled_at set, reward=NULL)
	for _, reqID := range []string{"req-goal", "req-loop"} {
		var settledAt *time.Time
		var reward *float64
		if err := pool.QueryRow(ctx, `SELECT settled_at, reward FROM auto_route_selections_hot WHERE request_id=$1`, reqID).Scan(&settledAt, &reward); err != nil {
			t.Fatalf("read %s: %v", reqID, err)
		}
		if settledAt == nil {
			t.Errorf("%s settled_at is NULL, want timestamp (should be abandoned)", reqID)
		}
		if reward != nil {
			t.Errorf("%s reward = %v, want NULL (synthetic actors must not produce rewards)", reqID, *reward)
		}
	}
}

// scanPendingSettleRows runs one settlePendingSQL variant and scans it the
// same way settleBatch does.
//
// It duplicates settleBatch's Scan argument list on purpose: the claim under
// test is "the session family yields the same rows as v1", and a shared scan
// helper would have meant refactoring the worker to share it — turning a test
// into a refactor. The duplication is bounded to this file and the column list
// is asserted against the worker's own by
// TestSettleSQLBuildersAreTheOnlyDefinitionOfTheLegs.
func scanPendingSettleRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, src settleSourceSpec) []pendingSelection {
	t.Helper()
	rows, err := pool.Query(ctx, settlePendingSQL(src), settleDelay.String(), settleBatchSize)
	if err != nil {
		t.Fatalf("settlePendingSQL(%s): %v", src.Family, err)
	}
	defer rows.Close()

	var out []pendingSelection
	for rows.Next() {
		var p pendingSelection
		if err := rows.Scan(
			&p.id, &p.partitionDate, &p.requestID, &p.taskType, &p.canonicalID, &p.ts,
			&p.success, &p.latencyMs, &p.costUSD,
			&p.originActor,
			&p.rlCanonicalID, &p.rlTenantID,
			&p.sessionHealth, &p.sessionErrors, &p.sessionReqs,
			&p.modelReqsInSes, &p.retryCount,
		); err != nil {
			t.Fatalf("scan (%s): %v", src.Family, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err (%s): %v", src.Family, err)
	}
	return out
}

// TestAutoRouteSettleSessionSourceMatchesV1OnIdenticalRows is the first
// execution of §9.43's session branch anywhere in this repo, and the only
// direct evidence for the claim "确保数据在更改前后一致".
//
// It seeds the SAME logical request into both families — identical success,
// latency, cost, canonical_id, tenant, origin_actor, routing_attempts — and
// differs only in which column carries the session key (gw_session_id vs
// session_id), which is the entire point of settleSourceSpec. Then it runs
// both SQL variants against real PostgreSQL and requires:
//
//   - the cohort baselines are equal (p95, p75, cohort_rows);
//   - every pending row is field-for-field equal;
//   - computeSelectionReward produces the identical reward for each row.
//
// §9.43 shipped with this untested: the fixture had no session_turns_hot table,
// so the only harness that could have run the branch could not name the table.
func TestAutoRouteSettleSessionSourceMatchesV1OnIdenticalRows(t *testing.T) {
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

	// Two sessions. ses-a: one real turn, one synthetic shadow round that the
	// SQLExcludeSyntheticActors filter must drop (so model_reqs=1, not 2).
	// ses-b: two real turns, so the LATERAL has something to aggregate. One
	// selection carries no session at all, exercising the s.session_id IS NOT
	// NULL guard.
	mustExec(`INSERT INTO request_logs_hot
		(request_id, success, latency_ms, cost_usd, canonical_id, tenant_id, gw_session_id, task_type, is_auto_request, origin_actor, routing_attempts, ts)
		VALUES
		('req-a1', TRUE,  800, 0.25, 7, 't1', 'ses-a', 'code', TRUE, 'user-app',  '{"attempts":[{"a":1},{"a":2}]}', NOW()),
		('req-a2', TRUE,  900, 0.30, 7, 't1', 'ses-a', 'code', TRUE, 'goal-audit', '{"attempts":[{"a":1},{"a":2},{"a":3}]}', NOW()),
		('req-b1', FALSE, 1500, 0.40, 8, 't1', 'ses-b', 'code', TRUE, 'user-app',  '{"attempts":[{"a":1}]}', NOW()),
		('req-b2', TRUE,  700, 0.20, 8, 't1', 'ses-b', 'code', TRUE, 'user-app',  NULL, NOW()),
		('req-c1', TRUE, 1100, 0.35, 9, 't1', NULL,    'code', TRUE, 'user-app',  '{"attempts":[{"a":1}]}', NOW())`)

	// Same rows, same values, only the session-key column is renamed.
	//
	// req-c1's turn belongs to ses-c, which NO selection references: the case
	// under test is that the *selection* has session_id NULL, so the LATERAL's
	// `s.session_id IS NOT NULL` guard must yield 0 rows even though a matching
	// turn exists. (session_id is NOT NULL in the real table — verified against
	// the live DB — so a session-less turn is not a state that can exist.)
	mustExec(`INSERT INTO session_turns_hot
		(session_id, turn_no, tenant_id, request_id, success, latency_ms, cost_usd, canonical_id, task_type, is_auto_request, origin_actor, routing_attempts, ts)
		VALUES
		('ses-a', 1, 't1', 'req-a1', TRUE,  800, 0.25, 7, 'code', TRUE, 'user-app',  '{"attempts":[{"a":1},{"a":2}]}', NOW()),
		('ses-a', 2, 't1', 'req-a2', TRUE,  900, 0.30, 7, 'code', TRUE, 'goal-audit', '{"attempts":[{"a":1},{"a":2},{"a":3}]}', NOW()),
		('ses-b', 1, 't1', 'req-b1', FALSE, 1500, 0.40, 8, 'code', TRUE, 'user-app',  '{"attempts":[{"a":1}]}', NOW()),
		('ses-b', 2, 't1', 'req-b2', TRUE,  700, 0.20, 8, 'code', TRUE, 'user-app',  NULL, NOW()),
		('ses-c', 1, 't1', 'req-c1', TRUE, 1100, 0.35, 9, 'code', TRUE, 'user-app',  '{"attempts":[{"a":1}]}', NOW())`)

	mustExec(`INSERT INTO session_summaries (session_key, health_score, error_count, request_count)
		VALUES ('ses-a', 90, 0, 1), ('ses-b', 40, 1, 2)`)

	mustExec(`INSERT INTO auto_route_selections_hot
		(request_id, task_type, canonical_id, ts, session_id)
		VALUES
		('req-a1', 'code', 7, NOW() - INTERVAL '10 minutes', 'ses-a'),
		('req-a2', 'code', 7, NOW() - INTERVAL '10 minutes', 'ses-a'),
		('req-b1', 'code', 8, NOW() - INTERVAL '10 minutes', 'ses-b'),
		('req-b2', 'code', NULL, NOW() - INTERVAL '10 minutes', 'ses-b'),
		('req-c1', 'code', 9, NOW() - INTERVAL '10 minutes', NULL)`)

	v1 := settleSourceFor(true)
	sess := settleSourceFor(false)
	if v1.TurnsTable == sess.TurnsTable {
		t.Fatal("settleSourceFor returned the same table for both families — the test would prove nothing")
	}

	// --- baselines must be identical -------------------------------------
	type baseRow struct {
		taskType   string
		p95        int
		p75        float64
		cohortRows int
	}
	readBaselines := func(src settleSourceSpec) map[string]baseRow {
		t.Helper()
		rows, err := pool.Query(ctx, settleBaselinesSQL(src), baselineWindow.String())
		if err != nil {
			t.Fatalf("settleBaselinesSQL(%s): %v", src.Family, err)
		}
		defer rows.Close()
		out := map[string]baseRow{}
		for rows.Next() {
			var b baseRow
			if err := rows.Scan(&b.taskType, &b.p95, &b.p75, &b.cohortRows); err != nil {
				t.Fatalf("scan baselines (%s): %v", src.Family, err)
			}
			out[b.taskType] = b
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows.Err baselines (%s): %v", src.Family, err)
		}
		return out
	}
	bV1 := readBaselines(v1)
	bSess := readBaselines(sess)
	if len(bV1) == 0 {
		t.Fatal("v1 baselines are empty — the fixture seeded no is_auto_request rows, so the comparison is vacuous")
	}
	if len(bV1) != len(bSess) {
		t.Fatalf("baseline task-type sets differ: v1=%v session=%v", bV1, bSess)
	}
	for tt, bv1 := range bV1 {
		bs := bSess[tt]
		if bs.p95 != bv1.p95 || bs.p75 != bv1.p75 || bs.cohortRows != bv1.cohortRows {
			t.Errorf("baseline %q differs across families: v1=%+v session=%+v", tt, bv1, bs)
		}
	}
	// The synthetic shadow round must have been excluded from BOTH cohort
	// reads. 5 auto rows are seeded (req-a1/a2/b1/b2/c1) and exactly one of
	// them is goal-audit, so cohort_rows==4. Assert it against the seeded
	// total rather than a bare literal so the filter's effect is visible.
	const seededAutoRows = 5
	if got, want := bV1["code"].cohortRows, seededAutoRows-1; got != want {
		t.Errorf("v1 cohort_rows = %d, want %d (%d seeded is_auto_request rows minus the goal-audit shadow round)", got, want, seededAutoRows)
	}

	// --- pending rows and rewards must be identical ---------------------
	pV1 := scanPendingSettleRows(t, ctx, pool, v1)
	pSess := scanPendingSettleRows(t, ctx, pool, sess)
	if len(pV1) != 5 || len(pSess) != 5 {
		t.Fatalf("expected 5 pending rows from each source, got v1=%d session=%d", len(pV1), len(pSess))
	}

	base := bV1["code"]
	baseLine := taskBaseline{P95LatencyMs: base.p95, P75CostUSD: base.p75}
	byReq := func(ps []pendingSelection) map[string]pendingSelection {
		m := map[string]pendingSelection{}
		for _, p := range ps {
			m[p.requestID] = p
		}
		return m
	}
	mV1, mSess := byReq(pV1), byReq(pSess)
	for reqID, a := range mV1 {
		b, ok := mSess[reqID]
		if !ok {
			t.Errorf("request %q present under v1 but missing under the session family", reqID)
			continue
		}
		// The comparison helpers print VALUES, not addresses. A gate whose
		// failure message reads "v1=0xc000123456 session=0xc000123789" tells
		// the next reader nothing about what diverged.
		if d := diffBoolPtr(a.success, b.success); d != "" {
			t.Errorf("%s: success differs: %s", reqID, d)
		}
		if d := diffIntPtr(a.latencyMs, b.latencyMs); d != "" {
			t.Errorf("%s: latency differs: %s", reqID, d)
		}
		if d := diffFloatPtr(a.costUSD, b.costUSD); d != "" {
			t.Errorf("%s: cost differs: %s", reqID, d)
		}
		if d := diffInt64Ptr(a.rlCanonicalID, b.rlCanonicalID); d != "" {
			t.Errorf("%s: rlCanonicalID differs: %s", reqID, d)
		}
		// The LATERAL must agree, including the synthetic-actor filter: this is
		// where a wrong SessionKeyCol would show up as model_reqs 2 vs 1.
		if d := diffIntPtr(a.modelReqsInSes, b.modelReqsInSes); d != "" {
			t.Errorf("%s: model_reqs differs: %s", reqID, d)
		}
		if d := diffIntPtr(a.retryCount, b.retryCount); d != "" {
			t.Errorf("%s: retry_count differs: %s", reqID, d)
		}

		rV1, srcV1, _ := computeSelectionReward(a, baseLine)
		rSess, srcSess, _ := computeSelectionReward(b, baseLine)
		if rV1 != rSess {
			t.Errorf("%s: REWARD differs across families: v1=%.10f (%s) session=%.10f (%s) — "+
				"this is the exact quantity §9.43 promised to keep constant across the source switch",
				reqID, rV1, srcV1, rSess, srcSess)
		}
		if srcV1 != srcSess {
			t.Errorf("%s: reward_source differs: v1=%q session=%q", reqID, srcV1, srcSess)
		}
	}

	// Pin the LATERAL's aggregate, not just its agreement: agreement between
	// two identical wrong shapes is still wrong.
	if got := mV1["req-a1"].modelReqsInSes; got == nil || *got != 1 {
		t.Errorf("req-a1 model_reqs = %v, want 1 (ses-a has 2 turns, one is goal-audit and must be filtered)", got)
	}
	if got := mV1["req-c1"].modelReqsInSes; got == nil || *got != 0 {
		t.Errorf("req-c1 model_reqs = %v, want 0 (its selection has session_id NULL)", got)
	}
}

// TestAutoRouteSettleMissingCohortIsCounted proves the §9.44 guard is live.
//
// The state under test: the cohort has no row for the selection's task_type, so
// both reward terms take the neutral 0.5 fallback. That fallback is correct
// behaviour and produces no error — which is the whole reason it needed a
// counter. Before §9.44 this was indistinguishable from "measured, and neutral".
func TestAutoRouteSettleMissingCohortIsCounted(t *testing.T) {
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

	// request rows exist for the outcome join, but with task_type 'other' —
	// so the cohort (GROUP BY task_type) has NO entry for the selections'
	// task_type 'ghost'.
	mustExec(`INSERT INTO request_logs_hot
		(request_id, success, latency_ms, cost_usd, task_type, is_auto_request, origin_actor, ts)
		VALUES ('req-g1', TRUE, 500, 0.1, 'other', TRUE, 'user-app', NOW())`)
	mustExec(`INSERT INTO auto_route_selections_hot
		(request_id, task_type, ts, session_id)
		VALUES ('req-g1', 'ghost', NOW() - INTERVAL '10 minutes', NULL),
		       ('req-g2', 'ghost', NOW() - INTERVAL '10 minutes', NULL)`)

	w := NewAutoRouteSettleWorker(pool)
	baselines, cohortRows, err := w.loadTaskBaselines(ctx)
	if err != nil {
		t.Fatalf("loadTaskBaselines: %v", err)
	}
	if _, ok := baselines["ghost"]; ok {
		t.Fatal("baseline for 'ghost' exists; the fixture no longer isolates the missing-cohort path")
	}
	if cohortRows != 1 {
		t.Errorf("cohortRows = %d, want 1 (the single seeded 'other' auto row)", cohortRows)
	}

	latency := autoRouteSettleBaselineNeutral.WithLabelValues(baselineTermLatency, settleFamilyV1)
	cost := autoRouteSettleBaselineNeutral.WithLabelValues(baselineTermCost, settleFamilyV1)
	beforeLatency := testutil.ToFloat64(latency)
	beforeCost := testutil.ToFloat64(cost)

	// Only req-g1 has a logged outcome; req-g2 must be abandoned (no outcome).
	settled, _, err := w.settleBatch(ctx, baselines)
	if err != nil {
		t.Fatalf("settleBatch: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want 1", settled)
	}

	if got := testutil.ToFloat64(latency) - beforeLatency; got != 1 {
		t.Errorf("latency neutral-fallback counter +%v, want +1 — the §9.44 guard is not wired into settleBatch", got)
	}
	if got := testutil.ToFloat64(cost) - beforeCost; got != 1 {
		t.Errorf("cost neutral-fallback counter +%v, want +1", got)
	}
}

// diffBoolPtr / diffIntPtr / diffFloatPtr / diffInt64Ptr report a human-readable
// difference between two nullable scalars, treating nil as a distinct value from
// zero (which matters: a missing outcome must not compare equal to a measured
// false / zero).
func diffBoolPtr(a, b *bool) string {
	return diffPtr(a, b, func(p *bool) string { return fmt.Sprintf("%v", *p) })
}

func diffIntPtr(a, b *int) string {
	return diffPtr(a, b, func(p *int) string { return fmt.Sprintf("%d", *p) })
}

func diffFloatPtr(a, b *float64) string {
	return diffPtr(a, b, func(p *float64) string { return fmt.Sprintf("%.8f", *p) })
}

func diffInt64Ptr(a, b *int64) string {
	return diffPtr(a, b, func(p *int64) string { return fmt.Sprintf("%d", *p) })
}

func diffPtr[T comparable](a, b *T, show func(*T) string) string {
	switch {
	case a == nil && b == nil:
		return ""
	case a == nil:
		return "v1=NULL session=" + show(b)
	case b == nil:
		return "v1=" + show(a) + " session=NULL"
	case *a != *b:
		return "v1=" + show(a) + " session=" + show(b)
	}
	return ""
}

// TestAutoRouteSettleSweepFailureCounterWiresUp 钉住 error 臂的接线（R32
// 审计 §四#2）。settleBatch 整查询失败此前只有 slog.Warn + 下轮重查同批——
// llmgw_autoroute_settle_sweep_failures_total{reason="error"} 是
// AutoRouteSettleSweepFailing 告警的唯一数据源，这条接线断了，告警就是装饰。
// 空私有 schema 上跑一轮 sweep：settlePendingSQL 引用的 request_logs_hot
// 不存在 → 42P01 → error 臂必须 +1。（loadTaskBaselines 同样失败，但按设计
// 它走中性回落不计数——那不是 sweep 失败，是降级，有 CohortEmpty 族盯着。）
func TestAutoRouteSettleSweepFailureCounterWiresUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// "SELECT 1" 只是让 extraSchema 非空，从而走 testschema 私有 schema 隔离
	// （空串会拿到未隔离的真库门池——那会让 sweep 结算真表，这是本测试
	// 明确不要的）。
	pool, cleanup := DispatchPostgresContainer(t, ctx, "SELECT 1;")
	defer cleanup()

	w := NewAutoRouteSettleWorker(pool)
	key := autoRouteSettleSweepFailures.WithLabelValues(sweepFailError)
	before := testutil.ToFloat64(key)
	w.sweep(ctx)
	if got := testutil.ToFloat64(key); got != before+1 {
		t.Fatalf("sweep failures{reason=error} delta = %v, want +1 — settleBatch failure is not "+
			"wired to the counter; AutoRouteSettleSweepFailing would be a decoration", got)
	}
}
