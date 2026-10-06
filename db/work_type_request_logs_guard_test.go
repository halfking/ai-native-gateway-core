package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureWorkTypeSchema_ShortCircuitsRequestLogsDDL pins the catalog guard
// added to ensureWorkTypeSchema.
//
// Background: `ALTER TABLE … ADD COLUMN IF NOT EXISTS` does not spare the
// lock. PostgreSQL must take ACCESS EXCLUSIVE on the parent table and on every
// partition before it can check whether the column exists, so a column that has
// been in place for months still costs a full exclusive pass on every boot.
// Measured at up to 109,456 ms on production against request_logs — the
// busiest table in the database, with 5 monthly partitions there (6 relations
// locked) for a statement that changes nothing.
//
// ensureRequestLogSchema already short-circuits its own 31 columns this way.
// This test guards the one remaining unguarded request_logs DDL on the startup
// path.
func TestEnsureWorkTypeSchema_ShortCircuitsRequestLogsDDL(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)

	t.Run("request_logs_ddl_is_a_separate_constant", func(t *testing.T) {
		// It must be split out of workTypeSchemaSQL, otherwise the whole
		// batch would have to be skipped to avoid the lock — and that batch
		// is also what seeds work_type_config.
		if !strings.Contains(text, "const workTypeRequestLogsDDL = `") {
			t.Fatal("db.go should declare workTypeRequestLogsDDL separately")
		}
		constStart := strings.Index(text, "const workTypeSchemaSQL = `")
		if constStart < 0 {
			t.Fatal("db.go should still declare workTypeSchemaSQL")
		}
		schemaSQL := text[constStart:]
		if end := strings.Index(schemaSQL, "\n`"); end > 0 {
			schemaSQL = schemaSQL[:end]
		}
		if strings.Contains(schemaSQL, "ALTER TABLE request_logs") {
			t.Error("workTypeSchemaSQL must not still touch request_logs — " +
				"that statement is what takes the exclusive lock")
		}
		// And the guard's own SQL must not reintroduce the ALTER.
		probeStart := strings.Index(text, "func (d *DB) workTypeRequestLogsCurrent(")
		if probeStart < 0 {
			t.Fatal("db.go should declare workTypeRequestLogsCurrent")
		}
		probe := text[probeStart:]
		if end := strings.Index(probe, "\n}\n"); end > 0 {
			probe = probe[:end]
		}
		if strings.Contains(probe, "ALTER TABLE request_logs") {
			t.Error("the catalog probe must read the catalog, not run DDL")
		}
	})

	t.Run("ddl_is_applied_only_after_the_guard_misses", func(t *testing.T) {
		// Ordering is the whole point: probing after the DDL would lock anyway.
		fnStart := strings.Index(text, "func (d *DB) ensureWorkTypeSchema(")
		if fnStart < 0 {
			t.Fatal("ensureWorkTypeSchema not found")
		}
		fn := text[fnStart:]
		if end := strings.Index(fn[1:], "\n}\n"); end > 0 {
			fn = fn[:end+1]
		}
		guardAt := strings.Index(fn, "d.workTypeRequestLogsCurrent(ctx)")
		ddlAt := strings.Index(fn, "workTypeRequestLogsDDL)")
		if guardAt < 0 || ddlAt < 0 {
			t.Fatalf("ensureWorkTypeSchema should both guard and apply "+
				"workTypeRequestLogsDDL (guard at %d, ddl at %d)", guardAt, ddlAt)
		}
		if guardAt > ddlAt {
			t.Error("the catalog guard must run BEFORE the DDL is applied")
		}

		// ★ Position alone is not control flow. A guard written as
		// `if false && d.workTypeRequestLogsCurrent(ctx) {` still appears before
		// the DDL in the source while its result is discarded entirely — the
		// DDL then runs unconditionally, which is exactly the bug this guard
		// exists to remove. Assert the result is consumed as the condition.
		if !strings.Contains(fn, "if d.workTypeRequestLogsCurrent(ctx) {") {
			t.Error("the guard's result must be used directly as an if condition — " +
				"a disabled, negated, or discarded guard silently restores the lock")
		}
		if strings.Contains(fn, "_ = d.workTypeRequestLogsCurrent") {
			t.Error("the guard's result must not be discarded")
		}
	})

	t.Run("failed_probe_falls_through_to_the_ddl", func(t *testing.T) {
		// ★ The inverse failure is the dangerous one. A probe error must never
		// read as "already current", or a database that genuinely lacks the
		// column would silently be skipped and never get it.
		fnStart := strings.Index(text, "func (d *DB) workTypeRequestLogsCurrent(")
		if fnStart < 0 {
			t.Fatal("workTypeRequestLogsCurrent not found")
		}
		fn := text[fnStart:]
		if end := strings.Index(fn, "\n}\n"); end > 0 {
			fn = fn[:end]
		}
		if !strings.Contains(fn, "return false") {
			t.Error("the probe's error path must return false (apply the DDL)")
		}
		// And `missing == 0` must be reached only on the success path.
		successAt := strings.Index(fn, "return missing == 0")
		errAt := strings.Index(fn, "return false")
		if errAt < 0 || successAt < 0 || errAt > successAt {
			t.Error("the error return must precede the success return")
		}
	})

	t.Run("guard_checks_both_the_column_and_the_index", func(t *testing.T) {
		fnStart := strings.Index(text, "func (d *DB) workTypeRequestLogsCurrent(")
		fn := text[fnStart:]
		if end := strings.Index(fn, "\n}\n"); end > 0 {
			fn = fn[:end]
		}
		// Only checking the column would let a database missing the index skip
		// the DDL and never receive it.
		for _, want := range []string{"work_type", "idx_request_logs_work_type"} {
			if !strings.Contains(fn, want) {
				t.Errorf("the guard should check %q", want)
			}
		}
		if !strings.Contains(fn, "information_schema.columns") ||
			!strings.Contains(fn, "pg_indexes") {
			t.Error("the guard should read the catalog (columns and indexes)")
		}
	})

	t.Run("wired_into_the_startup_sequence", func(t *testing.T) {
		if !strings.Contains(text, "ensureWorkTypeSchema(migCtx)") {
			t.Error("ensureWorkTypeSchema must still be called from the startup sequence")
		}
	})
}
