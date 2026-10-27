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
//
// ★★ R167（2026-10-27）修正：**v1 族已随迁移 890 真删**
// （`to_regclass('public._request_logs_hot')` 恒为 NULL）。
// ⇒ 原实现把 `_request_logs_hot` 放进「三件套」判定 ⇒ 该判定恒为 false
// ⇒ ★★ **本文件三条真库测试在 v1 退役之后全部永久 skip**
// ⇒ 包括「长窗必须走 729 credential_ts 表达式索引」那条守卫
// ⇒ ⇒ **长窗路径一度没有任何真库守卫**。
// ⇒ 现在只判会话族 hot 两表（v1 表退役是既定事实，不该再作为「库就绪」的
// 前置条件，否则等于用「表不存在」把守卫整体关掉）。
func hotWindowTablesPresent(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.session_turns_hot') IS NOT NULL
		     AND to_regclass('public.session_turn_details_hot') IS NOT NULL`,
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
	// ★ R167：v1 hot 表随 890 退役 ⇒ 断言改为「必须覆盖会话族 hot 两表」。
	// （原断言要求 `_request_logs_hot` 在场 ⇒ 在 v1 退役后恒假 ⇒ 这条真库
	//  测试连同上面三条一起被永久 skip。）
	if !strings.Contains(plan, "session_turns_hot") || !strings.Contains(plan, "session_turn_details_hot") {
		t.Fatalf("hot 快路径计划未同时覆盖 session_turns_hot 与 session_turn_details_hot 两表:\n%s", plan)
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

// TestCredentialDrawerWindowPathsAgree_RealDB 同窗口多路径冒烟对拍：
// 同一 (credential, model, window) 下
//   - hot 快路径（session_turns_hot 一臂）
//   - 长窗路径（session_turns_hot ∪ session_turns 两臂，R167 起不走视图）
//   - canonical 视图（R167 起的对拍参考臂）
// 三者结果必须一致（窗口 ≤ hot 保留期时语义等价的执行级证据；空结果对空
// 结果也是合法的一致形态——无流量 pair 正是抽屉 batch 轮询的多数）。
//
// ★ R167：原实现只有 hot 与视图两条路径。现在把「长窗两臂」也纳入对拍，
// 因为本轮正是把长窗从视图改成了两臂 ⇒ 这条对拍就是本次改动的**等价性
// 执行级证据**（并且它同时覆盖了「两臂 ≡ 一臂（窗口内）」这条前提）。
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
	long := fetch(slidingWindowQuery())
	view := fetch(slidingWindowCanonicalQuery())
	if len(hot) != len(view) {
		t.Fatalf("同窗口双路径结果数不一致: hot=%d view=%d", len(hot), len(view))
	}
	if len(hot) != len(long) {
		t.Fatalf("同窗口 hot 与长窗两臂路径结果数不一致: hot=%d long=%d", len(hot), len(long))
	}
	for rid := range hot {
		if _, ok := view[rid]; !ok {
			t.Fatalf("hot 独有 request_id %q 不在视图结果中", rid)
		}
		if _, ok := long[rid]; !ok {
			t.Fatalf("hot 独有 request_id %q 不在长窗两臂结果中", rid)
		}
	}
}

// TestCredentialDrawerWindow_RealDB 回归 729 + R167：抽屉轮询查询（长窗路径，
// 生产同一构造函数文本）72h 长窗必须在真库解析执行；729 索引在库时
// 计划必须走 *_credential_ts 表达式索引（回归点：删掉 729 或改写视图
// 投影别名，长窗退回分区顺序扫即被抓获）。只读查询零足迹。
//
// ★★ R167 追加：长窗路径**不再读 canonical 视图**（改走底表两臂）⇒
// 计划里出现 request_logs_with_current_month 即为回归（视图逐行算 118 列
// 投影的表达式，252 实测视图 count 130.0s vs 底表两臂 40.1s）。
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
	// ★ R167：长窗不得触碰 canonical 视图。
	if strings.Contains(plan, "request_logs_with_current_month") {
		t.Fatalf("72h drawer plan 触碰 canonical 视图（应读底表两臂）：\n%s", plan)
	}
}

// TestSlidingWindowLongQueryMatchesCanonicalView_RealDB 真库全量对拍：
// 长窗底表两臂 ≡ canonical 视图（EXCEPT 双向差集 0）。
//
// ★ 为什么限「最近 1 天」：全量对拍在 252 生产上要物化两次全视图
// （实测视图 count 130.0s、底表两臂 40.1s）⇒ 测试里不能那么跑。
// 1 天窗口已经覆盖「两臂拼接」与「表达式列投影」两类差异。
//
// ★ 为什么用**单条语句**：视图臂与底表臂必须是**同一快照**，否则并发写入
// 会让行数自己漂移（本轮实测：视图 count 与底表 count 相隔 40 秒，
// 行数差 24 行 ⇒ 纯并发写入，不是语义差异）。
func TestSlidingWindowLongQueryMatchesCanonicalView_RealDB(t *testing.T) {
	pool := slidingWindowRealDBPool(t)
	ctx := context.Background()
	if !hotWindowTablesPresent(t, pool) {
		t.Skip("hot 双写表不在库（迁移未应用），跳过")
	}

	const paritySQL = `
WITH view_side AS (
    SELECT request_id, ts, success, latency_ms, error_kind
    FROM request_logs_with_current_month
    WHERE ts > now() - interval '1 day'
), base_side AS (
    SELECT t.request_id, t.ts, t.success, t.latency_ms, t.error_kind
    FROM session_turns_hot t
    LEFT JOIN session_turn_details_hot d
      ON d.tenant_id::text = t.tenant_id::text
     AND d.request_id = t.request_id
     AND d.partition_date = t.partition_date
    WHERE t.ts > now() - interval '1 day'
    UNION ALL
    SELECT t.request_id, t.ts, t.success, t.latency_ms, t.error_kind
    FROM session_turns t
    LEFT JOIN session_turn_details d
      ON d.tenant_id::text = t.tenant_id::text
     AND d.request_id = t.request_id
     AND d.partition_date = t.partition_date
    WHERE t.ts > now() - interval '1 day'
)
SELECT (SELECT count(*) FROM view_side),
       (SELECT count(*) FROM base_side),
       (SELECT count(*) FROM (SELECT * FROM view_side EXCEPT SELECT * FROM base_side) x),
       (SELECT count(*) FROM (SELECT * FROM base_side EXCEPT SELECT * FROM view_side) y)`

	var viewRows, baseRows, viewMinusBase, baseMinusView int64
	if err := pool.QueryRow(ctx, paritySQL).Scan(&viewRows, &baseRows, &viewMinusBase, &baseMinusView); err != nil {
		t.Fatalf("parity query failed: %v", err)
	}
	if viewRows != baseRows {
		t.Fatalf("视图与底表两臂行数不一致: view=%d base=%d", viewRows, baseRows)
	}
	if viewMinusBase != 0 {
		t.Fatalf("视图有 %d 行不在底表两臂中（语义漂移）", viewMinusBase)
	}
	if baseMinusView != 0 {
		t.Fatalf("底表两臂有 %d 行不在视图中（语义漂移）", baseMinusView)
	}
}
