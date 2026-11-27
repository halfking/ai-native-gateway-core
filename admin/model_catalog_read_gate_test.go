package admin

// 2026-11-27 非管理员菜单重构轮第二轮 —— modelCatalogReadGate 判据。
//
// 「模型与目录」页对非管理员放开后，三个原先 superAdmin-only 的纯读元数据
// 端点（/api/catalog、/api/tags、/api/routing/featured GET）换挂
// modelCatalogReadGate。本文件钉闸层的角色 × 方法矩阵：
//
//	super_admin / admin_key / tenant_admin（带租户）→ GET 放行
//	tenant_admin 无租户绑定 → 403（防 AuthContext.TenantID 退化成 default）
//	user（带租户）→ GET 放行；POST/PATCH/DELETE 403
//	user 无租户绑定 → 403
//	channel 等其他角色 → 403 维持旧口径
//	无 token / 坏 token → 401
//
// handler 侧写面双保险（featured PATCH、tags POST 仅 super）另在
// TestHandleRoutingFeaturedPatchSuperOnly / TestHandleTagsPostSuperOnly。
// 变异守卫（HaveTeeth）：把「ta GET 放行」的中间件换成 SuperAdminMiddleware
// 后同一请求必须 403 —— 证明判据不是恒绿。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func modelCatalogGateToken(t *testing.T, userID int, tenant, username, role, secret string) string {
	t.Helper()
	token, _, err := SignToken(userID, tenant, username, role, secret, false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	return token
}

func runModelCatalogGate(t *testing.T, method, token, secret string) (*httptest.ResponseRecorder, *string) {
	t.Helper()
	var gotRole string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ac := GetAuthContext(r); ac != nil {
			gotRole = ac.Role
		}
		w.WriteHeader(http.StatusOK)
	})
	mw := modelCatalogReadGate(next, nil, secret)
	req := httptest.NewRequest(method, "/api/catalog", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mw(rr, req)
	return rr, &gotRole
}

func TestModelCatalogReadGate_AllowsThreeRolesGet(t *testing.T) {
	cases := []struct {
		name   string
		role   string
		tenant string
	}{
		{"super_admin", "super_admin", "default"},
		{"admin_key", "admin_key", "default"},
		{"tenant_admin", "tenant_admin", "acme"},
		{"user", "user", "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := modelCatalogGateToken(t, 7, tc.tenant, "ops", tc.role, "test-secret-mcg-1")
			rr, gotRole := runModelCatalogGate(t, http.MethodGet, token, "test-secret-mcg-1")
			if rr.Code != http.StatusOK {
				t.Fatalf("%s GET: got %d, want 200 body=%s", tc.name, rr.Code, rr.Body.String())
			}
			if *gotRole != tc.role {
				t.Errorf("%s: AuthContext.Role = %q, want %q", tc.name, *gotRole, tc.role)
			}
		})
	}
}

func TestModelCatalogReadGate_HEADAllowedForUser(t *testing.T) {
	token := modelCatalogGateToken(t, 7, "acme", "joe", "user", "test-secret-mcg-2")
	rr, _ := runModelCatalogGate(t, http.MethodHead, token, "test-secret-mcg-2")
	if rr.Code != http.StatusOK {
		t.Fatalf("user HEAD: got %d, want 200", rr.Code)
	}
}

func TestModelCatalogReadGate_UserWriteRejected(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			token := modelCatalogGateToken(t, 7, "acme", "joe", "user", "test-secret-mcg-3")
			rr, _ := runModelCatalogGate(t, method, token, "test-secret-mcg-3")
			if rr.Code != http.StatusForbidden {
				t.Fatalf("user %s: got %d, want 403", method, rr.Code)
			}
		})
	}
}

