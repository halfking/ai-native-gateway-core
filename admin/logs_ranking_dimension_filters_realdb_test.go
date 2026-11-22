package admin

// logs_ranking_dimension_filters_realdb_test.go —— 排行榜六维度过滤的**真库**判据。
//
// # 这道门测什么
//
// 静态门（logs_ranking_dimension_filters_test.go）只能证明「分支还在、谓词长什么样」。
// 本门证明**在真 PostgreSQL 上、面对真数据形态，谓词真的命中**。
//
// 背景（2026-11-23 实测）：`/admin/usage-trends` 的六张排行卡片会带维度参数
// 跳到 `/admin/request-logs`，但后端当时**只实现了 model 一维**，其余五维被
// 原样忽略 ⇒ 跳过去看到的是未筛选的全量请求。
//
// ★ 本门**直接调生产函数** buildRankingDimensionFilterClauses 拿谓词，
//   而不是在本文件里重抄一遍 SQL —— 后者测的是「测试自己写的 SQL」，
//   产品坏了测试照样绿（假绿）。
//
// # 核心不变式（本门的主断言）
//
//	**每一组的卡片行数 == 用该组名过滤得到的行数**
//
// ⇒ 这是「点击进去可以看到详细的请求」这句话的**可执行定义**：
//   排行侧按身份表达式分组、过滤侧按**同一个**表达式等值匹配
//   ⇒ 两边漂移（改了排行没改过滤，或反之）⇒ 这条断言立刻变红。
//
// # 为什么夹具要复刻「非法 JSON + 空值混在里面」这件事
//
// `end_user_id` 列的现网形态是混合的（211 实测）：
//
//	anonymous 379,142 / NULL 38,657 / JSON 对象 14,674 / 纯 id 49
//
// 而 `task_type` 的**最大一组原值就是空串**（211 实测 66,638 行）
// ⇒ 空值必须归 'unknown' 且**可点可筛**（改之前：卡片第一行是空白、
// 点过去过滤值为空被后端忽略 ⇒ 落点是未筛选的全量）。
//
// # 夹具纪律
//
// 夹具必须**同时**含：JSON 对象（两个会话同一设备）/ anonymous / 纯 id /
// NULL / 空串 / 无 device_id 的 JSON；且 client_model / application_code /
// task_type 三列都要有「空串」与「NULL」两种形态 ——
// 少一种，对应的「不该报错 / 该命中 / 该归 unknown」就证明不了。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRankingDimensionFiltersOnRealDB 在真 PG 上验证六维度过滤谓词。
func TestRankingDimensionFiltersOnRealDB(t *testing.T) {
	dsn := g5DSN(t)
	pool, ctx, _ := g5ScratchDB(t, dsn)
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.rl (
		  id               int PRIMARY KEY,
		  ts               timestamptz NOT NULL,
		  end_user_id      text,
		  client_model     text,
		  api_key_prefix   text,
		  application_code text,
		  task_type        text
		);
		-- ① JSON 形态：两个**不同 session**、**同一 device_id**（现网真实形态）
		INSERT INTO public.rl VALUES
		  (1, now(), '{"device_id":"dev-A","account_uuid":"","session_id":"s1"}', 'cli-1', 'sk-aaa****', 'app-1', 'chat'),
		  (2, now(), '{"device_id":"dev-A","account_uuid":"","session_id":"s2"}', 'cli-1', 'sk-aaa****', 'app-1', 'chat'),
		  (3, now(), '{"device_id":"dev-B","account_uuid":"","session_id":"s3"}', 'cli-2', 'sk-bbb****', 'app-2', 'code'),
		  -- ② anonymous 兜底
		  (4, now(), 'anonymous', 'cli-1', 'sk-ccc****', 'app-1', 'chat'),
		  -- ③ 纯 id
		  (5, now(), 'user-admin', 'cli-3', 'sk-ddd****', 'app-3', 'agent'),
		  -- ④ NULL（end_user_id / api_key_prefix）
		  (6, now(), NULL, 'cli-3', NULL, 'app-3', 'agent'),
		  -- ⑤ 空串
		  (7, now(), '', 'cli-3', 'sk-eee****', 'app-3', 'agent'),
		  -- ⑥ JSON 但**没有 device_id** ⇒ 同样归 'unknown'
		  (8, now(), '{"session_id":"s9"}', 'cli-4', 'sk-fff****', 'app-4', 'code'),
		  -- ⑦ 三个普通维度**全空串** ⇒ 归 'unknown'
		  (9, now(), 'plain-user', '', '', '', ''),
		  -- ⑧ 三个普通维度**全 NULL** ⇒ 归 'unknown'
		  (10, now(), 'plain-user2', NULL, NULL, NULL, NULL);
		ANALYZE public.rl;
	`); err != nil {
		t.Fatalf("建夹具失败: %v", err)
	}

	// ── 维度一：end_user_id（设备号语义）──────────────────────────────
	if n := rlFilterCount(t, pool, ctx, "end_user_id="+url.QueryEscape("dev-A"), 5); n != 2 {
		t.Errorf("按 device_id=dev-A 过滤应命中 2 行（两个 session 同一设备），实得 %d", n)
	}
	if n := rlFilterCount(t, pool, ctx, "end_user_id="+url.QueryEscape("dev-B"), 5); n != 1 {
		t.Errorf("按 device_id=dev-B 过滤应命中 1 行，实得 %d", n)
	}
	// 腿①：原值精确命中（修复前界面传的就是整段 JSON ⇒ 旧链接必须还能用）
	rawJSON := `{"device_id":"dev-A","account_uuid":"","session_id":"s1"}`
	if n := rlFilterCount(t, pool, ctx, "end_user_id="+url.QueryEscape(rawJSON), 5); n != 1 {
		t.Errorf("按原始 JSON 串过滤应命中 1 行（腿①原值命中），实得 %d", n)
	}
	// 'unknown' 组：NULL + 空串 + 无 device_id 的 JSON 共 3 行
	if n := rlFilterCount(t, pool, ctx, "end_user_id=unknown", 5); n != 3 {
		t.Errorf("按 unknown 过滤应命中 3 行（NULL + 空串 + 无 device_id 的 JSON），实得 %d", n)
	}
	// ★ 关键断言：谓词遇到 'anonymous'/NULL/纯 id 这些**非法 JSON** 值不得抛错
	//   （::jsonb 会对它们抛错 ⇒ 整条查询 500）。上面几条能跑出结果即证明未抛错。
	if n := rlFilterCount(t, pool, ctx, "end_user_id=anonymous", 5); n != 1 {
		t.Errorf("按 anonymous 过滤应命中 1 行，实得 %d", n)
	}
	if n := rlFilterCount(t, pool, ctx, "end_user_id="+url.QueryEscape("user-admin"), 5); n != 1 {
		t.Errorf("按纯 id=user-admin 过滤应命中 1 行，实得 %d", n)
	}
	// ★ 过滤真在筛，不是恒真
	if n := rlFilterCount(t, pool, ctx, "end_user_id=ZZZ_NOT_A_REAL_USER_12345", 5); n != 0 {
		t.Errorf("不存在的用户应命中 0 行（过滤真在筛，不是恒真），实得 %d", n)
	}

	// ── 维度三：api_key_prefix（掩码值 vs 明文前缀 ⇒ 必须前缀匹配）─────
	// ★ 排行侧 name 取自 ak.key_prefix（现网该列本身可能是明文
	//   'sk-pi-autollm-21'），而请求行里存的是**掩码**值
	//   'sk-pi-autollm-21***'（掩码长度随前缀长度变）⇒ 等值匹配命中 0 行。
	if n := rlFilterCount(t, pool, ctx, "api_key_prefix=sk-aaa", 5); n != 2 {
		t.Errorf("按明文前缀 sk-aaa 前缀匹配应命中 2 行（掩码值 sk-aaa****），实得 %d", n)
	}
	// 传**带星号的完整掩码值**也要能命中（现网 ak.key_prefix 常常本身就是掩码值）
	if n := rlFilterCount(t, pool, ctx, "api_key_prefix="+url.QueryEscape("sk-aaa****"), 5); n != 2 {
		t.Errorf("按完整掩码值 sk-aaa**** 前缀匹配应命中 2 行，实得 %d", n)
	}
	if n := rlFilterCount(t, pool, ctx, "api_key_prefix=sk-zzz", 5); n != 0 {
		t.Errorf("不存在的 api key 前缀应命中 0 行，实得 %d", n)
	}
	// ★ 前提断言：等值匹配确实匹配不上（否则这道门证明不了「必须前缀匹配」）
	if n := rlCount(t, pool, ctx, "SELECT count(*) FROM public.rl WHERE api_key_prefix = $1", "sk-aaa"); n != 0 {
		t.Fatalf("夹具前提不成立：等值匹配 'sk-aaa' 命中了 %d 行（应为 0）—— "+
			"掩码值必须与明文前缀**不相等**，否则「必须前缀匹配」证明不了", n)
	}
	// ★ starts_with 的语义是**字面前缀**，不是 LIKE 通配符
	if n := rlFilterCount(t, pool, ctx, "api_key_prefix="+url.QueryEscape("sk-%"), 5); n != 0 {
		t.Errorf("前缀 'sk-%%' 必须按字面匹配（LIKE 会把它当通配符 ⇒ 匹配所有 key），实得 %d", n)
	}

	// ── 核心不变式：卡片上的 request_count == 点进去看到的条数 ──────────
	//
	// ★ 对**每一个**维度、**每一组**都要相等。
	//   排行侧的分组键用 production 的身份表达式，过滤侧用 production 的谓词
	//   ⇒ 这条断言真的在验「两端一致」，不是验「测试自己写的 SQL」。
	for _, dim := range []struct {
		name  string
		param string
		expr  string
	}{
		{"end_user_id", "end_user_id", userIdentityExpr},
		{"client_model", "client_model", dimensionIdentityExpr("client_model")},
		{"application_code", "application_code", dimensionIdentityExpr("application_code")},
		{"task_type", "task_type", dimensionIdentityExpr("task_type")},
	} {
		groups := rlIdentityGroups(t, pool, ctx, dim.expr)
		if len(groups) == 0 {
			t.Errorf("%s：夹具没有产生任何身份分组（夹具坏了）", dim.name)
			continue
		}
		for name, want := range groups {
			got := rlFilterCount(t, pool, ctx, dim.param+"="+url.QueryEscape(name), 5)
			if got != want {
				t.Errorf("%s：分组 %q 的卡片行数是 %d，但按该名过滤得到 %d 行 —— 卡片与列表对不上",
					dim.name, name, want, got)
			}
		}
		// 「不存在」的值必须命中 0 行（过滤真在筛，不是恒真）
		if n := rlFilterCount(t, pool, ctx, dim.param+"=ZZZ_NOT_A_REAL_VALUE_12345", 5); n != 0 {
			t.Errorf("%s：传不存在的值应命中 0 行，实得 %d", dim.name, n)
		}
	}

	// ── 空值 ⇒ 'unknown' 的具体形状（防「归 unknown」被悄悄改掉）────────
	if got := rlIdentityGroups(t, pool, ctx, dimensionIdentityExpr("task_type"))["unknown"]; got != 2 {
		t.Errorf("task_type 的空串行与 NULL 行应合成一个 'unknown' 组（2 行），实得 %d", got)
	}
	if got := rlIdentityGroups(t, pool, ctx, dimensionIdentityExpr("client_model"))["unknown"]; got != 2 {
		t.Errorf("client_model 的空串行与 NULL 行应合成一个 'unknown' 组（2 行），实得 %d", got)
	}
	if got := rlIdentityGroups(t, pool, ctx, userIdentityExpr)["unknown"]; got != 3 {
		t.Errorf("end_user_id 的 NULL/空串/无 device_id 的 JSON 应合成一个 'unknown' 组（3 行），实得 %d", got)
	}
	if got := rlIdentityGroups(t, pool, ctx, userIdentityExpr)["dev-A"]; got != 2 {
		t.Errorf("排行侧按 device_id 分组后 dev-A 应有 2 行（两个 session 归一个设备），实得 %d —— "+
			"若这是 1，说明还在按 session 分组", got)
	}
}

// ── 本门的小工具 ──────────────────────────────────────────────────────

// rlFilterCount 用**生产代码** buildRankingDimensionFilterClauses 构造谓词，
// 然后在夹具上数命中行数。
//
// ★ 参数占位符从 5 起（与 listLogs 的实际起始位一致）⇒ 占位符推进的 bug
//
//	（比如某条分支忘了 argIdx++）在这里会表现为「参数个数对不上」而当场报出来。
//
// ★★ 前缀里补 $1..$4 四个**已占用**的占位符：★ 生产里 listLogs 的
//
//	$1/$2 = 分区键下界、$3/$4 = ts 窗口 ⇒ ⇒ 排行过滤从 $5 起。
//	⇒ 这里必须复刻这个上下文，否则测的是「$1 起编号」的另一个形态，
//	证明不了生产里 $5 的形态可用（PG 的 $n 是**绝对位置**，
//	少绑 4 个参数 ⇒ 直接 42P18 类型推断失败）。
//	★ 这条是本门**第一次跑就抓到的真问题**（测试自己写错了编号，
//	恰好证明了「生产编号形态必须被测」）。
func rlFilterCount(t *testing.T, pool *pgxpool.Pool, ctx context.Context, query string, argIdx int) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?"+query, nil)
	clauses, args, next := buildRankingDimensionFilterClauses(req, argIdx)
	if len(clauses) == 0 {
		t.Fatalf("参数 %q 没有生成任何过滤子句（过滤分支被删了？）", query)
	}
	if next != argIdx+len(args) {
		t.Errorf("占位符推进不一致：argIdx=%d args=%d next=%d（某条分支可能漏了 argIdx++）", argIdx, len(args), next)
	}
	const occupied = `($1::timestamptz IS NOT NULL) AND ($2::timestamptz IS NOT NULL)
		AND ($3::timestamptz IS NOT NULL) AND ($4::timestamptz IS NOT NULL) AND `
	bound := make([]any, 0, 4+len(args))
	for i := 0; i < 4; i++ {
		bound = append(bound, time.Now().UTC())
	}
	bound = append(bound, args...)
	sql := "SELECT count(*) FROM public.rl WHERE " + occupied + strings.Join(clauses, " AND ")
	return rlCount(t, pool, ctx, sql, bound...)
}

// rlIdentityGroups 复刻排行侧的分组口径（admin/usage.go 的 queryTopN）：
// 按给定的**生产身份表达式**分组，返回 {分组名: 行数}。
func rlIdentityGroups(t *testing.T, pool *pgxpool.Pool, ctx context.Context, expr string) map[string]int {
	t.Helper()
	rows, err := pool.Query(ctx,
		"SELECT COALESCE("+expr+", 'unknown') AS name, count(*) FROM public.rl GROUP BY 1")
	if err != nil {
		t.Fatalf("排行侧分组查询失败（expr=%s）: %v", expr, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[name] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// rlCount 是带参数的计数辅助（本文件自用，避免动既有 g5Count 的签名）。
func rlCount(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("查询失败（sql=%s args=%v）: %v", sql, args, err)
	}
	return n
}
