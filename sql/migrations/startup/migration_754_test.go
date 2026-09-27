package startup

// migration_754_test.go — R67 session-storage 审计子任务 7 (handoff §9) 的迁移
// 契约测试。
//
// 背景：331 (2026-07-04) 把 archive_request_logs / archive_request_wal 整族移除
// 后，request_logs 主表的月分区一直只被保留、从不归档。迁移 754 补齐该流水线。
//
// 编号：模板原写 746，但 746 已被 report_snapshots_internal_dims 占用；
// 750/751/752/753 也已占用（usage_facts_daily_partition /
// usage_facts_partition_tz_pin / mock_probe_history / session_turn_logs_ttl）。
// 754 是 2026-09-27 复核时的首个空闲号。提交前须再复核 origin/main。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestMigration754ArchiveRequestLogsDefaultContract(t *testing.T) {
	upBytes, err := os.ReadFile("754_archive_request_logs_default.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)
	upCode := normalizeSQL(stripSQLComments(up))

	// ── C1: function identity + set-returning shape ────────────────────
	// 返回形状是本任务最容易踩错的地方：每个归档分区一行，不是标量。
	if !strings.Contains(upCompact,
		"CREATE OR REPLACE FUNCTION PUBLIC.ARCHIVE_REQUEST_LOGS_DEFAULT(P_RETENTION_DAYS INTEGER) RETURNS TABLE(ARCHIVED_PARTITION TEXT, ROWS_ARCHIVED BIGINT)") {
		t.Error("754 must define archive_request_logs_default(p_retention_days integer) RETURNS TABLE(archived_partition text, rows_archived bigint) — one row per archived partition")
	}

	// ── C2: retention guard matches the spec entry [7,365] ─────────────
	// 越界 RAISE（失败即停）而不是静默 clamp：SQL 是最后一道边界。
	if !strings.Contains(upCode, "RAISE EXCEPTION") {
		t.Error("754 must RAISE EXCEPTION on out-of-range retention — fail-closed, matching bg.clampRequestLogsArchiveDays")
	}
	if !strings.Contains(upCode, "P_RETENTION_DAYS < 7") || !strings.Contains(upCode, "P_RETENTION_DAYS > 365") {
		t.Error("754 must guard retention against [7,365] to match lifecycle.request_logs_ttl_days Min/Max")
	}

	// ── C3: idempotent replay (no CONCURRENTLY, guarded CREATE TABLE) ──
	if strings.Contains(upCompact, "CONCURRENTLY") {
		t.Error("754 must not use CONCURRENTLY — it rides the installer single-transaction channel")
	}
	// 归档目标表是逐月动态建的。幂等可以用两种等价方式达成，本迁移用的是
	// 显式 pg_class 存在性守卫（比 IF NOT EXISTS 更严：同时钉住
	// relnamespace，避免同名表落在别的 schema 时误判存在）：
	//   a) CREATE TABLE ... IF NOT EXISTS
	//   b) IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname=… ) THEN
	// 断言的是「幂等」这个不变量，而不是某一种写法 —— 早期版本把这里写死成
	// IF NOT EXISTS，结果把正确的 pg_class 守卫判成失败。
	idempotentCreate := strings.Contains(upCompact, "CREATE TABLE IF NOT EXISTS") ||
		strings.Contains(upCode, "SELECT 1 FROM PG_CLASS")
	if !idempotentCreate {
		t.Error("754 must create archive partitions idempotently — either CREATE TABLE IF NOT EXISTS, or a pg_class existence guard before CREATE TABLE")
	}

	// ── C4: small batch + primary-key cursor (handoff §9 冻结契约) ─────
	// 交接文档把「小批量游标化」列为不可回退的冻结契约：一次性大范围
	// DELETE/INSERT 会长事务、锁表、并撑爆 WAL。
	batch := regexp.MustCompile(`BATCH_SIZE\s+CONSTANT\s+INTEGER\s*:=\s*(\d+)`).FindStringSubmatch(upCode)
	if batch == nil {
		t.Error("754 must declare an explicit BATCH_SIZE constant (handoff §9 frozen contract: small batches)")
	} else if n := batch[1]; n != "1000" {
		t.Errorf("BATCH_SIZE = %s, want 1000 (handoff §9 requires ≤1000 rows per batch)", n)
	}
	if !strings.Contains(upCode, "LAST_ID") {
		t.Error("754 must page by primary key (last_id) — a re-derived predicate per batch would re-scan and can re-archive")
	}

	// ── C5: source partitions are NOT dropped ──────────────────────────
	// R68 纪律（654 / 337 事故复盘）：禁止 DROP 父表的月分区。move-then-attach。
	if strings.Contains(upCode, "DROP TABLE") {
		t.Error("754 must not DROP source partitions — R68 forbids dropping monthly partitions of a partitioned parent; archive is move-then-attach only")
	}

	// ── C6: up/down symmetry ───────────────────────────────────────────
	downBytes, err := os.ReadFile("754_archive_request_logs_default.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "DROP FUNCTION IF EXISTS public.archive_request_logs_default") {
		t.Error("754 down must drop archive_request_logs_default")
	}
	if strings.Contains(normalizeSQL(stripSQLComments(down)), "CREATE TABLE") {
		t.Error("754 down must not create anything")
	}

	// ── C7: the Go caller exists and reads the setting ─────────────────
	// 没有调用方的迁移等于没写：这一条专门钉住「迁移不是惰性资产」。
	pmBytes, err := os.ReadFile("../../../bg/partition_manager.go")
	if err != nil {
		t.Fatal(err)
	}
	pm := string(pmBytes)
	// Assert the CALL SITE, not merely the presence of the function's name.
	// An earlier version of this assertion only grepped for
	// "archive_request_logs_default", which is satisfied by the method
	// definition alone — deleting `pm.archiveOldRequestLogs(ctx)` from
	// runCleanup left the test green. That is precisely the "looks wired,
	// changes nothing" failure this assertion exists to prevent.
	if !strings.Contains(pm, "pm.archiveOldRequestLogs(ctx)") {
		t.Error("bg.PartitionManager.runCleanup must actually CALL pm.archiveOldRequestLogs(ctx) — a defined-but-uncalled helper leaves the migration inert")
	}
	if !strings.Contains(pm, "lifecycle.request_logs_ttl_days") {
		t.Error("bg.PartitionManager must read lifecycle.request_logs_ttl_days (hot reload)")
	}

	// ── C8: revision-sequence registration ────────────────────────────
	seq, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seq), "754_archive_request_logs_default.sql") {
		t.Error("apply-db-revision-sequence.sh must carry 754_archive_request_logs_default.sql — upgrade databases would never apply it otherwise")
	}
	// Ordering vs the neighbouring entries it must follow.
	last := -1
	for _, name := range []string{
		"750_usage_facts_daily_partition.sql",
		"753_session_turn_logs_ttl.sql",
		"754_archive_request_logs_default.sql",
	} {
		pos := strings.Index(string(seq), name)
		if pos < 0 {
			t.Errorf("apply-db-revision-sequence.sh does not carry %s", name)
			continue
		}
		if pos < last {
			t.Errorf("migration channel order regressed: %s appears before its predecessor", name)
		}
		last = pos
	}
	_ = os.Getenv("TEST_PG_URL") // real-DB checks are opt-in; presence only documents the knob
}
