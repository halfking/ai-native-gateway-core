package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1039_egress_release_admin_test.go — 2026-10-20 egress 放行表**管理面入口**的判据。
//
// ## 为什么需要这道门
//
// owner 口径：「在被阻断时，需要给客户端提示，**由用户来选择是否执行**。」
// 客户端提示那一半已落地；「由用户选择」需要运维**够得着**放行入口。
// 放行表本体只有程序内的 Add/Remove，没有任何 HTTP 入口 ⇒ 运维够不着。
//
// ## 判据纪律
//
//   - 打**真实的 handler**（httptest + 真实 AuthContext），不是打 helper；
//   - 每条「应当拒绝」的用例都要有**对照**用例证明同一条请求在**正确输入**下
//     是通的 —— 否则「全都 403」也会让拒绝类判据全绿；
//   - 放行表是**进程级全局**的 ⇒ 每个用例前后都要**清干净**，否则用例之间互相污染。

// authCtxFor 造一个最小可用的 AuthContext。
func authCtxFor(role, tenantID, username string) *AuthContext {
	return &AuthContext{Role: role, TenantID: tenantID, Username: username}
}

// TestR1039EgressReleaseAdminGrantAndRevoke 钉住**写入路径**。
//
// 覆盖：超管能授予、能在表里看到、能撤销。
func TestR1039EgressReleaseAdminGrantAndRevoke(t *testing.T) {
	sanitizerules.SetEgressReleases(nil)
	defer sanitizerules.SetEgressReleases(nil)

	h := &Handler{}
	auth := authCtxFor("super_admin", "", "root")

	body := []byte(`{"tenant_id":"acme","scope":"egress","rule":"builtin.egress_sink","approved_by":"root"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/egress-releases", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, auth)
	rec := httptest.NewRecorder()
	h.handleEgressReleases(rec, req, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("授予放行应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "") {
		t.Fatal("授予后放行表必须真的生效（不能只回了个 201）")
	}
	// 别的租户不放行（边界不能漏）。
	if sanitizerules.EgressReleaseAllowed("other", "egress", "builtin.egress_sink", "") {
		t.Fatal("放行必须精确到租户：别的租户不得被放行")
	}
	// ⚠ 必须能区分「**精确**匹配」与「**前缀**匹配」：
	//   若实现改成 strings.HasPrefix(记录租户, 请求租户)，
	//   那么授给 "acme-eu" 的放行会**顺带**放行 "acme" —— 那是**扩大**了放行面。
	//   两个毫无前缀关系的租户名（acme / other）区分不出这两种实现。
	//
	//   ⚠ 必须用**另一条规则**做这个探针：上面已经给 "acme" 授过
	//   builtin.egress_sink，若沿用同一条规则，"acme" 本来就该被放行，
	//   断言会因「命中 acme 自己那条」而恒真 ⇒ 测不出前缀匹配。
	if err := sanitizerules.AddEgressRelease(sanitizerules.EgressRelease{
		TenantID: "acme-eu", Scope: "egress", Rule: "builtin.prefix_probe", ApprovedBy: "root",
	}); err != nil {
		t.Fatalf("seed prefix tenant release: %v", err)
	}
	if sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.prefix_probe", "") {
		t.Fatal("租户匹配必须是**精确**匹配：授给 acme-eu 的放行不得放行 acme")
	}
	if !sanitizerules.EgressReleaseAllowed("acme-eu", "egress", "builtin.prefix_probe", "") {
		t.Fatal("acme-eu 自己的放行必须仍然生效")
	}
	if !sanitizerules.RemoveEgressRelease("acme-eu", "egress", "builtin.prefix_probe", "") {
		t.Fatal("移除 acme-eu 的放行应当命中")
	}

	// 读回来。
	req = httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/egress-releases", nil)
	req = SetAuthContext(req, auth)
	rec = httptest.NewRecorder()
	h.handleEgressReleases(rec, req, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("读放行表应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Releases []sanitizerules.EgressRelease `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Releases) != 1 || got.Releases[0].TenantID != "acme" {
		t.Fatalf("读回放行表内容不对：%+v", got.Releases)
	}

	// 撤销。
	req = httptest.NewRequest(http.MethodDelete,
		"/api/admin/sanitize-rules/egress-releases?tenant_id=acme&scope=egress&rule=builtin.egress_sink", nil)
	req = SetAuthContext(req, auth)
	rec = httptest.NewRecorder()
	h.handleEgressReleases(rec, req, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("撤销放行应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "") {
		t.Fatal("撤销后不得再放行")
	}
}

// TestR1039EgressReleaseAdminRejectsBadInput 钉住**输入校验**。
//
// 这组全是「必须被拒」的用例 ⇒ 必须同时有对照（上一条已证明正确输入是通的）。
func TestR1039EgressReleaseAdminRejectsBadInput(t *testing.T) {
	sanitizerules.SetEgressReleases(nil)
	defer sanitizerules.SetEgressReleases(nil)

	h := &Handler{}
	auth := authCtxFor("super_admin", "", "root")

	cases := []struct {
		name string
		body string
	}{
		{"空租户（= 全网放行）", `{"tenant_id":"","scope":"egress","rule":"builtin.egress_sink"}`},
		{"缺租户", `{"scope":"egress","rule":"builtin.egress_sink"}`},
		{"不支持的 scope", `{"tenant_id":"acme","scope":"input","rule":"builtin.egress_sink"}`},
		{"缺规则", `{"tenant_id":"acme","scope":"egress","rule":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/egress-releases", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			// ⚠ 必须把 AuthContext 放进请求上下文：角色判定走的是请求上下文，
			//   没放就是「无身份」⇒ 403，而不是本用例要验的 400（输入非法）。
			req = SetAuthContext(req, auth)
			rec := httptest.NewRecorder()
			h.handleEgressReleases(rec, req, auth)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("必须被拒（400），实际 HTTP %d：%s", rec.Code, rec.Body.String())
			}
			if len(sanitizerules.EgressReleases()) != 0 {
				t.Fatal("被拒的请求不得留下放行记录")
			}
		})
	}
}

// TestR1039EgressReleaseAdminRequiresSuperAdminForWrite 钉住**角色边界**：
// 租户管理员可以**读**自己租户的，但不能**写**。
func TestR1039EgressReleaseAdminRequiresSuperAdminForWrite(t *testing.T) {
	sanitizerules.SetEgressReleases(nil)
	defer sanitizerules.SetEgressReleases(nil)

	// 先由超管给 acme 授一条。
	if err := sanitizerules.AddEgressRelease(sanitizerules.EgressRelease{
		TenantID: "acme", Scope: "egress", Rule: "builtin.egress_sink", ApprovedBy: "root",
	}); err != nil {
		t.Fatalf("seed release: %v", err)
	}
	// 再给 other 授一条（租户管理员不该看见）。
	if err := sanitizerules.AddEgressRelease(sanitizerules.EgressRelease{
		TenantID: "other", Scope: "egress", Rule: "builtin.egress_sink", ApprovedBy: "root",
	}); err != nil {
		t.Fatalf("seed release 2: %v", err)
	}

	h := &Handler{}
	tenantAuth := authCtxFor("tenant_admin", "acme", "acme-admin")

	// 读：只看得到自己租户的。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sanitize-rules/egress-releases", nil)
	rec := httptest.NewRecorder()
	h.handleEgressReleases(rec, req, tenantAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("租户管理员读应当被允许（200），实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Releases []sanitizerules.EgressRelease `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Releases) != 1 || got.Releases[0].TenantID != "acme" {
		t.Fatalf("租户管理员只应看得到自己租户的放行，实际 %+v", got.Releases)
	}

	// 写：必须被拒。
	body := []byte(`{"tenant_id":"acme","scope":"egress","rule":"builtin.other"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/admin/sanitize-rules/egress-releases", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	h.handleEgressReleases(rec, req, tenantAuth)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员授予放行必须被拒（403），实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.other", "") {
		t.Fatal("被拒的授予不得生效")
	}

	// 撤：必须被拒。
	req = httptest.NewRequest(http.MethodDelete,
		"/api/admin/sanitize-rules/egress-releases?tenant_id=acme&scope=egress&rule=builtin.egress_sink", nil)
	rec = httptest.NewRecorder()
	h.handleEgressReleases(rec, req, tenantAuth)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员撤销放行必须被拒（403），实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !sanitizerules.EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "") {
		t.Fatal("被拒的撤销不得生效")
	}
}
