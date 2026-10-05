package db

// Guards for migration 831 (work_type_model_route.source).
//
// The hazard 831 closes
// ---------------------
// Two writers replace a work type's entire route set:
// admin/acc_work_types.go syncWorkTypesFromACC and admin/work_types.go
// putRoutes. Neither recorded ownership, and the ACC seed carries
// `model_routes: []` on all 22 entries — so a successful sync deleted
// every route for a key and reinserted zero, silently wiping routes an
// operator had configured in the admin UI. The sync result reported
// "0 routes" and nothing warned.
//
// Today that loss is masked by a path bug: the gateway requests
// /api/llm/work-types while acc-go serves /api/v2/llm/work-types, so the
// sync 404s and never runs. That is exactly why the guards below assert
// both halves — if the path is fixed without the ownership column, the
// masked bug becomes a live data-loss bug.
//
// Why these read source text rather than a database
// -------------------------------------------------
// There is no DB fixture in this package, and standing one up is not
// available in every environment. The 709 guard (TestMigration709Ensure-
// WiredAndMirrorsSQL) sets the precedent: assert the ensure function is
// wired into applyMigrationsOnce and that the SQL it runs is the SQL the
// .sql migration contains. That catches the failure that actually happens
// in practice — a .sql file written and never wired, so it never runs in a
// binary deployment.

import (
	"os"
	"strings"
	"testing"
)

func readDBGo(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

// functionBody returns the body of the first function in text whose
// signature starts with sig, up to the next top-level func.
func functionBody(t *testing.T, text, sig string) string {
	t.Helper()
	start := strings.Index(text, sig)
	if start < 0 {
		t.Fatalf("db.go missing %s", sig)
	}
	nextFn := strings.Index(text[start+1:], "\nfunc ")
	if nextFn < 0 {
		t.Fatalf("db.go malformed: no function after %s", sig)
	}
	return text[start : start+nextFn]
}

// TestMigration831EnsureWiredAndMirrorsSQL pins that the ownership column
// actually gets created in a binary deployment.
//
// The failure this catches is silent and total: without the ensure, the
// .sql migration never runs, source is always NULL, and the sync's
// `AND source = 'acc'` filter matches nothing — which would look like the
// fix working (nothing gets deleted) while actually meaning the sync has
// silently stopped managing its own routes.
func TestMigration831EnsureWiredAndMirrorsSQL(t *testing.T) {
	text := readDBGo(t)

	callIdx := strings.Index(text, "ensureWorkTypeRouteSource(migCtx)")
	if callIdx < 0 {
		t.Fatal("db.go must call ensureWorkTypeRouteSource in applyMigrationsOnce")
	}
	// Must run after the 709 coverage ensure: same table family, and 831
	// adds the column the sync's WHERE clause depends on.
	wtCoverageIdx := strings.Index(text, "ensureWorkTypeRouteCoverage(migCtx)")
	if wtCoverageIdx < 0 || wtCoverageIdx > callIdx {
		t.Fatal("ensureWorkTypeRouteSource must be wired after ensureWorkTypeRouteCoverage")
	}

	body := functionBody(t, text, "func (d *DB) ensureWorkTypeRouteSource(")
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'operator'",
		"SET source = 'operator' WHERE source IS NULL OR source = ''",
		"wtmr_source_check CHECK (source IN ('operator', 'acc'))",
		"idx_wtmr_key_source",
		"'831'",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ensureWorkTypeRouteSource missing %q", want)
		}
	}

	// The .sql migration and the ensure must not drift: binary deployments
	// run the ensure, SQL-file deployments run the migration, and only one
	// of them exists in any given environment.
	sqlFile, err := os.ReadFile("../sql/migrations/startup/831_work_type_route_source.sql")
	if err != nil {
		t.Fatalf("read 831 sql: %v", err)
	}
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'operator'",
		"wtmr_source_check CHECK (source IN ('operator', 'acc'))",
		"idx_wtmr_key_source",
	} {
		if !strings.Contains(string(sqlFile), want) {
			t.Errorf("831 .sql missing %q — the ensure and the migration have drifted", want)
		}
	}
}

// TestSyncNeverDeletesOperatorRoutes is the guard for the actual fix.
//
// It asserts the sync's DELETE is scoped by source. A regression to the
// unqualified per-key DELETE restores the data-loss path, and because that
// path only fires when the sync succeeds — which it currently cannot — the
// regression would sit in the tree looking harmless.
func TestSyncNeverDeletesOperatorRoutes(t *testing.T) {
	source, err := os.ReadFile("../admin/acc_work_types.go")
	if err != nil {
		t.Fatalf("read acc_work_types.go: %v", err)
	}
	text := string(source)

	deletes := strings.Count(text, "DELETE FROM work_type_model_route")
	if deletes != 1 {
		t.Fatalf("expected exactly 1 DELETE against work_type_model_route, found %d", deletes)
	}
	if !strings.Contains(text, "DELETE FROM work_type_model_route WHERE work_type_key = $1 AND source = 'acc'") {
		t.Error("the sync's DELETE is not scoped to source='acc' — it will wipe operator-configured routes")
	}
	// ON CONFLICT DO NOTHING, not DO UPDATE: an operator's row for a model
	// must win, and the old bare INSERT aborted the whole transaction on
	// the UNIQUE (work_type_key, canonical_name) violation.
	if !strings.Contains(text, "ON CONFLICT (work_type_key, canonical_name) DO NOTHING") {
		t.Error("the sync's route INSERT must use ON CONFLICT DO NOTHING so an operator's row wins and the sync does not abort")
	}
	if strings.Contains(text, "ON CONFLICT (work_type_key, canonical_name) DO UPDATE") {
		t.Error("the sync's route INSERT must not DO UPDATE — that would let a sync overwrite an operator's route")
	}
	// Every row the sync writes must be attributable to the sync.
	if !strings.Contains(text, "'acc')") {
		t.Error("the sync's route INSERT must stamp source='acc', otherwise the next sync cannot identify its own rows")
	}
}

// TestPutRoutesStampsOperatorOwnership pins the other writer.
//
// Without this the symmetry breaks: the sync stops deleting operator rows
// it cannot recognise, and an operator editing a key in the UI produces
// rows the sync may overwrite.
func TestPutRoutesStampsOperatorOwnership(t *testing.T) {
	source, err := os.ReadFile("../admin/work_types.go")
	if err != nil {
		t.Fatalf("read work_types.go: %v", err)
	}
	text := string(source)
	// Compare on whitespace-normalised text: the INSERT spans two lines
	// (column list, then VALUES) and gofmt will not join them, so a
	// single-line match would be a test that breaks on formatting.
	flat := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(flat, "tier, task_quality_score, source) VALUES ($1, $2, $3, $4, $5, $6, $7, 'operator')") {
		t.Error("putRoutes must insert source='operator' so ACC sync leaves those rows alone")
	}
	// The asymmetry is intentional and must stay explained: putRoutes
	// keeps its unqualified full-key delete so an operator can remove a
	// synced row, while the sync may not.
	if !strings.Contains(text, "claiming it") {
		t.Error("putRoutes' unqualified DELETE lost its rationale comment — the asymmetry with the sync is deliberate and a future reader will otherwise 'fix' it into a data-loss bug")
	}
}
