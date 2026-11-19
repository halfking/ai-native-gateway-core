// Package sessionanalytics — 契约钉测
//
// ★ 这些测试存在的理由：Phase 2 的 8 个套件全绿、单测覆盖率看着不低，
//
//	但线上**没有任何一条路径能走到这段代码**（包从未被 import ⇒
//	RegisterRoutes 从未被调用 ⇒ 端点 404）。同类缺陷还有：
//	· SQL 引用了 users.owner —— 而 users 表**没有 owner 列**（真库实测）
//	  ⇒ 任何一条 JOIN users 的查询都会 42703；
//	· 身份从字符串 context 键读 —— 而认证中间件写的是**类型化**键
//	  ⇒ 恒 403；
//	· 角色词表含 "admin" —— 而真库只有 super_admin / tenant_admin / admin_key。
//
// ⇒ 通用式：**单测绿 ≠ 代码在生产路径上**。必须补三类钉测：
//
//	① 路由可达（注册后 ServeMux 确实能路由到）；
//	② SQL 里引用的每个列名都在真库存在（对着 information_schema 核）；
//	③ 身份/角色的判定与仓内认证中间件同一套键与同一套词表。
package sessionanalytics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterRoutesAreReachable 钉测：路由注册后真的能被路由到。
//
// ★ 这是「从未接线」这个洞的直接钉子：只测 handler 函数不够，
//
//	必须测**注册之后** ServeMux 能把请求送到 handler。
//
// ★★ 但这条**只覆盖"从没接线"**，不覆盖"接上了却撞车"：
//
//	2026-11-20 211 部署事故就是**撞车**——
//	  · 本包注册 `/api/admin/session-analytics/users`；
//	  · 仓内早有同名路径（admin/handler.go:1334，2026-07-07 用户画像，
//	    前端 web/src/api/admin.ts:239 在用）；
//	  · 同一个 ServeMux 上重复注册 ⇒ panic ⇒ **整个网关启动失败**
//	    ⇒ 蓝绿候选槽起不来 ⇒ 部署回退 ⇒ 新端点永远上不了线。
//	⇒ 本测试在**空 mux** 上跑 ⇒ 空 mux 上不存在任何冲突 ⇒ 必然漏。
//	⇒ **真正的冲突守卫在 cmd/gateway/session_analytics_routes_guard_test.go**：
//	  它把仓内真实已注册的路径扫出来、注册进同一个 mux（生产同形），
//	  再注册本包路由 ⇒ 必须不 panic。
//
//	⇒ 通用式：**可达性钉测必须在"生产同形的 mux"上做**。
func TestRegisterRoutesAreReachable(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, func(h http.HandlerFunc) http.HandlerFunc { return h })

	for _, path := range RoutePaths {
		req := httptest.NewRequest(http.MethodGet, path+"?start_date=2024-01-01&end_date=2024-01-31", nil)
		rec := httptest.NewRecorder()

		// ServeMux 不 panic ⇒ 模式没冲突；handler 没命中 ⇒ 会是 404。
		require.NotPanics(t, func() { mux.ServeHTTP(rec, req) })
		assert.NotEqual(t, http.StatusNotFound, rec.Code,
			"路由 %s 未被 ServeMux 路由到（说明 RegisterRoutes 没接上）", path)
	}
}

// TestRegisterRoutesDoNotCollideWithAdminSubtree 钉测：与 Echo 的
// "/api/admin/" 子树挂载共存。
//
// ★ cmd/gateway/main.go 里 mux.Handle("/api/admin/", e) 已经是 Echo 的子树挂载。
//
//	Go 的 ServeMux 只禁止**同一模式**重复注册；更精确的路径模式优先匹配。
//	⇒ 本测试钉住"精确模式不会被子树抢走"，否则端点会静默走到 Echo。
func TestRegisterRoutesDoNotCollideWithAdminSubtree(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/api/admin/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 子树兜底
	}))
	RegisterRoutes(mux, nil, func(h http.HandlerFunc) http.HandlerFunc { return h })

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/overview?start_date=2024-01-01&end_date=2024-01-31", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusTeapot, rec.Code,
		"请求被 /api/admin/ 子树兜底接走了 ⇒ 端点不可达")
}

// TestOwnerComesFromAggregateNotUsersTable 钉测：owner 列的来源。
//
// ★ users 表**没有 owner 列**（真库列：id/tenant_id/username/password_hash/
//
//	display_name/email/role/enabled/last_login_at/created_at/updated_at/
//	must_change_password）⇒ SQL 里出现 u.owner 就会 42703。
//	⇒ 这里钉住：owner 必须取聚合视图自己的列（a.owner / p.owner）。
func TestOwnerComesFromAggregateNotUsersTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"top users", topUsersSQL},
		{"user ranking", userRankingSQL},
		{"user ranking by project", userRankingByProjectSQL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotContains(t, tc.sql, "u.owner",
				"users 表没有 owner 列（真库实测）⇒ u.owner 会 42703")
			assert.NotContains(t, tc.sql, "u.username AS owner")
		})
	}
}

// TestTaskEmptyStringSemantics 钉测：project_task_aggregates 的 task 列用
// ” 表示「无具体任务」（不是 NULL），Go 侧必须按 ” 判空。
//
// ★ 为什么不是 NULL：视图的唯一索引必须是**纯列**索引且列上不能有 NULL
//
//	（PG 的 REFRESH MATERIALIZED VIEW CONCURRENTLY 要求）⇒ 迁移把 NULL 归一成 ''。
//	⇒ Go 侧写成 `task IS NOT NULL` 会把空任务行当成真任务返回。
func TestTaskEmptyStringSemantics(t *testing.T) {
	assert.Contains(t, userRankingByProjectSQL, "NULLIF(p.task, '')",
		"task 用 '' 表示无任务 ⇒ COUNT(DISTINCT) 必须用 NULLIF 归一")
	assert.Contains(t, latestTaskSQL, "task <> ''",
		"取最新任务必须过滤空串（写成 IS NOT NULL 会返回空任务行）")
	assert.NotContains(t, latestTaskSQL, "task IS NOT NULL")
}
