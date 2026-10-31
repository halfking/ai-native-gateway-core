package admin

// 输出合规总开关 + 拦截审核工作台的管理面判据（2026-10-29 输出合规误报轮）。
//
// ## 这道门钉什么
//
// ① **开关**：只有超管能读写；读数必须带「来源」与「语义」（运维要看得懂
//    关掉之后**具体**关掉了什么，不能猜）。
// ② **标注**：只有超管能打；闭集外必须被拒；标注**不改**放行状态。
// ③ **读数**：租户管理员读到的必须是**自己租户**的（越权读到别的租户就是
//    租户边界破了）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// registerOutputCheckSpec 把输出合规总开关的设置登记进 registry
// （管理面读数依赖 settings.Global 的 spec）。
func registerOutputCheckSpec(t *testing.T) {
	t.Helper()
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })
	settings.Global = settings.NewRegistry()
	for _, spec := range settings.ModuleSpecs() {
		if err := settings.Global.RegisterSpec(spec); err != nil {
			t.Fatalf("register spec %s: %v", spec.Key, err)
		}
	}
}

// TestOutputCheckSwitchRequiresSuperAdmin 钉住权限边界。
func TestOutputCheckSwitchRequiresSuperAdmin(t *testing.T) {
	registerOutputCheckSpec(t)
	h := &Handler{}
	tenantAuth := authCtxFor("tenant_admin", "acme", "acme-admin")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/output-check", nil)
	req = SetAuthContext(req, tenantAuth)
	rec := httptest.NewRecorder()
	h.handleOutputCheckSwitch(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员读总开关应被拒，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestOutputCheckSwitchReadReportsSourceAndSemantics 钉住读数的可解释性。
//
// ★ 「来源」必须给出来：运维要知道这个值是默认、库里存的、还是刚被改过的
//
//	—— 三者的可动性不同（默认的要写库才持久）。
func TestOutputCheckSwitchReadReportsSourceAndSemantics(t *testing.T) {
	registerOutputCheckSpec(t)
	h := &Handler{}
	superAuth := authCtxFor("super_admin", "", "root")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/output-check", nil)
	req = SetAuthContext(req, superAuth)
	rec := httptest.NewRecorder()
	h.handleOutputCheckSwitch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("读总开关失败: %d %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	for _, key := range []string{"enabled", "source", "semantics", "key"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("读数缺少 %s: %v", key, payload)
		}
	}
	if payload["key"] != outputCheckSettingKey {
		t.Errorf("读数的键必须与设置键一致: %v", payload["key"])
	}
	if semantics, _ := payload["semantics"].(string); semantics == "" {
		t.Error("读数必须带语义说明（运维不该靠猜）")
	}
}

// TestOutputCheckSwitchRejectsBadPayload 钉住入参校验。
func TestOutputCheckSwitchRejectsBadPayload(t *testing.T) {
	registerOutputCheckSpec(t)
	h := &Handler{}
	superAuth := authCtxFor("super_admin", "", "root")

	// 缺 enabled ⇒ 400（不能默默按 false 处理 —— 那是「关掉检查」）。
	req := httptest.NewRequest(http.MethodPut, "/api/admin/sanitize-rules/output-check",
		bytes.NewReader([]byte(`{"comment":"x"}`)))
	req = SetAuthContext(req, superAuth)
	rec := httptest.NewRecorder()
	h.handleOutputCheckSwitch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 enabled 应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBlockReviewLabelEndpointRequiresSuperAdmin 钉住标注的权限边界。
func TestBlockReviewLabelEndpointRequiresSuperAdmin(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(sanitizerules.ResetBlockReviews)
	h := &Handler{}
	tenantAuth := authCtxFor("tenant_admin", "acme", "acme-admin")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/blocked-reviews/1/label",
		bytes.NewReader([]byte(`{"label":"false_positive"}`)))
	req = SetAuthContext(req, tenantAuth)
	rec := httptest.NewRecorder()
	h.handleBlockReviewLabel(rec, req, tenantAuth)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员标注应被拒，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBlockReviewLabelEndpointRejectsUnknownLabel 钉住标注闭集。
func TestBlockReviewLabelEndpointRejectsUnknownLabel(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(sanitizerules.ResetBlockReviews)
	h := &Handler{}
	superAuth := authCtxFor("super_admin", "", "root")
	entry := sanitizerules.AddBlockReview(sanitizerules.BlockReviewInput{
		TenantID: "acme", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-label-admin",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/sanitize-rules/blocked-reviews/"+strconv.FormatInt(entry.ID, 10)+"/label",
		bytes.NewReader([]byte(`{"label":"随便写"}`)))
	req = SetAuthContext(req, superAuth)
	rec := httptest.NewRecorder()
	h.handleBlockReviewLabel(rec, req, superAuth)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("闭集外的标注应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBlockReviewLabelEndpointRecordsLabel 钉住标注落库（读数能查到）。
func TestBlockReviewLabelEndpointRecordsLabel(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(sanitizerules.ResetBlockReviews)
	h := &Handler{}
	superAuth := authCtxFor("super_admin", "", "root")
	entry := sanitizerules.AddBlockReview(sanitizerules.BlockReviewInput{
		TenantID: "acme", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-label-admin-ok",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/sanitize-rules/blocked-reviews/"+strconv.FormatInt(entry.ID, 10)+"/label",
		bytes.NewReader([]byte(`{"label":"rule_too_broad","comment":"判据太宽"}`)))
	req = SetAuthContext(req, superAuth)
	rec := httptest.NewRecorder()
	h.handleBlockReviewLabel(rec, req, superAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("标注失败: %d %s", rec.Code, rec.Body.String())
	}
	rows := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "acme", 10)
	found := false
	for _, row := range rows {
		if row.ID == entry.ID && row.Label == sanitizerules.BlockReviewLabelRuleTooBroad {
			found = true
		}
	}
	if !found {
		t.Fatalf("标注没有落库: %+v", rows)
	}
}

// TestBlockReviewInsightsEndpointScopesToTenant 钉住租户边界：
// 租户管理员读到的必须是**自己租户**的读数。
func TestBlockReviewInsightsEndpointScopesToTenant(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(sanitizerules.ResetBlockReviews)
	for _, tenant := range []string{"acme", "other"} {
		sanitizerules.AddBlockReview(sanitizerules.BlockReviewInput{
			TenantID: tenant, ClientType: "zcode", Scope: "egress",
			Rule: "builtin.egress_sink", RequestID: "req-insight-" + tenant,
		})
	}
	h := &Handler{}
	tenantAuth := authCtxFor("tenant_admin", "acme", "acme-admin")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/blocked-reviews/insights", nil)
	req = SetAuthContext(req, tenantAuth)
	rec := httptest.NewRecorder()
	h.handleBlockReviewInsights(rec, req, tenantAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("读数失败: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Insights []struct {
			TenantID string `json:"tenant_id"`
		} `json:"insights"`
		AutoRelaxThreshold int `json:"auto_relax_threshold"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if len(payload.Insights) == 0 {
		t.Fatal("读数为空")
	}
	for _, insight := range payload.Insights {
		if insight.TenantID != "acme" {
			t.Fatalf("租户管理员读到了别的租户的读数: %+v", payload.Insights)
		}
	}
	// 自动优化的阈值必须随读数一起给出（运维看得懂「为什么还没自动放行」）。
	if payload.AutoRelaxThreshold < 0 {
		t.Fatalf("自动放行阈值缺失: %+v", payload)
	}
}
