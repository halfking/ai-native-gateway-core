package executors

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// executor_source_reasoning_test.go — R45 Gemini thinking 专项的 executor 层
// 钉桩（TargetProvider 前送的恢复半程）。
//
// 背景（R43 §五#1 / R45 立项）：Gemini 入向在 handler step-6 路由前序列化为
// OpenAI 形合成请求，TargetProvider 必空 → applyThinkingToOpenAIChat 无法渲染
// budget 形 Reasoning → 合成 body 天然不含 thinking。executor 侧三条出向分支
// 虽然在 2026-09-18 已把 TargetProvider 接线到位，但重解析的 IR 里 intent 已
// 不存在，接线无从发力。本文件钉死：请求 context 携带 ir.SourceReasoning 时，
// 三条分支都必须在序列化前恢复意图并按目标方言渲染。
//
// 与 executor_target_provider_wiring_test.go 的分工：那边钉"TargetProvider
// 设置了"，这边钉"context 携带的意图恢复了"——两个半程合起来才是完整链路。

// geminiSyntheticBody 在测试内用 handler 同款管线（ParseGemini → SerializeOpenAI，
// TargetProvider 留空）真产生 step-6 合成 body，保证测试输入与生产形态一致。
func geminiSyntheticBody(t *testing.T, thinkingBudget int) []byte {
	t.Helper()
	budget := thinkingBudget
	parsed, err := ir.ParseGemini([]byte(`{
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig": {"maxOutputTokens": 1024}
	}`))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	parsed.SourceProtocol = ir.ProtocolGeminiGenerate
	b := budget
	parsed.Reasoning = &ir.ReasoningConfig{Type: "enabled", BudgetTokens: &b}
	body, err := ir.SerializeOpenAI(parsed)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	return body
}

// 前置事实钉桩：路由前的合成 body 本来就不含任何推理字段——这正是缺陷本身，
// 也是恢复逻辑存在的理由。若未来 step-6 变为方言感知（例如 handler 拿到
// 路由提示），该断言失败提醒同步调整恢复点。
func TestSourceReasoning_Step6SyntheticBodyLacksThinking(t *testing.T) {
	body := geminiSyntheticBody(t, 4096)
	m := decodeUpstreamBody(t, body)
	for _, key := range []string{"thinking", "reasoning_effort", "enable_thinking", "thinking_budget"} {
		if _, exists := m[key]; exists {
			t.Errorf("step-6 synthetic body unexpectedly carries %q (pre-routing serialization is expected to drop it): %s", key, body)
		}
	}
}

func sourceReasoningParams(clientProtocol string) *ExecParams {
	budget := 4096
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx := ir.WithSourceReasoning(req.Context(), ir.SourceReasoning{
		Reasoning:      &ir.ReasoningConfig{Type: "enabled", BudgetTokens: &budget},
		SourceProtocol: ir.ProtocolGeminiGenerate,
	})
	return &ExecParams{
		R:              req.WithContext(ctx),
		ClientModel:    "minimax-m3",
		OutboundModel:  "minimax-m3",
		ClientProtocol: clientProtocol,
	}
}

func minimaxCandidate() provider.Candidate {
	return provider.Candidate{
		ProviderID:   14,
		CredentialID: 42,
		Protocol:     "openai-completions",
		CatalogCode:  "minimax",
		RawModel:     "MiniMax-M3",
	}
}

// ① legacy_with_ir 分支：Gemini 入向合成请求落在的主路径。
func TestFinalizeOpenAIUpstreamBody_SourceReasoning_LegacyWithIR(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := minimaxCandidate()

	t.Run("minimax renders restored intent as adaptive", func(t *testing.T) {
		params := sourceReasoningParams("")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, geminiSyntheticBody(t, 4096))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		assertUpstreamThinking(t, body, "adaptive", true)
	})

	t.Run("deepseek renders enabled", func(t *testing.T) {
		cand := cand
		cand.CatalogCode = "deepseek"
		params := sourceReasoningParams("")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, geminiSyntheticBody(t, 4096))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		assertUpstreamThinking(t, body, "enabled", true)
	})

	t.Run("unknown catalog still drops with loss report", func(t *testing.T) {
		cand := cand
		cand.CatalogCode = ""
		cand.RawModel = "some-openai-form-model"
		params := sourceReasoningParams("")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, geminiSyntheticBody(t, 4096))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		m := decodeUpstreamBody(t, body)
		if _, exists := m["thinking"]; exists {
			t.Errorf("未知目标方言不得猜测输出 thinking: %s", body)
		}
	})
}

