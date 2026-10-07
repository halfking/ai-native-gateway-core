package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsurePartitionAutovacuumSchema_SkipsAlreadyConfigured pins the catalog
// guard on the autovacuum reloption loop.
//
// Background: `ALTER TABLE … SET (storage_parameter)` takes ACCESS EXCLUSIVE
// even when it changes nothing. ensurePartitionAutovacuumSchema walked every
// `%_hot` table and every partition of eleven parent tables and issued that
// ALTER unconditionally, then called the function without a guard — so every
// gateway start took 63 exclusive locks on production (23 hot tables + 40
// partitions), including the busiest tables in the database, for settings that
// were already exactly right.
//
// The guard must select on reloptions, and it must be present in BOTH loops:
// fixing only one leaves the other half of the locks in place, and because
// both loops still succeed, a partial fix is invisible to any test that only
// checks "the function runs without error".
func TestEnsurePartitionAutovacuumSchema_SkipsAlreadyConfigured(t *testing.T) {
	raw, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(raw)

	start := strings.Index(text, "func (d *DB) ensurePartitionAutovacuumSchema(")
	if start < 0 {
		t.Fatal("ensurePartitionAutovacuumSchema not found")
	}
	fn := text[start:]
	if end := strings.Index(fn[1:], "\n}\n"); end > 0 {
		fn = fn[:end+1]
	}

	// The five reloptions the function sets. A guard that checks a subset
	// would skip a table whose remaining settings are still wrong.
	wantOpts := []string{
		"autovacuum_enabled=true",
		"autovacuum_vacuum_scale_factor=0.05",
		"autovacuum_vacuum_threshold=10",
		"autovacuum_analyze_scale_factor=0.02",
		"autovacuum_analyze_threshold=50",
	}

	t.Run("both_loops_are_guarded", func(t *testing.T) {
		// Two loops select ALTER targets: the `%_hot` tables and the
		// partitions of eleven parents. Each needs its own guard.
		guards := strings.Count(fn, "AND NOT (COALESCE(c.reloptions, ARRAY[]::text[]) @> ARRAY[")
		if guards != 2 {
			t.Errorf("expected a reloptions guard in BOTH ALTER loops, found %d — "+
				"fixing one loop leaves the other's exclusive locks in place", guards)
		}
		// The hot-table loop sets the fixed 404 option list; the partition
		// loop is month-aware (839) and appends the per-row expected analyze
		// scale factor. One ALTER site of each shape.
		hotAlters := strings.Count(fn, "ALTER TABLE %I SET (%s)")
		partitionAlters := strings.Count(fn, "ALTER TABLE %I SET (%s, autovacuum_analyze_scale_factor=%s)")
		if hotAlters != 1 || partitionAlters != 1 {
			t.Errorf("expected one fixed-shape and one month-aware ALTER site, found %d / %d", hotAlters, partitionAlters)
		}
	})

	t.Run("partition_loop_honors_the_839_handoff", func(t *testing.T) {
		// Migration 839 hands the CURRENT month's heap partitions back to
		// autovacuum at analyze scale factor 0.005. A guard that expects
		// 0.02 everywhere rewrites 0.005 back on the first boot, and a
		// body that always sets 0.02 drops the handoff again at the next
		// month rollover — both guard and SET must be month-aware.
		if !strings.Contains(fn, "THEN '0.005' ELSE '0.02' END") {
			t.Error("partition guard/SET must expect 0.005 on current-month heap partitions (839)")
		}
		// month CASE appears in both the SELECT list (SET value) and the
		// reloptions guard; anything less means one half lost the
		// month-awareness.
		monthAware := strings.Count(fn, "to_char(date_trunc('month', now()), 'YYYY_MM')")
		if monthAware < 2 {
			t.Errorf("month-aware scale factor must appear in both the guard and the SET, found %d", monthAware)
		}
		if !strings.Contains(fn, "r.expect_sf") {
			t.Error("partition ALTER must apply the per-row expected scale factor")
		}
	})

	t.Run("guard_checks_every_reloption_the_function_sets", func(t *testing.T) {
		for _, opt := range wantOpts {
			if !strings.Contains(fn, "'"+opt+"'") {
				t.Errorf("guard should require %q, otherwise a table missing it is skipped", opt)
			}
		}
	})

	t.Run("guard_uses_containment_not_equality", func(t *testing.T) {
		// reloptions is a text[] that can carry unrelated entries (e.g.
		// fillfactor). An exact `= ARRAY[...]` comparison would never match and
		// would silently disable the guard entirely — the failure would look
		// exactly like "the guard is doing its job".
		if !strings.Contains(fn, "@> ARRAY[") {
			t.Error("the guard should use the @> containment operator on reloptions")
		}
		if strings.Contains(fn, "c.reloptions = ARRAY[") {
			t.Error("exact equality on reloptions would never match; use @>")
		}
	})

	t.Run("null_reloptions_still_gets_configured", func(t *testing.T) {
		// A table that has never been touched has reloptions = NULL.
		// `NULL @> ARRAY[...]` is NULL, and `NOT NULL` is also NULL, so the
		// row would be filtered out by WHERE — the table would never be
		// configured at all. COALESCE is what prevents that.
		if !strings.Contains(fn, "COALESCE(c.reloptions, ARRAY[]::text[])") {
			t.Error("a NULL reloptions must be coalesced, or untouched tables are skipped forever")
		}
	})

	t.Run("function_is_still_invoked_every_boot", func(t *testing.T) {
		// The guard lives inside the plpgsql loops, not in Go. Removing the
		// call would make startup cheaper but silently stop the settings from
		// ever being applied on a fresh database.
		if !strings.Contains(fn, "SELECT apply_llm_gateway_autovacuum_settings();") {
			t.Error("the function must still be invoked; the guard belongs in its loops")
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		if !strings.Contains(text, "ensurePartitionAutovacuumSchema(migCtx)") {
			t.Error("ensurePartitionAutovacuumSchema must still run at startup")
		}
	})
}
