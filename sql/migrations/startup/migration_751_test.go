package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 751 (R69 12h 审计轮, 2026-09-26; 1614 轮 O-3 裁决落地) pins the
// timezone of ensure_usage_facts_daily_partition via a function-level GUC
// (ALTER FUNCTION ... SET timezone). Rationale: 750 derives the partition
// bounds start_ts/end_ts in DECLARE initializers (p_date::timestamptz),
// which evaluate in the SESSION timezone — a UTC session yields a window
// shifted 8h off the Shanghai calendar that rollup/reconciliation (and the
// PartitionManager tick, partitionTZ) assume. The function-level SET takes
// effect at function entry, BEFORE DECLARE initializers — the dual of the
// 694 lesson (an in-body SET LOCAL does NOT cover initializers, which is
// why 694 moved its derivations into the body; a function-level SET does
// cover them, so 751 does not need a body rewrite).
//
// This contract pins its shape:
//
//	P1 up is a single idempotent ALTER FUNCTION ... SET timezone (no body
//	   rewrite, no SET LOCAL — the 694 anti-pattern for initializers);
//	P2 down RESETs the GUC only (no DROP, no body change);
//	P3 installer runner + upgrade-channel script both register 751;
//	P4 the Go boot channel (db.ensureUsageFactsDailyPartition) converges
//	   existing databases with the same ALTER statement;
//	P5 the Go boot channel derives today/tomorrow on the Shanghai calendar
//	   ((now() AT TIME ZONE 'Asia/Shanghai')::date), matching the tick's
//	   partitionTZ, instead of session-timezone current_date.
//
// Live PG17 evidence (UTC session → partition bounds still +08) is pinned by
// db/db_751_tz_pin_realdb_test.go.
// stripSQLCommentsFor751 removes `--` line comments so contract assertions
// hit executable statements, not the rationale prose (which necessarily
// mentions ALTER FUNCTION / SET LOCAL when explaining why they were chosen).
// (Suffixed twin of stripSQLComments in migration_602_test.go — the package
// predates a shared helper; keeping the family convention.)
func stripSQLCommentsFor751(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if idx := strings.Index(l, "--"); idx >= 0 {
			lines[i] = l[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func TestMigration751UsageFactsPartitionTZPinContract(t *testing.T) {
	upBytes, err := os.ReadFile("751_usage_facts_partition_tz_pin.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := normalizeSQL(stripSQLCommentsFor751(string(upBytes)))

	// ── P1: single ALTER FUNCTION ... SET timezone, no body rewrite ────
	if !strings.Contains(up,
		"ALTER FUNCTION PUBLIC.ENSURE_USAGE_FACTS_DAILY_PARTITION(DATE) SET TIMEZONE = 'ASIA/SHANGHAI'") {
		t.Error("751 up must pin timezone via ALTER FUNCTION ... SET timezone = 'Asia/Shanghai'")
	}
	if strings.Contains(up, "SET LOCAL") {
		t.Error("751 must not use in-body SET LOCAL — per the 694 lesson it does not cover DECLARE initializers")
	}
	if strings.Contains(up, "CREATE OR REPLACE") {
		t.Error("751 must not rewrite the function body (ALTER-only keeps 750's canonical body intact)")
	}
	if got := strings.Count(up, "ALTER FUNCTION"); got != 1 {
		t.Errorf("751 up should contain exactly one ALTER FUNCTION statement, got %d", got)
	}

	// ── P2: down resets the GUC only ───────────────────────────────────
	downBytes, err := os.ReadFile("751_usage_facts_partition_tz_pin.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := normalizeSQL(stripSQLCommentsFor751(string(downBytes)))
	if !strings.Contains(down,
		"ALTER FUNCTION PUBLIC.ENSURE_USAGE_FACTS_DAILY_PARTITION(DATE) RESET TIMEZONE") {
		t.Error("751 down must RESET timezone (restore 750's session-tz semantics)")
	}
	if strings.Contains(down, "DROP") {
		t.Error("751 down must not drop anything (function body and partitions survive)")
	}

	// ── P3: both delivery channels register 751 ───────────────────────
	runnerBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatal(err)
	}
	runner := string(runnerBytes)
	if !strings.Contains(runner, "751_usage_facts_partition_tz_pin.sql") {
		t.Error("installer dbinit runner must register 751 (fresh installs)")
	}
	if strings.Index(runner, "751_usage_facts_partition_tz_pin.sql") <
		strings.Index(runner, "750_usage_facts_daily_partition.sql") {
		t.Error("751 must be registered after 750 (pin applies to the function 750 installs)")
	}
	applyBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applyBytes), "751_usage_facts_partition_tz_pin.sql") {
		t.Error("apply-db-revision-sequence.sh must register 751 (upgrade channel)")
	}

	// ── P4: Go boot channel converges with the same ALTER ─────────────
	dbBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "db.go"))
	if err != nil {
		t.Fatal(err)
	}
	dbSrc := string(dbBytes)
	if !strings.Contains(dbSrc, "SET timezone = 'Asia/Shanghai'") {
		t.Error("db.ensureUsageFactsDailyPartition must run the same ALTER ... SET timezone (dual-channel convergence)")
	}

	// ── P5: boot derives today/tomorrow on the Shanghai calendar ──────
	if !strings.Contains(dbSrc, "(now() AT TIME ZONE 'Asia/Shanghai')::date") {
		t.Error("boot ensure must derive dates on the Shanghai calendar, not session-timezone current_date")
	}
	if strings.Contains(dbSrc, "ensure_usage_facts_daily_partition(current_date") {
		t.Error("boot ensure must no longer pass session-timezone current_date to the ensure function")
	}
}
