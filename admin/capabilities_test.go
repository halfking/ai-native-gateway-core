package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ★ 2026-10-21（R72f）：能力位端点的判据。
//
// ★★★ 这一组守的是**一条跨端契约**：前端「按能力隐藏入口」的前提是
//   「能力位说开着 ⇒ 端点真的在」。⇒ 而「能力位」若自己与端点**分两份状态**，
//   就会出现「能力位说开着、点进去 404」⇒ ★★ **404 与「端点不存在」同形
//   ⇒ 没有一层会说真话** ⇒ ⇒ 所以能力位必须**由端点是否注册派生**。
//
// ★ 判据分两侧：
//   ① 派生侧：能力位值 = 「handler 是否装配」；
//   ② 装配侧：端点**无条件注册**（否则前端学不到「关」）；
//   ③ 鉴权侧：用的是 `admin` 档而不是 `superAdmin` 档 ——
//      ★ 因为抽屉对 tenant_admin 也渲染；若用 superAdmin，tenant_admin 取能力位
//      会拿到 403 ⇒ 前端就会把 403 当成「全部能力关」⇒ super_admin 之外的人
//      看到**错误的**入口集。

const capabilitiesTestSecret = "capabilities-test-secret"

// capabilitiesTestHandler 造一个只带「对账 handler 装没装」的 Handler。
func capabilitiesTestHandler(reconciliation bool) *Handler {
	h := &Handler{db: nil, secret: capabilitiesTestSecret}
	if reconciliation {
		// ★ 只关心「非 nil」这个事实（能力位由它派生）⇒ 用一个零值实例即可。
		h.providerCostReconciliationHandler = &ProviderCostReconciliationHandler{}
	}
	return h
}

// TestCapabilitiesRouteIsRegisteredUnconditionally ★★★★
//
// ★★ 端点**无条件注册** —— 即使功能全关也要在。
//
//	理由：前端要**学得到「关」**才能「按能力隐藏入口」；
//	若端点本身随开关消失 ⇒ 前端连「这个功能是关的」都问不到
//	⇒ ⇒ 「按能力隐藏入口」就无从谈起（只能靠猜）。
func TestCapabilitiesRouteIsRegisteredUnconditionally(t *testing.T) {
	for _, reconciliation := range []bool{false, true} {
		mux := http.NewServeMux()
		capabilitiesTestHandler(reconciliation).RegisterCapabilityRoutes(mux)

		req := httptest.NewRequest(http.MethodGet, "/api/admin/capabilities", nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Fatalf("reconciliation=%v：能力位端点没被注册 ⇒ 前端学不到「关」", reconciliation)
		}
	}
}

// TestCapabilitiesDerivedFromHandlerAssembly ★★★★
//
// ★ 能力位**由「handler 是否装配」派生** ⇒ ⇒ 「能力位说开着」与
//
//	「端点真的在」不可能不一致（这是本端点存在的全部理由）。
func TestCapabilitiesDerivedFromHandlerAssembly(t *testing.T) {
	off := capabilitiesTestHandler(false).featureCapabilities()
	if off.CostReconciliation {
		t.Fatal("对账 handler 未装配 ⇒ 能力位必须为 false")
	}
	on := capabilitiesTestHandler(true).featureCapabilities()
	if !on.CostReconciliation {
		t.Fatal("对账 handler 已装配 ⇒ 能力位必须为 true")
	}
}

// TestCapabilitiesEndpointRequiresAuth ★★★
//
// ★ 未鉴权 ⇒ 401（能力位也是 admin 档资产，不是公开元数据）。
func TestCapabilitiesEndpointRequiresAuth(t *testing.T) {
	mux := http.NewServeMux()
	capabilitiesTestHandler(true).RegisterCapabilityRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/capabilities", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未鉴权应 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestCapabilitiesEndpointReachableByTenantAdmin ★★★★★
//
// ★★★ 这一条是**鉴别**判据：它区分「用 `admin` 档」与「用 `superAdmin` 档」。
//
//	· 若端点挂 `h.superAdmin(...)` ⇒ tenant_admin 拿到 403
//	  ⇒ 前端把 403 当成「全部能力关」⇒ super_admin 之外的人看到**错误的**入口集。
//	· 本端点挂 `h.admin(...)` ⇒ tenant_admin 拿到 200 ⇒ 能力集是**真的**。
func TestCapabilitiesEndpointReachableByTenantAdmin(t *testing.T) {
	mux := http.NewServeMux()
	capabilitiesTestHandler(true).RegisterCapabilityRoutes(mux)

	tok, _, err := SignToken(42, "tenant-a", "ta", "tenant_admin", capabilitiesTestSecret, false)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin 取能力位应 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body FeatureCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是能力位 JSON：%v（body=%s）", err, rec.Body.String())
	}
	if !body.CostReconciliation {
		t.Fatal("对账 handler 已装配 ⇒ 端点应报 cost_reconciliation=true")
	}
}

// TestCapabilitiesJSONFieldNamesAreStable ★★★
//
// ★ 能力位的**字段名**就是前端 `NavItem.requiresCapability` 用的名字
//
//	（逐字相等）⇒ ⇒ 后端改字段名 = 前端整个能力位静默失效（fail-closed
//	⇒ 表现为「那一席永远不出现」）。
//
// ⇒ 这里把 JSON 键**钉死**，让改名这件事在 Go 侧就先红。
func TestCapabilitiesJSONFieldNamesAreStable(t *testing.T) {
	mux := http.NewServeMux()
	capabilitiesTestHandler(false).RegisterCapabilityRoutes(mux)

	tok, _, err := SignToken(7, "tenant-a", "sa", "super_admin", capabilitiesTestSecret, false)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if _, ok := raw["cost_reconciliation"]; !ok {
		t.Fatalf("响应缺少 `cost_reconciliation` 键，实际键集 = %v", raw)
	}
	if len(raw) != 1 {
		t.Fatalf("能力位响应应只有 1 个键，实际 %d 个 = %v", len(raw), raw)
	}
}

// TestCapabilitiesEndpointRejectsWriteMethods ★★★★
//
// ★ 2026-10-22 12h 审计轮：能力位是**只读**端点，但旧注册形态
// `mux.HandleFunc("/api/admin/capabilities", …)` 对**任何**方法都注册
// ⇒ POST / PUT / DELETE 也会进同一个 handler ⇒ 返回 **200 + 能力位 JSON**
// ⇒ 调用方会以为「写成功了」。这正是「只读端点被写方法打中也报成功」这一族。
//
// ★ 修法：注册改成方法限定模式 `"GET /api/admin/capabilities"`
// （Go 1.22+ ServeMux）⇒ 写方法得到 405。
//
// ★ 本门的作用：**把这条修法钉住**。改回无方法限定的注册形态会静默回归，
// 而「能通过鉴权的写请求」在旧测试集里一条都没有 ⇒ 没有这一条，
// 回归不会被任何人发现。
func TestCapabilitiesEndpointRejectsWriteMethods(t *testing.T) {
	mux := http.NewServeMux()
	capabilitiesTestHandler(true).RegisterCapabilityRoutes(mux)

	tok, _, err := SignToken(42, "tenant-a", "ta", "tenant_admin", capabilitiesTestSecret, false)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/admin/capabilities", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK {
			t.Fatalf("%s /api/admin/capabilities 应被拒绝，实际 200（body=%s）——"+
				"只读端点被写方法打中也报成功", method, rec.Body.String())
		}
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/admin/capabilities 应 405，实际 %d（body=%s）",
				method, rec.Code, rec.Body.String())
		}
	}
}
