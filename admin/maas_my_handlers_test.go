package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// F-11 行为钉子（2026-11-04 修订审计档 §4.2 序号 8）：myOwnerScope 清掉死
// 返回值后收窄口径必须逐字不变 —— legacy admin_key（认证了但非 JWT）与
// super_admin 显式越权都是全租户视图（"" = 不过滤 owner），普通 JWT 用户
// 强制本人。auth == nil 与 legacy 两条路径在代码上已显式分开，但对外行为
// 同为不收窄（防御性兜底），这里一并钉住防止未来悄悄改语义。
func TestMyOwnerScope(t *testing.T) {
	jwtCtx := func(username, role string) *AuthContext {
		return &AuthContext{UserID: 7, TenantID: "default", Username: username, Role: role, IsJWT: true}
	}
	cases := []struct {
		name string
		auth *AuthContext // nil ⇒ 未认证态（不应到达，防御分支）
		url  string
		want string
	}{
		{"legacy admin_key stays full-tenant", &AuthContext{UserID: 1, TenantID: "default", Role: "admin_key"}, "/api/maas/my/usage", ""},
		{"unauthenticated falls to the defensive full-tenant branch", nil, "/api/maas/my/usage", ""},
		{"regular JWT user is narrowed to self", jwtCtx("alice", "tenant_admin"), "/api/maas/my/usage", "alice"},
		{"super_admin defaults to self", jwtCtx("root", "super_admin"), "/api/maas/my/usage", "root"},
		{"super_admin explicit owner_user override", jwtCtx("root", "super_admin"), "/api/maas/my/usage?owner_user=bob", "bob"},
		{"blank owner_user falls back to self", jwtCtx("root", "super_admin"), "/api/maas/my/usage?owner_user=%20", "root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.auth != nil {
				req = SetAuthContext(req, tc.auth)
			}
			if got := myOwnerScope(req); got != tc.want {
				t.Fatalf("myOwnerScope = %q, want %q", got, tc.want)
			}
		})
	}
}
