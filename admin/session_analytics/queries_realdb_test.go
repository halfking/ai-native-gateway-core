package sessionanalytics

// 真库契约测试：**查询层对着真库跑一遍**，断言「能查通 + 不是空壳」。
//
// ★ 为什么必须有这条（2026-11-20 211 实测的真缺陷）：
//
//	overview 端点在生产返回 **500**，根因是**扫描类型不匹配**：
//	  · 视图列 `total_tokens` 是 **numeric**（真库 pg_attribute 实测）
//	    ⇒ `SUM(total_tokens)` 返回 numeric；
//	  · Go 结构体却是 `int64`
//	    ⇒ database/sql 把 numeric 的 []byte 往 int64 上转 ⇒
//	      "converting driver.Value type []uint8 to type int64: invalid syntax"
//	    ⇒ handler 只能写 500 "failed to get overview"（且**不记日志**）。
//
//	★ 为什么单测打不穿：单测用 sqlmock/假库，**假库的列类型是自己定的**
//	  ⇒ 把 numeric 假成 bigint，扫描就"通过"了。
//	★ 为什么"端点返回 200"也不够：这条测试还要断言**数据非空**
//	  （"200 但空壳"不算数）。
//
// ★ 用法（对齐仓内 admin500_column_contract_realdb_test.go 的口径）：
//
//	source /tmp/lgw-env.sh 后
//	  go test ./admin/session_analytics/ -run TestRealDB -count=1 -v
//
//	环境变量优先级：TEST_SESSION_ANALYTICS_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL。
//	**没有 DSN 就跳过**（不连库、不造库、不写任何数据）⇒ CI 不会因缺环境而红。

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// realDBDSN 返回真库 DSN；没有就 skip。
func realDBDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{
		"TEST_SESSION_ANALYTICS_DSN",
		"LLM_GATEWAY_DATABASE_URL",
		"DATABASE_URL",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("没有真库 DSN（TEST_SESSION_ANALYTICS_DSN / LLM_GATEWAY_DATABASE_URL / DATABASE_URL）⇒ 跳过真库契约测试")
	return ""
}

// TestRealDB_QueriesRunAgainstRealSchema 真库上跑端点的**全部**查询。
//
// ★ 断言两件事，缺一不可：
//  1. **不报错**（覆盖扫描类型不匹配 ⇒ 这正是 500 的来源）；
//  2. **数据非空**（"查通了但全是零行"是另一种空壳，200 也不算数）。
func TestRealDB_QueriesRunAgainstRealSchema(t *testing.T) {
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

	t.Run("overview", func(t *testing.T) {
		resp, err := GetOverview(ctx, db, nil, start, end)
		if err != nil {
			t.Fatalf("★ GetOverview 在真库上报错（生产 500 的同一形态）：%v\n"+
				"★ 典型根因：视图列是 numeric 而 Go 目标字段是 int64 ⇒ 扫描失败。", err)
		}
		if resp == nil {
			t.Fatal("overview 返回 nil")
		}
		if resp.Summary.TotalSessions == 0 {
			t.Errorf("overview 总会话数 = 0 ⇒ 「查通了但空壳」也算没通")
		}
		if len(resp.TopUsers) == 0 {
			t.Errorf("overview top_users 为空 ⇒ 数据面没到位（视图空/口径不对）")
		}
		if len(resp.TopModels) == 0 {
			t.Errorf("overview top_models 为空 ⇒ 数据面没到位")
		}
		t.Logf("summary=%+v top_users=%d top_models=%d top_projects=%d",
			resp.Summary, len(resp.TopUsers), len(resp.TopModels), len(resp.TopProjects))
	})

	t.Run("user ranking", func(t *testing.T) {
		resp, err := GetUserRanking(ctx, db, nil, start, end, nil, "sessions", "desc", 10, 0)
		if err != nil {
			t.Fatalf("★ GetUserRanking 在真库上报错：%v", err)
		}
		if resp == nil || len(resp.Users) == 0 {
			t.Fatalf("人员排行为空 ⇒ 数据面没到位（resp=%+v）", resp)
		}
		t.Logf("users=%d top1=%+v", len(resp.Users), resp.Users[0])
	})
}
