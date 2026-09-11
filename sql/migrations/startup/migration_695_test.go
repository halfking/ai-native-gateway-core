package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 695 reinstalls promote_request_logs_hot_to_partition with a
// promote-side final-success self-heal. Production incident (2026-09-12 P2
// cold-migration stall): the claim guard's main.go wiring was lost in merge
// d2cbaf88b (2026-08-26), so a long-lived session (>8h promote boundary)
// claimed a second is_final_success=TRUE row while the first sat promoted in
// request_logs_2026_09. The single-CTE atomic promote then hit
// uq_request_logs_2026_09_final_success_session (23505) on every hourly tick
// for 2.5 days — batches are ts-ascending, so the poisoned row sits in batch
// #1 forever and zero rows move.
//
// The 695 demote block runs before the CTE: hot TRUE rows past retention whose
// session already holds the claim in a heap partition are flipped to FALSE
// (superseded, claim first-come semantics) so the backlog self-drains.
func TestMigration695FinalSuccessSelfHeal(t *testing.T) {
	migration, err := os.ReadFile("695_request_logs_promote_final_success_self_heal.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	body := stripSQLComments(string(migration))

	for _, required := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition",
		// 688 alignment: SQL DEFAULT must keep matching the Go scheduler (8h).
		"DEFAULT '8 hours'::interval",
		// Atomic CTE semantics from 602 must survive the re-install.
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM public.request_logs_hot",
		"moved_rows AS (",
		"INSERT INTO public.request_logs",
		"COMMIT;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration 695 missing %q", required)
		}
	}

	// The demote block: heap-partition catalog scan (columnar-safe, same
	// scope as the claim guard), ONLY-partition EXISTS probe, demote to
	// superseded FALSE, and an observable WARNING.
	for _, required := range []string{
		"am.amname = 'heap'",
		"SET is_final_success = FALSE",
		"FROM ONLY %I x",
		"x.is_final_success",
		"RAISE WARNING",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration 695 demote block missing %q", required)
		}
	}

	// The demote predicate must be scoped to promote-eligible rows only —
	// demoting fresh (< retention) claims would race the claim guard and
	// rewrite rows the promote never touches.
	if !strings.Contains(body, "h.ts < statement_timestamp() - $1") {
		t.Fatalf("migration 695 demote must scope to retention-eligible rows")
	}
	if !strings.Contains(body, `COALESCE(h.gw_session_id, '''') <> ''''`) {
		t.Fatalf("migration 695 demote must skip empty-session rows")
	}

	// Ordering: ensure-partition loop → demote → atomic CTE. The demote must
	// precede the CTE (self-heal before the batch is selected) and follow the
	// ensure loop it sits between.
	ensureIdx := strings.Index(body, "ensure_request_logs_partition(month_rec.month_start)")
	demoteIdx := strings.Index(body, "SET is_final_success = FALSE")
	cteIdx := strings.Index(body, "WITH batch AS (")
	if ensureIdx < 0 || demoteIdx < 0 || cteIdx < 0 {
		t.Fatalf("migration 695 structure anchors not found")
	}
	if !(ensureIdx < demoteIdx && demoteIdx < cteIdx) {
		t.Fatalf("migration 695 must order ensure (%d) < demote (%d) < atomic CTE (%d)", ensureIdx, demoteIdx, cteIdx)
	}

	// The demote must not reintroduce the swallowed-failure pattern.
	if strings.Contains(body, "EXCEPTION WHEN OTHERS") {
		t.Fatalf("migration 695 must not swallow errors (EXCEPTION WHEN OTHERS)")
	}
}

// The three explicit column lists must stay positionally identical after the
// 695 re-install (2026-08-25 positional-drift incident class).
func TestMigration695ColumnListsAligned(t *testing.T) {
	migration, err := os.ReadFile("695_request_logs_promote_final_success_self_heal.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	lists := migration602Columns(t, stripSQLComments(string(migration)))
	returning, insertCols, selectCols := lists[0], lists[1], lists[2]

	if len(returning) != len(insertCols) || len(insertCols) != len(selectCols) {
		t.Fatalf("column list length drift: returning=%d insert=%d select=%d", len(returning), len(insertCols), len(selectCols))
	}
	for i := range returning {
		if returning[i] != insertCols[i] || insertCols[i] != selectCols[i] {
			t.Fatalf("column %d misaligned: returning=%q insert=%q select=%q", i, returning[i], insertCols[i], selectCols[i])
		}
	}
	if len(returning) < 100 {
		t.Fatalf("expected the full shared column list, got %d", len(returning))
	}

	// The whole point of 695: the claim marker must survive promotion.
	found := false
	for _, c := range returning {
		if c == "is_final_success" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("promote column list missing is_final_success")
	}
}
