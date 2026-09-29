package ir

import (
	"encoding/json"
	"testing"
)

// R23-A regression: the Type > Budget > Effort precedence refactor
// (d55a0b115) stopped reading Reasoning.BudgetTokens in the explicit-Type
// branch, silently dropping an explicit positive budget.
// parse_gemini.go constructs exactly {Type:"enabled", BudgetTokens:X} for any
// positive thinkingBudget, so this is a production input shape, not a
// synthetic one:
//   - Anthropic requires budget_tokens whenever thinking.type=enabled; the
//     dropped budget produced {"type":"enabled"} alone → upstream 400.
//   - Gemini fell back to the -1 dynamic default, discarding the caller's cap.

func TestExplicitTypeKeepsExplicitBudgetAnthropic(t *testing.T) {
	budget := 5000
	req := &InternalRequest{
		Model:     "claude-sonnet-4-5",
		Reasoning: &ReasoningConfig{Type: "enabled", BudgetTokens: &budget},
	}
	raw, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var out struct {
		Thinking struct {
			Type         string `json:"type"`
			BudgetTokens *int   `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Thinking.Type != "enabled" {
		t.Fatalf("type = %q, want enabled", out.Thinking.Type)
	}
	if out.Thinking.BudgetTokens == nil {
		t.Fatalf("budget_tokens missing (Anthropic rejects enabled without budget); raw=%s", raw)
	}
	if *out.Thinking.BudgetTokens != budget {
		t.Fatalf("budget_tokens = %d, want %d", *out.Thinking.BudgetTokens, budget)
	}
}

func TestExplicitEnabledWithoutBudgetStillGetsDefaultAnthropic(t *testing.T) {
	// parse_gemini maps negative thinkingBudget to {Type:"enabled"} with no
	// budget: enabled must still carry the default budget (API contract).
	req := &InternalRequest{
		Model:     "claude-sonnet-4-5",
		Reasoning: &ReasoningConfig{Type: "enabled"},
	}
	raw, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var out struct {
		Thinking struct {
			Type         string `json:"type"`
			BudgetTokens *int   `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Thinking.Type != "enabled" || out.Thinking.BudgetTokens == nil {
		t.Fatalf("want {enabled, default budget}, got raw=%s", raw)
	}
}

func TestExplicitTypeKeepsExplicitBudgetGemini(t *testing.T) {
	budget := 5000
	req := &InternalRequest{
		Model:     "gemini-2.5-pro",
		Reasoning: &ReasoningConfig{Type: "enabled", BudgetTokens: &budget},
	}
	raw, err := SerializeGemini(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var out struct {
		GenerationConfig struct {
			ThinkingConfig struct {
				ThinkingBudget *int `json:"thinkingBudget"`
			} `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tb := out.GenerationConfig.ThinkingConfig.ThinkingBudget
	if tb == nil {
		t.Fatalf("thinkingConfig.thinkingBudget missing; raw=%s", raw)
	}
	if *tb != budget {
		t.Fatalf("thinkingBudget = %d, want %d (explicit cap must not fall back to -1 dynamic)", *tb, budget)
	}
}

func TestExplicitTypeAndZeroBudgetAnthropicStillEnabledWithDefault(t *testing.T) {
	// Explicit Type beats the zero-budget disable intent; enabled must not be
	// emitted without a budget in any shape.
	zero := 0
	req := &InternalRequest{
		Model:     "claude-sonnet-4-5",
		Reasoning: &ReasoningConfig{Type: "enabled", BudgetTokens: &zero},
	}
	raw, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var out struct {
		Thinking struct {
			Type         string `json:"type"`
			BudgetTokens *int   `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Thinking.Type != "enabled" || out.Thinking.BudgetTokens == nil {
		t.Fatalf("want {enabled, default budget} (Type wins over zero), got raw=%s", raw)
	}
}
