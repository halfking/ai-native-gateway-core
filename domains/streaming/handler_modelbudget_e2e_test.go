package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

// P1#4 Phase 2 —— 任务级预算(max_cost_per_task)场景进程内 e2e。
// 打的是 P1#3 真实接线面(serveWithExecutor 的 Budget-aware model selection 块):
// 与 key 级配额(CheckBudget→insufficient_quota,budget_failclosed_test.go)不同层,
// 这里钉的是 degrader 决策面:FitsBudget 透传 / Degraded 换梯内型号+body 回写 / 402。
//
// 预算数学(内置价目表 + estimateInputTokensFromMessages 最小保护 10 + output 固定 1000):
//   gpt-4         = 0.03*10/1000 + 0.06*1000/1000   = $0.0603
//   gpt-4-turbo   = 0.01*10/1000 + 0.03*1000/1000   = $0.0301
//   gpt-4o        = 0.005*10/1000 + 0.015*1000/1000 = $0.01505
//   gpt-4o-mini   = 0.00015*10/1000 + 0.0006*1000/1000 = $0.0006015  ← 梯内实际最便宜
//   gpt-3.5-turbo = 0.0005*10/1000 + 0.0015*1000/1000  = $0.001505
// 梯序 gpt-4→turbo→4o→4o-mini→3.5-turbo,首个 fit 即停 ⇒ 402 阈值 = budget < $0.0006015。
// ★ 本套 e2e 首跑抓出降级不回写 bodyBytes 的接线缺陷(2026-11-21 已根修)。
func runModelBudgetE2E(t *testing.T, body string) (*httptest.ResponseRecorder, *correlationCaptureExecutor) {
	t.Helper()
	capture := &correlationCaptureExecutor{}
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-mb", ApplicationID: 7}})
	h.provider = durableEndpointResolver{}
	h.executor = &executors.Executor{}
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	h.survivalAttemptExec = capture

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(rec, req)
	return rec, capture
}

// 上游实际收到的 model(从 ExecParams.BodyBytes 解出;未到达 executor 时返回 ok=false)。
func capturedUpstreamModel(t *testing.T, capture *correlationCaptureExecutor) (string, bool) {
	t.Helper()
	if capture.params == nil {
		return "", false
	}
	var upstream struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(capture.params.BodyBytes, &upstream); err != nil {
		t.Fatalf("unmarshal upstream body: %v; body=%s", err, capture.params.BodyBytes)
	}
	return upstream.Model, true
}

