// Command gateway — L1 守卫（2026-11-23，211 配置优化遗留 L1）
//
// 背景：211 的 maintain 服务跑在 127.0.0.1:8082（nginx 边缘已在代理
// /maintain-api/*），但**网关自身**没有设 MAINTAIN_SERVICE_URL ⇒
//
//	· 直连网关端口（8781/8783）的 /maintain-api/* 恒 503
//	  （code=maintain.not_configured）；
//	· 启动日志恒有一条 WARN「maintain proxy disabled」。
//
// 而 legacy 那 17 个 /api/* 前缀在 maintain 侧**全是 404**（它的路由挂在
// /maintain-api/* 下），鉴权模型也不同（maintain 认自己的会话，不认网关的
// admin key ⇒ 401）。
//
// ⇒ 接线的口径：**maintain 能服务就由 maintain 服务，不能服务网关自己顶上**
//
//	（尽力迁移，不是开关式切换）。
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── 门 1：上游 404 ⇒ 回落到网关自己的 in-process handler ──────────────
//
// ★ 这条是本轮最关键的守卫：如果没有回落，仅仅是「设了 MAINTAIN_SERVICE_URL」
//
//	就会把 /api/admin/licenses、/api/downloads/catalog … 一批现在能用的端点
//	变成 404。
func TestMaintainCompatFallsBackToInProcessOn404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	legacyCalled := false
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalled = true
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("legacy"))
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if !legacyCalled {
		t.Fatal("上游 404 时没有回落到 in-process handler：legacy 端点会被打成 404")
	}
	if response.Code != http.StatusTeapot {
		t.Errorf("status = %d，期望 %d（回落后的响应）", response.Code, http.StatusTeapot)
	}
	if body := response.Body.String(); body != "legacy" {
		t.Errorf("body = %q，期望 %q", body, "legacy")
	}
	// 回落后不应再带 Deprecation 标（响应不是 maintain 给的）。
	if got := response.Header().Get("Deprecation"); got != "" {
		t.Errorf("回落响应带上了 Deprecation=%q：它不是 maintain 服务的", got)
	}
}

// ── 门 2：上游 401/403 ⇒ 同样回落（鉴权模型不同，不是「端点不存在」）───
func TestMaintainCompatFallsBackOnAuthRejection(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
		legacyCalled := false
		handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			legacyCalled = true
			w.WriteHeader(http.StatusTeapot)
		}))

		request := httptest.NewRequest(http.MethodGet, "/api/admin/licenses", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		upstream.Close()

		if !legacyCalled {
			t.Errorf("上游 %d 时没有回落：legacy 端点会被网关的 admin key 打成 401", code)
		}
		if response.Code != http.StatusTeapot {
			t.Errorf("上游 %d：status = %d，期望 %d", code, response.Code, http.StatusTeapot)
		}
	}
}

// ── 门 3：上游 2xx ⇒ 采用 maintain 的响应（这才是「迁移」）────────────
func TestMaintainCompatAdoptsSuccessfulUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 路径改写：legacy /api/X → maintain /maintain-api/X
		if r.URL.Path != "/maintain-api/downloads/catalog" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("maintain"))
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	legacyCalled := false
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if legacyCalled {
		t.Error("上游 2xx 时却回落了：迁移没有生效")
	}
	if response.Code != http.StatusOK {
		t.Errorf("status = %d，期望 200", response.Code)
	}
	if body := response.Body.String(); body != "maintain" {
		t.Errorf("body = %q，期望 %q", body, "maintain")
	}
	if got := response.Header().Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation = %q，期望 true（legacy 路径由 maintain 服务时要带弃用标）", got)
	}
}

// ── 门 4：上游 5xx ⇒ 统一 503 信封，**不回落** ────────────────────────
//
// 既有契约：上游故障时运维看到的必须是「维护面挂了」，而不是
// 「端点偶尔正常」（静默回落旧实现）。这条门防止有人把回落范围扩大到 5xx。
func TestMaintainCompatDoesNotFallBackOnUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	legacyCalled := false
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if legacyCalled {
		t.Error("上游 5xx 时回落了：运维会看到「端点偶尔正常」而不是「维护面挂了」")
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d，期望 503", response.Code)
	}
	if !strings.Contains(response.Body.String(), "maintain.upstream_unavailable") {
		t.Errorf("body 不是统一 503 信封: %s", response.Body.String())
	}
}

// ── 门 5：canonical 路径（/maintain-api/*）不做回落 ───────────────────
//
// /maintain-api/* 是 maintain 的**权威**前缀：它回 404 就意味着「这个
// 端点不存在」，回落到网关的 in-process handler 会掩盖问题（客户端拿到
// 一个网关自造的响应，却以为问的是维护面）。
func TestMaintainCanonicalPathDoesNotFallBack(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	t.Setenv("MAINTAIN_SERVICE_URL", upstream.URL)
	legacyCalled := false
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "/maintain-api/menu/ops", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if legacyCalled {
		t.Error("canonical 路径回落了：客户端会拿到网关自造的响应，却以为问的是维护面")
	}
	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d，期望 404", response.Code)
	}
}

// ── 门 6：未配置 MAINTAIN_SERVICE_URL 时行为不变（回落到 legacy）──────
func TestMaintainCompatUnconfiguredStillServesLegacy(t *testing.T) {
	t.Setenv("MAINTAIN_SERVICE_URL", "")
	legacyCalled := false
	handler := newMaintainCompatHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/downloads/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if !legacyCalled {
		t.Fatal("未配置 maintain 时 legacy 端点不可达")
	}
	if response.Code != http.StatusTeapot {
		t.Errorf("status = %d，期望 %d", response.Code, http.StatusTeapot)
	}
}
