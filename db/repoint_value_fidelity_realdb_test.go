package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repoint value-fidelity gate (audit §9.166, rewritten §9.167).
//
// ## Why this file was rewritten
//
// The first version of this gate (shipped in c4a05637f, §9.166) re-derived the
// view's projection locally with a LATERAL over `session_turns_hot` /
// `session_turns` and compared the result to v1. It reported **zero** mismatches
// on `client_model` and `outbound_model`, and §9.166 published that as "the view
// reproduces v1's values exactly".
//
// **Both numbers were empty.** `session_turns` has no `client_model` column and
// no `outbound_model` column — it has `model`, `raw_model_name` and
// `canonical_model` (106 columns, verified against information_schema). Inside
// a LATERAL subquery PostgreSQL resolves an unqualified column name against the
// inner FROM first and **falls back to the outer query's columns** when the inner
// relation does not have it. So `SELECT credential_id, client_model,
// outbound_model FROM public.session_turns` silently returned **v1's own**
// `client_model` and `outbound_model`, and the gate compared v1 against itself:
//
//	-- direct proof, 2026-10-04
//	--   v1.client_model  = minimax-m3
//	--   t.client_model    = minimax-m3   <- from the outer scope, not from session_turns
//
// A copy of a query does not degrade gracefully. §9.163's version dropped two
// NOT EXISTS anti-joins and reported 745,385 losses against a truth of 6. This
// version dropped **nothing** and was worse: it did not fail, it quietly
// measured the wrong thing and agreed with itself.
//
// The fix is structural rather than a patch: **do not re-derive the view's
// projection locally at all.** Query the deployed view and compare its output
// to v1. Then there is no local copy to drift, and no outer-scope fallback to
// exploit — the only way to compare v1 to itself is to not read the view.
//
// ## What it measures
//
// For every v1 success row in the window, the view's own output for that
// `request_id`, split by which leg produced it:
//
//   - **no twin** → the v1 leg, which projects the row verbatim
//   - **has twin** → the anti-join suppresses the v1 row and the view emits the
//     **session leg's** value, which is a different string
//
// Measured 2026-10-04 on the local database, 24h window (and identical at 72h):
//
//	leg              rows  client_model  outbound_model  credential_id  success
//	v1 leg (no twin)   410             0              0             0        0
//	session leg        290           153             92             0        0
//
// So the split is the finding: the v1 leg is faithful, and the session leg is
// not — 52.8% of twin rows carry a different `client_model`. `credential_id` and
// `success` agree on both legs.
//
// ## Why a zero needs a positive control
//
// Every mismatch count here can be 0 for two very different reasons: the view
// agrees with v1, or the comparison cannot detect disagreement. Those look
// identical in a log line. `TestRepointValueFidelity` therefore runs a
// **positive control** on every invocation: the same join, compared against a
// deliberately wrong column pair, which must report a non-zero count. A control
// that reports 0 means the detector is broken and the rest of the numbers are
// meaningless — so the test fails instead of reporting a clean sheet.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run 'TestRepointValueFidelity' -count=1 -v

// repointFidelityColumns are the columns the three repoint-safe readers select
// or filter on, which is what this gate certifies. Pinned against the source
// files by TestRepointFidelityColumnsMatchTheReaders.
var repointFidelityColumns = []string{
	"success", "credential_id", "client_model", "outbound_model", "ts",
}

// repointSafeReaders are the files §9.165 graded `repoint-safe`. §9.167
// withdraws that grade — the names stay because the gate must keep measuring
// the readers it was written for, and a reader that changed without the
// measurement changing is exactly how a stale verdict survives.
var repointSafeReaders = []string{
	"admin/swim_lane_init.go",
	"bg/model_probe.go",
	"bg/today_success_probe.go",
}

// TestRepointFidelityColumnsMatchTheReaders is the anti-drift check: every
// certified column must actually appear in at least one of the three readers.
func TestRepointFidelityColumnsMatchTheReaders(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(file))

	var blob strings.Builder
	for _, rel := range repointSafeReaders {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		blob.Write(b)
	}
	src := blob.String()

	for _, col := range repointFidelityColumns {
		if !strings.Contains(src, col) {
			t.Errorf("repointFidelityColumns names %q, which appears in none of the %d "+
				"readers this gate is about — either the reader changed or the certified "+
				"column set is stale; both mean the gate is measuring the wrong thing",
				col, len(repointSafeReaders))
		}
	}
	t.Logf("certified %d columns against %d readers", len(repointFidelityColumns), len(repointSafeReaders))
}

