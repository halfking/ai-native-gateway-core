package reasonnorm

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
)

// anthropicCaps is a typical claude-sonnet-4 capability set.
var anthropicCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectAnthropic,
	BudgetMin: 1024, BudgetMax: 32000, CanDisable: true, Adaptive: true,
}

// openaiCaps is a typical o3 capability set.
var openaiCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectOpenAI,
	Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
	CanDisable: true,
}

var deepseekCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectDeepSeek,
	Efforts: []string{"low", "high", "max"}, CanDisable: true,
}

var gemini25Caps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectGemini25,
	BudgetMin: 1, BudgetMax: 24576, CanDisable: true,
}

var gemini3Caps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectGemini3,
	Efforts: []string{"minimal", "low", "medium", "high"}, CanDisable: true,
}

var qwenCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectQwen,
	BudgetMin: 0, BudgetMax: 38912, CanDisable: true,
}

var minimaxCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectMiniMax, CanDisable: true, Adaptive: true,
}

var arkCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectArk,
	Efforts: []string{"minimal", "low", "medium", "high"}, CanDisable: true,
}

var grokCaps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectGrok,
	Efforts: []string{"none", "low", "medium", "high"}, CanDisable: true,
}

var grok45Caps = reasoncap.Caps{
	Supported: true, Dialect: reasoncap.DialectGrok,
	Efforts: []string{"low", "medium", "high"}, CanDisable: false,
}

// ─── Effort → budget mapping (LiteLLM baseline) ──────────────────────────────

func TestEffortToBudget(t *testing.T) {
	tests := []struct {
		effort string
		want   int
	}{
		{"none", 0}, {"minimal", 1024}, {"low", 1024}, {"medium", 2048},
		{"high", 4096}, {"xhigh", 8192}, {"max", 16384},
	}
	for _, tt := range tests {
		got, ok := EffortToBudget(tt.effort)
		if !ok {
			t.Errorf("EffortToBudget(%q) not found", tt.effort)
		}
		if got != tt.want {
			t.Errorf("EffortToBudget(%q) = %d, want %d", tt.effort, got, tt.want)
		}
	}
	if _, ok := EffortToBudget("unknown_effort"); ok {
		t.Error("unknown effort should return ok=false")
	}
}

func TestBudgetToEffort(t *testing.T) {
	tests := []struct {
		budget int
		want   string
	}{
		{0, "none"}, {512, "low"}, {1024, "low"}, {2048, "medium"},
		{3000, "high"}, {4096, "high"}, {8192, "xhigh"}, {16384, "max"}, {99999, "max"},
	}
	for _, tt := range tests {
		if got := BudgetToEffort(tt.budget); got != tt.want {
			t.Errorf("BudgetToEffort(%d) = %q, want %q", tt.budget, got, tt.want)
		}
	}
}

// ─── ClampEffort ──────────────────────────────────────────────────────────────

func TestClampEffort(t *testing.T) {
	deeps := []string{"low", "high", "max"} // DeepSeek doesn't have medium/xhigh
	tests := []struct{ effort, want string }{
		{"medium", "high"}, // nearest above
		{"low", "low"},     // exact
		{"high", "high"},   // exact
		{"xhigh", "max"},   // clamp to max
		{"none", "low"},    // below set → lowest
		{"max", "max"},
		{"minimal", "low"},
	}
	for _, tt := range tests {
		if got := ClampEffort(tt.effort, deeps); got != tt.want {
			t.Errorf("ClampEffort(%q, deepseek) = %q, want %q", tt.effort, got, tt.want)
		}
	}
	// Empty supported → return ""
	if got := ClampEffort("medium", nil); got != "" {
		t.Errorf("ClampEffort with nil supported = %q, want \"\"", got)
	}
}

// ─── Empty intent produces empty result ──────────────────────────────────────

func TestRender_EmptyIntent(t *testing.T) {
	r := Render(Intent{}, anthropicCaps, 32000)
	if !r.IsEmpty() {
		t.Error("empty intent should produce empty result")
	}
}

// ─── Anthropic ────────────────────────────────────────────────────────────────

func TestRender_Anthropic_EffortToThinking(t *testing.T) {
	// medium effort → budget 2048
	r := Render(Intent{Mode: ModeEnabled, Effort: "medium"}, anthropicCaps, 32000)
	if r.Thinking == nil {
		t.Fatal("Anthropic: expected thinking block")
	}
	if r.Thinking.Type != "enabled" {
		t.Errorf("type = %q, want enabled", r.Thinking.Type)
	}
	if r.Thinking.BudgetTokens != 2048 {
		t.Errorf("budget = %d, want 2048", r.Thinking.BudgetTokens)
	}
}

