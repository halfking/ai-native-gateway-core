// session_family_runtime_sql_test.go — 2026-10-20 退役轮 R70：
// 会话族相关 admin SQL 的**真库可执行性**门 + 静态守卫。
//
// ★ 这道门存在的理由（三处真缺陷，全部由「打开 2s 慢查询日志 + 读 PG
// 错误日志」这条新量具发现，不是靠读代码猜出来的）：
//
//  1. admin/session_analytics_tasks.go 的两个面板 SQL：
//     `INNER JOIN session_dim sd ON ss.session_key = sd.session_id`
//     —— session_dim 里**没有** session_id 列（是 gw_session_id）⇒
//     ERROR: column sd.session_id does not exist (42703)
//     ⇒ 这两个面板**每次都失败**，而代码 `if err == nil { ... }` 把错误
//     吞掉 ⇒ 面板静默变空 ⇒ 页面上分不清「没数据」和「SQL 坏了」。
//
//  2. admin/misc.go 的 listTags SQL：
//     `ARRAY_AGG(canonical_name ORDER BY canonical_name)[1:5]`
//     —— 给聚合表达式直接下标，PG 不允许 ⇒ ERROR: syntax error at or
//     near "[" (42601) ⇒ 同上，被 `writeJSON(200, 空列表)` 吞掉。
//     正确写法是 `(ARRAY_AGG(...))[1:5]`。
//
//  3. admin/data_lifecycle_partition.go 的分区状态查询：
//     清单里仍有 `_request_logs`（迁移 890 已把 v1 族物理删除）⇒
//     每次查状态都 42P01 ⇒ /api/admin/data-lifecycle/partitions 每次
//     刷一条 WARN 噪声，且该表状态永远拿不到。
//
// ★ 为什么门要「真库执行」而不是纯文本：
//
//	这三处都不是「形状」问题，而是「列/语法在真库里成不成立」的问题。
//	纯文本门只能钉住已知写法（例如「必须出现 gw_session_id」），
//	钉不住「还有没有第 4 处同样的错」⇒ 必须有一条能把 SQL 丢给真库
//	的腿。这条腿只跑 SELECT ⇒ 零足迹。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时真库腿跳过（静态腿照跑）。
package admin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ⚠ 本文件复用仓内既有的 stripGoComments（admin/session_view_dependency_risk_test.go
// 里的那个正则版）—— **不要**再定义一份重复实现。
//
// 为什么必须剥注释：门断言的是 SQL 文本，而 SQL 文本**在字符串字面量里**；
// 但注释里也会出现「session_dim」「ARRAY_AGG」这些词 ⇒ 不剥就会把注释里的
// 说明文字当成代码判红（本门第一版就假红了一处自己的注释）。
//
// 已知边界（既有 helper 的性质）：正则版会把字符串字面量里的 "//" 也当注释
// 起点 ⇒ 对本门的两条判据（session_dim 别名 / ARRAY_AGG 下标）最多造成
// 假阴性（漏判），不会造成假阳性。

