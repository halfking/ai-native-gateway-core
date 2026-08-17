package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration532FinalSuccessContract 钉死 v4 T7 migration 的关键语义：
// 列（hot + 分区母表双侧、NOT NULL DEFAULT FALSE）、hot 侧部分唯一索引
// （谓词含 is_final_success 且排除空 gw_session_id）、分区侧 best-effort
// 索引（DO 循环 + 异常降级，不阻塞升级）、ensure_request_logs_partition
// 为新分区补索引、视图 append pattern、幂等标记。
func TestMigration532FinalSuccessContract(t *testing.T) {
	up, err := os.ReadFile("532_request_logs_final_success.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)

	required := []string{
		// 1) 双侧加列（promote SELECT * 位置对齐，参照 491/510）
		"ALTER TABLE request_logs_hot\n    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN NOT NULL DEFAULT FALSE",
		"ALTER TABLE request_logs\n    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN NOT NULL DEFAULT FALSE",
		// 2) hot 侧部分唯一索引：只约束被标记行，空会话不受限
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_request_logs_hot_final_success_session",
		"ON request_logs_hot (gw_session_id)",
		"WHERE is_final_success",
		"AND gw_session_id IS NOT NULL",
		"AND gw_session_id <> ''",
		// 3) 分区侧 best-effort：pg_inherits 循环 + 异常降级
		"FROM pg_inherits i",
		"uq_%s_final_success_session",
		"EXCEPTION WHEN OTHERS THEN",
		// 4) ensure_request_logs_partition 为未来分区补索引
		"CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition",
		// 5) 视图 append pattern（448/459/491/510 同款）
		"new_cols text[] := ARRAY['is_final_success']",
		"SELECT %s FROM request_logs_hot",
		"SELECT %s FROM request_logs",
		"request_logs_with_current_month",
		// 6) 幂等与事务边界
		"BEGIN;",
		"COMMIT;",
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("532 up missing %q", want)
		}
	}

	// 唯一索引必须建在 hot（写路径）而非分区母表（母表唯一索引需含分区键 ts，
	// 42P17 无法建单列 gw_session_id 唯一索引）。
	if strings.Contains(body, "CREATE UNIQUE INDEX IF NOT EXISTS uq_request_logs_final_success_session\n    ON request_logs (") {
		t.Error("532 must not create a parent-table unique index (partition key ts not included → 42P17)")
	}
}

func TestMigration532DownRestoresPreState(t *testing.T) {
	down, err := os.ReadFile("532_request_logs_final_success.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(down)
	for _, want := range []string{
		"DROP INDEX IF EXISTS uq_request_logs_hot_final_success_session",
		"uq_' || part.partition_name || '_final_success_session",
		"DROP COLUMN IF EXISTS is_final_success",
		// 视图先重建（去掉列）再删列，避免视图依赖阻塞 DROP COLUMN。
		"request_logs_with_current_month",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("532 down missing %q", want)
		}
	}
}

// TestMigration532EnsurePartitionFunctionKeepsPriorBehavior：重建后的
// ensure_request_logs_partition 必须保留原有 trgm 索引语句（不回退既有行为），
// 仅追加 final-success 索引步骤。
func TestMigration532EnsurePartitionFunctionKeepsPriorBehavior(t *testing.T) {
	up, err := os.ReadFile("532_request_logs_final_success.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)
	for _, want := range []string{
		"'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)'",
		"'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)'",
		"'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)'",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("532 ensure_request_logs_partition rebuild lost prior statement %q", want)
		}
	}
}
