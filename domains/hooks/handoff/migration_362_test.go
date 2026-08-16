package handoff

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Migration 362 (and its startup mirror 527) widens
// handoff_pending_confirmations.status to accommodate the durable GoalState
// lifecycle and adds the goal_state / restore_* columns. The down path must
// collapse the new statuses back to 'confirmed' so a rollback does not break
// the legacy three-state model.
//
// These tests read the SQL files directly and assert structural invariants.
// They do NOT execute the migrations against a real database — that would
// require either a TEST_DATABASE_URL or testcontainers, which is out of scope
// for the current CI surface. The sqlmock tests in confirmation_pg_test.go
// cover the runtime SQL; these tests catch regressions in the schema
// definition itself.

// repoRoot walks up from this test file's directory until it finds a sibling
// go.mod. The result is the absolute path of the repository root, regardless
// of where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root above %s", filepath.Dir(thisFile))
	return ""
}

func readMigration(t *testing.T, rel string) string {
	t.Helper()
	full := filepath.Join(repoRoot(t), rel)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read %s: %v", full, err)
	}
	return string(data)
}

func TestMigration_362_UpAddsGoalStateColumns(t *testing.T) {
	up := readMigration(t, "db/migrations/362_handoff_durable_goal_state.sql")

	wantSubstrings := []string{
		// columns
		"goal_state JSONB",
		"goal_state_version INTEGER",
		"restore_status VARCHAR(32)",
		"restore_error VARCHAR(512)",
		"restore_attempted_at TIMESTAMPTZ",
		"restored_at TIMESTAMPTZ",
		// status widening
		"ALTER COLUMN status TYPE VARCHAR(32)",
		// new status set must include the durable lifecycle states
		`'accounting_confirmed'`,
		`'restored'`,
		`'manual_required'`,
		// backfill pending → confirmed rows into the new lifecycle
		"SET status = 'accounting_confirmed'",
		// partial index for the restore queue
		"idx_handoff_pending_restore",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(up, want) {
			t.Errorf("362 up migration missing required substring %q", want)
		}
	}
}

func TestMigration_362_DownCollapsesNewStatusesToConfirmed(t *testing.T) {
	down := readMigration(t, "db/migrations/362_handoff_durable_goal_state.down.sql")

	wantSubstrings := []string{
		// legacy three-state mapping
		"WHEN status IN ('accounting_confirmed', 'restored', 'manual_required') THEN 'confirmed'",
		// durable columns dropped
		"DROP COLUMN IF EXISTS goal_state",
		"DROP COLUMN IF EXISTS goal_state_version",
		"DROP COLUMN IF EXISTS restore_status",
		"DROP COLUMN IF EXISTS restore_error",
		"DROP COLUMN IF EXISTS restore_attempted_at",
		"DROP COLUMN IF EXISTS restored_at",
		// partial index dropped
		"DROP INDEX IF EXISTS idx_handoff_pending_restore",
		// status column re-narrowed
		"ALTER COLUMN status TYPE VARCHAR(16)",
		// legacy CHECK constraint restored
		"CHECK (status IN ('pending', 'confirmed', 'expired'))",
		// restore_* bookkeeping wiped before the column drops
		"restore_status = NULL",
		"restore_error = NULL",
		"restore_attempted_at = NULL",
		"restored_at = NULL",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(down, want) {
			t.Errorf("362 down migration missing required substring %q", want)
		}
	}
}

// 527 is the startup mirror of 362 — the same schema change applied during
// startup migration. It must carry the same columns and the same status
// widening; a divergence would split runtime schema from migration history.
func TestMigration_527_Mirrors362(t *testing.T) {
	up362 := readMigration(t, "db/migrations/362_handoff_durable_goal_state.sql")
	down362 := readMigration(t, "db/migrations/362_handoff_durable_goal_state.down.sql")
	up527 := readMigration(t, "sql/migrations/startup/527_handoff_durable_goal_state.sql")
	down527 := readMigration(t, "sql/migrations/startup/527_handoff_durable_goal_state.down.sql")

	// Each 527 invariant must also appear in the 362 pair. We don't require
	// byte-equality (whitespace and ordering can legitimately differ) but
	// the load-bearing tokens must be present.
	upInvariant := []string{
		"goal_state JSONB",
		"goal_state_version INTEGER",
		"restore_status VARCHAR(32)",
		`'accounting_confirmed'`,
		`'restored'`,
		`'manual_required'`,
		"idx_handoff_pending_restore",
	}
	downInvariant := []string{
		"WHEN status IN ('accounting_confirmed', 'restored', 'manual_required') THEN 'confirmed'",
		"DROP COLUMN IF EXISTS goal_state",
		"DROP COLUMN IF EXISTS restore_status",
		"CHECK (status IN ('pending', 'confirmed', 'expired'))",
	}
	for _, want := range upInvariant {
		if !strings.Contains(up527, want) || !strings.Contains(up362, want) {
			t.Errorf("527 up must mirror 362; missing %q in 527 or 362", want)
		}
	}
	for _, want := range downInvariant {
		if !strings.Contains(down527, want) || !strings.Contains(down362, want) {
			t.Errorf("527 down must mirror 362; missing %q in 527 or 362", want)
		}
	}
}
