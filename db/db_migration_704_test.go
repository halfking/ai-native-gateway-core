package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration704EnsureWiredAndMirrorsSQL guards the point-6 wiring sync for
// migration 704 (R28 #12a, 096141ecc): binary environments only run the
// db.go ensure chain (sql/migrations/startup files are never applied there),
// so a missing ensure left llm_gateway.credentials without
// plan_quota_probe_failed_at and the balance_floor_guard 5-minute sweep
// failed with 42703 on every round (2026-09-14 PG audit).
func TestMigration704EnsureWiredAndMirrorsSQL(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	// The self-heal must stay wired in applyMigrationsOnce, right after the
	// 701 ensure it extends (same table, later migration).
	callIdx := strings.Index(text, "ensureCredentialPlanQuotaProbeBackoff(migCtx)")
	if callIdx < 0 {
		t.Fatal("db.go must call ensureCredentialPlanQuotaProbeBackoff in applyMigrationsOnce")
	}
	balanceIdx := strings.Index(text, "ensureCredentialBalanceFloor(migCtx)")
	if balanceIdx < 0 || balanceIdx > callIdx {
		t.Fatal("ensureCredentialPlanQuotaProbeBackoff must be wired after ensureCredentialBalanceFloor")
	}

	const sqlConst = "func (d *DB) ensureCredentialPlanQuotaProbeBackoff("
	fnStart := strings.Index(text, sqlConst)
	if fnStart < 0 {
		t.Fatal("db.go missing ensureCredentialPlanQuotaProbeBackoff wrapper")
	}
	nextFn := strings.Index(text[fnStart+1:], "func (d *DB) ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensureCredentialPlanQuotaProbeBackoff")
	}
	body := text[fnStart : fnStart+1+nextFn]

	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS plan_quota_probe_failed_at timestamp with time zone") {
		t.Fatal("ensure SQL must ADD COLUMN IF NOT EXISTS plan_quota_probe_failed_at timestamptz (migration 704)")
	}
	if !strings.Contains(body, "VALUES ('704', 'plan quota probe failure backoff stamp (credentials.plan_quota_probe_failed_at)')") {
		t.Fatal("ensure SQL must stamp schema_migrations version 704 (dual-ledger convention)")
	}

	// The ensure SQL must stay in sync with the canonical migration file:
	// same ALTER column + type, and the migration file must not gain columns
	// the ensure chain does not mirror.
	migBytes, err := os.ReadFile("../sql/migrations/startup/704_plan_quota_probe_backoff.sql")
	if err != nil {
		t.Fatal(err)
	}
	mig := string(migBytes)
	if !strings.Contains(mig, "ADD COLUMN IF NOT EXISTS plan_quota_probe_failed_at timestamp with time zone") {
		t.Fatal("migration 704 file altered; keep ensureCredentialPlanQuotaProbeBackoff mirrored")
	}
	// Count only the actual clause (the file header comment repeats the
	// phrase without the column name).
	alterCount := strings.Count(mig, "ADD COLUMN IF NOT EXISTS plan_quota_probe_failed_at")
	if alterCount != 1 {
		t.Fatalf("migration 704 now has %d ADD COLUMN clauses; extend the ensure chain and this guard", alterCount)
	}

	// The scan SQL in bg/balance_floor_guard.go depends on the column; if the
	// feature gains more plan_quota_* columns this guard must grow with it.
	bgBytes, err := os.ReadFile("../bg/balance_floor_guard.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bgBytes), "plan_quota_probe_failed_at") {
		t.Fatal("bg/balance_floor_guard.go stopped referencing plan_quota_probe_failed_at; revisit the 704 ensure")
	}
}
