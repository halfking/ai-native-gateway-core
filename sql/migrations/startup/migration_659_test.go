package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 659 (audit 2026-09-05 D-2#6) reinstalls the seven remaining legacy
// hot-table promote functions as atomic data-modifying CTEs, following the
// migration 656 template. The contract below pins the properties that make the
// bodies loss-safe: single-statement delete+insert (FOR UPDATE SKIP LOCKED),
// no EXCEPTION swallow, no temp table, no SELECT * (positional drift), explicit
// column lists everywhere, parameter guards, and no ON CONFLICT (columnar leaf
// partitions reject speculative insertion, per migration 399).
func TestMigration659LegacyPromoteAtomicContract(t *testing.T) {
	upPath := filepath.Join("659_legacy_promote_atomic_cte.sql")
	downPath := filepath.Join("659_legacy_promote_atomic_cte.down.sql")
	upBytes, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upUpper := strings.ToUpper(up)
	down := string(downBytes)

	functions := []string{
		"promote_usage_ledger_hot_to_partition",
		"promote_request_wal_hot_to_partition",
		"promote_routing_decision_log_hot_to_partition",
		"promote_credential_model_index_hot_to_partition",
		"promote_tool_usage_stats_hot_to_partition",
		"promote_credit_ledger_hot_to_partition",
		"promote_request_logs_bodies_hot_to_partition",
	}

	// All seven functions are reinstalled with the unchanged signature and
	// DEFAULTs (retention '7 days', batch 5000).
	if got := strings.Count(up, "CREATE OR REPLACE FUNCTION public.promote_"); got != 7 {
		t.Errorf("migration 659 must reinstall exactly 7 promote functions, found %d", got)
	}
	if got := strings.Count(up, "p_retention interval DEFAULT '7 days'::interval"); got != 7 {
		t.Errorf("expected 7 unchanged retention DEFAULTs, found %d", got)
	}
	if got := strings.Count(up, "p_batch_size integer DEFAULT 5000"); got != 7 {
		t.Errorf("expected 7 unchanged batch_size DEFAULTs, found %d", got)
	}
	for _, fn := range functions {
		if !strings.Contains(up, "CREATE OR REPLACE FUNCTION public."+fn) {
			t.Errorf("migration 659 missing CREATE OR REPLACE FUNCTION public.%s", fn)
		}
	}

	// Atomicity: every function drains rows through one FOR UPDATE SKIP LOCKED
	// batch CTE; delete and insert are a single statement. The two extra plain
	// occurrences belong to the runtime verification block, so the exact count
	// uses the distinctive per-body batch scan pattern.
	if !strings.Contains(upUpper, "FOR UPDATE SKIP LOCKED") {
		t.Error("migration 659 must drain hot rows through a FOR UPDATE SKIP LOCKED batch")
	}
	if got := strings.Count(upUpper, "LIMIT P_BATCH_SIZE FOR UPDATE SKIP LOCKED"); got != 7 {
		t.Errorf("expected 7 batch LIMIT guards, found %d", got)
	}

	// Error propagation: no EXCEPTION handler may swallow INSERT failures
	// (the pre-602 loss window, audit D-2#6).
	if strings.Contains(upUpper, "EXCEPTION WHEN") {
		t.Error("migration 659 must not swallow errors via EXCEPTION WHEN")
	}
	if strings.Contains(upUpper, "RAISE WARNING") {
		t.Error("migration 659 must not warn-and-continue on promote failure")
	}

	// No temp-table staging and no positional SELECT * (the 2026-08-25
	// request_logs incident pattern).
	if strings.Contains(upUpper, "CREATE TEMP TABLE") || strings.Contains(up, "_promote_hot_batch") {
		t.Error("migration 659 must not use the legacy temp-table staging")
	}
	if strings.Contains(upUpper, "SELECT *") {
		t.Error("migration 659 must use explicit columns, not SELECT *")
	}

	// Parameter guards (retention > 0, batch 1..50000) in every function.
	if got := strings.Count(up, "p_retention must be positive"); got != 7 {
		t.Errorf("expected 7 retention guards, found %d", got)
	}
	if got := strings.Count(up, "p_batch_size must be between 1 and 50000"); got != 7 {
		t.Errorf("expected 7 batch-size guards, found %d", got)
	}

	// Monthly partitions are pre-ensured with the same predicate family as the
	// batch CTE, via each table's existing ensure function.
	for _, ensure := range []string{
		"PERFORM public.ensure_usage_ledger_partition(",
		"PERFORM public.ensure_request_wal_partition(",
		"PERFORM public.ensure_routing_decision_log_partition(",
		"PERFORM public.ensure_credential_model_index_partition(",
		"PERFORM public.ensure_tool_usage_stats_partition(",
		"PERFORM public.ensure_credit_ledger_partition(",
		"PERFORM public.ensure_request_logs_bodies_partition(",
	} {
		if !strings.Contains(up, ensure) {
			t.Errorf("migration 659 missing pre-ensure call %q", ensure)
		}
	}

	// Every parent INSERT carries an explicit column list, and the delete
	// side returns explicit columns (moved_rows).
	for _, table := range []string{
		"usage_ledger", "request_wal", "routing_decision_log",
		"credential_model_index", "tool_usage_stats", "credit_ledger",
		"request_logs_bodies",
	} {
		if !strings.Contains(up, "INSERT INTO public."+table+" (") {
			t.Errorf("migration 659 missing explicit-column INSERT INTO public.%s", table)
		}
		if !strings.Contains(up, "DELETE FROM public."+table+"_hot ") {
			t.Errorf("migration 659 missing DELETE FROM public.%s_hot", table)
		}
	}

	// No ON CONFLICT in the promote bodies: columnar leaf partitions (migration
	// 399) reject speculative insertion, and duplicates surface as errors with
	// the hot rows preserved by the atomic CTE. The single allowed occurrence is
	// the schema_migrations bookkeeping upsert.
	if got := strings.Count(upUpper, "ON CONFLICT"); got != 1 {
		t.Errorf("expected exactly 1 ON CONFLICT (schema_migrations upsert), found %d", got)
	}
	if !strings.Contains(up, "VALUES ('659',") || !strings.Contains(up, "ON CONFLICT (version) DO NOTHING") {
		t.Error("the only permitted ON CONFLICT is the schema_migrations (version) upsert")
	}

	// request_logs_bodies keeps migration 528's TTL-expiry first phase.
	if !strings.Contains(up, "lifecycle.request_logs_bodies_ttl_days") {
		t.Error("migration 659 must preserve the 528 body-TTL expiry phase")
	}

	// Bookkeeping follows the 656 convention.
	if !strings.Contains(up, "INSERT INTO public.schema_migrations (version, description)") ||
		!strings.Contains(up, "'659'") {
		t.Error("migration 659 must record itself in schema_migrations")
	}

	// Down restores the seven pre-659 legacy bodies (delete-before-insert,
	// temp-table staging) for emergency rollback only.
	if got := strings.Count(down, "CREATE OR REPLACE FUNCTION"); got != 7 {
		t.Errorf("down migration must restore 7 legacy functions, found %d", got)
	}
	if !strings.Contains(down, "_promote_hot_batch") || !strings.Contains(down, "EXCEPTION WHEN OTHERS") {
		t.Error("down migration must restore the verbatim legacy bodies (temp table + EXCEPTION)")
	}
	if !strings.Contains(strings.ToUpper(down), "WARNING") {
		t.Error("down migration must carry the loss-window warning header")
	}
}
