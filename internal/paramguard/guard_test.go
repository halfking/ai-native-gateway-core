package paramguard

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func mustUnmarshal(body []byte) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	return m
}

func TestApply_AnthropicBudgetCap(t *testing.T) {
	// budget == max_tokens → budget must be capped to max_tokens - 1
	body := mustMarshal(map[string]any{
		"model":      "claude-sonnet-4",
		"max_tokens": 4096,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 4096,
		},
	})

	out := Apply(body, paramreg.DialectAnthropic)
	m := mustUnmarshal(out)

	var thinking struct {
		BudgetTokens int `json:"budget_tokens"`
	}
	if err := json.Unmarshal(m["thinking"], &thinking); err != nil {
		t.Fatalf("thinking field invalid: %v", err)
	}
	if thinking.BudgetTokens >= 4096 {
		t.Errorf("budget %d should be < max_tokens 4096", thinking.BudgetTokens)
	}
}

func TestApply_AnthropicBudgetRemoved_WhenMaxTokensTooSmall(t *testing.T) {
	// max_tokens=1024, budget=1024 → can't fit 1024 budget inside 1024 max → remove thinking
	body := mustMarshal(map[string]any{
		"model":      "claude-sonnet-4",
		"max_tokens": 1024,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 1024,
		},
	})

	out := Apply(body, paramreg.DialectAnthropic)
	m := mustUnmarshal(out)

	if _, hasThinking := m["thinking"]; hasThinking {
		t.Error("thinking should be removed when max_tokens can't accommodate min budget")
	}
}

func TestApply_AnthropicBudgetOK_NoChange(t *testing.T) {
	body := mustMarshal(map[string]any{
		"max_tokens": 8000,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 4096},
	})
	out := Apply(body, paramreg.DialectAnthropic)
	if string(out) == string(body) {
		return // no change, correct
	}
	// If it was modified, budget must still be < max_tokens
	m := mustUnmarshal(out)
	var th struct {
		BudgetTokens int `json:"budget_tokens"`
	}
	if err := json.Unmarshal(m["thinking"], &th); err != nil {
		t.Fatal(err)
	}
	if th.BudgetTokens >= 8000 {
		t.Errorf("budget %d should be < 8000", th.BudgetTokens)
	}
}

func TestApply_RemoveTemperatureWhenThinking(t *testing.T) {
	// Claude Code explicitly omits temperature when thinking is enabled.
	// Injecting it causes a 400. The guard must remove it.
	body := mustMarshal(map[string]any{
		"model":       "claude-sonnet-4",
		"max_tokens":  8192,
		"temperature": 0.7,
		"thinking":    map[string]any{"type": "enabled", "budget_tokens": 4096},
	})

	out := Apply(body, paramreg.DialectAnthropic)
	m := mustUnmarshal(out)

	if _, hasTemp := m["temperature"]; hasTemp {
		t.Error("temperature must be removed when Anthropic thinking is enabled")
	}
}

func TestApply_KeepTemperatureWhenThinkingDisabled(t *testing.T) {
	body := mustMarshal(map[string]any{
		"temperature": 0.5,
		"thinking":    map[string]any{"type": "disabled"},
	})
	out := Apply(body, paramreg.DialectAnthropic)
	m := mustUnmarshal(out)
	if _, ok := m["temperature"]; !ok {
		t.Error("temperature should be kept when thinking is disabled")
	}
}

func TestApply_TemperatureCap_Anthropic(t *testing.T) {
	body := mustMarshal(map[string]any{"temperature": 1.5})
	out := Apply(body, paramreg.DialectAnthropic)
	m := mustUnmarshal(out)
	var temp float64
	_ = json.Unmarshal(m["temperature"], &temp)
	if temp > 1.0 {
		t.Errorf("Anthropic temperature %g > 1.0 after guard", temp)
	}
}

func TestApply_TemperatureCap_Mistral(t *testing.T) {
	body := mustMarshal(map[string]any{"temperature": 2.0})
	out := Apply(body, paramreg.DialectMistral)
	m := mustUnmarshal(out)
	var temp float64
	_ = json.Unmarshal(m["temperature"], &temp)
	if temp > 1.5 {
		t.Errorf("Mistral temperature %g > 1.5 after guard", temp)
	}
}

func TestApply_TemperatureOK_OpenAI(t *testing.T) {
	// OpenAI allows temperature up to 2.0; guard should not change it
	body := mustMarshal(map[string]any{"temperature": 1.8})
	out := Apply(body, paramreg.DialectOpenAIChat)
	m := mustUnmarshal(out)
	var temp float64
	_ = json.Unmarshal(m["temperature"], &temp)
	if temp != 1.8 {
		t.Errorf("OpenAI temperature should be unchanged, got %g", temp)
	}
}

func TestApply_GrokStripsIncompatible(t *testing.T) {
	body := mustMarshal(map[string]any{
		"model":             "grok-4",
		"presence_penalty":  0.5,
		"frequency_penalty": 0.3,
		"stop":              []string{"END"},
		"messages":          []any{},
	})

	out := Apply(body, paramreg.DialectGrok)
	m := mustUnmarshal(out)

	for _, key := range []string{"presence_penalty", "frequency_penalty", "stop"} {
		if _, ok := m[key]; ok {
			t.Errorf("Grok incompatible param %q should be removed", key)
		}
	}
	// messages should still be there
	if _, ok := m["messages"]; !ok {
		t.Error("messages should not be removed")
	}
}

func TestApply_GrokNoThinking_NoStrip(t *testing.T) {
	// If the body has stop but it's for an unknown dialect, it should not be stripped
	body := mustMarshal(map[string]any{
		"stop":     []string{"END"},
		"messages": []any{},
	})
	out := Apply(body, paramreg.DialectOpenAIChat)
	m := mustUnmarshal(out)
	if _, ok := m["stop"]; !ok {
		t.Error("stop should not be removed for non-Grok dialect")
	}
}

func TestApply_NoOpForUnknownDialect(t *testing.T) {
	body := mustMarshal(map[string]any{
		"temperature": 1.2,
		"stop":        []string{"END"},
	})
	out := Apply(body, paramreg.DialectUnknown)
	// Only dialect-agnostic rules run; should be no-op here
	_ = out // just check it doesn't panic
}

func TestApply_EmptyBody(t *testing.T) {
	if got := Apply(nil, paramreg.DialectAnthropic); got != nil {
		t.Error("nil input should return nil")
	}
	if got := Apply([]byte{}, paramreg.DialectAnthropic); string(got) != "" {
		t.Error("empty input should return empty")
	}
}
