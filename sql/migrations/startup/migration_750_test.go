package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Migration 750 (R68 24h 审计轮, 2026-09-26; 当日修订审计 P1 重写)
// installs ensure_usage_facts_daily_partition(p_date DATE) and prebuilds
// today + tomorrow partitions for usage_facts. The 2026-09-26 same-day
// revision audit found the initial body (plain CREATE TABLE ... PARTITION OF)
// deterministically fails on data-bearing databases: PG rejects creating a
// concrete partition whose bounds overlap rows already sitting in the
// DEFAULT partition ("updated partition constraint for default partition
// would be violated by some row"), which turned into a boot-blocking
// failure through db.Open → ApplyMigrations → ensureUsageFactsDailyPartition
// (no-DB mode + deploy auto-rollback on 252-family shared PG).
//
// The rewritten body is move-then-attach. This contract pins its shape:
//
//	C1 advisory xact lock serializes concurrent ensures + post-lock re-check;
//	C2 idempotent short-circuit on an already-attached partition (pg_inherits);
//	C3 standalone staging table via LIKE ... INCLUDING DEFAULTS INCLUDING
//	   INDEXES so ATTACH's partitioned-index matching is metadata-only;
//	C4 ACCESS EXCLUSIVE lock on usage_facts_default across the move window;
//	C5 in-bounds rows are moved out of the DEFAULT partition
//	   (DELETE ... RETURNING → INSERT) before ATTACH — the load-bearing fix;
//	C6 ATTACH PARTITION with the day's bounds;
//	C7 down drops only the function (daily partitions survive);
//	C8 installer runner + upgrade-channel script both register 750.
func TestMigration750UsageFactsDailyPartitionContract(t *testing.T) {
	upBytes, err := os.ReadFile("750_usage_facts_daily_partition.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := normalizeSQL(string(upBytes))

	// ── C1: advisory lock + double-check ──────────────────────────────
	if !strings.Contains(up, "PG_ADVISORY_XACT_LOCK( HASHTEXT('ENSURE_USAGE_FACTS_DAILY_PARTITION:' || PNAME))") {
		t.Error("750 must serialize concurrent ensures with pg_advisory_xact_lock keyed on the partition name")
	}
	if got := strings.Count(up, "FROM PG_INHERITS I"); got < 2 {
		t.Errorf("750 must short-circuit AND post-lock re-check via pg_inherits (found %d probes, want ≥2)", got)
	}

	// ── C2: idempotent short-circuit precedes the lock ────────────────
	// (anchor on the call shape — the header comment also spells the bare name)
	scPos := strings.Index(up, "IF EXISTS ( SELECT 1 FROM PG_INHERITS")
	lockPos := strings.Index(up, "PG_ADVISORY_XACT_LOCK( HASHTEXT")
	if scPos < 0 || scPos > lockPos {
		t.Error("750's idempotent short-circuit must come before the advisory lock (uncontended fast path)")
	}

	// ── C3: staging table carries parent defaults + indexes ──────────
	if !strings.Contains(up,
		"CREATE TABLE %I (LIKE USAGE_FACTS INCLUDING DEFAULTS INCLUDING INDEXES)") {
		t.Error("750 must stage the partition via LIKE ... INCLUDING DEFAULTS INCLUDING INDEXES (metadata-only index attach)")
	}

	// ── C4: DEFAULT locked exclusively across the move window ─────────
	lockDefaultPos := strings.Index(up, "LOCK TABLE USAGE_FACTS_DEFAULT IN ACCESS EXCLUSIVE MODE")
	deletePos := strings.Index(up, "DELETE FROM USAGE_FACTS_DEFAULT")
	attachPos := strings.Index(up, "ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)")
	if lockDefaultPos < 0 || deletePos < 0 || attachPos < 0 {
		t.Fatal("750 move-then-attach skeleton incomplete (lock/delete/attach missing)")
	}
	if !(lockDefaultPos < deletePos && deletePos < attachPos) {
		t.Error("750 must lock DEFAULT exclusively BEFORE the move and keep it through ATTACH")
	}

	// ── C5: in-bounds rows leave the DEFAULT partition first ──────────
	if !strings.Contains(up, "RETURNING *") ||
		!strings.Contains(up, "INSERT INTO %I SELECT * FROM MOVED") {
		t.Error("750 must move in-bounds DEFAULT rows via DELETE ... RETURNING → INSERT (the P1 fix itself)")
	}
	// The move predicate must use the same bounds as the ATTACH.
	boundsRE := regexp.MustCompile(`OCCURRED_AT >= %L AND OCCURRED_AT < %L`)
	if !boundsRE.MatchString(up) {
		t.Error("750's move predicate must mirror the ATTACH bounds (occurred_at >= %L AND < %L)")
	}

	// The initial (broken) one-shot shape must not come back.
	if strings.Contains(up, "CREATE TABLE IF NOT EXISTS %I PARTITION OF USAGE_FACTS") {
		t.Error("750 must not create the partition in one shot — that shape is the boot-blocker this rewrite removes")
	}

	// ── C7: down only drops the function ──────────────────────────────
	downBytes, err := os.ReadFile("750_usage_facts_daily_partition.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := normalizeSQL(string(downBytes))
	if !strings.Contains(down, "DROP FUNCTION IF EXISTS ENSURE_USAGE_FACTS_DAILY_PARTITION") {
		t.Error("750 down must drop the ensure function")
	}
	if strings.Contains(down, "DROP TABLE") {
		t.Error("750 down must not drop daily partitions (data-bearing objects)")
	}

	// ── C8: both delivery channels register 750 ───────────────────────
	runnerBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runnerBytes), "750_usage_facts_daily_partition.sql") {
		t.Error("installer dbinit runner must register 750 (fresh installs)")
	}
	applyBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applyBytes), "750_usage_facts_daily_partition.sql") {
		t.Error("apply-db-revision-sequence.sh must register 750 (upgrade channel)")
	}
}
