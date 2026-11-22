// provider_task_ranking_contract_test.go — 契约钉测（2026-11-23 审计轮）
//
// ★ 为什么要有这道门：上一轮「供应商 Top5」的实现踩了两个坑，
//   ① 前端从 overview 的 top_models 聚合供应商 —— 那份数据只有 **3 条**
//      （GetOverview 里 getTopModels(…, 3)），且 model 列是**纯模型名**
//      （不含 provider/ 前缀）⇒ split('/')[0] 会把模型名当 provider；
//   ② 「全局任务排行」在本包根本没有端点（只有某 owner 的某 project 下的
//      任务列表）⇒ 任务维度无数据源。
//   ⇒ provider / task 两个维度必须由 SQL 直接按对应列聚合。
//
// 这道门钉住四件事：
//   ① 两个端点已注册（且进了 RoutePaths 的路由冲突守卫清单）；
//   ② provider 榜按 provider 列聚合（不是按 model 前缀 split）；
//   ③ task 榜过滤空串 task（「无具体任务」不算一个任务）；
//   ④ 两个端点都带陈旧度门 + admin 权限门（不得裸奔）。
package sessionanalytics

import (
	"os"
	"strings"
	"testing"
)

func TestProviderRankingRouteRegistered(t *testing.T) {
	if RouteProviderRanking != "/api/admin/session-analytics/provider-ranking" {
		t.Fatalf("provider 端点路径漂移：%s", RouteProviderRanking)
	}
	if !containsString(RoutePaths, RouteProviderRanking) {
		t.Fatalf("provider 端点未进 RoutePaths ⇒ 路由冲突守卫漏检它")
	}
}

func TestTaskRankingRouteRegistered(t *testing.T) {
	if RouteTaskRanking != "/api/admin/session-analytics/task-ranking" {
		t.Fatalf("task 端点路径漂移：%s", RouteTaskRanking)
	}
	if !containsString(RoutePaths, RouteTaskRanking) {
		t.Fatalf("task 端点未进 RoutePaths ⇒ 路由冲突守卫漏检它")
	}
}

// ★ 口径钉死：provider 榜必须 GROUP BY provider（视图有 provider 列）。
//
// ⚠ GROUP BY 是在常量**之外**拼接的（WHERE 里还要按租户条件续写），
//   所以断言要看**生产函数的源码**，不是只看常量本身。
func TestProviderRankingGroupsByProviderColumn(t *testing.T) {
	src := readQueriesSource(t)
	if !strings.Contains(providerRankingSQL, "model_usage_aggregates") {
		t.Fatal("provider 榜未走 model_usage_aggregates（与 model-ranking 同源）")
	}
	if !strings.Contains(src, `" GROUP BY provider"`) {
		t.Fatal("provider 榜未按 provider 列聚合")
	}
	// ★ 绝不能按 model 列的前缀切分出 provider —— model 列是纯模型名。
	if strings.Contains(providerRankingSQL, "split_part") || strings.Contains(providerRankingSQL, "SPLIT_PART") {
		t.Fatal("provider 榜不得从 model 列切前缀 —— model 列是纯模型名，切出来的是模型名不是供应商")
	}
	// model_count 必须按 DISTINCT model 计（同一 provider 下的模型数）。
	if !strings.Contains(providerRankingSQL, "COUNT(DISTINCT model)") {
		t.Fatal("provider 榜的 model_count 未按 DISTINCT model 统计")
	}
}

// ★ 口径钉死：task 榜走 project_task_aggregates，且过滤空串 task。
func TestTaskRankingSourceAndEmptyTaskFilter(t *testing.T) {
	src := readQueriesSource(t)
	if !strings.Contains(taskRankingSQL, "project_task_aggregates") {
		t.Fatal("task 榜未走 project_task_aggregates")
	}
	// task 空串 = 「无具体任务」（迁移 981 唯一索引要求列上无 NULL）
	// ⇒ 不过滤的话「无任务」会被算成一个任务。
	if !strings.Contains(taskRankingSQL, "task <> ''") {
		t.Fatal("task 榜未过滤空串 task ⇒ 「无具体任务」会被算成一个任务")
	}
	if !strings.Contains(src, `" GROUP BY task"`) {
		t.Fatal("task 榜未按 task 列聚合")
	}
}

// ★ 权限与陈旧度门：两个端点都必须有，不得裸奔。
func TestNewRankingsHaveAuthAndFreshnessGates(t *testing.T) {
	// handlers.go 的源码里，两个 handler 都必须调 isAdminFromRequest 与
	// requireFreshViews。用源码级断言（而非运行时 DB）——门禁要在无库环境也成立。
	src := readHandlerSource(t)
	for _, name := range []string{"HandleGetProviderRanking", "HandleGetTaskRanking"} {
		i := strings.Index(src, "func (h *Handler) "+name+"(")
		if i < 0 {
			t.Fatalf("%s 未找到", name)
		}
		body := src[i:]
		if end := strings.Index(body, "\nfunc "); end > 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "isAdminFromRequest") {
			t.Fatalf("%s 缺 admin 权限门", name)
		}
		if !strings.Contains(body, "requireFreshViews") {
			t.Fatalf("%s 缺陈旧度门", name)
		}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// readHandlerSource 读本包的 handlers.go 源码（用于源码级契约断言）。
func readHandlerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("读 handlers.go 失败：%v", err)
	}
	return string(b)
}

// readQueriesSource 读本包的 queries.go 源码（用于源码级契约断言）。
func readQueriesSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("queries.go")
	if err != nil {
		t.Fatalf("读 queries.go 失败：%v", err)
	}
	return string(b)
}
