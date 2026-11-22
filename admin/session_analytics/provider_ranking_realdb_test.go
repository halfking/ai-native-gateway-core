package sessionanalytics

// provider_ranking_realdb_test.go —— 供应商排行的**真库**契约（2026-11-23 复核轮）
//
// ★ 为什么这条测试必须存在（owner 报障的形态）：
//
//	owner 报「/admin/session-analytics/users 的『Top 5 供应商』显示的是模型数据」。
//	上游 `614b72e9c` 已把供应商排行改成**按 provider 维度聚合**
//	（providerRankingSQL：GROUP BY provider + COUNT(DISTINCT model)），
//	仓内也已有**源码级**契约钉测（provider_task_ranking_contract_test.go：
//	路由已注册 / GROUP BY provider / 权限门+陈旧度门）。
//	★ 但**源码级钉测钉不住"真库上的数据形状"**：SQL 写对了不代表扫出来的行
//	  长得对（列错位/类型不匹配/口径取错列都可能让"供应商列"退化成模型名）。
//	  这正是本仓自己的教训（queries_realdb_test.go 头注释：overview 端点在生产
//	  返回 500，根因是 numeric 扫进 int64 ⇒ 单测全绿、假库把列类型定成自己
//	  想要的样子所以扫得过去）。
//
// ★ 本条断言里的**反面断言**才是这次报障的核心：
//	「provider 列的值不得是同窗口里任何一个模型名」。
//	原缺陷形态正是 provider 列 = 模型名（前端 split('/')[0] 的结果）。
//	⇒ 只要 provider 列里混进模型名，这条测试立刻红。
//
// ★ 用法（对齐 queries_realdb_test.go）：
//
//	source /tmp/lgw-env.sh 后
//	  go test ./admin/session_analytics/ -run TestRealDBProviderRanking -count=1 -v
//
//	环境变量优先级：TEST_SESSION_ANALYTICS_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL。
//	**没有 DSN 就跳过**（不连库、不造库、不写任何数据）⇒ CI 不会因缺环境而红。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// providerRankingUnknownSentinel 是 provider 维度与 model 维度**共用**的占位值。
//
// ★ 真库实测（211）：provider_id 为空 ⇒ providers.code 为 NULL ⇒ COALESCE 成
//
//	'unknown'；client_model 与 outbound_model 都为空 ⇒ 模型名同样 COALESCE 成
//	'unknown'。⇒ 「provider ≠ model」的反面断言必须把它排除，否则真库上假红。
const providerRankingUnknownSentinel = "unknown"

// TestRealDBProviderRanking 供应商排行在真库上的形状与口径断言。
func TestRealDBProviderRanking(t *testing.T) {
	dsn := realDBDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("打开真库：%v", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("连不上真库：%v", err)
	}

	// 窗口取足够宽的一段，保证有数据（真库会话量随时间增长）。
	const start, end = "2026-01-01", "2026-12-31"

	resp, err := GetProviderRanking(ctx, db, nil, start, end, "requests", "desc")
	if err != nil {
		t.Fatalf("★ GetProviderRanking 在真库上报错（扫描类型不匹配的同一形态）：%v", err)
	}
	if resp == nil || len(resp.Providers) == 0 {
		t.Fatalf("供应商排行为空 ⇒ 数据面没到位（resp=%+v）", resp)
	}

	// 同窗口的模型名集合：供应商列若出现模型名 ⇒ 口径退回了「拿模型名当供应商」。
	//
	// ★ 'unknown' 是**两个维度共用的占位值**（真库实测才发现的坑）：
	//   · provider 侧：provider_id 为空 ⇒ providers.code 为 NULL ⇒ COALESCE 成 'unknown'；
	//   · model 侧：client_model 与 outbound_model 都为空 ⇒ 模型名也 COALESCE 成 'unknown'。
	//   ⇒ 若把 'unknown' 留在模型名集合里，反面断言会在**真库上假红**
	//     （'unknown' 供应商行会被误判成"供应商列出现了模型名"）。
	//   ⇒ 这里把占位值排除出模型名集合：断言要抓的是**真实模型名**漏进供应商列。
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT model FROM model_usage_aggregates WHERE date >= $1 AND date <= $2`,
		start, end)
	if err != nil {
		t.Fatalf("查模型名集合：%v", err)
	}
	modelNames := make(map[string]bool)
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatalf("扫描模型名：%v", err)
		}
		if m == providerRankingUnknownSentinel {
			continue
		}
		modelNames[m] = true
	}
	_ = rows.Close()

	for _, p := range resp.Providers {
		if p.Provider == "" {
			t.Errorf("供应商列为空字符串")
		}
		if modelNames[p.Provider] {
			t.Errorf("★ 供应商列出现模型名 %q ⇒ 又退回「拿模型名当供应商」了"+
				"（模型名是裸名、无 provider/ 前缀，且同名模型会跨供应商，无法反推）", p.Provider)
		}
		if p.ModelCount <= 0 {
			t.Errorf("供应商 %q 的模型数 = %d ⇒ 口径没取到 COUNT(DISTINCT model)", p.Provider, p.ModelCount)
		}
		if p.RequestCount <= 0 {
			t.Errorf("供应商 %q 的请求数 = %d ⇒ 空壳行", p.Provider, p.RequestCount)
		}
	}

	// 排序断言：默认按请求数降序 ⇒ 第 1 行的请求数不得小于第 2 行。
	if len(resp.Providers) >= 2 && resp.Providers[0].RequestCount < resp.Providers[1].RequestCount {
		t.Errorf("供应商排行未按请求数降序：top1=%d < top2=%d",
			resp.Providers[0].RequestCount, resp.Providers[1].RequestCount)
	}

	top := resp.Providers
	if len(top) > 5 {
		top = top[:5]
	}
	t.Logf("providers=%d top5=%+v", len(resp.Providers), top)
}
