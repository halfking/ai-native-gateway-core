//go:build integration

package admin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// TestCredentialHeatmapSQL_ExecutesOnRealDatabase runs the production heatmap SQL
// against a real database. This is the gate that should have existed all along,
// for the same reason admin/session_export_sql_integration_test.go exists:
// pgxmock matches a query *string* and never lets PostgreSQL parse it, so every
// defect class that lives in the SQL text is invisible to unit tests.
//
// The concrete incident:
//
//	admin/credential_monitor_heatmap.go  buildHeatmapSQL
//	  ERROR: column rl.origin_stage does not exist          (42703)
//
// live for ≥2 weeks, on the default path, because exclude_self_test defaults to
// true. It stayed invisible because TestBuildHeatmapSQL_GuardsAgainst42803Regression
// asserts on substrings of the SQL and passed happily.
//
// The static guards cannot cover this shape: the view reference and the offending
// predicate are two separate literals joined at runtime, so no single-literal rule
// sees both. Executing the assembled statement does.
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestCredentialHeatmapSQL
func TestCredentialHeatmapSQL_ExecutesOnRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the heatmap query parses")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	now := time.Now().UTC()
	// ExcludeSelfTest: true is the load-bearing part — that arm is the one that
	// referenced origin_stage, and it is the default for every real call.
	cases := []struct {
		name string
		p    heatmapQueryParams
	}{
		{"exclude-self-test-default", heatmapQueryParams{
			TimeStart: now.Add(-24 * time.Hour), TimeEnd: now,
			BucketSeconds: 3600, ExcludeSelfTest: true,
		}},
		{"include-self-test", heatmapQueryParams{
			TimeStart: now.Add(-24 * time.Hour), TimeEnd: now,
			BucketSeconds: 3600, ExcludeSelfTest: false,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// runHeatmapQuery is the production execution path (same func the
			// handler calls), so this exercises the assembled SQL, not a copy.
			got, err := runHeatmapQuery(ctx, pool, tc.p)
			if err != nil {
				t.Fatalf("heatmap query failed: %v", err)
			}
			t.Logf("heatmap returned %d credential series", len(got))
		})
	}
}

// TestHeatmapExcludeSelfTestKeepsSessionBranchRows is the half a parse check
// cannot give you: a query can execute and still be silently wrong.
//
// The old predicate's second arm was `NOT ('probe' = ANY(rl.quality_flags))` with
// no COALESCE. The session branch of the view NULL-pads quality_flags, so
// `'probe' = ANY(NULL)` is NULL, `NOT NULL` is not TRUE, and every session row is
// dropped by WHERE. Measured on the local DB (2026-10-02, 7d window):
// 40,225 of 40,275 session-branch rows removed — i.e. the heatmap would have gone
// blind to essentially the entire migrated dataset while still returning 200.
//
// The invariant asserted here is deliberately NOT "kept == some other predicate's
// count". An earlier version of this test compared the full three-arm production
// predicate against a flags-only predicate and duly failed at 41,733 vs 50,283 —
// which is the CORRECT result, because the other two arms (task_type,
// origin_actor) legitimately exclude more rows. Comparing two different
// predicates proves nothing; it only looks like a defect.
//
// The real property is: **the exclusion's verdict on a row must not depend on
// quality_flags happening to be NULL.** So evaluate the identical production
// predicate twice over the same rows — once with the real (possibly NULL) flags,
// once with NULL replaced by an empty array — and require the counts to match. If
// the COALESCE is ever lost, the first count collapses and this goes red while the
// query still parses and still returns 200.
func TestHeatmapExcludeSelfTestKeepsSessionBranchRows(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that session rows survive exclusion")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pred := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "rl", "rl", "rl")
	var keptAsIs, keptDefaulted, nullFlags int64
	err = pool.QueryRow(ctx, fmt.Sprintf(`
		WITH actual AS (
		    SELECT quality_flags, task_type, origin_actor
		    FROM request_logs_with_current_month
		    WHERE credential_id IS NOT NULL AND ts > now() - interval '7 days'
		),
		defaulted AS (
		    SELECT COALESCE(quality_flags, '{}'::text[]) AS quality_flags,
		           task_type, origin_actor
		    FROM actual
		)
		SELECT
		    (SELECT count(*) FROM actual    rl WHERE %[1]s) AS kept_as_is,
		    (SELECT count(*) FROM defaulted rl WHERE %[1]s) AS kept_defaulted,
		    (SELECT count(*) FROM actual WHERE quality_flags IS NULL) AS null_flags
		`, pred)).Scan(&keptAsIs, &keptDefaulted, &nullFlags)
	if err != nil {
		t.Fatalf("counting query failed: %v", err)
	}
	t.Logf("7d window: %d rows have NULL quality_flags; production predicate keeps %d as-is / %d with NULL defaulted",
		nullFlags, keptAsIs, keptDefaulted)

	if keptAsIs != keptDefaulted {
		t.Errorf("the production exclusion keeps %d rows with real flags but %d with NULL "+
			"coalesced to an empty array — its verdict depends on quality_flags being NULL, "+
			"so the view's NULL-padded session branch (%d such rows) is silently excluded. "+
			"The flags arm needs COALESCE('probe' = ANY(...), FALSE).",
			keptAsIs, keptDefaulted, nullFlags)
	}
}

// TestPhysicalOnlyColumnsListMatchesRealDatabase reverse-validates the hardcoded
// list in view_source_column_contract_test.go against the real schema, so the list
// cannot silently rot: if the view ever gains one of these columns, or a column is
// dropped from request_logs, this fails and forces the list to be updated.
func TestPhysicalOnlyColumnsListMatchesRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence the column list is current")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rows, err := pool.Query(ctx, `
		SELECT string_agg(a.attname, ',')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname='public' AND c.relname='request_logs'
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND NOT EXISTS (
		    SELECT 1 FROM pg_attribute v
		    JOIN pg_class vc ON vc.oid = v.attrelid
		    JOIN pg_namespace vn ON vn.oid = vc.relnamespace
		    WHERE vn.nspname='public' AND vc.relname='request_logs_with_current_month'
		      AND v.attname = a.attname AND v.attnum > 0 AND NOT v.attisdropped)`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	var joined string
	if err := rows.Scan(&joined); err != nil {
		t.Fatalf("scan: %v", err)
	}
	real := map[string]bool{}
	for _, c := range strings.Split(joined, ",") {
		if c = strings.TrimSpace(c); c != "" {
			real[c] = true
		}
	}
	if len(real) == 0 {
		t.Skip("schema query returned no columns — cannot validate the list")
	}
	for _, col := range physicalOnlyRequestLogColumns {
		if !real[col] {
			t.Errorf("physicalOnlyRequestLogColumns lists %q but it is NOT (request_logs minus view) "+
				"on the real schema — the view may have gained it, or the column was dropped. "+
				"Re-derive the list before trusting the static guard.", col)
		}
	}
	t.Logf("validated %d listed columns against the live schema (%d physical-only columns total)",
		len(physicalOnlyRequestLogColumns), len(real))
}
