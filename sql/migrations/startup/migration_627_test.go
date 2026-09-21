package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration627CandidateFailureLogsAggregationIdUnified verifies the 627
// up/down migration carries the columnar-safe design contract:
//
//   - historical rows whose aggregation_id is NULL are projected as
//     `COALESCE(aggregation_id, -id)` in the unified view (columnar partitions
//     are append-only; UPDATE/CTID-scan paths used in 622's heap hot-table
//     backfill are not supported on Citus ColumnarScan).
//   - the watermark is seeded to bigint-min on first deploy so the first
//     aggregator tick replays every historical bucket before settling on a
//     positive last_source_id.
//   - the column is added at the parent level so the view column projects
//     without requiring per-partition ALTER.
//   - the down migration intentionally leaves the watermark seed in place and
//     only drops the view/index/column.
func TestMigration627CandidateFailureLogsAggregationIdUnified(t *testing.T) {
	up, err := os.ReadFile("627_candidate_failure_logs_aggregation_id_unified.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(up)

	wantFragments := []string{
		// Column add at parent (works on columnar partitions).
		"ADD COLUMN IF NOT EXISTS aggregation_id bigint",
		// View with synthesized historical aggregation_id.
		"CREATE OR REPLACE VIEW public.candidate_failure_logs_unified",
		"COALESCE(aggregation_id, -id) AS aggregation_id",
		// View hardening matches 625's session_bodies_unified pattern. Use the
		// parenthesized option-list form so the migration is portable across
		// PG 14/15/Citus builds (env 154's Citus rejects the PG 15 shorthand
		// `SET SECURITY_INVOKER = true`).
		"SET (security_invoker = true)",
		// Index on aggregation_id for forward lookups (no UPDATE — index only
		// covers rows that actually got a value via promote).
		"idx_candidate_failure_logs_aggregation_id",
		// Watermark seed for one-time historical replay.
		"provider_error_aggregator_state",
		"-9223372036854775807",
		// Negative assertion: the broken UPDATE+CTID path is gone.
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 627 up missing required fragment %q", fragment)
		}
	}

	bannedFragments := []string{
		// The original 627 design attempted a CTID-scan UPDATE on the columnar
		// parent, which Postgres rejects with "UPDATE and CTID scans not
		// supported for ColumnarScan". The rewritten migration must not
		// regress to that path.
		"SELECT ctid",
		"WHERE cfl.ctid = ordered.ctid",
		"UPDATE public.candidate_failure_logs cfl",
	}
	for _, fragment := range bannedFragments {
		if strings.Contains(sql, fragment) {
			t.Errorf("migration 627 up contains banned fragment %q (columnar UPDATE/CTID path is unsupported)", fragment)
		}
	}

	down, err := os.ReadFile("627_candidate_failure_logs_aggregation_id_unified.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := string(down)
	for _, fragment := range []string{
		"DROP VIEW IF EXISTS public.candidate_failure_logs_unified",
		"DROP INDEX IF EXISTS public.idx_candidate_failure_logs_aggregation_id",
		"DROP COLUMN IF EXISTS aggregation_id",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Errorf("migration 627 down missing required fragment %q", fragment)
		}
	}
}