func TestRender_Anthropic_BudgetPassthrough(t *testing.T) {
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 5000}, anthropicCaps, 32000)
	if r.Thinking == nil || r.Thinking.BudgetTokens != 5000 {
		t.Errorf("budget passthrough failed: %+v", r.Thinking)
	}
}

func TestRender_Anthropic_BudgetCapToMaxTokens(t *testing.T) {
	// budget must be < max_tokens (Anthropic spec + LiteLLM transformation.py:1246)
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 4000}, anthropicCaps, 4000)
	if r.Thinking == nil {
		t.Fatal("expected thinking block")
	}
	if r.Thinking.BudgetTokens >= 4000 {
		t.Errorf("budget %d should be < max_tokens 4000", r.Thinking.BudgetTokens)
	}
}

func TestRender_Anthropic_BudgetFloor(t *testing.T) {
	// Very small budget must be raised to the 1024 floor.
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 100}, anthropicCaps, 32000)
	if r.Thinking == nil || r.Thinking.BudgetTokens < AnthropicMinBudget {
		t.Errorf("budget should be at least %d, got %d", AnthropicMinBudget, func() int {
			if r.Thinking == nil {
				return 0
			}
			return r.Thinking.BudgetTokens
		}())
	}
}

func TestRender_Anthropic_Adaptive(t *testing.T) {
	r := Render(Intent{Mode: ModeAdaptive}, anthropicCaps, 32000)
	if r.Thinking == nil || r.Thinking.Type != "adaptive" {
		t.Errorf("adaptive mode: %+v", r.Thinking)
	}
}

func TestRender_Anthropic_Disabled(t *testing.T) {
	r := Render(Intent{Mode: ModeDisabled}, anthropicCaps, 32000)
	if r.Thinking == nil || r.Thinking.Type != "disabled" {
		t.Errorf("disabled mode: %+v", r.Thinking)
	}
}

// ─── OpenAI Chat ──────────────────────────────────────────────────────────────

func TestRender_OpenAI_EffortPassthrough(t *testing.T) {
	r := Render(Intent{Mode: ModeEnabled, Effort: "high"}, openaiCaps, 0)
	if r.ReasoningEffort != "high" {
		t.Errorf("effort = %q, want high", r.ReasoningEffort)
	}
}

func TestRender_OpenAI_BudgetToEffort(t *testing.T) {
	// budget 8192 → xhigh — this fills the "budget-only → OpenAI" gap
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 8192}, openaiCaps, 0)
	if r.ReasoningEffort != "xhigh" {
		t.Errorf("effort = %q, want xhigh (from budget 8192)", r.ReasoningEffort)
	}
}

func TestRender_OpenAI_EffortClamped(t *testing.T) {
	// "none" should be clamped when targeting Grok-4.5 which cannot disable
	r := Render(Intent{Mode: ModeEnabled, Effort: "none"}, grok45Caps, 0)
	if r.ReasoningEffort == "none" {
		t.Error("Grok 4.5 cannot disable, 'none' should be clamped")
	}
}

func TestRender_OpenAI_Disabled(t *testing.T) {
	r := Render(Intent{Mode: ModeDisabled}, openaiCaps, 0)
	if r.ReasoningEffort != "none" {
		t.Errorf("disabled → effort = %q, want none", r.ReasoningEffort)
	}
}

// ─── DeepSeek ─────────────────────────────────────────────────────────────────

func TestRender_DeepSeek_Enabled(t *testing.T) {
	r := Render(Intent{Mode: ModeEnabled}, deepseekCaps, 0)
	if r.ThinkingObject == nil || r.ThinkingObject.Type != "enabled" {
		t.Errorf("deepseek enabled: %+v", r.ThinkingObject)
	}
}

func TestRender_DeepSeek_Disabled(t *testing.T) {
	r := Render(Intent{Mode: ModeDisabled}, deepseekCaps, 0)
	if r.ThinkingObject == nil || r.ThinkingObject.Type != "disabled" {
		t.Errorf("deepseek disabled: %+v", r.ThinkingObject)
	}
}

// ─── Gemini 2.5 ───────────────────────────────────────────────────────────────

func TestRender_Gemini25_BudgetPassthrough(t *testing.T) {
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 4096}, gemini25Caps, 0)
	if r.ThinkingConfig25 == nil || r.ThinkingConfig25.ThinkingBudget != 4096 {
		t.Errorf("gemini25 budget: %+v", r.ThinkingConfig25)
	}
	if !r.ThinkingConfig25.IncludeThoughts {
		t.Error("includeThoughts should default to true")
	}
}

func TestRender_Gemini25_EffortToBudget(t *testing.T) {
	// effort "high" → budget 4096
	r := Render(Intent{Mode: ModeEnabled, Effort: "high"}, gemini25Caps, 0)
	if r.ThinkingConfig25 == nil || r.ThinkingConfig25.ThinkingBudget != 4096 {
		t.Errorf("gemini25 effort→budget: %+v", r.ThinkingConfig25)
	}
}

