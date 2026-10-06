package db

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureMaasSchema_ShortCircuitsRequestLogsDDL pins the catalog guard added
// to EnsureMaasSchema.
//
// Background: `ALTER TABLE … ADD COLUMN IF NOT EXISTS` does not spare the lock.
// PostgreSQL must take ACCESS EXCLUSIVE on the parent table and on every
// partition before it can check whether the column exists, so a column that has
// been in place for months still costs a full exclusive pass on every boot.
// Measured on production (runbook §10.97) against request_logs — the busiest
// table in the database, with 5 monthly partitions there, i.e. 6 relations
// locked for a statement that changes nothing:
//
//	calls 420 · mean 2,289 ms · worst single call 104,322 ms
//
// The same shape already guards work_type in db.go
// (workTypeRequestLogsCurrent, see work_type_request_logs_guard_test.go);
// this is the remaining unguarded request_logs DDL in the startup path.
func TestEnsureMaasSchema_ShortCircuitsRequestLogsDDL(t *testing.T) {
	src, err := os.ReadFile("maas_schema.go")
	if err != nil {
		t.Fatalf("read maas_schema.go: %v", err)
	}
	text := string(src)

	slice := func(anchor string) string {
		t.Helper()
		start := strings.Index(text, anchor)
		if start < 0 {
			t.Fatalf("%q not found", anchor)
		}
		body := text[start:]
		if end := strings.Index(body, "\n}\n"); end > 0 {
			body = body[:end+1]
		}
		return body
	}

	t.Run("request_logs_ddl_is_a_separate_constant", func(t *testing.T) {
		if !strings.Contains(text, "const maasRequestLogsDDL = `") {
			t.Fatal("maas_schema.go should declare maasRequestLogsDDL separately")
		}
		ddl := slice("const maasRequestLogsDDL = `")
		for _, want := range []string{"ALTER TABLE request_logs", "idx_request_logs_credits_charged"} {
			if !strings.Contains(ddl, want) {
				t.Errorf("maasRequestLogsDDL should still carry %q", want)
			}
		}
		// The rest of the batch must NOT be inside the guarded constant —
		// otherwise skipping it would also skip maas_settings seeding.
		if strings.Contains(ddl, "maas_settings") || strings.Contains(ddl, "model_credit_rates") {
			t.Error("maasRequestLogsDDL must stay scoped to request_logs; " +
				"the settings/rates batch is what seeds pricing data")
		}
	})

	t.Run("ddl_is_applied_only_after_the_guard_misses", func(t *testing.T) {
		fn := slice("func (d *DB) EnsureMaasSchema(")
		guardAt := strings.Index(fn, "d.maasRequestLogsCurrent(ctx)")
		ddlAt := strings.Index(fn, "maasRequestLogsDDL)")
		if guardAt < 0 || ddlAt < 0 {
			t.Fatalf("EnsureMaasSchema should both guard and apply maasRequestLogsDDL "+
				"(guard at %d, ddl at %d)", guardAt, ddlAt)
		}
		if guardAt > ddlAt {
			t.Error("the catalog guard must run BEFORE the DDL is applied")
		}
		// ★ Position alone is not control flow: a guard written as
		// `if false && d.maasRequestLogsCurrent(ctx) {` still appears before the
		// DDL while its result is discarded entirely — the DDL then runs
		// unconditionally, which is exactly the lock this guard removes.
		if !strings.Contains(fn, "if d.maasRequestLogsCurrent(ctx) {") {
			t.Error("the guard's result must be used directly as an if condition — " +
				"a disabled, negated, or discarded guard silently restores the lock")
		}
		if strings.Contains(fn, "_ = d.maasRequestLogsCurrent") {
			t.Error("the guard's result must not be discarded")
		}
	})

	t.Run("failed_probe_falls_through_to_the_ddl", func(t *testing.T) {
		// ★ The inverse failure is the dangerous one: a probe error must never
		// read as "already current", or a database that genuinely lacks the
		// column would silently be skipped and never get it.
		fn := slice("func (d *DB) maasRequestLogsCurrent(")
		if !strings.Contains(fn, "return false") {
			t.Error("the probe's error path must return false (apply the DDL)")
		}
		successAt := strings.Index(fn, "return missing == 0")
		errAt := strings.Index(fn, "return false")
		if errAt < 0 || successAt < 0 || errAt > successAt {
			t.Error("the error return must precede the success return")
		}
	})

	t.Run("guard_checks_both_the_column_and_the_index", func(t *testing.T) {
		fn := slice("func (d *DB) maasRequestLogsCurrent(")
		// Only checking the column would let a database missing the index skip
		// the DDL and never receive it.
		for _, want := range []string{"credits_charged", "idx_request_logs_credits_charged"} {
			if !strings.Contains(fn, want) {
				t.Errorf("the guard should check %q", want)
			}
		}
		if !strings.Contains(fn, "information_schema.columns") ||
			!strings.Contains(fn, "pg_indexes") {
			t.Error("the guard should read the catalog (columns and indexes)")
		}
	})

	t.Run("probe_must_not_run_ddl", func(t *testing.T) {
		probe := slice("func (d *DB) maasRequestLogsCurrent(")
		if strings.Contains(probe, "ALTER TABLE") || strings.Contains(probe, "CREATE INDEX") {
			t.Error("the catalog probe must read the catalog, not run DDL")
		}
	})

	t.Run("non_request_logs_batch_is_preserved", func(t *testing.T) {
		// Reverse gate: the guard must not have become a way to skip the pricing
		// tables. Their shape is part of the function's contract.
		fn := slice("func (d *DB) EnsureMaasSchema(")
		for _, want := range []string{
			"CREATE TABLE IF NOT EXISTS maas_settings",
			"base_credits_per_1m_cache_out",
			"global_discount",
			"CREATE TABLE IF NOT EXISTS model_credit_rates",
			"manual_cache_out",
		} {
			if !strings.Contains(fn, want) {
				t.Errorf("EnsureMaasSchema must keep %q in the unguarded batch", want)
			}
		}
	})

	t.Run("still_wired_into_the_startup_sequence", func(t *testing.T) {
		dbGo, err := os.ReadFile("db.go")
		if err != nil {
			t.Fatalf("read db.go: %v", err)
		}
		if !strings.Contains(string(dbGo), "EnsureMaasSchema(") {
			t.Error("EnsureMaasSchema must still be called from db.go")
		}
	})
}
