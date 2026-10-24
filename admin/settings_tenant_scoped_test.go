package admin

// settings_tenant_scoped_test.go — 平台路由视角下 tenant-scoped spec 的行为钉测。
//
// 2026-10-23 llmgo 245 生产缺陷：管理页 /admin/settings 点开任何 tenant-scoped
// 键（如 goal.retry_delay_seconds）⇒ GET /api/admin/settings/{key} 走
// EffectiveValue(ScopeTenant, key, "") ⇒ "tenant-scoped; tenant_id required"
// ⇒ 500 "internal error (see server logs)"（245 journal settings.go:215 ×5）。
// PUT 同根：StoreDB.Set 硬拒 ScopeTenant ⇒ 500 "save failed"。
//
// 修法：GET 返回 spec 默认值（source=default，前端本就为 tenant scope 渲染
// 警告并回退默认值）；PUT 返回 400 并指向 tenant-settings 端点。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func withTenantScopedSpecRegistry(t *testing.T) {
	t.Helper()
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })
	settings.Global = settings.NewRegistry()
	_ = settings.Global.RegisterSpec(&settings.Spec{
		Key:      "goal.retry_delay_seconds",
		Type:     settings.TypeInt,
		Scope:    settings.ScopeTenant,
		Category: settings.CategoryRetry,
		Default:  20,
	})
	_ = settings.Global.RegisterSpec(&settings.Spec{
		Key:      "platform.plain_key",
		Type:     settings.TypeInt,
		Scope:    settings.ScopePlatform,
		Category: settings.CategorySession,
		Default:  7,
	})
}

// 平台路由 GET tenant-scoped spec：必须 200 + 默认值，不得 500。
func TestSettingsGetTenantScopedOnPlatformRouteReturnsDefault(t *testing.T) {
	withTenantScopedSpecRegistry(t)
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings/goal.retry_delay_seconds", nil)
	rec := httptest.NewRecorder()
	h.settingsRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"value":20`, `"source":"default"`, `"Scope":"tenant"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s; body: %s", want, body)
		}
	}
}

// 平台路由 PUT tenant-scoped spec：必须 400 并指向 tenant-settings 端点。
func TestSettingsPutTenantScopedOnPlatformRouteRejected(t *testing.T) {
	withTenantScopedSpecRegistry(t)
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/admin/settings/goal.retry_delay_seconds",
		strings.NewReader(`{"value":30}`))
	rec := httptest.NewRecorder()
	h.settingsRouter(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tenant-settings") {
		t.Errorf("body should point at tenant-settings endpoint; body: %s", rec.Body.String())
	}
}

// 回归：平台路由 GET platform-scoped spec 的默认值解析链不受影响。
func TestSettingsGetPlatformScopedStillResolves(t *testing.T) {
	withTenantScopedSpecRegistry(t)
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings/platform.plain_key", nil)
	rec := httptest.NewRecorder()
	h.settingsRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"value":7`) {
		t.Errorf("platform-scoped default not resolved; body: %s", rec.Body.String())
	}
}

// 平台路由 POST /rollback 对 tenant-scoped spec：必须 400 并指向 tenant-settings 端点。
//
// ★ 2026-10-24 12h 审计轮（问题⑮）：这是**同族键的第三个入口**。
//
//	GET /api/admin/settings/{key} 与 PUT /api/admin/settings/{key} 都已对
//	tenant-scoped spec 做了分支；平台 rollback 此前**没有** ⇒ ⇒ 它会一路走到
//	store.Rollback(scope=ScopeTenant, tid="") ⇒ `WHERE tenant_id = ''`
//	⇒ pgx.ErrNoRows ⇒ 400「no previous value to rollback to」。
//
//	★ 那不是 500（⇒ 「500 根修」的结论仍成立），但它是**误导性错误**：
//	用户看到的是「没有可回滚的历史值」，真实原因是「这个键压根不属于平台层」。
//
// ★ 变异：把 settings.go 里这段分支删掉 ⇒ 本门红。
func TestSettingsRollbackTenantScopedOnPlatformRouteRejected(t *testing.T) {
	withTenantScopedSpecRegistry(t)
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/settings/goal.retry_delay_seconds/rollback", nil)
	rec := httptest.NewRecorder()
	h.settingsRouter(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "tenant-settings") {
		t.Errorf("body should point at tenant-settings endpoint; body: %s", body)
	}
	// ★ 必须**不是**「no previous value to rollback to」——那是误导性的错误文案。
	if strings.Contains(body, "no previous value to rollback to") {
		t.Errorf("body still carries the misleading 'no previous value' error; body: %s", body)
	}
}