// runtimeSQLPool 返回一个连到真库的池（无 DSN 时跳过该腿）。
func runtimeSQLPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSessionFamilyRuntimeSQL_ExecutesOnRealDB 把三处修好的 SQL 直接丢给
// 真库执行：任何列名错（42703）、语法错（42601）、关系不存在（42P01）
// 都会直接把这道门打红。
//
// 只跑 SELECT ⇒ 零足迹。
func TestSessionFamilyRuntimeSQL_ExecutesOnRealDB(t *testing.T) {
	pool := runtimeSQLPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 会话族在位？（不在 ⇒ 跳过而不是误报红）
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.session_summaries') IS NOT NULL
		    AND to_regclass('public.session_dim') IS NOT NULL
		    AND to_regclass('public.models_canonical') IS NOT NULL`,
	).Scan(&ok); err != nil {
		t.Skipf("probe session family: %v", err)
	}
	if !ok {
		t.Skip("会话族不在位，跳过（需要装过会话族迁移的库）")
	}

	cases := []struct {
		name string
		sql  string
		args []interface{}
	}{
		{
			name: "daily_cost_trend",
			sql: fmt.Sprintf(sessionAnalyticsTaskDailyCostTrendSQL,
				"WHERE sd.task_id = $1 AND DATE(ss.first_request_at) >= CURRENT_DATE - $2::INT"),
			args: []interface{}{"unknown", 30},
		},
		{
			name: "recent_sessions",
			sql:  fmt.Sprintf(sessionAnalyticsTaskRecentSessionsSQL, "WHERE sd.task_id = $1"),
			args: []interface{}{"unknown"},
		},
		{
			name: "list_tags",
			sql:  listTagsSQL,
		},
		{
			name: "system_monitor_probe_run_counters",
			sql:  systemMonitorProbeRunCountersSQL,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := pool.Query(ctx, tc.sql, tc.args...)
			if err != nil {
				t.Fatalf("SQL 在真库执行失败：%v\n语句：\n%s", err, tc.sql)
			}
			defer rows.Close()
			// 必须能真正取到行（列解析错会在 Scan 时才暴露）
			for rows.Next() {
				cols := rows.FieldDescriptions()
				dests := make([]interface{}, len(cols))
				for i := range dests {
					dests[i] = new(interface{})
				}
				if err := rows.Scan(dests...); err != nil {
					t.Fatalf("扫描行失败（列解析错）：%v\n语句：\n%s", err, tc.sql)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows.Err: %v", err)
			}
		})
	}
}

// TestRetiredPartitionTableStatusIsNotAnError 是缺陷 3 的端到端门：
// 直接调用产品代码的 getPartitionTableStatus，对一张**已退役**的表
// （_request_logs，迁移 890 已 DROP）必须：
//   - 不返回错误（修复前是 42P01）；
//   - Retired=true。
//
// 对一张**在位**的表必须 Retired=false（防止「一律标退役」这种偷懒修法）。
func TestRetiredPartitionTableStatusIsNotAnError(t *testing.T) {
	pool := runtimeSQLPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	h := &Handler{db: pool}

	retiredCfg := partitionedTableConfig{
		TableName:       "_request_logs",
		PartitionColumn: "ts",
		Description:     "请求日志表",
	}
	var retiredExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public._request_logs') IS NOT NULL`).Scan(&retiredExists); err != nil {
		t.Skipf("probe v1 family: %v", err)
	}
	if retiredExists {
		t.Skip("本库的 _request_logs 仍在位（未装 890），跳过退役判定")
	}

	status, err := h.getPartitionTableStatus(ctx, retiredCfg)
	if err != nil {
		t.Fatalf("已退役的表不应返回错误（修复前是 42P01）：%v", err)
	}
	if !status.Retired {
		t.Fatalf("_request_logs 已不存在，status.Retired 必须是 true")
	}
	if status.TableName != "_request_logs" {
		t.Fatalf("status.TableName = %q，want _request_logs（清单条目必须保留：既有端点契约）", status.TableName)
	}

	// 在位的表不得被标成退役。
	liveCfg := partitionedTableConfig{
		TableName:       "usage_ledger",
		PartitionColumn: "ts",
		Description:     "用量账本",
	}
	var liveExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.usage_ledger') IS NOT NULL`).Scan(&liveExists); err != nil {
		t.Skipf("probe usage_ledger: %v", err)
	}
	if !liveExists {
		t.Skip("本库没有 usage_ledger，跳过在位表判定")
	}
	liveStatus, err := h.getPartitionTableStatus(ctx, liveCfg)
	if err != nil {
		t.Fatalf("在位表的状态查询不应失败：%v", err)
	}
	if liveStatus.Retired {
		t.Fatalf("usage_ledger 在位，Retired 不得为 true")
	}
}

// adminProductionFiles 列出 admin 包下的**生产** .go 文件（不含 _test.go）。
func adminProductionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		files = append(files, e.Name())
	}
	if len(files) == 0 {
		t.Fatal("没找到 admin 包的生产源文件")
	}
	return files
}

