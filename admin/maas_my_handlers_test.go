package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// F-11 行为钉子（2026-11-04 修订审计档 §4.2 序号 8）：myOwnerScope 清掉死
// 返回值后收窄口径必须逐字不变 —— legacy admin_key（认证了但非 JWT）与
// super_admin 显式越权都是全租户视图（"" = 不过滤 owner），普通 JWT 用户
// 强制本人。
//
// 2026-11-04 合并调和（5be6b2f0e 并行会话与本地修复同日各修 F-11，取其
// fail-closed 语义）：auth == nil 现在返回 identified=false ⇒ 调用方 401，
// 不再退化成全租户可见；legacy 服务账号是 identified=true + 不收窄（有意）。
// 两个分支在代码上显式分开，这里逐分支钉住防止未来悄悄改语义。
func TestMyOwnerScope(t *testing.T) {
	jwtCtx := func(username, role string) *AuthContext {
		return &AuthContext{UserID: 7, TenantID: "default", Username: username, Role: role, IsJWT: true}
	}
	cases := []struct {
		name string
		auth *AuthContext // nil ⇒ 未认证态（不应到达，防御分支）
		url  string
		want string
		idnt bool
	}{
		{"legacy admin_key stays full-tenant", &AuthContext{UserID: 1, TenantID: "default", Role: "admin_key"}, "/api/maas/my/usage", "", true},
		{"unauthenticated is fail-closed, not full-tenant", nil, "/api/maas/my/usage", "", false},
		{"regular JWT user is narrowed to self", jwtCtx("alice", "tenant_admin"), "/api/maas/my/usage", "alice", true},
		{"super_admin defaults to self", jwtCtx("root", "super_admin"), "/api/maas/my/usage", "root", true},
		{"super_admin explicit owner_user override", jwtCtx("root", "super_admin"), "/api/maas/my/usage?owner_user=bob", "bob", true},
		{"blank owner_user falls back to self", jwtCtx("root", "super_admin"), "/api/maas/my/usage?owner_user=%20", "root", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.auth != nil {
				req = SetAuthContext(req, tc.auth)
			}
			got, identified := myOwnerScope(req)
			if got != tc.want || identified != tc.idnt {
				t.Fatalf("myOwnerScope = (%q, %v), want (%q, %v)", got, identified, tc.want, tc.idnt)
			}
		})
	}
}