func TestRender_Gemini25_Disabled(t *testing.T) {
	r := Render(Intent{Mode: ModeDisabled}, gemini25Caps, 0)
	if r.ThinkingConfig25 == nil || r.ThinkingConfig25.ThinkingBudget != 0 {
		t.Errorf("gemini25 disabled (budget sentinel 0): %+v", r.ThinkingConfig25)
	}
}

// ─── Gemini 3 ─────────────────────────────────────────────────────────────────

func TestRender_Gemini3_Level(t *testing.T) {
	r := Render(Intent{Mode: ModeEnabled, Effort: "medium"}, gemini3Caps, 0)
	if r.ThinkingConfig3 == nil || r.ThinkingConfig3.ThinkingLevel != "medium" {
		t.Errorf("gemini3 level: %+v", r.ThinkingConfig3)
	}
}

func TestRender_Gemini3_BudgetToLevel(t *testing.T) {
	// budget 4096 → effort "high"
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 4096}, gemini3Caps, 0)
	if r.ThinkingConfig3 == nil || r.ThinkingConfig3.ThinkingLevel != "high" {
		t.Errorf("gemini3 budget→level: %+v", r.ThinkingConfig3)
	}
}

// ─── Qwen ─────────────────────────────────────────────────────────────────────

func TestRender_Qwen_EnableWithBudget(t *testing.T) {
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 16384}, qwenCaps, 0)
	if r.EnableThinking == nil || !*r.EnableThinking {
		t.Error("qwen: enable_thinking should be true")
	}
	if r.ThinkingBudget == nil || *r.ThinkingBudget != 16384 {
		t.Errorf("qwen budget: %v", r.ThinkingBudget)
	}
}

func TestRender_Qwen_Disable(t *testing.T) {
	r := Render(Intent{Mode: ModeDisabled}, qwenCaps, 0)
	if r.EnableThinking == nil || *r.EnableThinking {
		t.Error("qwen disabled: enable_thinking should be false")
	}
}

// ─── MiniMax ──────────────────────────────────────────────────────────────────

func TestRender_MiniMax_UsesAdaptive(t *testing.T) {
	// MiniMax has no "enabled" value — must use "adaptive"
	r := Render(Intent{Mode: ModeEnabled}, minimaxCaps, 0)
	if r.ThinkingObject == nil || r.ThinkingObject.Type != "adaptive" {
		t.Errorf("minimax should use adaptive (not enabled): %+v", r.ThinkingObject)
	}
}

func TestRender_MiniMax_Disabled(t *testing.T) {
	r := Render(Intent{Mode: ModeDisabled}, minimaxCaps, 0)
	if r.ThinkingObject == nil || r.ThinkingObject.Type != "disabled" {
		t.Errorf("minimax disabled: %+v", r.ThinkingObject)
	}
}

// ─── Ark ──────────────────────────────────────────────────────────────────────

func TestRender_Ark_AdaptiveUsesAuto(t *testing.T) {
	// Ark supports "auto" for adaptive mode
	r := Render(Intent{Mode: ModeAdaptive}, arkCaps, 0)
	if r.ThinkingObject == nil || r.ThinkingObject.Type != "auto" {
		t.Errorf("ark adaptive should use auto: %+v", r.ThinkingObject)
	}
}

// ─── Grok effort enum ─────────────────────────────────────────────────────────

func TestRender_Grok_EffortClamped(t *testing.T) {
	// Grok doesn't have "xhigh" or "max" → clamped to "high"
	r := Render(Intent{Mode: ModeEnabled, Effort: "max"}, grokCaps, 0)
	if r.ReasoningEffort != "high" {
		t.Errorf("grok max→clamped: %q, want high", r.ReasoningEffort)
	}
}

// ─── Cross-dialect gaps filled (key invariants from audit doc) ────────────────

func TestRender_BudgetOnlyToOpenAI_NotSilentlyDropped(t *testing.T) {
	// Before P5, a budget-only config from Anthropic → OpenAI produced nothing.
	// This test pins the fixed behaviour.
	r := Render(Intent{Mode: ModeEnabled, BudgetTokens: 8192}, openaiCaps, 0)
	if r.ReasoningEffort == "" {
		t.Error("budget-only intent targeting OpenAI should produce reasoning_effort (xhigh from 8192)")
	}
}

func TestRender_EffortOnlyToGemini_NotSilentlyDropped(t *testing.T) {
	// Before P5, an effort-only config targeting Gemini produced nothing.
	r := Render(Intent{Mode: ModeEnabled, Effort: "high"}, gemini25Caps, 0)
	if r.ThinkingConfig25 == nil || r.ThinkingConfig25.ThinkingBudget == 0 {
		t.Error("effort-only intent targeting Gemini 2.5 should produce thinkingBudget (4096 from high)")
	}
}