// TestSessionDimJoinNeverUsesSessionId 静态守卫（缺陷 1 的家族）：
// admin 包里任何把 **session_dim 的别名** 与 `.session_id` 关联的写法都要红 ——
// session_dim 没有 session_id 列（真库实测：gw_session_id / session_key /
// tenant_id / task_id / ...），一写错就是 42703，而且会被 `if err == nil`
// 吞掉变成「面板静默变空」。
//
// ★ 别名感知（第一版踩过的坑）：第一版用「session_dim 之后 400 字符内出现
// .session_id」来判 ⇒ **假阳性**了 admin/session_turns_v2.go:766 与
// admin/turns_sessions.go:167 —— 那两处的正确写法是
// `LEFT JOIN session_dim sd ON sd.gw_session_id = s.session_id`，
// 窗口跨到了下一张表（sessions）的合法列上。
// ⇒ 现在先从 `session_dim <别名>` 里取出别名，再只找 `<别名>.session_id`。
func TestSessionDimJoinNeverUsesSessionId(t *testing.T) {
	aliasRe := regexp.MustCompile(`session_dim\s+([a-z_][a-z0-9_]*)`)
	for _, name := range adminProductionFiles(t) {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := stripGoComments(string(b))
		for _, m := range aliasRe.FindAllStringSubmatchIndex(body, -1) {
			alias := body[m[2]:m[3]]
			// 该别名直接带 .session_id ⇒ 错。
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.session_id\b`)
			if loc := re.FindStringIndex(body); loc != nil {
				line := 1 + strings.Count(body[:loc[0]], "\n")
				t.Errorf("%s:%d session_dim 的别名 %q 被关联到 .session_id（该列不存在 ⇒ 42703）：%s",
					name, line, alias, strings.TrimSpace(body[loc[0]:loc[0]+80]))
			}
		}
	}

	// 正向断言：两处面板 SQL 必须用 gw_session_id。
	for _, sql := range []string{sessionAnalyticsTaskDailyCostTrendSQL, sessionAnalyticsTaskRecentSessionsSQL} {
		if !strings.Contains(sql, "sd.gw_session_id") {
			t.Errorf("面板 SQL 必须 JOIN sd.gw_session_id（session_dim 的真实列）")
		}
	}
}

// TestArrayAggSubscriptIsParenthesized 静态守卫（缺陷 2）：
// 给聚合表达式下标必须写成 `(ARRAY_AGG(...))[1:5]`；
// 写成 `ARRAY_AGG(...)[1:5]` 是 42601。
//
// ★ 必须先确认「真的被下标了」（第一版踩过的坑）：第一版只看「ARRAY_AGG
// 前面有没有左括号」⇒ 把 `ARRAY_AGG(ma.raw_name ORDER BY ma.raw_name) AS aliases`
// 这种**根本没有下标**的写法也判成红（admin/routing.go 两处假阳性）。
// ⇒ 现在先配平括号找到聚合表达式的收尾，再看它后面是不是跟着 "["。
func TestArrayAggSubscriptIsParenthesized(t *testing.T) {
	for _, name := range adminProductionFiles(t) {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := stripGoComments(string(b))
		idx := 0
		for {
			i := strings.Index(body[idx:], "ARRAY_AGG")
			if i < 0 {
				break
			}
			abs := idx + i
			idx = abs + len("ARRAY_AGG")
			// 配平括号，找到聚合表达式的收尾 ')'。
			depth := 0
			end := -1
			for k := abs; k < len(body); k++ {
				switch body[k] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						end = k
					}
				}
				if end >= 0 {
					break
				}
			}
			if end < 0 {
				continue
			}
			// 收尾后面（非空白）是不是 "["？不是 ⇒ 没下标，不管。
			j := end + 1
			for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
				j++
			}
			if j >= len(body) || body[j] != '[' {
				continue
			}
			// 有下标：那就必须整体被括号包住（"(" 紧贴在 ARRAY_AGG 前）。
			k := abs - 1
			for k >= 0 && (body[k] == ' ' || body[k] == '\t' || body[k] == '\n' || body[k] == '\r') {
				k--
			}
			if k < 0 || body[k] != '(' {
				line := 1 + strings.Count(body[:abs], "\n")
				t.Errorf("%s:%d ARRAY_AGG 表达式直接被下标 ⇒ PG 语法错 42601；"+
					"正确写法是 (ARRAY_AGG(...))[1:5]", name, line)
			}
		}
	}
}

// TestPartitionStatusProbesTableExistence 静态守卫（缺陷 3）：
// getPartitionTableStatus 必须在查表**之前**先探表是否存在
// （to_regclass），否则已退役的表每次都 42P01。
func TestPartitionStatusProbesTableExistence(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(".", "data_lifecycle_partition.go"))
	if err != nil {
		t.Fatalf("read data_lifecycle_partition.go: %v", err)
	}
	body := string(b)
	probe := strings.Index(body, "to_regclass")
	if probe < 0 {
		t.Fatal("getPartitionTableStatus 里没有 to_regclass 探针 ⇒ 已退役的表每次都 42P01")
	}
	sizeQuery := strings.Index(body, "pg_total_relation_size('%s')")
	if sizeQuery < 0 {
		t.Fatal("找不到分区大小查询（形状变了？）")
	}
	if probe > sizeQuery {
		t.Fatal("探针必须排在查表之前")
	}
}
