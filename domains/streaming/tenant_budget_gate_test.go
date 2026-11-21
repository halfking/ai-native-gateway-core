package streaming

// 租户月度预算闸（938 enforcement_mode，迁移 996）的 handler 级钉测。
//
//	背景（R-MAAS6）：F3 抽共用判定体时四面门分支零自动化覆盖——纯函数单测
//	结构上抓不到接线级缺陷（12h 审计轮 F3 就是这么漏进生产的）。
//	本文件钉死四象限（429 / 503 fail-closed / warn 放行 / 豁免）+ 面信封注入
//	（chat=OpenAI 形 / messages=Anthropic 形 / responses=responses 形）+
//	四面接线存在性（防再漂移：新增数据面不接闸、或接闸不接信封都会红）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/maas"
)

// fakeTenantBudgetChecker 可编程的 tenantBudgetChecker 桩（不碰真库）。
type fakeTenantBudgetChecker struct {
	verdict *maas.MonthlyBudgetVerdict
	err     error
}

func (f *fakeTenantBudgetChecker) CheckMonthlyBudget(ctx context.Context, tenantID string) (*maas.MonthlyBudgetVerdict, error) {
	return f.verdict, f.err
}

func hardOverVerdict() *maas.MonthlyBudgetVerdict {
	return &maas.MonthlyBudgetVerdict{
		TenantID:           "t1",
		EnforcementMode:    maas.BudgetEnforcementHard,
		MonthlyBudgetCents: 100,
		SpentCents:         200,
		Over:               true,
	}
}

func gateKeyInfo() *authentication.KeyInfo {
	return &authentication.KeyInfo{TenantID: "t1"}
}

// runGate 跑一遍闸并返回 (放行?, 拒绝响应 recorder, onReject 收到的 errCode)。
func runGate(t *testing.T, checker tenantBudgetChecker, envelope tenantBudgetEnvelope) (bool, *httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	var rejectCode string
	allowed := applyTenantMonthlyBudgetGate(context.Background(), rec, checker, gateKeyInfo(), "req-1",
		func(errCode, errMsg string, clientStatus int) { rejectCode = errCode }, envelope)
	return allowed, rec, rejectCode
}

