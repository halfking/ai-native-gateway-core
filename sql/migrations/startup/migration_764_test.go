package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 764 (三十六轮 R36-B3, 2026-09-30) 给 request_logs 分区家族补
// (tenant_id, ts DESC) 索引：341 只索引了 hot 侧，分区父表从未有 tenant
// 前导索引，tenant 维度 days>7 聚合对每分区全表扫（252-dev 真库 EXPLAIN
// 实证）。契约：
//
//	C1  父表上 CREATE INDEX IF NOT EXISTS（级联全部分区，重放 no-op）。
//	C2  形态与 hot 侧 idx_request_logs_hot_tenant_ts 对齐：(tenant_id, ts DESC)。
//	C3  顶层语句，无显式 BEGIN/COMMIT（758 DO-EXECUTE 教训 + installer
//	    --single-transaction 纪律）。
//	C4  台账自登记（695-705 定式，ON CONFLICT 幂等）。
//	C5  sequence 通道登记：apply-db-revision-sequence.sh 携带 764 行。
//	C6  installer 五点同步：embeddata 副本与 canonical 逐字节一致。
func TestMigration764RequestLogsTenantTsIndex(t *testing.T) {
	upBytes, err := os.ReadFile("764_request_logs_tenant_ts_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCode := normalizeSQL(stripSQLComments(up))

	// ── C1/C2: 分区父表索引，形态对齐 hot 侧 ─────────────────────
	if !strings.Contains(upCode, "CREATE INDEX IF NOT EXISTS IDX_REQUEST_LOGS_TENANT_TS ON PUBLIC.REQUEST_LOGS USING BTREE (TENANT_ID, TS DESC)") {
		t.Errorf("764 必须在分区父表上 CREATE INDEX IF NOT EXISTS (tenant_id, ts DESC)——省略 DESC 会与 hot 侧形态漂移")
	}

	// ── C3: 顶层语句 ─────────────────────────────────────────────
	if strings.Contains(upCode, "BEGIN;") || strings.Contains(upCode, "COMMIT;") {
		t.Error("764 不得携带显式 BEGIN/COMMIT（installer --single-transaction 纪律）")
	}
	if strings.Contains(upCode, "EXECUTE") {
		t.Error("764 不得使用 DO $$ … EXECUTE $ddl$ 形态（758 静默无效教训）")
	}

	// ── C4: 台账自登记 ───────────────────────────────────────────
	if !strings.Contains(up, "INSERT INTO public.schema_migrations (version, description)") ||
		!strings.Contains(up, "'764'") || !strings.Contains(up, "ON CONFLICT (version) DO NOTHING") {
		t.Error("764 必须自带 schema_migrations 自登记（幂等）")
	}

	// ── C5: sequence 通道登记 ────────────────────────────────────
	seqBytes, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seqBytes), "764_request_logs_tenant_ts_index.sql") {
		t.Error("apply-db-revision-sequence.sh 必须登记 764（R63 迁移双轨制死区教训）")
	}

	// ── C6: installer embeddata 副本一致 ─────────────────────────
	embBytes, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/764_request_logs_tenant_ts_index.sql")
	if err != nil {
		t.Fatalf("installer embeddata 副本缺失（c8c102698 五点缺失红的前四点）: %v", err)
	}
	if string(embBytes) != string(upBytes) {
		t.Error("installer embeddata 副本必须与 canonical 逐字节一致")
	}

	// down 文件存在且撤回同一索引。
	downBytes, err := os.ReadFile("764_request_logs_tenant_ts_index.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(downBytes), "DROP INDEX IF EXISTS public.idx_request_logs_tenant_ts") {
		t.Error("764 down 必须撤回 idx_request_logs_tenant_ts")
	}
}
