package autoupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

// TestAdminAPI_SelfUpdateEndpointsAreReachable 是自更新执行面的**端点清单门**。
//
// ★ 为什么要有这道门：本仓已发生过「注册了 handler 但路由形状没接上」的同型事故
//
//	（AGENTS.md §708①：handler 层测试全绿测不出 mux 接线漂移）。
//	自更新的端点全部带静态段（/check、/install-status…），一旦被 /:version 参数
//	路由吞掉或拼错，**所有单元测试仍然全绿**，只有前端点了没反应。
//	所以这里不测 handler 内部，而是**真的发 HTTP 请求**，断言路由可达。
func TestAdminAPI_SelfUpdateEndpointsAreReachable(t *testing.T) {
	store := newStubStore()
	// 播种一个版本：GET /:version 在版本不存在时返回 404 是**正确行为**，
	// 那样这条断言就测不出「静态路由把参数路由吞掉」的问题。
	if err := store.CreateRelease(context.Background(), &Release{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
		Title: "2.7.0", ImageTag: "2.7.0", CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed release: %v", err)
	}
	api := NewAdminAPI(store, NewDownloader(t.TempDir()), NewInstaller("/tmp/x", t.TempDir(), t.TempDir()), nil)

	e := echo.New()
	g := e.Group("/api/admin/releases")
	api.RegisterRoutes(g)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/admin/releases/check"},
		{http.MethodGet, "/api/admin/releases/check-status"},
		{http.MethodGet, "/api/admin/releases/install-status"},
		{http.MethodGet, "/api/admin/releases/notifications"},
		{http.MethodPost, "/api/admin/releases/2.7.0/install"},
		{http.MethodPost, "/api/admin/releases/2.7.0/source-build"},
		{http.MethodGet, "/api/admin/releases/2.7.0"},
		{http.MethodGet, "/api/admin/releases/upgrade-logs"},
		{http.MethodGet, "/api/admin/releases/gray-rules"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("endpoint %s %s is NOT reachable (404) — route shape drifted", tc.method, tc.path)
			}
			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("endpoint %s %s rejected with 405 — wrong method", tc.method, tc.path)
			}
		})
	}
}

// TestAdminAPI_CheckLatest_ReportsUnconfiguredSource 门：未配置远端源时，
// 「立即检查」必须明确说「没配」，而不是静默返回一份看似正常的结果。
func TestAdminAPI_CheckLatest_ReportsUnconfiguredSource(t *testing.T) {
	api := NewAdminAPI(newStubStore(), nil, nil, nil)
	e := echo.New()
	api.RegisterRoutes(e.Group("/api/admin/releases"))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/releases/check", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no checker is configured, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("expected an explicit 'not configured' explanation, got %s", rec.Body.String())
	}
}

// TestAdminAPI_InstallRelease_ReportsUnconfiguredExecutor 门：未配置执行器时，
// 「点击安装升级」必须明确失败，不能静默 200。
func TestAdminAPI_InstallRelease_ReportsUnconfiguredExecutor(t *testing.T) {
	api := NewAdminAPI(newStubStore(), nil, nil, nil)
	e := echo.New()
	api.RegisterRoutes(e.Group("/api/admin/releases"))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/releases/2.7.0/install", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no executor is configured, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCustomerAPI_Check_TriggersRemoteCheck 门：客户面「检查更新」必须真的去远端查一次，
// 而不是只读本地表（本地表是检索器的产物，读它是自证）。
func TestCustomerAPI_Check_TriggersRemoteCheck(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
		BinaryURL: "https://example.test/bin", BinarySHA256: "sha",
	}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))

	api := NewCustomerAPI(store, func() (string, int) { return "2.6.0", 3040 }, ChannelStable)
	api.SetChecker(checker)

	e := echo.New()
	g := e.Group("/api/system/upgrade")
	api.RegisterRoutes(g)

	req := httptest.NewRequest(http.MethodPost, "/api/system/upgrade/check", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if source.calls() == 0 {
		t.Fatal("remote source was never consulted — 'check' only read the local table")
	}
	if !strings.Contains(rec.Body.String(), `"has_update":true`) {
		t.Errorf("expected has_update=true in response, got %s", rec.Body.String())
	}
}

// TestCustomerAPI_Check_ThrottlesRemoteCalls 门：**无鉴权**的公开检查端点
// 不得每次调用都出网 —— 否则它就是一个「谁都能打的远端放大器」。
//
// 期望：窗口内的第二次调用复用最近结论 ⇒ 远端只被访问一次。
func TestCustomerAPI_Check_ThrottlesRemoteCalls(t *testing.T) {
	store := newStubStore()
	source := &stubSource{release: &RemoteRelease{
		Version: "2.7.0", BuildSeq: 3100, Channel: ChannelStable,
	}}
	checker := NewChecker(store, source, func() (string, int) { return "2.6.0", 3040 }, ChannelStable,
		WithInstanceID("gw-test"))

	api := NewCustomerAPI(store, func() (string, int) { return "2.6.0", 3040 }, ChannelStable)
	api.SetChecker(checker)

	e := echo.New()
	g := e.Group("/api/system/upgrade")
	api.RegisterRoutes(g)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/system/upgrade/check", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d body=%s", i, rec.Code, rec.Body.String())
		}
	}

	if calls := source.calls(); calls != 1 {
		t.Fatalf("remote source was called %d times for 3 public requests — expected 1 (throttled)", calls)
	}
}
