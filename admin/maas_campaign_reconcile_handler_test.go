package admin

// F-2 延伸（2026-11-04 审计修复轮）：campaign-reconciliation 端点的判据。
//
// 覆盖面（无 DB 依赖 —— 钳位/组装是纯函数，鉴权走真中间件 + 铸签 JWT）：
//   ① page / page_size 钳位（切片分页：page 下界 1、上界护栏、
//      page_size 越界回默认 —— 与 QueryMyRequests 同族口径）；
//   ② balanced 字段判 **全量** 而不是页内（看板红绿不随翻页变）；
//   ③ 路由注册在案（pattern 非空）；
//   ④ 鉴权档位 super-only：无 token 401、tenant_admin 403（superAdmin
//      中间件原文「super_admin role required」）、super_admin 放行进
//      handler（nil db ⇒ 503，证明到达的是 handler 自己的服务守卫）；
//   ⑤ GET-only：POST ⇒ 405。
//
// ★ 200 路径（真库返回 items）不在本文件覆盖 —— 需要真 pgxpool；
//   SQL 侧口径由 maas 包既有 7 条测试钉住（本文件零改 SQL）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func campaignReconcileTestHandler() (*Handler, *http.ServeMux) {
	h := &Handler{db: nil, secret: "test-secret"}
	mux := http.NewServeMux()
	h.registerMaasCampaignReconcileRoutes(mux)
	return h, mux
}

func campaignReconcileToken(t *testing.T, role string) string {
	t.Helper()
	tok, _, err := SignToken(42, "default", "tester", role, "test-secret", false)
	if err != nil {
		t.Fatalf("SignToken(%s): %v", role, err)
	}
	return tok
}

// 1) 钳位：页码与页宽的越界都收敛，切片内容与 total 保持一致。
func TestPaginateCampaignReconciliationClamps(t *testing.T) {
	recs := make([]maas.CampaignReconciliation, 0, 120)
	for i := 1; i <= 120; i++ {
		recs = append(recs, maas.CampaignReconciliation{CampaignID: int64(i)})
	}

	cases := []struct {
		name            string
		page, pageSize  int
		wantPage        int
		wantPageSize    int
		wantItems       int
		wantFirstItemID int64
	}{
		{"defaults", 0, 0, 1, campaignReconcileDefaultPageSize, 50, 1},
		{"negative page clamps to 1", -5, 20, 1, 20, 20, 1},
		{"huge page clamps to bound", 1 << 20, 20, campaignReconcileMaxPage, 20, 0, 0},
		{"page_size over max falls back to default", 1, 9999, 1, campaignReconcileDefaultPageSize, 50, 1},
		{"page_size zero falls back to default", 1, 0, 1, campaignReconcileDefaultPageSize, 50, 1},
		{"passthrough middle page", 2, 100, 2, 100, 20, 101},
		{"page beyond data keeps total", 3, 100, 3, 100, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, total, page, pageSize := paginateCampaignReconciliation(recs, tc.page, tc.pageSize)
			if page != tc.wantPage || pageSize != tc.wantPageSize {
				t.Fatalf("clamped page/size = %d/%d, want %d/%d", page, pageSize, tc.wantPage, tc.wantPageSize)
			}
			if total != 120 {
				t.Fatalf("total = %d, want 120", total)
			}
			if len(items) != tc.wantItems {
				t.Fatalf("items = %d, want %d", len(items), tc.wantItems)
			}
			if tc.wantItems > 0 && items[0].CampaignID != tc.wantFirstItemID {
				t.Fatalf("first item id = %d, want %d", items[0].CampaignID, tc.wantFirstItemID)
			}
		})
	}
}

// 2) balanced 判全量：坏行落在第二页 ⇒ 第一页看着全平，balanced 仍必须 false。
func TestCampaignReconciliationResponseBalancedOverFullSet(t *testing.T) {
	match := maas.CampaignReconciliation{CampaignID: 1, RedemptionCredits: 100, LedgerCredits: 100, GrantedCredits: 100, Matched: true}
	broken := maas.CampaignReconciliation{CampaignID: 2, RedemptionCredits: 50, LedgerCredits: 30, GrantedCredits: 50, Matched: false}

	resp := campaignReconciliationResponse([]maas.CampaignReconciliation{match, broken}, 1, 1)
	if resp["balanced"] != false {
		t.Fatal("one mismatched campaign (even on a later page) must make balanced=false")
	}
	if resp["total"] != 2 {
		t.Fatalf("total = %v, want 2", resp["total"])
	}
	if n := len(resp["items"].([]maas.CampaignReconciliation)); n != 1 {
		t.Fatalf("page_size=1 ⇒ items = %d, want 1", n)
	}

	ok := campaignReconciliationResponse([]maas.CampaignReconciliation{match}, 1, 50)
	if ok["balanced"] != true {
		t.Fatal("all matched ⇒ balanced=true")
	}

	empty := campaignReconciliationResponse([]maas.CampaignReconciliation{}, 0, 0)
	if empty["balanced"] != true || empty["total"] != 0 {
		t.Fatalf("empty set ⇒ balanced=true/total=0, got %v/%v", empty["balanced"], empty["total"])
	}
	if _, ok := empty["page_size"]; !ok {
		t.Fatal("response must carry page_size (dashboard echoes the clamped shape)")
	}
}

// 3) 路由注册在案：GET 能在 mux 上命中 pattern。
func TestCampaignReconciliationRouteRegistered(t *testing.T) {
	_, mux := campaignReconcileTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/maas/campaign-reconciliation", nil)
	if _, pattern := mux.Handler(req); pattern == "" {
		t.Fatal("GET /api/admin/maas/campaign-reconciliation not registered")
	}
}

//  4. 鉴权档位：无 token 401；tenant_admin（和普通 user）403 —— 对账是
//     平台级数据，与同族 /api/admin/maas/bills/reconcile 同档 super-only。
func TestCampaignReconciliationSuperOnly(t *testing.T) {
	_, mux := campaignReconcileTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/maas/campaign-reconciliation", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rr.Code)
	}

	for _, role := range []string{"tenant_admin", "user"} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/maas/campaign-reconciliation", nil)
		req.Header.Set("Authorization", "Bearer "+campaignReconcileToken(t, role))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("role=%s: got %d, want 403", role, rr.Code)
		}
		var body struct {
			Error struct {
				Detail string `json:"detail"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("role=%s: body not the writeError shape: %v (%s)", role, err, rr.Body.String())
		}
		if body.Error.Detail != "super_admin role required for this endpoint" {
			t.Fatalf("role=%s: 403 detail = %q", role, body.Error.Detail)
		}
	}
}

//  5. super_admin 放行进 handler：nil db ⇒ handler 自己的服务守卫 503
//     （证明中间件没有把 super_admin 拦在门外）。
func TestCampaignReconciliationSuperAdminReachesHandler(t *testing.T) {
	_, mux := campaignReconcileTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/maas/campaign-reconciliation", nil)
	req.Header.Set("Authorization", "Bearer "+campaignReconcileToken(t, "super_admin"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("super_admin + nil db: got %d, want 503 (middleware must pass, handler 503s)", rr.Code)
	}
}

// 6) GET-only（只读端点）：POST ⇒ 405。
func TestCampaignReconciliationRejectsNonGet(t *testing.T) {
	_, mux := campaignReconcileTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/maas/campaign-reconciliation", nil)
	req.Header.Set("Authorization", "Bearer "+campaignReconcileToken(t, "super_admin"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: got %d, want 405", rr.Code)
	}
}
