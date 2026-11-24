package errorplaybook

// 数据面异常探测源的真库契约测试。
//
// ★ 与 store_realdb_test.go 同纪律：单测（sqlmock）证明不了「探测 SQL 里的
//   列名/表名与生产库形状逐列对得上」——只有真库能。本测试**只读**
//   （三条 SELECT COUNT），无 DSN 即跳过 ⇒ CI 缺环境不红。

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// testDSN 环境变量链（同 store_realdb_test.go 口径）。
func testDSN(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_ERROR_PLAYBOOK_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("无真库 DSN（TEST_ERROR_PLAYBOOK_DSN / LLM_GATEWAY_DATABASE_URL / DATABASE_URL）；真库契约在部署后跑")
	return ""
}

// TestRealDBDataQualityDetectSQLQueriesParse 钉三条探测 SQL 在真库上可执行
// （列名/表名/类型全部对得上）且返回单行单列。
func TestRealDBDataQualityDetectSQLQueriesParse(t *testing.T) {
	dsn := testDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck // 测试收尾
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := NewDataQualitySource(&stubSource{}, db, dqCfgForTest)
	for _, c := range s.checks {
		var count int64
		if err := db.QueryRowContext(ctx, c.countSQL).Scan(&count); err != nil {
			t.Errorf("探测 SQL 查不通（%s）：%v\nSQL: %s", c.anomalyType, err, c.countSQL)
			continue
		}
		if count < 0 {
			t.Errorf("%s 计数为负：%d", c.anomalyType, count)
		}
	}
}

// TestRealDBDataQualityMigrationEntriesPresent 钉迁移 999a 的六条种子条目
// 在真库里（部署后形态）。
func TestRealDBDataQualityMigrationEntriesPresent(t *testing.T) {
	dsn := testDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck // 测试收尾
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM public.error_playbook_entries
		WHERE created_by = 'seed:999a' AND error_type LIKE 'data_maintenance:%'
	`).Scan(&n); err != nil {
		t.Fatalf("查手册条目失败（995/999a 未部署？）：%v", err)
	}
	if n != 6 {
		t.Fatalf("seed:999a 条目数 = %d，want 6（迁移 999a 未应用或被改动）", n)
	}
}
