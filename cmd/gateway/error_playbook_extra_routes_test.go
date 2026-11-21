package main

// error_playbook_extra_routes_test.go — call_admin_api 闭集扩容的钉测（任务 3）。
//
// ★ 锁四层契约：
//   ① 依赖齐备 ⇒ 4 条路由进闭集（HasRoute 归一化期 fail-fast 的正样本）
//   ② 依赖缺项 ⇒ 对应路由**不进**闭集（诚实语义：不宣传不可执行的动作）
//   ③ 每条路由的 body 闸（缺参/坏参 ⇒ ErrPlanInvalid ⇒ 422 门控原文）
//   ④ 变更类判定：三条 POST 路由的方案 Mutating()=true（0.80 置信度门覆盖）

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/errorplaybook"
	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

func fullDeps() PlaybookDeps {
	return PlaybookDeps{
		ForceRecoverCredential: func(ctx context.Context, credID int64) ([]string, error) {
			return []string{"fake-recover-step"}, nil
		},
		IndexRefresh:    &fakeRefresher{},
		CredibilityScan: &fakeCredibilityScanner{},
	}
}

type fakeRefresher struct{ called int }

func (f *fakeRefresher) RefreshOnce(ctx context.Context) error { f.called++; return nil }

type fakeCredibilityScanner struct {
	nodeCalled bool
	allCalled  bool
}

func (f *fakeCredibilityScanner) ScanNode(ctx context.Context, credentialID int64, rawModelName string) (*modelcredibility.Result, error) {
	f.nodeCalled = true
	return &modelcredibility.Result{Overall: 0.9, Status: "success"}, nil
}

func (f *fakeCredibilityScanner) ScanAll(ctx context.Context) (int, error) {
	f.allCalled = true
	return 25, nil
}

func hasRoute(t *testing.T, actions errorplaybook.ActionRegistry, method, path string) bool {
	t.Helper()
	rc, ok := actions.(errorplaybook.RouteChecker)
	if !ok {
		t.Fatal("registry does not implement RouteChecker")
	}
	return rc.HasRoute(method, path)
}

func execRoute(t *testing.T, actions errorplaybook.ActionRegistry, method, path string, body string) (string, error) {
	t.Helper()
	params := map[string]interface{}{"method": method, "path": path}
	if body != "" {
		params["body"] = json.RawMessage(body)
	}
	_, result, err := actions.Execute(context.Background(), errorplaybook.ActionCallAdminAPI, params, nil)
	return result, err
}

func TestExtraRoutesClosedSet(t *testing.T) {
	actions := errorplaybook.NewBuiltinActions(nil, buildErrorPlaybookExtraRoutes(fullDeps()))
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/admin/pi-analysis/trigger"},
		{"POST", "/api/admin/credentials/force-recover"},
		{"POST", "/api/admin/auto-route/refresh"},
		{"POST", "/api/admin/model-credibility/trigger"},
	} {
		if !hasRoute(t, actions, tc.method, tc.path) {
			t.Fatalf("route %s %s should be registered", tc.method, tc.path)
		}
		// 归一化期 fail-fast 正样本：api 方案引用在册路由 ⇒ 通过
		raw, _ := json.Marshal(map[string]string{"method": tc.method, "path": tc.path})
		if _, err := errorplaybook.NormalizePlan(errorplaybook.PlanKindAPI, raw, actions); err != nil {
			t.Fatalf("normalize api plan for %s %s: %v", tc.method, tc.path, err)
		}
	}
	// 未登记路由 ⇒ 归一化期拒绝（fail-fast 负样本）
	raw, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/admin/not-a-route"})
	if _, err := errorplaybook.NormalizePlan(errorplaybook.PlanKindAPI, raw, actions); err == nil {
		t.Fatal("unregistered route must be rejected at normalization time")
	}
}

func TestExtraRoutesOmittedWhenDepsNil(t *testing.T) {
	actions := errorplaybook.NewBuiltinActions(nil, buildErrorPlaybookExtraRoutes(PlaybookDeps{}))
	for _, p := range []string{
		"/api/admin/credentials/force-recover",
		"/api/admin/auto-route/refresh",
		"/api/admin/model-credibility/trigger",
	} {
		if hasRoute(t, actions, "POST", p) {
			t.Fatalf("route %s must NOT be registered when its dep is nil", p)
		}
	}
}

func TestExtraRoutesBodyGates(t *testing.T) {
	actions := errorplaybook.NewBuiltinActions(nil, buildErrorPlaybookExtraRoutes(fullDeps()))

	// force-recover：缺 credential_id ⇒ 业务性失败（422 语义）
	if _, err := execRoute(t, actions, "POST", "/api/admin/credentials/force-recover", "{}"); err == nil {
		t.Fatal("force-recover without credential_id must fail")
	} else {
		var inv *errorplaybook.ErrPlanInvalid
		if !errors.As(err, &inv) {
			t.Fatalf("missing-param failure must be business-plan-invalid, got %T", err)
		}
	}
	// force-recover：合法 body ⇒ 执行成功且结果含步骤摘要
	res, err := execRoute(t, actions, "POST", "/api/admin/credentials/force-recover", `{"credential_id":42}`)
	if err != nil || res == "" {
		t.Fatalf("force-recover valid body: res=%q err=%v", res, err)
	}

	// credibility：缺参 ⇒ 业务性失败；all=true ⇒ ScanAll；单点 ⇒ ScanNode
	if _, err := execRoute(t, actions, "POST", "/api/admin/model-credibility/trigger", "{}"); err == nil {
		t.Fatal("credibility trigger without params must fail")
	}
	scanner := fakeCredibilityScanner{}
	actions2 := errorplaybook.NewBuiltinActions(nil, buildErrorPlaybookExtraRoutes(PlaybookDeps{CredibilityScan: &scanner}))
	if _, err := execRoute(t, actions2, "POST", "/api/admin/model-credibility/trigger", `{"all":true}`); err != nil {
		t.Fatalf("credibility scan-all: %v", err)
	}
	if !scanner.allCalled || scanner.nodeCalled {
		t.Fatalf("all=true should hit ScanAll (allCalled=%v nodeCalled=%v)", scanner.allCalled, scanner.nodeCalled)
	}
	if _, err := execRoute(t, actions2, "POST", "/api/admin/model-credibility/trigger", `{"credential_id":1,"raw_model_name":"m"}`); err != nil {
		t.Fatalf("credibility scan-node: %v", err)
	}
	if !scanner.nodeCalled {
		t.Fatal("node form should hit ScanNode")
	}
}

func TestExtraRoutesAreMutating(t *testing.T) {
	// 三条 POST 处置路由都必须落在「变更类」⇒ PI 自动执行过 0.80 置信度门。
	actions := errorplaybook.NewBuiltinActions(nil, buildErrorPlaybookExtraRoutes(fullDeps()))
	for _, p := range []string{
		"/api/admin/credentials/force-recover",
		"/api/admin/auto-route/refresh",
		"/api/admin/model-credibility/trigger",
	} {
		raw, _ := json.Marshal(map[string]string{"method": "POST", "path": p})
		plan, err := errorplaybook.NormalizePlan(errorplaybook.PlanKindAPI, raw, actions)
		if err != nil {
			t.Fatalf("normalize %s: %v", p, err)
		}
		if !plan.Mutating() {
			t.Fatalf("POST route %s must be classified as mutating (confidence gate)", p)
		}
	}
}
