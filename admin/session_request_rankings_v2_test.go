package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

func TestSessionRequestRankingV2RejectsInvalidScopeAndSort(t *testing.T) {
	tenant := &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true}
	base := "/api/admin/session-analytics/v2/agent-ranking?start_date=2026-11-23&end_date=2026-11-23"
	for _, tc := range []struct {
		name, method, query string
		auth                *AuthContext
		status              int
	}{
		{"rejects writes", http.MethodPost, "", tenant, http.StatusMethodNotAllowed},
		{"requires authentication", http.MethodGet, "", nil, http.StatusUnauthorized},
		// ★ 2026-11-27 非管理员菜单重构轮：user 档放行（有租户绑定时走 owner 收窄
		//   读面），不再 403 —— 见 TestSessionRankingScopeUserTier。
		{"user without tenant binding rejected", http.MethodGet, "", &AuthContext{Role: "user"}, http.StatusForbidden},
		{"user scoped to own tenant proceeds", http.MethodGet, "", &AuthContext{TenantID: tenant.TenantID, Role: "user", Username: "e2e-user"}, http.StatusServiceUnavailable},
		{"user rejects another tenant", http.MethodGet, "&tenant_id=tenant-b", &AuthContext{TenantID: tenant.TenantID, Role: "user", Username: "e2e-user"}, http.StatusForbidden},
		{"rejects another tenant", http.MethodGet, "&tenant_id=tenant-b", tenant, http.StatusForbidden},
		{"rejects model sort on agent route", http.MethodGet, "&sort_by=cache_rate", tenant, http.StatusBadRequest},
		{"accepts tenant and session sort", http.MethodGet, "&sort_by=sessions", tenant, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, base+tc.query, nil)
			if tc.auth != nil {
				r = SetAuthContext(r, tc.auth)
			}
			w := httptest.NewRecorder()
			(&Handler{}).handleSessionRequestRankingV2(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
}

// ★ 2026-11-27 非管理员菜单重构轮：user 档收窄判据（变异守卫）——
// scope 的 owner/ownerIsUser 只在 user 档非零；tenant_admin/super 档恒零
// （变异：把 user 分支的 owner 注入删掉 ⇒ ownerUser 断言红）。
func TestSessionRankingScopeUserTier(t *testing.T) {
	u := &AuthContext{TenantID: "tenant-a", Role: "user", Username: "e2e-user", IsJWT: true}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/v2/user-ranking?start_date=2026-11-23&end_date=2026-11-23", nil)
	r = SetAuthContext(r, u)
	w := httptest.NewRecorder()
	scope, ok := prepareSessionRankingScope(w, r)
	if !ok {
		t.Fatalf("prepareSessionRankingScope not ok: %s", w.Body)
	}
	if scope.tenant != "tenant-a" {
		t.Fatalf("tenant = %q, want own tenant", scope.tenant)
	}
	if scope.owner != "e2e-user" {
		t.Fatalf("owner = %q, want caller username (owner scoping)", scope.owner)
	}
	if !scope.ownerIsUser {
		t.Fatal("ownerIsUser = [REDACTED:account], want true for user tier")
	}
	if scope.bypass != "false" || scope.role != "tenant_admin" {
		t.Fatalf("rls = role=%s bypass=%s, want tenant_admin/false (no bypass)", scope.role, scope.bypass)
	}

	// 变异守卫：管理员档 owner 恒空（否则管理员 detail 页被人档收窄）。
	ta := &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true}
	r2 := SetAuthContext(httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/v2/user-ranking?start_date=2026-11-23&end_date=2026-11-23", nil), ta)
	scope2, ok2 := prepareSessionRankingScope(httptest.NewRecorder(), r2)
	if !ok2 || scope2.owner != "" || scope2.ownerIsUser {
		t.Fatalf("admin tier owner scoping wrong: ok=%v owner=%q isUser=%v", ok2, scope2.owner, scope2.ownerIsUser)
	}

	// rankings 维投影的 Owner 参数按 owner_user 收窄：非空时进 WHERE。
	// （SQL 文本断言：占位 $4 必须在 observable WHERE 内 —— 变异：删掉
	// AND ($4='' OR owner_user=$4) 行 ⇒ 本判据红。）
	sql := metrics.SessionRequestRankingObservableWhereForTest
	if !strings.Contains(sql, "($4='' OR owner_user=$4)") {
		t.Fatal("rankings observable WHERE lost owner narrowing ($4)")
	}
}