// ① hard 达限 ⇒ 429 + error.code=tenant_budget_exceeded（OpenAI 兜底信封）。
func TestTenantBudgetGateHardOverBlocks429(t *testing.T) {
	allowed, rec, code := runGate(t, &fakeTenantBudgetChecker{verdict: hardOverVerdict()}, nil)
	if allowed {
		t.Fatalf("hard over verdict must block, got allow")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if code != "tenant_budget_exceeded" {
		t.Fatalf("onReject errCode = %q, want tenant_budget_exceeded", code)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Type != "insufficient_quota" || body.Error.Code != "tenant_budget_exceeded" {
		t.Fatalf("envelope type/code = %s/%s, want insufficient_quota/tenant_budget_exceeded", body.Error.Type, body.Error.Code)
	}
}

// ② hard 模式下花费查不出来 ⇒ fail-closed 503（≠「没超」）。
func TestTenantBudgetGateHardCheckFailureFailsClosed503(t *testing.T) {
	checker := &fakeTenantBudgetChecker{err: &maas.TenantBudgetCheckError{Mode: maas.BudgetEnforcementHard, Err: context.DeadlineExceeded}}
	allowed, rec, code := runGate(t, checker, nil)
	if allowed {
		t.Fatalf("hard check failure must fail closed, got allow")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if code != "tenant_budget_unavailable" {
		t.Fatalf("onReject errCode = %q, want tenant_budget_unavailable", code)
	}
}

// ③ 非 hard 模式（warn/适用性未知）查询失败 ⇒ 门不承重，放行（938 A-15）。
func TestTenantBudgetGateNonHardCheckFailureFailsOpen(t *testing.T) {
	checker := &fakeTenantBudgetChecker{err: &maas.TenantBudgetCheckError{Mode: "", Err: context.DeadlineExceeded}}
	allowed, rec, _ := runGate(t, checker, nil)
	if !allowed {
		t.Fatalf("non-hard check failure must fail open")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("nothing should be written on fail-open, status = %d", rec.Code)
	}
}

// ④ warn 超限 ⇒ 只告警不拦截。
func TestTenantBudgetGateWarnOverPasses(t *testing.T) {
	allowed, rec, _ := runGate(t, &fakeTenantBudgetChecker{verdict: &maas.MonthlyBudgetVerdict{
		TenantID: "t1", EnforcementMode: maas.BudgetEnforcementWarn, MonthlyBudgetCents: 100, SpentCents: 200, Over: true,
	}}, nil)
	if !allowed {
		t.Fatalf("warn mode must not block")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("nothing should be written on warn pass, status = %d", rec.Code)
	}
}

// ⑤ 豁免面：无预算行 / default 租户 / 内部回环 key / nil checker ⇒ 全放行。
func TestTenantBudgetGateExemptions(t *testing.T) {
	allowed, _, _ := runGate(t, nil, nil)
	if !allowed {
		t.Fatalf("nil checker must pass (optional interface)")
	}
	rec := httptest.NewRecorder()
	if !applyTenantMonthlyBudgetGate(context.Background(), rec, &fakeTenantBudgetChecker{verdict: hardOverVerdict()},
		&authentication.KeyInfo{TenantID: "default"}, "req-1", nil, nil) {
		t.Fatalf("default tenant must be exempt")
	}
	rec2 := httptest.NewRecorder()
	if !applyTenantMonthlyBudgetGate(context.Background(), rec2, &fakeTenantBudgetChecker{verdict: hardOverVerdict()},
		&authentication.KeyInfo{TenantID: "t1", IsInternal: true}, "req-1", nil, nil) {
		t.Fatalf("internal key must be exempt")
	}
}

// ⑥ 面信封注入：Anthropic 形（messages 面）——429 用 rate_limit_error。
func TestTenantBudgetGateMessagesEnvelope(t *testing.T) {
	messagesEnvelope := func(w http.ResponseWriter, status int, reqID, errType, errMsg, errCode string) {
		anthropicType := "api_error"
		if status == http.StatusTooManyRequests {
			anthropicType = "rate_limit_error"
		}
		writeAnthropicError(w, status, anthropicType, errMsg)
	}
	allowed, rec, _ := runGate(t, &fakeTenantBudgetChecker{verdict: hardOverVerdict()}, messagesEnvelope)
	if allowed {
		t.Fatalf("hard over must block regardless of envelope")
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Type != "error" {
		t.Fatalf("top-level type = %q, want error (Anthropic envelope)", body.Type)
	}
	if body.Error.Type != "rate_limit_error" {
		t.Fatalf("anthropic error type = %q, want rate_limit_error", body.Error.Type)
	}
}

// ⑦ 面信封注入：responses 形——429 用本面惯例 rate_limit_exceeded，code 保留可区分性。
func TestTenantBudgetGateResponsesEnvelope(t *testing.T) {
	responsesEnvelope := func(w http.ResponseWriter, status int, reqID, errType, errMsg, errCode string) {
		respType := errType
		if status == http.StatusTooManyRequests {
			respType = "rate_limit_exceeded"
		}
		writeResponsesError(w, status, errMsg, respType, errCode)
	}
	allowed, rec, _ := runGate(t, &fakeTenantBudgetChecker{verdict: hardOverVerdict()}, responsesEnvelope)
	if allowed {
		t.Fatalf("hard over must block regardless of envelope")
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Type != "rate_limit_exceeded" || body.Error.Code != "tenant_budget_exceeded" {
		t.Fatalf("responses envelope type/code = %s/%s, want rate_limit_exceeded/tenant_budget_exceeded", body.Error.Type, body.Error.Code)
	}
}

// ⑧ 四面接线存在性（防再漂移）：messages/responses/audio 三面必须仍调用闸，
// 且 messages/responses 的信封注入必须仍在（删除任一 ⇒ 本钉测红）。
func TestTenantBudgetGateWiredOnAllFaces(t *testing.T) {
	// 测试进程 cwd = 包目录 ⇒ 用包内相对路径。
	mustContain := map[string][]string{
		"messages.go":      {"enforceTenantMonthlyBudget(r.Context()", `anthropicType = "rate_limit_error"`},
		"responses.go":     {"enforceTenantMonthlyBudget(r.Context()", `respType = "rate_limit_exceeded"`},
		"audio_service.go": {"applyTenantMonthlyBudgetGate(r.Context()"},
		"handler.go":       {"func applyTenantMonthlyBudgetGate(", "tenantBudgetEnvelope"},
	}
	for file, needles := range mustContain {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(src), needle) {
				t.Fatalf("%s lost tenant budget gate wiring: %q not found (new data faces must call applyTenantMonthlyBudgetGate)", file, needle)
			}
		}
	}
}
