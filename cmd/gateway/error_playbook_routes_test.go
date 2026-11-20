package main

// 错误处理手册管理 API 的**路由可达性 + 鉴权**守门测试。
//
// 与 pi_analysis_routes_reachable_test.go 同款纪律（AGENTS.md §708① 前车之鉴）：
//
//	handler 层单测覆盖不到「路由到底注册到 main mux 没有」；
//	`/api/admin/` 前缀路由在 ServeMux 上会**绕过** Echo 的 JWT 中间件
//	⇒ 必须真发 HTTP 请求断言「401（路由在 + 中间件跑到）而不是 404」，
//	且**鉴权材料缺失 ⇒ 拒绝注册**（fail-closed）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/errorplaybook"
)

var errorPlaybookRoutePaths = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/admin/error-playbook/entries"},
	{http.MethodPost, "/api/admin/error-playbook/entries"},
	{http.MethodGet, "/api/admin/error-playbook/entries/1"},
	{http.MethodPut, "/api/admin/error-playbook/entries/1"},
	{http.MethodDelete, "/api/admin/error-playbook/entries/1"},
	{http.MethodPost, "/api/admin/error-playbook/entries/1/execute"},
	{http.MethodGet, "/api/admin/error-playbook/executions"},
	{http.MethodGet, "/api/admin/error-playbook/executions/1"},
	{http.MethodPost, "/api/admin/error-playbook/executions/1/revert"},
	{http.MethodPost, "/api/admin/error-playbook/analyze"},
	{http.MethodGet, "/api/admin/error-playbook/stats"},
}

func TestErrorPlaybookRoutesReachableOverHTTP(t *testing.T) {
	mux := http.NewServeMux()

	// 竞争路由（与真实 main mux 同型）：注册顺序在先，证明「最长模式优先」。
	mux.HandleFunc("/api/admin/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("admin prefix catch-all"))
	})
	mux.HandleFunc("/api/admin/error-playbook/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("playbook prefix catch-all"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("root catch-all"))
	})

	pool, err := pgxpool.New(context.Background(),
		"postgres://pb_test:pb_test@127.0.0.1:1/pb_test?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("create lazy pgx pool: %v", err)
	}
	t.Cleanup(pool.Close)

	store := errorplaybook.NewStore(nil) // 路由可达性测试不碰 DB
	actions := errorplaybook.NewBuiltinActions(nil, nil)
	h := admin.NewErrorPlaybookHandler(store, nil, actions, pool, "test-secret")
	h.RegisterRoutes(mux, "/api/admin/error-playbook")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	for _, tc := range errorPlaybookRoutePaths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("route NOT registered (404) — 管理面会整组失联")
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("want 401 (AdminMiddleware), got %d — 鉴权没有跑到", resp.StatusCode)
			}
		})
	}
}

// TestErrorPlaybookRoutesFailClosedNoAuth 鉴权材料缺失 ⇒ 拒绝注册（404）。
//
//	★ 宁可端点 404，也不能让「忘了配 secret」的部署把手册/执行留痕
//	敞在公网上（PI handler 同款判据）。
func TestErrorPlaybookRoutesFailClosedNoAuth(t *testing.T) {
	mux := http.NewServeMux()
	store := errorplaybook.NewStore(nil)
	actions := errorplaybook.NewBuiltinActions(nil, nil)

	h := admin.NewErrorPlaybookHandler(store, nil, actions, nil, "")
	h.RegisterRoutes(mux, "/api/admin/error-playbook")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/admin/error-playbook/entries")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no-auth material must NOT register routes (want 404), got %d", resp.StatusCode)
	}
}
