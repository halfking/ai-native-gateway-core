package ir

import (
	"context"
	"testing"
)

// source_reasoning_context_test.go — R45 Gemini thinking 专项的 IR 携带层
// 契约：With/From 往返、RestoreSourceReasoning 的优先级与 SourceProtocol
// 盖写规则（openAIReasoningIntent 的防双表达排除以 SourceProtocol 为键，
// 恢复意图必须同时恢复协议身份，否则合并的 intent 会被自己的守卫拒掉）。

func TestSourceReasoning_HasReasoningIntent(t *testing.T) {
	cases := []struct {
		name string
		src  SourceReasoning
		want bool
	}{
		{"empty", SourceReasoning{}, false},
		{"reasoning only", SourceReasoning{Reasoning: &ReasoningConfig{Type: "enabled"}}, true},
		{"thinking only", SourceReasoning{Thinking: &ThinkingConfig{Type: "enabled"}}, true},
		{"protocol only", SourceReasoning{SourceProtocol: ProtocolGeminiGenerate}, false},
	}
	for _, tc := range cases {
		if got := tc.src.HasReasoningIntent(); got != tc.want {
			t.Errorf("%s: HasReasoningIntent() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWithSourceReasoning_RoundTrip(t *testing.T) {
	budget := 8192
	src := SourceReasoning{
		Reasoning:      &ReasoningConfig{Type: "enabled", BudgetTokens: &budget},
		SourceProtocol: ProtocolGeminiGenerate,
	}
	ctx := WithSourceReasoning(context.Background(), src)

	got, ok := SourceReasoningFromContext(ctx)
	if !ok {
		t.Fatal("SourceReasoningFromContext: not found after WithSourceReasoning")
	}
	if got.Reasoning == nil || got.Reasoning.Type != "enabled" || got.Reasoning.BudgetTokens == nil || *got.Reasoning.BudgetTokens != 8192 {
		t.Errorf("Reasoning round-trip mismatch: %+v", got.Reasoning)
	}
	if got.SourceProtocol != ProtocolGeminiGenerate {
		t.Errorf("SourceProtocol = %q, want %q", got.SourceProtocol, ProtocolGeminiGenerate)
	}

	if _, ok := SourceReasoningFromContext(context.Background()); ok {
		t.Error("plain context must not report a carried intent")
	}
	if _, ok := SourceReasoningFromContext(WithSourceReasoning(context.Background(), SourceReasoning{})); ok {
		t.Error("empty SourceReasoning must not count as carried intent")
	}
	if _, ok := SourceReasoningFromContext(nil); ok {
		t.Error("nil context must not report a carried intent")
	}
}

// RestoreSourceReasoning 的核心场景：ParseOpenAI 重解析产物（SourceProtocol
// 被 stamp 成 ProtocolOpenAIChat、无任何推理字段）恢复为携带的意图与真实
// 入向协议。
func TestRestoreSourceReasoning_MergesIntoReparsedIR(t *testing.T) {
	budget := 4096
	carried := SourceReasoning{
		Reasoning:      &ReasoningConfig{Type: "enabled", BudgetTokens: &budget},
		SourceProtocol: ProtocolGeminiGenerate,
	}
	ctx := WithSourceReasoning(context.Background(), carried)

	// Simulate the executor's re-parse of a step-6 synthetic body: intent
	// fields absent, SourceProtocol stamped OpenAI by ParseOpenAI.
	req := &InternalRequest{Model: "m", SourceProtocol: ProtocolOpenAIChat}
	req = RestoreSourceReasoning(req, ctx)

	if req.Reasoning == nil || req.Reasoning.BudgetTokens == nil || *req.Reasoning.BudgetTokens != 4096 {
		t.Errorf("Reasoning not restored: %+v", req.Reasoning)
	}
	if req.SourceProtocol != ProtocolGeminiGenerate {
		t.Errorf("SourceProtocol = %q, want %q (protocol identity must be restored or the double-expression guard rejects the intent)", req.SourceProtocol, ProtocolGeminiGenerate)
	}
}

// body 优先：重解析 IR 已表达推理（无论来自哪个字段）时不得覆盖。
func TestRestoreSourceReasoning_BodyExpressionWins(t *testing.T) {
	carried := SourceReasoning{
		Reasoning:      &ReasoningConfig{Type: "enabled"},
		SourceProtocol: ProtocolGeminiGenerate,
	}
	ctx := WithSourceReasoning(context.Background(), carried)

	req := &InternalRequest{
		SourceProtocol: ProtocolOpenAIChat,
		Reasoning:      &ReasoningConfig{Effort: "high"}, // native body expression
	}
	got := RestoreSourceReasoning(req, ctx)
	if got.Reasoning.Effort != "high" || got.Reasoning.Type != "" {
		t.Errorf("body's own reasoning expression must win: %+v", got.Reasoning)
	}
	if got.SourceProtocol != ProtocolOpenAIChat {
		t.Errorf("SourceProtocol must stay untouched when body wins: %q", got.SourceProtocol)
	}
}

// 无携带值（原生 OpenAI 入向的常态）必须是无操作。
func TestRestoreSourceReasoning_NoCarrierIsNoOp(t *testing.T) {
	req := &InternalRequest{SourceProtocol: ProtocolOpenAIChat}
	got := RestoreSourceReasoning(req, context.Background())
	if got.Reasoning != nil || got.Thinking != nil || got.SourceProtocol != ProtocolOpenAIChat {
		t.Errorf("plain OpenAI inbound must be untouched: %+v", got)
	}
	if got2 := RestoreSourceReasoning(nil, context.Background()); got2 != nil {
		t.Errorf("nil req must stay nil, got %v", got2)
	}
}

// SourceProtocol 盖写只针对 ParseOpenAI 的 stamp（openai 族/空）；非 OpenAI
// 协议身份（如 anthropic 解析路径设置）不得被覆盖。
func TestRestoreSourceReasoning_ProtocolOverwriteScope(t *testing.T) {
	carried := SourceReasoning{Reasoning: &ReasoningConfig{Type: "enabled"}, SourceProtocol: ProtocolGeminiGenerate}
	ctx := WithSourceReasoning(context.Background(), carried)

	anthropicStamped := &InternalRequest{SourceProtocol: ProtocolAnthropicMessages}
	got := RestoreSourceReasoning(anthropicStamped, ctx)
	if got.SourceProtocol != ProtocolAnthropicMessages {
		t.Errorf("non-OpenAI SourceProtocol must not be overwritten: %q", got.SourceProtocol)
	}

	emptyStamped := &InternalRequest{}
	got = RestoreSourceReasoning(emptyStamped, ctx)
	if got.SourceProtocol != ProtocolGeminiGenerate {
		t.Errorf("empty SourceProtocol should be filled from carrier: %q", got.SourceProtocol)
	}
}
