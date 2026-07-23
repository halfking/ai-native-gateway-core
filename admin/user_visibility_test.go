package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 2026-07-23: tenant_admin 放宽可见性后，handleTenants 在用户无权访问时必须返回 403；
// 这些测试只覆盖角色路由（h.db == nil → 503 在 db 守卫之后才发生，但角色守卫在前，
// 所以 403 永远先于 503 触发——若未来调整代码顺序请同步更新此测试）。
func TestHandleTenants_TenantAdminCannotListOrCreate(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"list tenants", http.MethodGet, "/api/admin/tenants"},
		{"create tenant", http.MethodPost, "/api/admin/tenants"},
		{"patch tenant", http.MethodPatch, "/api/admin/tenants/hansi"},
		{"model-policies list", http.MethodGet, "/api/admin/tenants/hansi/model-policies"},
		{"model-policies audit", http.MethodGet, "/api/admin/tenants/hansi/model-policies/audit"},
		{"model-policies check", http.MethodPost, "/api/admin/tenants/hansi/model-policies/check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req = SetAuthContext(req, &AuthContext{UserID: 1, TenantID: "hansi", Username: "alice", Role: "tenant_admin", IsJWT: true})
			rec := httptest.NewRecorder()

			h.handleTenants(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for tenant_admin, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// 角色守卫后，super_admin 在 db 未配置时仍会落到 503（db 早于角色检查）。
// 这里只验证 super_admin 的非写操作不会因角色守卫被错拒。
func TestHandleTenants_SuperAdminReachesDBGuard(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/api/admin/tenants"},
		{"get", http.MethodGet, "/api/admin/tenants/hansi"},
		{"users", http.MethodGet, "/api/admin/tenants/hansi/users"},
		{"keys", http.MethodGet, "/api/admin/tenants/hansi/keys"},
		{"stats", http.MethodGet, "/api/admin/tenants/hansi/stats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req = SetAuthContext(req, &AuthContext{UserID: 9, TenantID: "default", Username: "root", Role: "super_admin", IsJWT: true})
			rec := httptest.NewRecorder()

			h.handleTenants(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503 (db guard) for super_admin, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// handleTenants 最外层 db guard 在缺 db 时返回 503；本测试确认 db guard 优先于路由。
// 行为契约：除非 db == nil，否则请求一定会进入角色路由器；后续 GET on /{code}/users 等
// 子资源因为 db=nil 会回到 503——这点由前一个测试覆盖。
func TestHandleTenants_DBGuardFirst(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/hansi/users", nil)
	// 故意不设 AuthContext，模拟路由外层 AdminMiddleware 还没跑过的状态。
	rec := httptest.NewRecorder()

	h.handleTenants(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (db guard), got %d body=%s", rec.Code, rec.Body.String())
	}
}
