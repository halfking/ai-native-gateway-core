package executors

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// executor_source_reasoning_anthropic_test.go — R45 补面（D01+D02 复核发现）：
// Gemini 入向 → anthropic-messages 上游的 Q3 转换路径恢复点。
//
// 此前 R45 主体修复只盖 OpenAI 形上游三分支；Q3 分支（openai 形合成 body →
// ParseOpenAI → SerializeAnthropic）重解析时 intent 已丢，serialize_anthropic
// 的 Reasoning→thinking 渲染能力拿不到输入。本文件钉死恢复 + Anthropic
// budget 约束钳制（floor 1024 / cap max_tokens-1 / 放不下丢弃）。

// geminiToAnthropicCandidate：anthropic-messages 协议上游（claude relay 族）。
func geminiToAnthropicCandidate() provider.Candidate {
	return provider.Candidate{
		ProviderID:   31,
		CredentialID: 130,
		Protocol:     "anthropic-messages",
		CatalogCode:  "apiclaude",
		RawModel:     "claude-sonnet-4-5",
	}
}

// anthropicCarrierParams 构造带 Gemini 推理意图载体的 ExecParams（client 侧
// 是合成 openai 形请求）。maxTokens 决定 source body 的 maxOutputTokens——
// Anthropic 钳制以 req.MaxTokens（重解析自 body）为上限。
func anthropicCarrierParams(budget int) *ExecParams {
	b := budget
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx := ir.WithSourceReasoning(req.Context(), ir.SourceReasoning{
		Reasoning:      &ir.ReasoningConfig{Type: "enabled", BudgetTokens: &b},
		SourceProtocol: ir.ProtocolGeminiGenerate,
	})
	return &ExecParams{
		R:              req.WithContext(ctx),
		ClientModel:    "claude-sonnet-4-5",
		OutboundModel:  "claude-sonnet-4-5",
		ClientProtocol: "openai-completions", // 合成请求的真实 client protocol
	}
}

// anthropicSourceBody 用 handler 同款管线产生 step-6 合成 body（无 thinking），
// maxOutputTokens 可变（Anthropic 钳制的上限来自它）。
func anthropicSourceBody(t *testing.T, thinkingBudget, maxOutputTokens int) []byte {
	t.Helper()
	body := fmt.Sprintf(`{
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig": {"maxOutputTokens": %d, "thinkingConfig": {"thinkingBudget": %d}}
	}`, maxOutputTokens, thinkingBudget)
	parsed, err := ir.ParseGemini([]byte(body))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	parsed.SourceProtocol = ir.ProtocolGeminiGenerate
	out, err := ir.SerializeOpenAI(parsed)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	return out
}

func decodeAnthropicBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("anthropic body not valid JSON: %v\n%s", err, body)
	}
	return m
}

func TestPrepareAnthropicRequestBody_SourceReasoning_Restored(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := geminiToAnthropicCandidate()
	// step-6 合成 body：maxOutputTokens=16384，thinking 已丢（前置事实由
	// TestSourceReasoning_Step6SyntheticBodyLacksThinking 钉住）。
	sourceBody := anthropicSourceBody(t, 8192, 16384)

	t.Run("budget restored and within anthropic rules passes through", func(t *testing.T) {
		params := anthropicCarrierParams(4096)
		body, err := executor.prepareAnthropicRequestBody(params, cand, sourceBody)
		if err != nil {
			t.Fatalf("prepareAnthropicRequestBody: %v", err)
		}
		m := decodeAnthropicBody(t, body)
		th, ok := m["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("anthropic body lacks thinking object (Q3 restore missing): %s", body)
		}
		if th["type"] != "enabled" {
			t.Errorf("thinking.type = %v, want enabled", th["type"])
		}
		if th["budget_tokens"] != float64(4096) {
			t.Errorf("budget_tokens = %v, want 4096 (within floor/cap, unchanged)", th["budget_tokens"])
		}
	})

	t.Run("budget below anthropic floor clamps up to 1024", func(t *testing.T) {
		params := anthropicCarrierParams(128) // gemini 合法、anthropic 非法
		body, err := executor.prepareAnthropicRequestBody(params, cand, sourceBody)
		if err != nil {
			t.Fatalf("prepareAnthropicRequestBody: %v", err)
		}
		th, _ := decodeAnthropicBody(t, body)["thinking"].(map[string]any)
		if th == nil || th["budget_tokens"] != float64(1024) {
			t.Errorf("budget must clamp 128 → 1024, got %s", body)
		}
	})

	t.Run("no carrier keeps anthropic body without injected thinking", func(t *testing.T) {
		params := &ExecParams{
			R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
			ClientModel:    "claude-sonnet-4-5",
			OutboundModel:  "claude-sonnet-4-5",
			ClientProtocol: "openai-completions",
		}
		body, err := executor.prepareAnthropicRequestBody(params, cand, sourceBody)
		if err != nil {
			t.Fatalf("prepareAnthropicRequestBody: %v", err)
		}
		m := decodeAnthropicBody(t, body)
		if _, exists := m["thinking"]; exists {
			t.Errorf("no-carrier request must not gain thinking: %s", body)
		}
	})
}

// 放不下（max_tokens 太小，钳后仍 < 1024）整体丢弃 thinking，请求仍可发。
func TestPrepareAnthropicRequestBody_SourceReasoning_CannotFitDropsThinking(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := geminiToAnthropicCandidate()
	// maxOutputTokens=1024：任何 ≥1024 的 budget 钳到 1023 后仍低于 floor。
	smallBody := anthropicSourceBody(t, 8192, 1024)
	params := anthropicCarrierParams(8192)
	body, err := executor.prepareAnthropicRequestBody(params, cand, smallBody)
	if err != nil {
		t.Fatalf("prepareAnthropicRequestBody: %v", err)
	}
	m := decodeAnthropicBody(t, body)
	if _, exists := m["thinking"]; exists {
		t.Errorf("unfittable thinking must be dropped entirely: %s", body)
	}
}
