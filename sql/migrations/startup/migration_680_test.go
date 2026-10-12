package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 680 bootstraps the canonical request_logs query view when it has
// been dropped out-of-band (2026-09-07 /request-logs 42P01 incident). The
// contract test pins the staged wrapper-chain shape so a future edit cannot
// silently drift the column contract (base UNION + customer_id 577 +
// request_class/due_at 610).
func TestMigration680BootstrapsCurrentMonthView(t *testing.T) {
	up, err := os.ReadFile("680_request_logs_current_month_view_bootstrap.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("680_request_logs_current_month_view_bootstrap.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for _, sql := range []string{string(up), string(down)} {
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Fatal("view migration must be transactional")
		}
	}
	upText := string(up)
	// Purely additive on the healthy path: the bootstrap must reach its early
	// RETURN before any DDL when the canonical view is already present — the
	// incident state 680 repairs is a half-applied DROP+CREATE sequence. Check
	// executable statements only; the header comments legitimately quote the
	// 341-style replay that caused it.
	var ddlStatements []string
	for _, line := range strings.Split(upText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		ddlStatements = append(ddlStatements, strings.ToUpper(trimmed))
	}
	executable := strings.Join(ddlStatements, "\n")

	// The 2026-10-11 host-first repair branch (commit 4f5ae1a82) made the
	// guard check the view's SHAPE rather than its existence, so a view left
	// behind with a pre-577/610 column set falls through to a rebuild. That
	// path does reach DROP VIEW IF EXISTS: CREATE OR REPLACE VIEW refuses to
	// add or drop view columns, so a stale column set can only move forward by
	// dropping and rebuilding — the same constraint 680b documents for the two
	// lower wrappers (and the same DROP CASCADE pair, applied there inside one
	// transaction). Reverting it would reinstate the documented production
	// failure this branch was written for: on a fresh deploy the snapshot's
	// stale view made 680 return early, the wrappers were never created, and
	// 696 then aborted permanently on a missing relation.
	//
	// So the gate is no longer "never drops a view" — that blanket substring
	// ban was the pre-2026-10-11 spelling and is what went red against a
	// deliberate, deployed fix. What must still hold, and is asserted below:
	// the healthy path is additive, drops are guarded and reachable only from
	// the stale-shape branch, and every drop is followed by a rebuild inside
	// the same transaction.
	//
	// The healthy-path marker is the RETURN STATEMENT, not the RAISE NOTICE
	// beside it. Mutation M5 deleted only the `RETURN;` and left the notice
	// prose in place; an assertion keyed on that prose stayed green while the
	// migration had become exactly the destructive path 680 exists to prevent.
	dropIdx := strings.Index(executable, "DROP VIEW")
	if dropIdx != -1 {
		// Scope the RETURN to the shape check: the last `INTO canonical_exists;`
		// before the first drop is the 577/610 shape probe (the other one is the
		// plain pg_views existence test that opens the function).
		shapeInto := strings.LastIndex(executable[:dropIdx], "INTO CANONICAL_EXISTS;")
		if shapeInto == -1 {
			t.Error("680 drops a view without first re-checking the canonical view's " +
				"577/610 column shape; the drop must be reachable only from the repair path")
		} else if !strings.Contains(executable[shapeInto:dropIdx], "RETURN") {
			t.Error("680 reaches a DROP VIEW with no RETURN between its shape check and " +
				"the drop, so an already-canonical view is dropped and rebuilt every run " +
				"(the 2026-09-07 42P01 incident was a half-applied rebuild)")
		}
		// An unguarded drop is what makes a re-run destructive: the second
		// application would abort on the missing relation instead of no-oping.
		for _, stmt := range ddlStatements {
			if strings.Contains(stmt, "DROP VIEW") && !strings.HasPrefix(stmt, "DROP VIEW IF EXISTS ") {
				t.Errorf("680 drops a view without IF EXISTS, so a re-run aborts instead "+
					"of no-oping: %.80s", stmt)
			}
		}
		// The drop is only safe because the rebuild follows in the same
		// transaction (BEGIN/COMMIT is asserted above). If it were terminal, a
		// failed rebuild would leave precisely the 42P01 state 680 exists to
		// repair. Matched against the canonical name specifically: the two
		// ..._without_* wrappers start with the same string, so a plain
		// Contains would also be satisfied by the wrapper rebuilds alone.
		const canonicalCreate = "CREATE VIEW PUBLIC.REQUEST_LOGS_WITH_CURRENT_MONTH"
		lastDrop := strings.LastIndex(executable, "DROP VIEW")
		rebuildIdx := -1
		for off := lastDrop; off >= 0 && off < len(executable); {
			hit := strings.Index(executable[off:], canonicalCreate)
			if hit < 0 {
				break
			}
			abs := off + hit
			if !strings.HasPrefix(executable[abs+len(canonicalCreate):], "_WITHOUT_") {
				rebuildIdx = abs
				break
			}
			off = abs + 1
		}
		if rebuildIdx == -1 {
			t.Error("680 drops the canonical view without recreating it afterwards; a " +
				"failed rebuild would leave the 42P01 half-applied state 680 exists to repair")
		}
	}
	if strings.Contains(executable, "RENAME") {
		t.Error("680 up must not RENAME any view (purely additive bootstrap)")
	}
	for _, needle := range []string{
		"request_logs_with_current_month",
		"request_logs_with_current_month_without_request_class_due_at",
		"request_logs_with_current_month_without_customer_id",
		"public.request_logs_hot",
		"public.request_logs",
		"customer_id",
		"request_class",
		"due_at",
		"NOT IN ('customer_id', 'request_class', 'due_at')",
		"LEFT JOIN LATERAL",
	} {
		if !strings.Contains(upText, needle) {
			t.Errorf("migration missing %q", needle)
		}
	}
}
