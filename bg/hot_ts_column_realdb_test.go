// hot_ts_column_realdb_test.go — 2026-09-23 252 PG SQL 日志审计轮的真库回归。
//
// R48 的 hotTableTSColumn 映射有 10/20 张表与真库不符，pgxmock/纯单元测试
// 钉死了错误期望值（TestHotTableTSColumn 当时"全绿"），直到 252 生产日志
// 出现每 promote 周期 10 条 42703 才暴露。本测试把闭环钉在驱动层：
// 对 promoteSpecs() 的每个 label，用 hotTableOldestRowAge 同构的 SQL 真实
// 打一把真库——列名错 = 42703 直接红。无 TEST_DATABASE_URL / TEST_DB_URL
// 时跳过（与 sql_audit_realdb_test.go 同门控）。
package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestHotTableOldestRowAge_RealDB 每 promoteSpec label 的 oldest-age 查询
// 必须在真库上可执行（列存在性语义），空表/有表都必须无错返回。
func TestHotTableOldestRowAge_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer conn.Close(ctx)

	// 与 db/db.go 生产配置对齐：SimpleProtocol（列名错误在该模式下同样爆）。
	for _, s := range promoteSpecs() {
		col := hotTableTSColumn(s.label)
		table := s.label
		if table[len(table)-4:] != "_hot" {
			table += "_hot"
		}
		// 与 hotTableOldestRowAge 同构；不允许的列名直接红。
		switch col {
		case "ts", "created_at", "bucket", "occurred_at":
		default:
			t.Errorf("label %q: column %q not in whitelist", s.label, col)
			continue
		}
		q := "SELECT EXTRACT(EPOCH FROM (now() - MIN(" + col + ")))::bigint FROM " + table
		var age *float64
		if err := conn.QueryRow(ctx, q).Scan(&age); err != nil {
			t.Errorf("label %q: %s -> %v", s.label, q, err)
		}
	}
}