// TestRepointValueFidelity compares the **deployed view's output** to v1.
//
// It asserts zero mismatch on the v1 leg and, on the session leg, asserts the
// measured divergence matches the registered findings — so a change in either
// direction is a red test rather than a silently-stale claim.
func TestRepointValueFidelity(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var v1Total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_logs_hot`).Scan(&v1Total); err != nil {
		t.Skipf("request_logs_hot not readable (%v) — v1 already retired here", err)
	}
	if v1Total == 0 {
		t.Skip("request_logs_hot is empty — nothing to certify")
	}

	const window = "24 hours"

	// One pass over the view. Every compared value is either read straight off
	// the view (aliased v_*) or off v1, and each side is **explicitly qualified**
	// with its relation alias — no unqualified name is in scope anywhere, so the
	// outer-scope fallback that voided the first version of this gate cannot
	// apply.
	//
	// `has_twin` uses EXISTS, which is a different formulation from the JOIN, so
	// it doubles as the cross-check on the leg split.
	var (
		v1Rows, found, absent              int64
		v1LegRows, sessLegRows             int64
		v1LegClientMM, v1LegOutboundMM     int64
		v1LegCredMM, v1LegSuccessMM        int64
		sessLegClientMM, sessLegOutboundMM int64
		sessLegCredMM, sessLegSuccessMM    int64
		controlClientMM                    int64
	)
	if err := pool.QueryRow(ctx, `
		WITH v1 AS (
		    SELECT r.request_id,
		           r.success, r.credential_id, r.client_model, r.outbound_model,
		           (EXISTS (SELECT 1 FROM public.session_turns_hot t WHERE t.request_id = r.request_id)
		            OR EXISTS (SELECT 1 FROM public.session_turns     t WHERE t.request_id = r.request_id)) AS has_twin
		    FROM public.request_logs_hot r
		    WHERE r.ts >= now() - $1::interval AND r.success = TRUE
		), j AS (
		    SELECT v1.request_id, v1.has_twin, v.request_id AS matched,
		           v1.client_model, v1.outbound_model, v1.credential_id, v1.success,
		           v.client_model AS v_client, v.outbound_model AS v_outbound,
		           v.credential_id AS v_cred, v.success AS v_success
		    FROM v1 LEFT JOIN public.request_logs_with_current_month v
		           ON v.request_id = v1.request_id
		)
		SELECT
		    (SELECT count(*) FROM v1),
		    count(*) FILTER (WHERE matched IS NOT NULL),
		    count(*) FILTER (WHERE matched IS NULL),
		    count(*) FILTER (WHERE NOT has_twin),
		    count(*) FILTER (WHERE has_twin),
		    count(*) FILTER (WHERE NOT has_twin AND matched IS NOT NULL AND v_client IS DISTINCT FROM client_model),
		    count(*) FILTER (WHERE NOT has_twin AND matched IS NOT NULL AND v_outbound IS DISTINCT FROM outbound_model),
		    count(*) FILTER (WHERE NOT has_twin AND matched IS NOT NULL AND v_cred IS DISTINCT FROM credential_id),
		    count(*) FILTER (WHERE NOT has_twin AND matched IS NOT NULL AND v_success IS DISTINCT FROM success),
		    count(*) FILTER (WHERE has_twin AND matched IS NOT NULL AND v_client IS DISTINCT FROM client_model),
		    count(*) FILTER (WHERE has_twin AND matched IS NOT NULL AND v_outbound IS DISTINCT FROM outbound_model),
		    count(*) FILTER (WHERE has_twin AND matched IS NOT NULL AND v_cred IS DISTINCT FROM credential_id),
		    count(*) FILTER (WHERE has_twin AND matched IS NOT NULL AND v_success IS DISTINCT FROM success),
		    count(*) FILTER (WHERE v_client IS DISTINCT FROM outbound_model)
		FROM j`, window).Scan(
		&v1Rows, &found, &absent,
		&v1LegRows, &sessLegRows,
		&v1LegClientMM, &v1LegOutboundMM, &v1LegCredMM, &v1LegSuccessMM,
		&sessLegClientMM, &sessLegOutboundMM, &sessLegCredMM, &sessLegSuccessMM,
		&controlClientMM,
	); err != nil {
		t.Fatalf("value fidelity: %v", err)
	}

	t.Logf("window=%s v1_success_rows=%d found_in_view=%d absent=%d", window, v1Rows, found, absent)
	t.Logf("leg=v1(no twin)      rows=%d client_model_mm=%d outbound_model_mm=%d credential_id_mm=%d success_mm=%d",
		v1LegRows, v1LegClientMM, v1LegOutboundMM, v1LegCredMM, v1LegSuccessMM)
	t.Logf("leg=session(twin)    rows=%d client_model_mm=%d outbound_model_mm=%d credential_id_mm=%d success_mm=%d",
		sessLegRows, sessLegClientMM, sessLegOutboundMM, sessLegCredMM, sessLegSuccessMM)

	if v1Rows == 0 {
		t.Skipf("no v1 success rows in the last %s — measured nothing", window)
	}

	// ---- positive control -------------------------------------------------
	//
	// `client_model` compared against `outbound_model` is wrong by construction
	// and must be non-zero. If it is zero, this query cannot detect divergence
	// and every "0" above is the absence of a measurement, not agreement.
	if controlClientMM == 0 {
		t.Fatal("POSITIVE CONTROL FAILED: comparing client_model against outbound_model " +
			"reported 0 divergences, so this query is not able to detect a mismatch and " +
			"the zero counts above mean nothing")
	}
	t.Logf("positive control (client_model vs outbound_model) = %d divergences — detector works", controlClientMM)

	// ---- coverage ---------------------------------------------------------
	if absent != 0 {
		t.Errorf("%d of %d v1 success rows in the last %s are absent from the canonical view; "+
			"repointing any recent-window reader would drop them", absent, v1Rows, window)
	}

	// ---- the v1 leg must be faithful -------------------------------------
	//
	// This is the half of §9.166's claim that survives measurement: rows the
	// anti-join leaves in the v1 leg come out verbatim.
	for _, m := range []struct {
		name string
		n    int64
	}{
		{"client_model", v1LegClientMM},
		{"outbound_model", v1LegOutboundMM},
		{"credential_id", v1LegCredMM},
		{"success", v1LegSuccessMM},
	} {
		if m.n != 0 {
			t.Errorf("v1 leg: %d of %d rows came back with a different %s, but the v1 leg "+
				"projects request_logs verbatim — either the view changed or this gate's "+
				"join is no longer measuring what it claims", m.n, v1LegRows, m.name)
		}
	}

	// ---- the session leg's divergence is a registered finding -------------
	//
	// Asserted in **both** directions, against a registered rate floor rather
	// than a pinned count. A count is a snapshot of one window on one database
	// and goes red every time the database takes a row (§9.166.4); a floor
	// states the substantive claim — "the session leg does not reproduce this
	// column" — without freezing a number that drifts.
	//
	// Direction 1: every registered column must still be diverging at or above
	// its floor. Otherwise the "this is broken" claim is stale and readers of it
	// have learned to ignore it.
	// Direction 2: every certified column NOT registered must have zero
	// divergence. Otherwise something started diverging and nobody was told.
	if sessLegRows == 0 {
		t.Skipf("0 of %d v1 success rows have a session twin — the session-leg half measured "+
			"nothing (the v1-leg and coverage halves did run)", v1Rows)
	}
	measured := map[string]int64{
		"client_model":   sessLegClientMM,
		"outbound_model": sessLegOutboundMM,
		"credential_id":  sessLegCredMM,
		"success":        sessLegSuccessMM,
	}
	registered := map[string]float64{}
	for _, d := range RetirementSessionLegDivergence {
		registered[d.Column] = d.MinRate
		n, ok := measured[d.Column]
		if !ok {
			t.Errorf("RetirementSessionLegDivergence registers %q, which this gate does not "+
				"measure — a registered column nobody measures is a claim with no evidence",
				d.Column)
			continue
		}
		rate := float64(n) / float64(sessLegRows)
		t.Logf("session leg %-14s diverges %d/%d = %.1f%% (floor %.0f%%)",
			d.Column, n, sessLegRows, rate*100, d.MinRate*100)
		if rate < d.MinRate {
			t.Errorf("session leg: %s diverges on %.1f%% of twin rows, registered floor is %.0f%% — "+
				"either the normalisation was fixed (drop the registration) or it regressed",
				d.Column, rate*100, d.MinRate*100)
		}
	}
	for col, n := range measured {
		if _, ok := registered[col]; ok {
			continue
		}
		if n != 0 {
			t.Errorf("session leg: %s diverges on %d of %d twin rows but is not in "+
				"RetirementSessionLegDivergence — an unregistered divergence is a behaviour "+
				"change nobody was told about", col, n, sessLegRows)
		}
	}
}
