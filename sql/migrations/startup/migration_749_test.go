package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 749 (R67 24h 审计轮, 2026-09-26) adds the occurred_at leading
// btree on usage_facts. R66 48h 审计轮静态发现 + R67 存储域子代理复核：
//
//	usage_facts 是 PARTITION BY RANGE (occurred_at) 父表但仅有一个
//	DEFAULT 分区，4 个二级索引全部非 occurred_at 前导；每日 rollup
//	五查询（domains/reportrollup/rollup.go）与 stats 对账查询
//	（domains/stats/reconciliation.go）全是纯 occurred_at 范围条件，
//	只能全表顺序扫，线性退化至被共享 PG 的 30s statement_timeout
//	成批击杀。
//
// The contract pins the invariants that make 749 safe and effective:
//
//	C1  the index is bound to the real range predicates (parsed from the
//	    rollup/reconciliation sources, not copy-paste);
//	C2  three-stage shape for the partitioned parent (per-partition
//	    CONCURRENTLY template → ON ONLY shell → ATTACH loop), 744 同款；
//	C3  the file is NOT wrapped in BEGIN/COMMIT and never issues a plain
//	    full-data build (installer psql --single-transaction 与分区父表
//	    42809 双约束；749 走升级通道 + Go ensure db 包 ensureUsageFactsOccurredAtIndex);
//	C4  down drops the parent index + sweeps unattached per-partition
//	    leftovers.
func TestMigration749UsageFactsOccurredAtIndexContract(t *testing.T) {
	upBytes, err := os.ReadFile("749_usage_facts_occurred_at_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)

	// ── C1: bound to the real occurred_at range consumers ─────────────
	rollupSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "domains", "reportrollup", "rollup.go"))
	if err != nil {
		t.Fatal(err)
	}
	rollupCompact := normalizeSQL(string(rollupSrc))
	// 五个查询逐个数没有稳定锚点，用总数下限钉住。
	if got := strings.Count(rollupCompact, "OCCURRED_AT >= $1 AND OCCURRED_AT < $2"); got < 5 {
		t.Errorf("rollup.go 纯 occurred_at 范围查询少于 5 处（found %d）——若已改写查询形状，重新审计 749 是否仍是有效索引", got)
	}
	reconSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "domains", "stats", "reconciliation.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(normalizeSQL(string(reconSrc)), "OCCURRED_AT") {
		t.Error("reconciliation.go 不再按 occurred_at 过滤——749 索引的对账侧受益面消失，重新审计")
	}

	// ── C2: three-stage shape ──────────────────────────────────────────
	if !strings.Contains(upCompact, "CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON PUBLIC.%I (OCCURRED_AT DESC)") {
		t.Error("749 must build per-partition indexes CONCURRENTLY via the \\gexec format template (occurred_at DESC)")
	}
	if !strings.Contains(upCompact, "INHRELID FROM PG_INHERITS WHERE INHPARENT = 'PUBLIC.USAGE_FACTS'::REGCLASS") {
		t.Error("749 must enumerate partitions of the usage_facts parent (pg_inherits), not hardcode DEFAULT")
	}
	if !strings.Contains(upCompact, "CREATE INDEX IF NOT EXISTS IDX_USAGE_FACTS_OCCURRED_AT ON ONLY PUBLIC.USAGE_FACTS (OCCURRED_AT DESC)") {
		t.Error("749 must create the parent index as an ON ONLY metadata shell")
	}
	if !strings.Contains(upCompact, "ATTACH PARTITION PUBLIC.%I") {
		t.Error("749 must ATTACH every per-partition index to the parent index")
	}

	// ── C3: non-transactional + Go ensure channel ──────────────────────
	if strings.Contains(upCompact, "BEGIN;") || strings.Contains(upCompact, "COMMIT;") {
		t.Error("749 must NOT wrap its body in BEGIN/COMMIT — CREATE INDEX CONCURRENTLY cannot run inside a transaction block")
	}
	if strings.Contains(upCompact, "CREATE INDEX IF NOT EXISTS IDX_USAGE_FACTS_DEFAULT_OCCURRED_AT") &&
		!strings.Contains(upCompact, "%I") {
		t.Error("749 must not hardcode the DEFAULT partition index without the partition-generic template")
	}
	dbSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "db.go"))
	if err != nil {
		t.Fatal(err)
	}
	dbCompact := normalizeSQL(string(dbSrc))
	if !strings.Contains(dbCompact, "ENSUREUSAGEFACTSOCCURREDATINDEX") {
		t.Error("db.ensureUsageFactsOccurredAtIndex 缺失——存量库靠 Go ensure 通道收敛，749 SQL 文件不经 installer")
	}

	// ── C4: down drops parent + sweeps unattached leftovers ───────────
	downBytes, err := os.ReadFile("749_usage_facts_occurred_at_index.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downCompact := normalizeSQL(string(downBytes))
	if !strings.Contains(downCompact, "DROP INDEX IF EXISTS PUBLIC.IDX_USAGE_FACTS_OCCURRED_AT") {
		t.Error("749 down must drop the parent index")
	}
	if !strings.Contains(downCompact, "INHPARENT = 'PUBLIC.USAGE_FACTS'::REGCLASS") {
		t.Error("749 down must sweep unattached per-partition leftover indexes")
	}
}
