package ir

import (
	"encoding/json"
	"math"
	"testing"
)

func parseOpenAIReasoningForDialectTest(t *testing.T, effort string) *InternalRequest {
	t.Helper()
	input, err := json.Marshal(map[string]any{
		"model": "test-model", "max_tokens": 4096,
		"messages":         []map[string]any{{"role": "user", "content": "hello"}},
		"reasoning_effort": effort,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseOpenAI(input)
	if err != nil {
		t.Fatal(err)
	}
	if req.Reasoning == nil || req.Reasoning.Effort != effort {
		t.Fatalf("parsed reasoning = %+v, want effort %q", req.Reasoning, effort)
	}
	return req
}

func TestOpenAIDisabledEffortToAnthropicThinking(t *testing.T) {
	for _, effort := range []string{"none", "disabled", " DISABLED "} {
		t.Run(effort, func(t *testing.T) {
			req := parseOpenAIReasoningForDialectTest(t, effort)
			req.Model = "claude-opus-5"
			body, err := SerializeAnthropic(req)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Thinking map[string]any `json:"thinking"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if out.Thinking["type"] != "disabled" {
				t.Fatalf("thinking = %v, want explicit disabled: %s", out.Thinking, body)
			}
			if _, ok := out.Thinking["budget_tokens"]; ok {
				t.Fatalf("disabled thinking must not carry budget_tokens: %s", body)
			}
		})
	}
}

func TestOpenAIDisabledEffortToGeminiThinking(t *testing.T) {
	for _, effort := range []string{"none", "disabled", " DISABLED "} {
		t.Run(effort, func(t *testing.T) {
			req := parseOpenAIReasoningForDialectTest(t, effort)
			req.Model = "gemini-2.5-flash"
			body, err := SerializeGemini(req)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				GenerationConfig struct {
					ThinkingConfig map[string]any `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if got := out.GenerationConfig.ThinkingConfig["thinkingBudget"]; got != float64(0) {
				t.Fatalf("thinkingConfig = %v, want thinkingBudget=0: %s", out.GenerationConfig.ThinkingConfig, body)
			}
			if _, ok := out.GenerationConfig.ThinkingConfig["includeThoughts"]; ok {
				t.Fatalf("disabled thinking must not request thoughts: %s", body)
			}
		})
	}
}

func TestReasoningDisabledConflictPrecedence(t *testing.T) {
	budget := 2048
	zero := 0
	for _, tc := range []struct {
		name            string
		reasoning       ReasoningConfig
		anthropicType   string
		anthropicBudget float64
		geminiBudget    float64
	}{
		{name: "explicit budget beats effort", reasoning: ReasoningConfig{Effort: "disabled", BudgetTokens: &budget}, anthropicType: "enabled", anthropicBudget: 2048, geminiBudget: 2048},
		{name: "explicit budget beats none", reasoning: ReasoningConfig{Effort: "none", BudgetTokens: &budget}, anthropicType: "enabled", anthropicBudget: 2048, geminiBudget: 2048},
		{name: "explicit zero budget disables", reasoning: ReasoningConfig{Effort: "high", BudgetTokens: &zero}, anthropicType: "disabled", geminiBudget: 0},
		{name: "type disabled beats budget", reasoning: ReasoningConfig{Type: "disabled", Effort: "high", BudgetTokens: &budget}, anthropicType: "disabled", geminiBudget: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &InternalRequest{Model: "claude-opus-5", MaxTokens: 4096, Reasoning: &tc.reasoning}
			anthropicBody, err := SerializeAnthropic(req)
			if err != nil {
				t.Fatal(err)
			}
			var anthropic struct {
				Thinking map[string]any `json:"thinking"`
			}
			if err := json.Unmarshal(anthropicBody, &anthropic); err != nil {
				t.Fatal(err)
			}
			if got := anthropic.Thinking["type"]; got != tc.anthropicType {
				t.Fatalf("Anthropic thinking type=%v, want %q: %s", got, tc.anthropicType, anthropicBody)
			}
			if tc.anthropicBudget > 0 {
				if got := anthropic.Thinking["budget_tokens"]; got != tc.anthropicBudget {
					t.Fatalf("Anthropic budget=%v, want %v: %s", got, tc.anthropicBudget, anthropicBody)
				}
			} else if _, ok := anthropic.Thinking["budget_tokens"]; ok {
				t.Fatalf("disabled Anthropic thinking has budget: %s", anthropicBody)
			}

			req.Model = "gemini-2.5-flash"
			geminiBody, err := SerializeGemini(req)
			if err != nil {
				t.Fatal(err)
			}
			var gemini struct {
				GenerationConfig struct {
					ThinkingConfig map[string]any `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(geminiBody, &gemini); err != nil {
				t.Fatal(err)
			}
			if got := gemini.GenerationConfig.ThinkingConfig["thinkingBudget"]; got != tc.geminiBudget {
				t.Fatalf("Gemini budget=%v, want %v: %s", got, tc.geminiBudget, geminiBody)
			}
		})
	}
}

func TestAnthropicNativeThinkingPrecedesReasoning(t *testing.T) {
	for _, tc := range []struct {
		name     string
		thinking ThinkingConfig
		effort   string
		wantType string
		budget   float64
	}{
		{name: "native enabled beats disabled effort", thinking: ThinkingConfig{Type: "enabled", BudgetTokens: 2048}, effort: "disabled", wantType: "enabled", budget: 2048},
		{name: "native disabled beats high effort", thinking: ThinkingConfig{Type: "disabled"}, effort: "high", wantType: "disabled"},
		{name: "native adaptive has no budget", thinking: ThinkingConfig{Type: "adaptive"}, effort: "high", wantType: "adaptive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &InternalRequest{Model: "test-model", MaxTokens: 4096, Thinking: &tc.thinking, Reasoning: &ReasoningConfig{Effort: tc.effort}}
			body, err := SerializeAnthropic(req)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Thinking map[string]any `json:"thinking"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if got := out.Thinking["type"]; got != tc.wantType {
				t.Fatalf("type=%v, want %q: %s", got, tc.wantType, body)
			}
			if tc.budget > 0 {
				if got := out.Thinking["budget_tokens"]; got != tc.budget {
					t.Fatalf("budget=%v, want %v: %s", got, tc.budget, body)
				}
			} else if _, ok := out.Thinking["budget_tokens"]; ok {
				t.Fatalf("native disabled thinking has budget: %s", body)
			}
		})
	}
}

func TestAnthropicExplicitEnabledBeatsDisabledEffort(t *testing.T) {
	req := &InternalRequest{Model: "test-model", MaxTokens: 16000, Reasoning: &ReasoningConfig{Type: "enabled", Effort: "disabled"}}
	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Thinking map[string]any `json:"thinking"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Thinking["type"] != "enabled" || out.Thinking["budget_tokens"] != float64(8192) {
		t.Fatalf("explicit enabled must take priority over conflicting off effort: %s", body)
	}
}

func TestGeminiExplicitEnabledBeatsZeroBudget(t *testing.T) {
	zero := 0
	for _, reasoning := range []ReasoningConfig{
		{Type: "enabled", BudgetTokens: &zero},
		{Type: "enabled", Effort: "disabled"},
		{Type: "adaptive", Effort: "high"},
	} {
		body, err := SerializeGemini(&InternalRequest{Reasoning: &reasoning})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			GenerationConfig struct {
				ThinkingConfig map[string]any `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if got := out.GenerationConfig.ThinkingConfig["thinkingBudget"]; got != float64(-1) {
			t.Fatalf("reasoning=%+v, thinkingBudget=%v, want dynamic -1: %s", reasoning, got, body)
		}
	}
}

func TestGeminiDisabledRequiresModelSupport(t *testing.T) {
	for _, tc := range []struct {
		model      string
		wantBudget bool
	}{
		{"gemini-2.5-flash", true},
		{"google/gemini-2.5-flash-preview-09-2025", true},
		{"gemini-2.5-flash-lite", true},
		{"gemini-2.5-pro", false},
		{"gemini-2.5-pro-exp-03-25", false},
		{"gemini-3-pro", false},
		{"unknown-model-alias", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			req := parseOpenAIReasoningForDialectTest(t, "disabled")
			req.Model = tc.model
			req.Metadata = &Metadata{RequestID: "gemini-disable-" + tc.model}
			var body []byte
			var err error
			losses := captureProtocolLoss(t, func() { body, err = SerializeGemini(req) })
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				GenerationConfig struct {
					ThinkingConfig map[string]any `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			_, hasBudget := out.GenerationConfig.ThinkingConfig["thinkingBudget"]
			if hasBudget != tc.wantBudget {
				t.Fatalf("thinkingConfig=%v, want budget present=%v: %s", out.GenerationConfig.ThinkingConfig, tc.wantBudget, body)
			}
			wantLosses := 0
			if !tc.wantBudget {
				wantLosses = 1
			}
			if got := len(losses); got != wantLosses {
				t.Fatalf("protocol losses=%v, want %d", losses, wantLosses)
			}
			if wantLosses == 1 && (losses[0].Reason != "unsupported" || losses[0].FieldPath != "reasoning_effort" || losses[0].Metadata["model"] != tc.model) {
				t.Fatalf("loss is not model-scoped and attributable: %+v", losses[0])
			}
		})
	}
}

func TestAnthropicOpus55DisabledOmittedAndReported(t *testing.T) {
	for _, tc := range []struct {
		model       string
		wantDisable bool
	}{
		{"claude-opus-5", true},
		{"anthropic/claude-opus-5-5", false},
		{"claude-opus-5-5-20260901", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			req := parseOpenAIReasoningForDialectTest(t, "disabled")
			req.Model = tc.model
			req.Metadata = &Metadata{RequestID: "anthropic-disable-" + tc.model}
			var body []byte
			var err error
			losses := captureProtocolLoss(t, func() { body, err = SerializeAnthropic(req) })
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Thinking map[string]any `json:"thinking"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			_, hasThinking := out.Thinking["type"]
			if hasThinking != tc.wantDisable {
				t.Fatalf("thinking=%v, want disabled present=%v: %s", out.Thinking, tc.wantDisable, body)
			}
			wantLosses := 0
			if !tc.wantDisable {
				wantLosses = 1
			}
			if got := len(losses); got != wantLosses {
				t.Fatalf("protocol losses=%v, want %d", losses, wantLosses)
			}
			if wantLosses == 1 && (losses[0].Reason != "unsupported" || losses[0].FieldPath != "reasoning_effort" || losses[0].Metadata["model"] != tc.model) {
				t.Fatalf("loss is not model-scoped and attributable: %+v", losses[0])
			}
		})
	}
}

func TestNativeDisabledModelLossesRemainVisible(t *testing.T) {
	for _, tc := range []struct {
		name, model, source, target, field string
	}{
		{"Gemini same protocol", "gemini-2.5-pro", ProtocolGeminiGenerate, ProtocolGeminiGenerate, "reasoning"},
		{"Anthropic native thinking", "claude-opus-5-5", ProtocolAnthropicMessages, ProtocolAnthropicMessages, "thinking.type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &InternalRequest{Model: tc.model, SourceProtocol: tc.source, Metadata: &Metadata{RequestID: "native-disable-" + tc.model}}
			var body []byte
			var err error
			if tc.target == ProtocolGeminiGenerate {
				req.Reasoning = &ReasoningConfig{Type: "disabled"}
				losses := captureProtocolLoss(t, func() { body, err = SerializeGemini(req) })
				if err != nil {
					t.Fatal(err)
				}
				if len(losses) != 1 || losses[0].FieldPath != tc.field {
					t.Fatalf("Gemini native loss=%+v: %s", losses, body)
				}
			} else {
				req.Thinking = &ThinkingConfig{Type: "disabled"}
				losses := captureProtocolLoss(t, func() { body, err = SerializeAnthropic(req) })
				if err != nil {
					t.Fatal(err)
				}
				if len(losses) != 1 || losses[0].FieldPath != tc.field {
					t.Fatalf("Anthropic native loss=%+v: %s", losses, body)
				}
			}
		})
	}
}

// ── 2026-09-30 批判式复审修正轮 ─────────────────────────────────────────────
// N21-2 残留：二十三轮「优先级重构实质收口」结论不完整——
// parseResponsesReasoning 把 Responses reasoning.summary（"auto"/"concise"/
// "detailed"）灌进 Reasoning.Type（词汇碰撞本体），未知值会被序列化成
// 非法 thinking.type（Anthropic {"type":"auto","budget_tokens":8192} 上游
// 400；修复前 PoC 实录）。已知指令值只有 enabled/disabled/adaptive；其余
// 词汇不是思考开关，必须回落 Budget/Effort 或整体不发射。

func TestResponsesSummaryVocabIsNotAThinkingDirective(t *testing.T) {
	for _, typ := range []string{"auto", "concise", "detailed"} {
		t.Run("anthropic/"+typ, func(t *testing.T) {
			body, err := SerializeAnthropic(&InternalRequest{Model: "claude-sonnet-4-5",
				Reasoning: &ReasoningConfig{Type: typ}})
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if _, ok := out["thinking"]; ok {
				t.Fatalf("summary vocab %q must not become thinking.type: %s", typ, body)
			}
		})
		t.Run("gemini/"+typ, func(t *testing.T) {
			body, err := SerializeGemini(&InternalRequest{Model: "gemini-2.5-flash",
				Reasoning: &ReasoningConfig{Type: typ}})
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Gen struct {
					TC map[string]any `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Gen.TC) > 0 {
				t.Fatalf("summary vocab %q must not emit thinkingConfig: %s", typ, body)
			}
		})
	}
	// 未知 Type 与真实 Effort 并存时 effort 仍生效（回落而非吞掉）。
	body, err := SerializeAnthropic(&InternalRequest{Model: "claude-sonnet-4-5",
		Reasoning: &ReasoningConfig{Type: "concise", Effort: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Thinking struct {
			Type         string `json:"type"`
			BudgetTokens *int   `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Thinking.Type != "enabled" || out.Thinking.BudgetTokens == nil || *out.Thinking.BudgetTokens != 4096 {
		t.Fatalf("unknown Type must fall back to Effort: %s", body)
	}
}

// 修复前：reasoning 路径 Type=adaptive 发 {"type":"adaptive",
// "budget_tokens":8192}，违背 native canonical（norm.go adaptive 无预算、
// TestAnthropicNativeThinkingPrecedesReasoning 同款形态）。
func TestAnthropicReasoningAdaptiveOmitsBudget(t *testing.T) {
	body, err := SerializeAnthropic(&InternalRequest{Model: "claude-sonnet-4-5",
		Reasoning: &ReasoningConfig{Type: "adaptive"}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Thinking map[string]any `json:"thinking"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Thinking["type"] != "adaptive" {
		t.Fatalf("type=%v, want adaptive: %s", out.Thinking["type"], body)
	}
	if _, ok := out.Thinking["budget_tokens"]; ok {
		t.Fatalf("adaptive canonical form carries no budget: %s", body)
	}
}

// 修复前：SerializeGemini 完全忽略 req.Thinking，原生意图被并发 Reasoning
// 反超（PoC：Thinking budget=2048 + Effort=high → 输出 4096）。镜像
// TestAnthropicNativeThinkingPrecedesReasoning 钉 Gemini 侧原生优先。
func TestGeminiNativeThinkingPrecedesReasoning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		thinking   ThinkingConfig
		effort     string
		model      string
		wantBudget float64 // -1 = dynamic block present; math.MinInt = block must be absent
	}{
		{name: "native enabled beats disabled effort", thinking: ThinkingConfig{Type: "enabled", BudgetTokens: 2048}, effort: "disabled", model: "gemini-2.5-flash", wantBudget: 2048},
		{name: "native disabled on flash", thinking: ThinkingConfig{Type: "disabled"}, effort: "high", model: "gemini-2.5-flash", wantBudget: 0},
		{name: "native disabled unsupported on pro omits block", thinking: ThinkingConfig{Type: "disabled"}, effort: "high", model: "gemini-2.5-pro", wantBudget: math.MinInt},
		{name: "native adaptive is dynamic", thinking: ThinkingConfig{Type: "adaptive"}, effort: "high", model: "gemini-2.5-flash", wantBudget: -1},
		{name: "native enabled without budget is dynamic", thinking: ThinkingConfig{Type: "enabled"}, effort: "high", model: "gemini-2.5-flash", wantBudget: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &InternalRequest{Model: tc.model, MaxTokens: 4096,
				Thinking: &tc.thinking, Reasoning: &ReasoningConfig{Effort: tc.effort}}
			body, err := SerializeGemini(req)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Gen struct {
					TC map[string]any `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if tc.wantBudget == math.MinInt {
				if len(out.Gen.TC) > 0 {
					t.Fatalf("unsupported native disable must omit thinkingConfig: %s", body)
				}
				return
			}
			got, ok := out.Gen.TC["thinkingBudget"]
			if !ok {
				t.Fatalf("thinkingConfig missing: %s", body)
			}
			if got != tc.wantBudget {
				t.Fatalf("thinkingBudget=%v, want %v: %s", got, tc.wantBudget, body)
			}
		})
	}
}
