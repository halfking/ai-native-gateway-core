package ir

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// P5 of the 2026-09-18 MiniMax thinking incident (docs/audit/
// 2026-09-18-minimax-thinking-incident.md §4/§6.1): parse_anthropic consumes
// `thinking` into ir.Thinking and parse_gemini maps thinkingConfig into
// Reasoning{Type:"enabled",BudgetTokens}; serialize_openai previously only
// reported protocol loss for them, so Anthropic/Gemini-protocol clients
// routed to OpenAI-form thinking upstreams lost their reasoning intent
// silently. These tests pin the reasonnorm.Render-by-TargetProvider contract.
//
// Dialect keying follows the incident lesson: TargetProvider decides, never
// the model name.
func TestSerializeOpenAI_P5_AnthropicThinkingRenderedByTargetProvider(t *testing.T) {
	anthropicBody := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": [{"role":"user","content":"hi"}],
		"thinking": {"type":"enabled","budget_tokens":4096}
	}`)

	t.Run("minimax renders minimal adaptive object", func(t *testing.T) {
		irReq, err := ParseAnthropic(anthropicBody)
		if err != nil {
			t.Fatalf("ParseAnthropic: %v", err)
		}
		if irReq.Thinking == nil {
			t.Fatal("parse_anthropic 应把 thinking 消费进 ir.Thinking")
		}
		irReq.TargetProvider = "minimax"
		body, err := SerializeOpenAI(irReq)
		if err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		th, ok := m["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("minimax body 缺少 thinking 对象: %s", body)
		}
		if th["type"] != "adaptive" {
			t.Errorf("thinking.type=%v, want adaptive（与 openai 入向 translateThinking v2 语义一致）", th["type"])
		}
		if len(th) != 1 {
			t.Errorf("thinking 应为最小对象(仅 type)，避免严格校验 400: %v", th)
		}
	})

	t.Run("deepseek renders enabled toggle", func(t *testing.T) {
		irReq, err := ParseAnthropic(anthropicBody)
		if err != nil {
			t.Fatalf("ParseAnthropic: %v", err)
		}
		irReq.TargetProvider = "deepseek"
		body, err := SerializeOpenAI(irReq)
		if err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		th, ok := m["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("deepseek body 缺少 thinking 对象: %s", body)
		}
		// 与 Extensions 路径的透传不同，P5 生成的对象走 reasonnorm 最小对象
		// 语义：只表达意图，不带 vendor 私有 budget 字段（防严格校验 400）。
		if th["type"] != "enabled" || len(th) != 1 {
			t.Errorf("deepseek thinking=%v, want {\"type\":\"enabled\"}", th)
		}
	})

	t.Run("qwen renders enable_thinking plus budget", func(t *testing.T) {
		irReq, err := ParseAnthropic(anthropicBody)
		if err != nil {
			t.Fatalf("ParseAnthropic: %v", err)
		}
		irReq.TargetProvider = "qwen"
		body, err := SerializeOpenAI(irReq)
		if err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v, ok := m["enable_thinking"].(bool); !ok || !v {
			t.Errorf("enable_thinking=%v, want true", m["enable_thinking"])
		}
		if v, ok := m["thinking_budget"].(float64); !ok || int(v) != 4096 {
			t.Errorf("thinking_budget=%v, want 4096", m["thinking_budget"])
		}
	})

	t.Run("disabled intent renders disabled", func(t *testing.T) {
		body := []byte(`{
			"model": "claude-sonnet-4-5",
			"max_tokens": 1024,
			"messages": [{"role":"user","content":"hi"}],
			"thinking": {"type":"disabled"}
		}`)
		for _, target := range []string{"minimax", "deepseek"} {
			irReq, err := ParseAnthropic(body)
			if err != nil {
				t.Fatalf("ParseAnthropic (%s): %v", target, err)
			}
			irReq.TargetProvider = target
			out, err := SerializeOpenAI(irReq)
			if err != nil {
				t.Fatalf("SerializeOpenAI (%s): %v", target, err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal (%s): %v", target, err)
			}
			th, ok := m["thinking"].(map[string]any)
			if !ok || th["type"] != "disabled" {
				t.Errorf("target=%s thinking=%v, want disabled toggle", target, m["thinking"])
			}
		}
	})

	t.Run("no TargetProvider keeps loss-drop behavior and reports loss", func(t *testing.T) {
		irReq, err := ParseAnthropic(anthropicBody)
		if err != nil {
			t.Fatalf("ParseAnthropic: %v", err)
		}
		if irReq.TargetProvider != "" {
			t.Fatalf("precondition: TargetProvider 应为空")
		}
		losses := captureProtocolLoss(t, func() {
			if _, err := SerializeOpenAI(irReq); err != nil {
				t.Errorf("SerializeOpenAI: %v", err)
			}
		})
		var out map[string]any
		body, err := SerializeOpenAI(irReq)
		if err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, exists := out["thinking"]; exists {
			t.Errorf("未知目标方言不得猜测输出 thinking: %s", body)
		}
		if len(losses) == 0 {
			t.Errorf("thinking 丢失仍应上报 ir_protocol_loss")
		}
	})
}

// TestSerializeOpenAI_P5_GeminiBudgetRenderedByTargetProvider covers the
// Gemini inbound shape: parse_gemini maps generationConfig.thinkingConfig
// into Reasoning{Type:"enabled",BudgetTokens} — no ir.Thinking involved.
func TestSerializeOpenAI_P5_GeminiBudgetRenderedByTargetProvider(t *testing.T) {
	geminiBody := []byte(`{
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig": {"thinkingConfig": {"thinkingBudget": 2048}}
	}`)

	irReq, err := ParseGemini(geminiBody)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	if irReq.Reasoning == nil || irReq.Reasoning.BudgetTokens == nil {
		t.Fatalf("parse_gemini 应把 thinkingConfig 映射进 Reasoning.BudgetTokens")
	}
	irReq.TargetProvider = "minimax"
	body, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	th, ok := m["thinking"].(map[string]any)
	if !ok || th["type"] != "adaptive" {
		t.Errorf("gemini budget → minimax thinking=%v, want adaptive", m["thinking"])
	}
}

// TestSerializeOpenAI_P5_OpenAISourceNotDoubleExpressed pins the guard: an
// OpenAI-protocol request whose reasoning_effort was already serialized must
// not additionally grow a thinking object when TargetProvider is a thinking
// dialect.
func TestSerializeOpenAI_P5_OpenAISourceNotDoubleExpressed(t *testing.T) {
	body := []byte(`{
		"model": "minimax-m3",
		"max_tokens": 256,
		"messages": [{"role":"user","content":"hi"}],
		"reasoning_effort": "medium"
	}`)
	irReq, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	irReq.TargetProvider = "minimax"
	out, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := m["thinking"]; exists {
		t.Errorf("openai 入向 effort 意图不得二次表达为 thinking: %s", out)
	}
	if m["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort=%v, want medium", m["reasoning_effort"])
	}
}

// TestOpenAIReasoningIntent unit-pins the intent extraction priorities.
func TestOpenAIReasoningIntent(t *testing.T) {
	budget := 4096
	cases := []struct {
		name string
		req  *InternalRequest
		want reasonnorm.Intent
		have bool
	}{
		{
			name: "anthropic thinking enabled",
			req:  &InternalRequest{Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: budget}},
			want: reasonnorm.Intent{Mode: reasonnorm.ModeEnabled, BudgetTokens: budget},
			have: true,
		},
		{
			name: "anthropic thinking disabled",
			req:  &InternalRequest{Thinking: &ThinkingConfig{Type: "disabled"}},
			want: reasonnorm.Intent{Mode: reasonnorm.ModeDisabled},
			have: true,
		},
		{
			name: "anthropic thinking adaptive",
			req:  &InternalRequest{Thinking: &ThinkingConfig{Type: "adaptive"}},
			want: reasonnorm.Intent{Mode: reasonnorm.ModeAdaptive},
			have: true,
		},
		{
			name: "gemini budget shape",
			req: &InternalRequest{
				SourceProtocol: ProtocolGeminiGenerate,
				Reasoning:      &ReasoningConfig{Type: "enabled", BudgetTokens: &budget},
			},
			want: reasonnorm.Intent{Mode: reasonnorm.ModeEnabled, BudgetTokens: budget},
			have: true,
		},
		{
			name: "openai effort shape excluded",
			req: &InternalRequest{
				SourceProtocol: ProtocolOpenAIChat,
				Reasoning:      &ReasoningConfig{Effort: "high"},
			},
			have: false,
		},
		{
			name: "nil everywhere",
			req:  &InternalRequest{},
			have: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, have := openAIReasoningIntent(tc.req)
			if have != tc.have {
				t.Fatalf("have=%v, want %v", have, tc.have)
			}
			if have && got != tc.want {
				t.Errorf("intent=%+v, want %+v", got, tc.want)
			}
		})
	}
}

// captureProtocolLoss collects ir_protocol_loss events reported while run()
// executes, restoring the previous reporter afterwards.
func captureProtocolLoss(t *testing.T, run func()) []AnomalyEvent {
	t.Helper()
	prev := SetAnomalyReporter(func(ev AnomalyEvent) {
		if ev.AnomalyType == AnomalyProtocolLoss {
			capturedLosses = append(capturedLosses, ev)
		}
	})
	capturedLosses = nil
	defer func() {
		SetAnomalyReporter(prev)
	}()
	run()
	return capturedLosses
}

var capturedLosses []AnomalyEvent

// R43 (2026-09-18, P5 补课补漏): Gemini inbound budget-shaped reasoning had
// no loss report on a plain openai_chat target — the Gemini handler's
// pre-route serialization runs with an empty TargetProvider, so the intent
// was dropped silently (the exact shape P5 set out to make visible). These
// pins cover both directions: loss reported pre-route, and rendered (no
// loss) once the executor knows the target dialect.
func TestSerializeOpenAI_P5_GeminiBudgetLossReportedByTargetProvider(t *testing.T) {
	geminiBody := []byte(`{
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig": {"thinkingConfig":{"thinkingBudget":8192}}
	}`)

	t.Run("empty target (handler step-6 shape) reports loss", func(t *testing.T) {
		cap := resetDedupAndInstall(t)
		irReq, err := ParseGemini(geminiBody)
		if err != nil {
			t.Fatalf("ParseGemini: %v", err)
		}
		if irReq.Reasoning == nil || irReq.Reasoning.BudgetTokens == nil {
			t.Fatal("parse_gemini 应把 thinkingConfig.thinkingBudget 消费进 ir.Reasoning")
		}
		body, err := SerializeOpenAI(irReq)
		if err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if _, ok := m["thinking"]; ok {
			t.Fatalf("openai_chat 目标不应渲染 thinking 对象: %s", body)
		}
		if !cap.hasEvent(AnomalyEvent{
			AnomalyType:       AnomalyProtocolLoss,
			FieldPath:         "reasoning.budget_tokens",
			SourceProtocol:    ProtocolGeminiGenerate,
			TargetProtocol:    ProtocolOpenAIChat,
			RawValueTruncated: true,
		}) {
			t.Fatalf("budget-shape Reasoning 丢失未上报, events=%v", cap.snapshot())
		}
	})

	t.Run("minimax target renders intent, no loss", func(t *testing.T) {
		cap := resetDedupAndInstall(t)
		irReq, err := ParseGemini(geminiBody)
		if err != nil {
			t.Fatalf("ParseGemini: %v", err)
		}
		irReq.TargetProvider = "minimax"
		body, err := SerializeOpenAI(irReq)
		if err != nil {
			t.Fatalf("SerializeOpenAI: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := m["thinking"].(map[string]any); !ok {
			t.Fatalf("minimax 目标应渲染 thinking 对象: %s", body)
		}
		for _, ev := range cap.snapshot() {
			if ev.FieldPath == "reasoning.budget_tokens" {
				t.Fatalf("已渲染的 intent 不应上报丢失: %v", ev)
			}
		}
	})
}
