package admin

// usage_trend_deep_link_filters_realdb_test.go —— 用量趋势页「深链条件真的过滤数据」
// 的**真库**判据。
//
// # 这道门测什么
//
// 静态门（usage_trend_deep_link_filters_test.go）只能证明「分支还在、谓词长什么样」。
// 本门证明**在真 PostgreSQL 上、面对真数据形态，别名 r 的谓词真的能跑、真的命中**。
//
// ★ 为什么要专门验「别名 r」：同一套身份口径要在两个 FROM 源上复用
//   · 排行/日志侧：canonical 视图 request_logs_with_current_month（别名 rl）
//   · 用量趋势明细档：会话族原生源 db.SessionFamilyTurnsSourceSQL（别名 r）
//   ⇒ 别名渲染错了 ⇒ SQL 引用不存在的列 ⇒ 端点 500 ⇒ 页面整块降级。
//   ★ 静态门只能看见字符串；**真库**才能证明它真的能执行。
//
// # 核心不变式（本门的主断言）
//
//	**每一组的排行行数 == 用该组名过滤得到的行数**
//
// ⇒ 这是「卡片上的数字 == 点进去看到的条数」在**用量趋势侧**的可执行定义：
//   排行侧按身份表达式分组、过滤侧按**同一个**表达式等值匹配 ⇒ 两边漂移即红。
//
// # 夹具纪律
//
// 必须**同时**含：JSON 对象（两个会话同一设备）/ anonymous / 纯 id / NULL /
// 空串 / 无 device_id 的 JSON；client_model / application_code / task_type
// 三列都要有「空串」与「NULL」两种形态 ⇒ 少一种，对应的「不该报错 / 该命中 /
// 该归 unknown」就证明不了。
import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUsageTrendDeepLinkFiltersOnRealDB 在真 PG 上验证用量趋势侧的五维过滤谓词。
func TestUsageTrendDeepLinkFiltersOnRealDB(t *testing.T) {
	dsn := g5DSN(t)
	pool, ctx, _ := g5ScratchDB(t, dsn)
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.ut_rl (
		  id               int PRIMARY KEY,
		  ts               timestamptz NOT NULL,
		  end_user_id      text,
		  client_model     text,
		  api_key_prefix   text,
		  application_code text,
		  task_type        text
		);
		-- ① JSON 形态：两个不同 session、同一 device_id（现网真实形态）
		INSERT INTO public.ut_rl VALUES
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
		  -- ⑥ JSON 但没有 device_id ⇒ 同样归 'unknown'
		  (8, now(), '{"session_id":"s9"}', 'cli-4', 'sk-fff****', 'app-4', 'code'),
		  -- ⑦ 三个普通维度全空串 ⇒ 归 'unknown'
		  (9, now(), 'plain-user', '', '', '', ''),
		  -- ⑧ 三个普通维度全 NULL ⇒ 归 'unknown'
		  (10, now(), 'plain-user2', NULL, NULL, NULL, NULL);
		ANALYZE public.ut_rl;
	`); err != nil {
		t.Fatalf("建夹具失败: %v", err)
	}

	// ── ① 别名 r 的谓词必须在真库上**可执行**（这是本门存在的头号理由）──
	// ★ 用「五维全设」的最复杂形态跑一次：任何一个维度渲染错（列名/占位符/
	//   参数个数）都会在这里炸出来（42P01 / 42703 / 42P18）。
	all := rankingDimensionFilters{
		clientModel:     "cli-1",
		endUserID:       "dev-A",
		apiKeyPrefix:    "sk-aaa",
		applicationCode: "app-1",
		taskType:        "chat",
	}
	clauses, args, _ := buildRankingDimensionClausesFor(all, "r", 1)
	where := strings.Join(clauses, " AND ")
	if n := utCount(t, pool, ctx, "SELECT count(*) FROM public.ut_rl r WHERE "+where, args...); n != 2 {
		t.Errorf("五维同时设时应命中 2 行（dev-A 的两个 session，且都在 cli-1/sk-aaa/app-1/chat 上），实得 %d", n)
	}

	// ★★ **变异自证**：同一份谓词用 **rl** 别名渲染后，必须在真库上**报错**
	//   （列 rl.client_model 不存在）⇒ 证明别名真的参与了渲染、
	//   且「r 能跑」不是因为谓词里压根没引用列。
	rlClauses, _, _ := buildRankingDimensionClausesFor(all, "rl", 1)
	if _, err := pool.Query(ctx,
		"SELECT count(*) FROM public.ut_rl r WHERE "+strings.Join(rlClauses, " AND "),
		args...); err == nil {
		t.Error("用 rl 别名渲染的谓词在 r 别名的表上必须报错（列 rl.xxx 不存在）—— " +
			"若这里不报错，说明别名没参与渲染，判据是恒真的")
	}

	// ── ② 逐维：命中数必须与预期一致 ──────────────────────────────────
	for _, tc := range []struct {
		name string
		dims rankingDimensionFilters
		want int
	}{
		{"client_model=cli-1", rankingDimensionFilters{clientModel: "cli-1"}, 3},
		{"client_model=cli-3", rankingDimensionFilters{clientModel: "cli-3"}, 3},
		{"client_model=unknown", rankingDimensionFilters{clientModel: "unknown"}, 2},
		{"end_user_id=dev-A", rankingDimensionFilters{endUserID: "dev-A"}, 2},
		{"end_user_id=unknown", rankingDimensionFilters{endUserID: "unknown"}, 3},
		{"end_user_id=anonymous", rankingDimensionFilters{endUserID: "anonymous"}, 1},
		{"api_key_prefix=sk-aaa", rankingDimensionFilters{apiKeyPrefix: "sk-aaa"}, 2},
		{"api_key_prefix=sk-zzz", rankingDimensionFilters{apiKeyPrefix: "sk-zzz"}, 0},
		{"application_code=app-1", rankingDimensionFilters{applicationCode: "app-1"}, 3},
		{"application_code=unknown", rankingDimensionFilters{applicationCode: "unknown"}, 2},
		{"task_type=chat", rankingDimensionFilters{taskType: "chat"}, 3},
		{"task_type=unknown", rankingDimensionFilters{taskType: "unknown"}, 2},
	} {
		cl, ar, _ := buildRankingDimensionClausesFor(tc.dims, "r", 1)
		got := utCount(t, pool, ctx,
			"SELECT count(*) FROM public.ut_rl r WHERE "+strings.Join(cl, " AND "), ar...)
		if got != tc.want {
			t.Errorf("%s：应命中 %d 行，实得 %d", tc.name, tc.want, got)
		}
	}

	// ── ③ 核心不变式：排行分组行数 == 按该名过滤的行数 ────────────────
	for _, dim := range []struct {
		name  string
		param string
		expr  string
	}{
		{"end_user_id", "end_user_id", userIdentityExprAs("r")},
		{"client_model", "client_model", dimensionIdentityExprAs("r", "client_model")},
		{"application_code", "application_code", dimensionIdentityExprAs("r", "application_code")},
		{"task_type", "task_type", dimensionIdentityExprAs("r", "task_type")},
	} {
		groups := utIdentityGroups(t, pool, ctx, dim.expr)
		if len(groups) == 0 {
			t.Errorf("%s：夹具没有产生任何身份分组（夹具坏了）", dim.name)
			continue
		}
		for name, want := range groups {
			dims := rankingDimensionFilters{}
			switch dim.name {
			case "end_user_id":
				dims.endUserID = name
			case "client_model":
				dims.clientModel = name
			case "application_code":
				dims.applicationCode = name
			case "task_type":
				dims.taskType = name
			}
			cl, ar, _ := buildRankingDimensionClausesFor(dims, "r", 1)
			got := utCount(t, pool, ctx,
				"SELECT count(*) FROM public.ut_rl r WHERE "+strings.Join(cl, " AND "), ar...)
			if got != want {
				t.Errorf("%s：分组 %q 的排行行数是 %d，但按该名过滤得到 %d 行 —— 卡片与列表对不上",
					dim.name, name, want, got)
			}
		}
	}
}

// utCount 是带参数的计数辅助（本文件自用）。
func utCount(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count query failed (%s): %v", sql, err)
	}
	return n
}

// utIdentityGroups 复刻排行侧的分组口径（别名 r）：按给定的**生产身份表达式**
// 分组，返回 {分组名: 行数}。
func utIdentityGroups(t *testing.T, pool *pgxpool.Pool, ctx context.Context, expr string) map[string]int {
	t.Helper()
	rows, err := pool.Query(ctx,
		"SELECT COALESCE("+expr+", 'unknown') AS name, count(*) FROM public.ut_rl r GROUP BY 1")
	if err != nil {
		t.Fatalf("分组查询失败: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("扫描分组失败: %v", err)
		}
		out[name] = n
	}
	return out
}
