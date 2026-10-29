package admin

// sanitize_rules_hardblock_test.go — 2026-10-28 拦截反馈明确化轮的判据。
//
// 覆盖两块新入口：
//  1. 硬阻断清单的运行时读/写（「后台有个开关一次性关掉」）。
//  2. 拦截审核队列的列表与批准/拒绝（「后台有个列表，需要审核，审核完成后
//     可以返回给前端」——批准 ⇒ 放行记录 ⇒ 用户重试即返回）。
//
// 判据纪律（沿用 r1039_egress_release_admin_test.go）：
//   - 打真实 handler（httptest + 真实 AuthContext）；
//   - 每个「应当拒绝」用例都有对照用例证明正确输入是通的；
//   - 进程级全局状态（清单/队列/放行表）每用例前后清干净。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func resetHardblockForTest(t *testing.T) []sanitizerules.HardBlockAllow {
	t.Helper()
	prev := sanitizerules.HardBlockAllows()
	sanitizerules.SetHardBlockAllows([]sanitizerules.HardBlockAllow{{Scope: "input", Rule: "*"}})
	t.Cleanup(func() { sanitizerules.SetHardBlockAllows(prev) })
	return prev
}

// TestHardblockRuntimeSwitchGetAndPut：开关读写闭环——PUT 立即生效，
// 清单是「整表替换」语义。
func TestHardblockRuntimeSwitchGetAndPut(t *testing.T) {
	resetHardblockForTest(t)
	h := &Handler{}
	auth := authCtxFor("super_admin", "", "root")

	// 对照前提：出厂默认（input:*）下 output 不允许硬阻断。
	if sanitizerules.HardBlockAllowed("output", "pii.email") {
		t.Fatal("前提失效：默认清单不应允许 output 硬阻断")
	}

	body := []byte(`{"rules":["input:*","egress:builtin.egress_sink"],"comment":"open sink guard"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/sanitize-rules/hardblock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, auth)
	rec := httptest.NewRecorder()
	h.handleHardBlockRules(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !sanitizerules.HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Fatal("PUT 后清单必须立即生效（不能只回了个 200）")
	}
	if sanitizerules.HardBlockAllowed("output", "pii.email") {
		t.Fatal("整表替换语义：没写的 scope 不得残留旧条目")
	}

	// 读回。
	req = httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/hardblock", nil)
	req = SetAuthContext(req, auth)
	rec = httptest.NewRecorder()
	h.handleHardBlockRules(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应当成功，实际 HTTP %d", rec.Code)
	}
	var got struct {
		Rules []sanitizerules.HardBlockAllow `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("读回清单条目数 = %d，期望 2：%+v", len(got.Rules), got.Rules)
	}

	// 「一次性关掉」：显式确认后清空（含请求侧底线）。
	body = []byte(`{"rules":[],"confirm_disable_all":true}`)
	req = httptest.NewRequest(http.MethodPut, "/api/admin/sanitize-rules/hardblock", bytes.NewReader(body))
	req = SetAuthContext(req, auth)
	rec = httptest.NewRecorder()
	h.handleHardBlockRules(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("显式清空应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if sanitizerules.HardBlockAllowed("input", "anything") || sanitizerules.HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Fatal("清空后任何 scope 都不得允许硬阻断")
	}
}

// TestHardblockRuntimeSwitchRejectsDangerousWrites：危险/非法写入必须被拒，
// 且不留下半套清单。
func TestHardblockRuntimeSwitchRejectsDangerousWrites(t *testing.T) {
	resetHardblockForTest(t)
	h := &Handler{}
	auth := authCtxFor("super_admin", "", "root")

	cases := []struct {
		name string
		body string
	}{
		{"空清单未确认（会静默关掉请求侧底线）", `{"rules":[]}`},
		{"egress 通配（r1039 禁止：放大误报面）", `{"rules":["egress:*"]}`},
		{"egress 前缀族（r1039 禁止）", `{"rules":["egress:secret.*"]}`},
		{"未知 scope", `{"rules":["typo:rule"]}`},
		{"非法格式（缺冒号）", `{"rules":["egress_sink"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/admin/sanitize-rules/hardblock", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			req = SetAuthContext(req, auth)
			rec := httptest.NewRecorder()
			h.handleHardBlockRules(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("必须被拒（400），实际 HTTP %d：%s", rec.Code, rec.Body.String())
			}
		})
	}
	// 被拒的写入不得碰清单：仍是出厂默认。
	if !sanitizerules.HardBlockAllowed("input", "any") {
		t.Fatal("被拒的写入不得改变清单")
	}
}

// TestHardblockRuntimeSwitchRequiresSuperAdmin：租户管理员读写都拒。
func TestHardblockRuntimeSwitchRequiresSuperAdmin(t *testing.T) {
	resetHardblockForTest(t)
	h := &Handler{}
	tenantAuth := authCtxFor("tenant_admin", "acme", "acme-admin")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/hardblock", nil)
	req = SetAuthContext(req, tenantAuth)
	rec := httptest.NewRecorder()
	h.handleHardBlockRules(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员读清单必须被拒（403），实际 HTTP %d", rec.Code)
	}

	body := []byte(`{"rules":["egress:builtin.egress_sink"]}`)
	req = httptest.NewRequest(http.MethodPut, "/api/admin/sanitize-rules/hardblock", bytes.NewReader(body))
	req = SetAuthContext(req, tenantAuth)
	rec = httptest.NewRecorder()
	h.handleHardBlockRules(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员写清单必须被拒（403），实际 HTTP %d", rec.Code)
	}
	if sanitizerules.HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Fatal("被拒的写入不得生效")
	}
}

// TestBlockedReviewsAdminListAndDecide：审核列表 + 批准（⇒ 放行）+ 拒绝。
func TestBlockedReviewsAdminListAndDecide(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	defer sanitizerules.ResetBlockReviews()
	sanitizerules.SetEgressReleases(nil)
	defer sanitizerules.SetEgressReleases(nil)

	entry := sanitizerules.AddBlockReview(sanitizerules.BlockReviewInput{
		TenantID: "acme", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", Reason: "sink destination",
		Field: "$.command", Digest: "digest-1", RequestID: "req-100",
	})
	sanitizerules.AddBlockReview(sanitizerules.BlockReviewInput{
		TenantID: "other", ClientType: "claude-code", Scope: "egress",
		Rule: "builtin.secret_field.egress", Reason: "credential",
		Field: "$.password", Digest: "digest-2", RequestID: "req-101",
	})

	h := &Handler{}
	superAuth := authCtxFor("super_admin", "", "root")

	// 超管看全部 pending。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/blocked-reviews?status=pending", nil)
	req = SetAuthContext(req, superAuth)
	rec := httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, superAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Reviews []sanitizerules.BlockReview `json:"reviews"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Reviews) != 2 {
		t.Fatalf("pending 记录数 = %d，期望 2", len(list.Reviews))
	}

	// 批准 acme 那单：放行绑定到 digest-1。
	body := []byte(`{"decision":"approve","comment":"checked, benign"}`)
	req = httptest.NewRequest(http.MethodPost,
		"/api/admin/sanitize-rules/blocked-reviews/"+strconv.FormatInt(entry.ID, 10)+"/review", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, superAuth)
	rec = httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, superAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("批准应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "digest-1") {
		t.Fatal("批准后放行必须真的生效（不能只回了个 200）")
	}
	if sanitizerules.EgressReleaseAllowed("other", "egress", "builtin.secret_field.egress", "digest-2") {
		t.Fatal("别的单不受影响")
	}

	// 拒绝 other 那单：拦截维持。
	body = []byte(`{"decision":"reject"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/blocked-reviews/2/review", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, superAuth)
	rec = httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, superAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("拒绝应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if sanitizerules.EgressReleaseAllowed("other", "egress", "builtin.secret_field.egress", "digest-2") {
		t.Fatal("拒绝不得放行")
	}
}

// TestBlockedReviewsAdminRejectsBadInput：非法输入与角色边界。
func TestBlockedReviewsAdminRejectsBadInput(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	defer sanitizerules.ResetBlockReviews()
	sanitizerules.SetEgressReleases(nil)
	defer sanitizerules.SetEgressReleases(nil)

	entry := sanitizerules.AddBlockReview(sanitizerules.BlockReviewInput{
		TenantID: "acme", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", Reason: "sink destination", RequestID: "req-1",
	})

	h := &Handler{}
	superAuth := authCtxFor("super_admin", "", "root")
	tenantAuth := authCtxFor("tenant_admin", "acme", "acme-admin")
	otherTenantAuth := authCtxFor("tenant_admin", "other", "other-admin")

	// 非法 decision。
	body := []byte(`{"decision":"maybe"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/blocked-reviews/1/review", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, superAuth)
	rec := httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, superAuth)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 decision 必须被拒（400），实际 HTTP %d", rec.Code)
	}

	// 租户管理员可以读自己租户的队列（「为什么我被拦了」）。
	req = httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/blocked-reviews", nil)
	req = SetAuthContext(req, tenantAuth)
	rec = httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, tenantAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("租户管理员读自己队列应当被允许，实际 HTTP %d", rec.Code)
	}
	var list struct {
		Reviews []sanitizerules.BlockReview `json:"reviews"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Reviews) != 1 || list.Reviews[0].TenantID != "acme" {
		t.Fatalf("租户管理员只应看到自己租户的单：%+v", list.Reviews)
	}

	// 其它租户管理员看不到 acme 的单。
	req = httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/blocked-reviews", nil)
	req = SetAuthContext(req, otherTenantAuth)
	rec = httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, otherTenantAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("读应当成功，实际 HTTP %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Reviews) != 0 {
		t.Fatalf("其它租户不应看到 acme 的单：%+v", list.Reviews)
	}

	// 租户管理员不能决定。
	body = []byte(`{"decision":"approve"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/blocked-reviews/1/review", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, tenantAuth)
	rec = httptest.NewRecorder()
	h.handleBlockedReviews(rec, req, tenantAuth)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员决定必须被拒（403），实际 HTTP %d", rec.Code)
	}
	if sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "") {
		t.Fatal("被拒的批准不得生效")
	}
	_ = entry
}
