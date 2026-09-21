// sliding_window_realdb_test.go — 2026-09-21 252 部署验证轮（纪律⑪）：
// PG 语义修复的完成态 = 真库经驱动执行。抽屉轮询查询的 session_turns
// 分支长窗劣化由 729 表达式索引根治，本文件把生产同一文本的查询经
// SimpleProtocol 池对真库执行（bg/sql_audit_realdb_test.go 模板）：
//   - 语义面：视图冻结投影 + CASE credential 表达式 + COALESCE 模型过滤
//     在真实 planner 下解析执行、列型可扫；
//   - 计划面：729 索引在库时断言 72h 长窗走
//     *_credential_ts 索引而非分区顺序扫（索引缺席时跳过计划断言，
//     只做语义执行——迁移未应用的库不误报）。
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
package admin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func slidingWindowRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("parse dsn: %v", err)
	}
	// 生产全量 SimpleProtocol（db/db.go）——参数内联语义只有该模式可证。
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestCredentialDrawerWindow_RealDB 回归 729：抽屉轮询查询（生产同一
// 构造函数文本）72h 长窗必须在真库解析执行；729 索引在库时计划必须
// 走 *_credential_ts 表达式索引（回归点：删掉 729 或改写视图投影别名，
// 长窗退回分区顺序扫即被抓获）。只读查询零足迹。
func TestCredentialDrawerWindow_RealDB(t *testing.T) {
	pool := slidingWindowRealDBPool(t)
	ctx := context.Background()

	var viewOK bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.request_logs_with_current_month') IS NOT NULL`,
	).Scan(&viewOK); err != nil {
		t.Fatalf("probe view: %v", err)
	}
	if !viewOK {
		t.Skip("request_logs_with_current_month 不存在，跳过")
	}

	rows, err := pool.Query(ctx, slidingWindowQuery(), 0, "__no_such_model__", "72", 50)
	if err != nil {
		t.Fatalf("drawer window query failed under SimpleProtocol: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var requestID string
		var tsMillis int64
		var success bool
		var latencyMs int64
		var errorKind string
		if err := rows.Scan(&requestID, &tsMillis, &success, &latencyMs, &errorKind); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	var idxOK bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		                 WHERE c.relnamespace = 'public'::regnamespace
		                   AND c.relname LIKE 'session_turns%_credential_ts'
		                   AND i.indisvalid)`,
	).Scan(&idxOK); err != nil {
		t.Fatalf("probe 729 index: %v", err)
	}
	if !idxOK {
		t.Skip("729 credential_ts 表达式索引不在库（迁移未应用），跳过计划断言")
	}

	plan := "EXPLAIN " + slidingWindowQuery()
	expl, err := pool.Query(ctx, plan, 0, "__no_such_model__", "72", 50)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer expl.Close()
	var sb strings.Builder
	for expl.Next() {
		var line string
		if err := expl.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		sb.WriteString(line)
	}
	if err := expl.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	if !strings.Contains(sb.String(), "_credential_ts") {
		t.Fatalf("72h drawer plan 未走 729 credential_ts 表达式索引（计划退回顺序/裸 ts 扫描）:\n%s", sb.String())
	}
}