// TestModelBudgetE2E_AmpleBudgetPassthrough 场景 1:预算充足($1.0 ≥ $0.0603)
// → 闸门放行不降级,executor 收到的 model 与请求一致,无降级头。
func TestModelBudgetE2E_AmpleBudgetPassthrough(t *testing.T) {
	rec, capture := runModelBudgetE2E(t,
		`{"model":"gpt-4","max_cost_per_task":1.0,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code == http.StatusPaymentRequired {
		t.Fatalf("ample budget must not 402; body=%s", rec.Body.String())
	}
	if rec.Header().Get("X-Gw-Budget-Degraded") != "" {
		t.Fatalf("ample budget must not set degraded header: %q", rec.Header().Get("X-Gw-Budget-Degraded"))
	}
	if rec.Header().Get("X-Gw-Budget-Requested") != "gpt-4" {
		t.Fatalf("requested-observed header missing: %q", rec.Header().Get("X-Gw-Budget-Requested"))
	}
	model, ok := capturedUpstreamModel(t, capture)
	if !ok {
		t.Fatalf("executor not reached; status=%d body=%s", rec.Code, rec.Body.String())
	}
	if model != "gpt-4" {
		t.Fatalf("ample budget must keep requested model, got %q", model)
	}
}

// TestModelBudgetE2E_TightBudgetDegrades 场景 2:预算紧张($0.05 < gpt-4 $0.0603)
// → 首个 fit = gpt-4-turbo($0.0301);★ executor 收到的 body 必须是换后模型
// (降级回写 bodyBytes —— 本用例在根修前失败:上游仍收 gpt-4)。
func TestModelBudgetE2E_TightBudgetDegrades(t *testing.T) {
	rec, capture := runModelBudgetE2E(t,
		`{"model":"gpt-4","max_cost_per_task":0.05,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code == http.StatusPaymentRequired {
		t.Fatalf("degradable budget must not 402; body=%s", rec.Body.String())
	}
	if rec.Header().Get("X-Gw-Budget-Degraded") != "true" {
		t.Fatalf("degraded header missing; headers=%v", rec.Header())
	}
	if got := rec.Header().Get("X-Gw-Budget-Selected"); got != "gpt-4-turbo" {
		t.Fatalf("selected header = %q, want gpt-4-turbo", got)
	}
	model, ok := capturedUpstreamModel(t, capture)
	if !ok {
		t.Fatalf("executor not reached; status=%d body=%s", rec.Code, rec.Body.String())
	}
	if model == "gpt-4" {
		t.Fatal("tight budget must rewrite upstream body to degraded model (got original gpt-4)")
	}
	if model != "gpt-4-turbo" {
		t.Fatalf("degrade ladder first fit = gpt-4-turbo, got %q", model)
	}
}

// TestModelBudgetE2E_DeeperLadderStep 场景 2b:更紧预算($0.001)
// → 走过 turbo/4o 两步后首个 fit = gpt-4o-mini($0.0006015)。
func TestModelBudgetE2E_DeeperLadderStep(t *testing.T) {
	rec, capture := runModelBudgetE2E(t,
		`{"model":"gpt-4","max_cost_per_task":0.001,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code == http.StatusPaymentRequired {
		t.Fatalf("budget 0.001 ≥ gpt-4o-mini 0.0006015 must degrade not 402; body=%s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Gw-Budget-Selected"); got != "gpt-4o-mini" {
		t.Fatalf("selected header = %q, want gpt-4o-mini", got)
	}
	model, ok := capturedUpstreamModel(t, capture)
	if !ok {
		t.Fatalf("executor not reached; status=%d body=%s", rec.Code, rec.Body.String())
	}
	if model != "gpt-4o-mini" {
		t.Fatalf("ladder walk must land on gpt-4o-mini, got %q", model)
	}
}

// TestModelBudgetE2E_ExtremelyLowBudget402 场景 3:预算低于全梯最便宜
// ($0.0005 < gpt-4o-mini $0.0006015)→ 402 insufficient_budget/budget_exceeded,
// executor 不执行。
func TestModelBudgetE2E_ExtremelyLowBudget402(t *testing.T) {
	rec, capture := runModelBudgetE2E(t,
		`{"model":"gpt-4","max_cost_per_task":0.0005,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Fatalf("body missing budget_exceeded: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "insufficient_budget") {
		t.Fatalf("body missing insufficient_budget type: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no model within budget") {
		t.Fatalf("body missing decision message: %s", rec.Body.String())
	}
	if capture.params != nil {
		t.Fatal("budget-exceeded must be rejected before the executor runs")
	}
}

// TestModelBudgetE2E_NoBudgetFieldBackwardCompat 无 max_cost_per_task 字段 →
// 预算面整体不激活(向后兼容),executor 收到原始 model。
func TestModelBudgetE2E_NoBudgetFieldBackwardCompat(t *testing.T) {
	rec, capture := runModelBudgetE2E(t,
		`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code == http.StatusPaymentRequired {
		t.Fatalf("no-budget request must not 402; body=%s", rec.Body.String())
	}
	if rec.Header().Get("X-Gw-Budget-Requested") != "" {
		t.Fatalf("no-budget request must not set budget headers")
	}
	model, ok := capturedUpstreamModel(t, capture)
	if !ok {
		t.Fatalf("executor not reached; status=%d body=%s", rec.Code, rec.Body.String())
	}
	if model != "gpt-4" {
		t.Fatalf("no-budget backward compat must keep model, got %q", model)
	}
}