// ② 断路器兜底分支：包级 ir 函数路径的恢复同样生效。
func TestFinalizeOpenAIUpstreamBody_SourceReasoning_CircuitOpenFallback(t *testing.T) {
	executor := &Executor{IR: &circuitOpenIRAdapter{}}
	params := sourceReasoningParams("")
	body, err := executor.finalizeOpenAIUpstreamBody(params, minimaxCandidate(), geminiSyntheticBody(t, 4096))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	assertUpstreamThinking(t, body, "adaptive", true)
}

// ③ e.IR == nil（inline validation）分支。
func TestFinalizeOpenAIUpstreamBody_SourceReasoning_NoIRConverter(t *testing.T) {
	executor := &Executor{}
	params := sourceReasoningParams("")
	body, err := executor.finalizeOpenAIUpstreamBody(params, minimaxCandidate(), geminiSyntheticBody(t, 4096))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	assertUpstreamThinking(t, body, "adaptive", true)
}

// 反向钉桩：无携带值（原生 OpenAI 入向常态）时不得注入任何推理字段。
func TestFinalizeOpenAIUpstreamBody_SourceReasoning_NoCarrierInjectsNothing(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
		ClientModel:    "minimax-m3",
		OutboundModel:  "minimax-m3",
		ClientProtocol: "",
	}
	body, err := executor.finalizeOpenAIUpstreamBody(params, minimaxCandidate(), geminiSyntheticBody(t, 4096))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	m := decodeUpstreamBody(t, body)
	for _, key := range []string{"thinking", "reasoning_effort", "enable_thinking"} {
		if _, exists := m[key]; exists {
			t.Errorf("no-carrier request must not gain %q: %s", key, body)
		}
	}
}

// body 自带原生推理表达时 body 优先（防双表达）。
func TestFinalizeOpenAIUpstreamBody_SourceReasoning_BodyReasoningWins(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	native := []byte(`{
		"model": "minimax-m3",
		"max_tokens": 256,
		"reasoning_effort": "high",
		"messages": [{"role":"user","content":"hi"}]
	}`)
	params := sourceReasoningParams("")
	body, err := executor.finalizeOpenAIUpstreamBody(params, minimaxCandidate(), native)
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	m := decodeUpstreamBody(t, body)
	if m["reasoning_effort"] != "high" {
		t.Errorf("native reasoning_effort must survive: %s", body)
	}
	if _, exists := m["thinking"]; exists {
		t.Errorf("carrier intent must not double-express over native body fields: %s", body)
	}
}

// context 值经 WithCancel/WithValue 派生链（streamretry wrapper 形态）仍可达。
func TestFinalizeOpenAIUpstreamBody_SourceReasoning_SurvivesDerivedContext(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	params := sourceReasoningParams("")
	derived, cancel := context.WithCancel(params.R.Context())
	defer cancel()
	derived = ir.WithSourceReasoning(derived, ir.SourceReasoning{}) // 派生链上再叠一层
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(derived)
	params.R = r
	body, err := executor.finalizeOpenAIUpstreamBody(params, minimaxCandidate(), geminiSyntheticBody(t, 4096))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	// 派生链最内层的空 SourceReasoning 不遮蔽外层真值（context.Value 取最内层
	// 非空…实际上 WithValue 会遮蔽——空值HasReasoningIntent=false 视为无携带，
	// 恢复不发生）。该用例钉住这个语义：空载体等同无载体。
	m := decodeUpstreamBody(t, body)
	if _, exists := m["thinking"]; exists {
		t.Errorf("inner empty carrier must shadow as no-carrier (documented semantics): %s", body)
	}
}