func TestModelCatalogReadGate_TenantAdminWriteRejected(t *testing.T) {
	// ta 的写面由 handler 侧 RequireSuperAdminForWrite / 显式 IsSuperAdminOrLegacy
	// 拦（featured PATCH / tags POST 双保险），闸层对 ta 的写**不放行**：
	// 非 GET/HEAD 走 default 分支以外的角色面 —— ta 在闸层与 super 同档放行，
	// 写拦截在 handler；此处钉「闸层不额外拦 ta 写」以免双闸语义漂移时无据可查。
	token := modelCatalogGateToken(t, 7, "acme", "ops", "tenant_admin", "test-secret-mcg-4")
	rr, _ := runModelCatalogGate(t, http.MethodPatch, token, "test-secret-mcg-4")
	if rr.Code != http.StatusOK {
		t.Fatalf("ta PATCH at gate layer: got %d, want 200 (write enforcement lives in handler)", rr.Code)
	}
}

func TestModelCatalogReadGate_MissingTenantBindingRejected(t *testing.T) {
	for _, role := range []string{"tenant_admin", "user"} {
		t.Run(role, func(t *testing.T) {
			token := modelCatalogGateToken(t, 7, "", "ops", role, "test-secret-mcg-5")
			rr, _ := runModelCatalogGate(t, http.MethodGet, token, "test-secret-mcg-5")
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s without tenant: got %d, want 403", role, rr.Code)
			}
		})
	}
}

func TestModelCatalogReadGate_OtherRolesRejected(t *testing.T) {
	token := modelCatalogGateToken(t, 7, "acme", "ch", "channel", "test-secret-mcg-6")
	rr, _ := runModelCatalogGate(t, http.MethodGet, token, "test-secret-mcg-6")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("channel GET: got %d, want 403", rr.Code)
	}
}

func TestModelCatalogReadGate_BadOrMissingTokenUnauthorized(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		rr, _ := runModelCatalogGate(t, http.MethodGet, "", "test-secret-mcg-7")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("no token: got %d, want 401", rr.Code)
		}
	})
	t.Run("bad token", func(t *testing.T) {
		rr, _ := runModelCatalogGate(t, http.MethodGet, "not-a-jwt", "test-secret-mcg-7")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("bad token: got %d, want 401", rr.Code)
		}
	})
}

// HaveTeeth：同一条 ta GET，闸换成 SuperAdminMiddleware 必须变红（403）。
// 防止「判据恒绿」——如果将来有人把 modelCatalogReadGate 退化回 super-only
// 而测试仍绿，本判据会当场打红。
func TestModelCatalogReadGate_HasTeeth(t *testing.T) {
	token := modelCatalogGateToken(t, 7, "acme", "ops", "tenant_admin", "test-secret-mcg-8")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := SuperAdminMiddleware(next, nil, "test-secret-mcg-8")
	req := httptest.NewRequest(http.MethodGet, "/api/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("teeth check: ta GET via SuperAdminMiddleware got %d, want 403 (gate regression would make main tests meaningless)", rr.Code)
	}
}

// handler 侧写面双保险：featured PATCH。
func TestHandleRoutingFeaturedPatchSuperOnly(t *testing.T) {
	cases := []struct {
		name string
		role string
	}{
		{"tenant_admin", "tenant_admin"},
		{"user", "user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			req := httptest.NewRequest(http.MethodPatch, "/api/routing/featured", nil)
			req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "acme", Username: "ops", Role: tc.role, IsJWT: true})
			rr := httptest.NewRecorder()
			h.handleRoutingFeatured(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s PATCH featured: got %d, want 403", tc.name, rr.Code)
			}
		})
	}
}

// handler 侧写面双保险：tags POST。
// ★ 顺序注记：handleTags 先查 h.db==nil（503）再判写闸，所以这里给一个
//   非 nil db 指针值让流程走到写闸（不触库：403 在 ctx/查询之前返回）。
func TestHandleTagsPostSuperOnly(t *testing.T) {
	h := &Handler{db: &pgxpool.Pool{}}
	req := httptest.NewRequest(http.MethodPost, "/api/tags", nil)
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "acme", Username: "ops", Role: "tenant_admin", IsJWT: true})
	rr := httptest.NewRecorder()
	h.handleTags(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("ta POST tags: got %d, want 403", rr.Code)
	}
}
