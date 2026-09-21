package db

import (
	"os"
	"strings"
	"testing"
)

// TestApplyMigrationsIncludesAutoRouteSelectionsHotEnsure pins the 2026-09-05
// audit D-2#4/H-2 fix: ApplyMigrations must call the gateway-side 656 ensure
// (same position as the 655 session_summaries ensure), and the ensure SQL
// must carry the full hot-table contract from
// sql/migrations/startup/656_auto_route_selections_hot.sql — table shape,
// unique request constraint (selection_writer UPSERT depends on it), the
// unsettled partial index (settle worker watermark), and the unified view
// (affinity worker read path). This follows the source-text contract style
// of TestApplyMigrationsIncludesMigration538Ensure.
func TestApplyMigrationsIncludesAutoRouteSelectionsHotEnsure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"ensureAutoRouteSelectionsHotSchema(migCtx)",
		"func (d *DB) ensureAutoRouteSelectionsHotSchema",
		"to_regclass('public.auto_route_selections') IS NOT NULL",
		"CREATE TABLE IF NOT EXISTS public.auto_route_selections_hot",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_ars_hot_request",
		"CREATE INDEX IF NOT EXISTS idx_ars_hot_task_profile_ts",
		"CREATE INDEX IF NOT EXISTS idx_ars_hot_session",
		"CREATE INDEX IF NOT EXISTS idx_ars_hot_unsettled",
		"ars_hot_profile_check",
		"ars_hot_reward_range",
		"ars_hot_reward_source_check",
		"CREATE OR REPLACE VIEW public.auto_route_selections_all AS",
		"UNION ALL",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing migration 656 ensure contract %q", want)
		}
	}
}
