package analytics

import (
	"strings"
	"testing"
)

// TestSessionUniverseJoinDedup 验证去重子查询的 SQL 形态：
// sessions 按天分区（同一会话每天一行）⇒ 必须用 DISTINCT ON 去重。
func TestSessionUniverseJoinDedup(t *testing.T) {
	got := SessionUniverseJoin("ss", "s", "sd")
	normalized := strings.ToLower(strings.Join(strings.Fields(got), " "))

	// 必须包含 DISTINCT ON (session_id, tenant_id)
	if !strings.Contains(normalized, "distinct on (session_id, tenant_id)") {
		t.Errorf("SessionUniverseJoin 缺少去重子查询 DISTINCT ON (session_id, tenant_id):\n%s", got)
	}

	// 必须包含 ORDER BY ... partition_date DESC（取最新分区行）
	if !strings.Contains(normalized, "partition_date desc") {
		t.Errorf("SessionUniverseJoin 缺少 ORDER BY partition_date DESC（未取最新分区行）:\n%s", got)
	}

	// 必须从子查询选出探测判定所需字段
	requiredFields := []string{"session_id", "tenant_id", "is_probe", "owner_user", "client_type", "title"}
	for _, field := range requiredFields {
		if !strings.Contains(normalized, strings.ToLower(field)) {
			t.Errorf("SessionUniverseJoin 去重子查询缺少必需字段 %s:\n%s", field, got)
		}
	}
}
