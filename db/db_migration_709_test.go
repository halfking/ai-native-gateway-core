package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration709EnsureWiredAndMirrorsSQL guards the dual-channel sync for
// migration 709 (2026-09-14 auto-matching audit O1′-c): binary environments
// only run the db.go ensure chain (sql/migrations/startup files are never
// applied there), so a missing or drifting ensure would leave
// code_audit / function_call / intent_classification / planning without
// work_type routes and those task classes permanently on the 48h fallback
// pool — the exact gap the human-confirmed audit item closes.
func TestMigration709EnsureWiredAndMirrorsSQL(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	// The ensure must stay wired in applyMigrationsOnce, right after the
	// work_type schema ensure it extends (same table family, later migration).
	callIdx := strings.Index(text, "ensureWorkTypeRouteCoverage(migCtx)")
	if callIdx < 0 {
		t.Fatal("db.go must call ensureWorkTypeRouteCoverage in applyMigrationsOnce")
	}
	wtIdx := strings.Index(text, "ensureWorkTypeSchema(migCtx)")
	if wtIdx < 0 || wtIdx > callIdx {
		t.Fatal("ensureWorkTypeRouteCoverage must be wired after ensureWorkTypeSchema")
	}

	const fnSig = "func (d *DB) ensureWorkTypeRouteCoverage("
	fnStart := strings.Index(text, fnSig)
	if fnStart < 0 {
		t.Fatal("db.go missing ensureWorkTypeRouteCoverage wrapper")
	}
	nextFn := strings.Index(text[fnStart+1:], "\nfunc ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensureWorkTypeRouteCoverage")
	}
	body := text[fnStart : fnStart+nextFn]

	// Per-key guard (491 convention): route blocks only seed when the
	// work_type_key has no routes at all — a per-row guard would resurrect
	// operator-deleted rows on every boot.
	if n := strings.Count(body, "SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key"); n != 4 {
		t.Fatalf("ensure must use the per-key guard for all 4 route blocks, found %d", n)
	}
	if !strings.Contains(body, `('709', 'work_type route coverage for code_audit/function_call/intent_classification/planning`) {
		t.Fatal("ensure SQL must stamp schema_migrations version 709 (dual-ledger convention)")
	}

	// The ensure SQL must stay in sync with the canonical migration file:
	// same 3 config keys and the same 9 route rows.
	migBytes, err := os.ReadFile("../sql/migrations/startup/709_work_type_route_coverage.sql")
	if err != nil {
		t.Fatal(err)
	}
	mig := string(migBytes)
	for _, key := range []string{"'code_audit'", "'intent_classification'", "'planning'", "'fn_call'"} {
		if !strings.Contains(mig, key) || !strings.Contains(body, key) {
			t.Fatalf("config/route key %s missing from migration file or ensure body", key)
		}
	}
	for _, model := range []string{"'deepseek-v4-flash'", "'minimax-m2.7'", "'glm-5.2'"} {
		if !strings.Contains(mig, model) || !strings.Contains(body, model) {
			t.Fatalf("route model %s missing from migration file or ensure body", model)
		}
	}
	if got := strings.Count(mig, "ON CONFLICT (work_type_key, canonical_name) DO NOTHING"); got != 4 {
		t.Fatalf("migration file route blocks changed; keep ensureWorkTypeRouteCoverage mirrored (found %d)", got)
	}
}
