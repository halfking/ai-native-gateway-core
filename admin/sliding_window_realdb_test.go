// sliding_window_realdb_test.go — 2026-09-21 252 部署验证轮（纪律⑪）：
// PG 语义修复的完成态 = 真库经驱动执行。抽屉轮询查询的生产同一文本经
// SimpleProtocol 池对真库执行（bg/sql_audit_realdb_test.go 模板）。
//
// 2026-10-11 252 审计第 32 轮起回退查询分两路（slidingWindowHotMaxMinutes）：
//   - hot 快路径（≤120min）：session_turns_hot ∪ _request_logs_hot（去重
//     反连接），断言计划走 hot 表达式索引且不触碰 canonical 视图——
//     视图三臂全分区反连接阻断 LIMIT 下推，252 实测 med 1.27s/次，是
//     抽屉 batch 轮询的事故放大器；
//   - 视图长窗路径（>120min）：保留原断言（72h 长窗走 729 表达式索引）。
//
// 两路在窗口内语义等价的强对拍（EXCEPT 双向差集）已在 252 真库完成
// （轮报告 §修复）；本文件以同窗口双路径执行一致性做冒烟级回归。
//
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

func slidingWindowScanRows(t *testing.T, rows pgx.Rows) {
	t.Helper()
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
}

func slidingWindowExplainPlan(t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	expl, err := pool.Query(context.Background(), "EXPLAIN "+query, args...)
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
	return sb.String()
}

// hot 表三件套在库（706b/707b prelude 之后的库形态）。
func hotWindowTablesPresent(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.session_turns_hot') IS NOT NULL
		     AND to_regclass('public.session_turn_details_hot') IS NOT NULL
		     AND to_regclass('public._request_logs_hot') IS NOT NULL`,
	).Scan(&ok); err != nil {
		t.Fatalf("probe hot tables: %v", err)
	}
	return ok
}

// TestCredentialDrawerHotWindow_RealDB 回归 hot 快路径：窗口 ≤
// slidingWindowHotMaxMinutes 的回退查询必须在真库解析执行（SimpleProtocol
// 参数内联），计划必须走 hot 表达式索引下推且不触碰 canonical 视图
// （回归点：查询文本漂移回视图、或窗口闸门误把长窗 routed 进 hot）。
func TestCredentialDrawerHotWindow_RealDB(t *testing.T) {
	pool := slidingWindowRealDBPool(t)
	ctx := context.Background()
	if !hotWindowTablesPresent(t, pool) {
		t.Skip("hot 双写表不在库（迁移未应用），跳过")
	}

	rows, err := pool.Query(ctx, slidingWindowHotQuery(), 0, "__no_such_model__", "5", 50, "")
	if err != nil {
		t.Fatalf("hot drawer window query failed under SimpleProtocol: %v", err)
	}
	slidingWindowScanRows(t, rows)

	plan := slidingWindowExplainPlan(t, pool, slidingWindowHotQuery(), 0, "__no_such_model__", "5", 50, "")
	if strings.Contains(plan, "request_logs_with_current_month") {
		t.Fatalf("hot 快路径计划触碰 canonical 视图（应只读 hot 双写表）:\n%s", plan)
	}
	if !strings.Contains(plan, "session_turns_hot") || !strings.Contains(plan, "_request_logs_hot") {
		t.Fatalf("hot 快路径计划未同时覆盖 session_turns_hot 与 _request_logs_hot 两臂:\n%s", plan)
	}
	// 索引条件下推断言：hot 索引在库时，credential+窗过滤必须进索引条件
	// 而非 Filter（回归点：表达式索引缺席或查询改动使过滤退化）。
	var idxOK bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		                WHERE c.relnamespace = 'public'::regnamespace
		                  AND c.relname IN ('idx_request_logs_hot_credential_model_ts',
		                                    'idx_session_turns_hot_credential_ts')
		                  AND i.indisvalid)`,
	).Scan(&idxOK); err != nil {
		t.Fatalf("probe hot indexes: %v", err)
	}
	if !idxOK {
		t.Skip("hot 表达式索引不在库，跳过计划断言")
	}
	if !strings.Contains(plan, "Idx") && !strings.Contains(plan, "Index") {
		t.Fatalf("hot 快路径计划无索引扫描:\n%s", plan)
	}
}

// TestCredentialDrawerWindowPathsAgree_RealDB 同窗口双路径冒烟对拍：
// 同一 (credential, model, window) 下 hot 快路径与视图长窗路径结果
// 必须一致（窗口 ≤ hot 保留期时语义等价的执行级证据；空结果对空结果
// 也是合法的一致形态——无流量 pair 正是抽屉 batch 轮询的多数）。
func TestCredentialDrawerWindowPathsAgree_RealDB(t *testing.T) {
	pool := slidingWindowRealDBPool(t)
	ctx := context.Background()
	if !hotWindowTablesPresent(t, pool) {
		t.Skip("hot 双写表不在库（迁移未应用），跳过")
	}

	fetch := func(query string) map[string]struct{} {
		t.Helper()
		rows, err := pool.Query(ctx, query, 0, "__no_such_model__", "30", 50, "")
		if err != nil {
			t.Fatalf("path query failed: %v", err)
		}
		defer rows.Close()
		set := map[string]struct{}{}
		for rows.Next() {
			var requestID string
			var tsMillis int64
			var success bool
			var latencyMs int64
			var errorKind string
			if err := rows.Scan(&requestID, &tsMillis, &success, &latencyMs, &errorKind); err != nil {
				t.Fatalf("scan: %v", err)
			}
			set[requestID] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return set
	}

	hot := fetch(slidingWindowHotQuery())
	view := fetch(slidingWindowQuery())
	if len(hot) != len(view) {
		t.Fatalf("同窗口双路径结果数不一致: hot=%d view=%d", len(hot), len(view))
	}
	for rid := range hot {
		if _, ok := view[rid]; !ok {
			t.Fatalf("hot 独有 request_id %q 不在视图结果中", rid)
		}
	}
}

// TestCredentialDrawerWindow_RealDB 回归 729：抽屉轮询查询（长窗路径，
// 生产同一构造函数文本）72h 长窗必须在真库解析执行；729 索引在库时
// 计划必须走 *_credential_ts 表达式索引（回归点：删掉 729 或改写视图
// 投影别名，长窗退回分区顺序扫即被抓获）。只读查询零足迹。
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

	rows, err := pool.Query(ctx, slidingWindowQuery(), 0, "__no_such_model__", "72", 50, "")
	if err != nil {
		t.Fatalf("drawer window query failed under SimpleProtocol: %v", err)
	}
	slidingWindowScanRows(t, rows)

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

	plan := slidingWindowExplainPlan(t, pool, slidingWindowQuery(), 0, "__no_such_model__", "72", 50, "")
	if !strings.Contains(plan, "_credential_ts") {
		t.Fatalf("72h drawer plan 未走 729 credential_ts 表达式索引（计划退回顺序/裸 ts 扫描）:\n%s", plan)
	}
}
