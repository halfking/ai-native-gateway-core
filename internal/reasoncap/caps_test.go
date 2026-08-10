package reasoncap

import (
	"context"
	"testing"
)

// stubDB is a test implementation of DBSource.
type stubDB struct {
	data map[string]*Caps
}

func (s *stubDB) LookupReasoningCaps(_ context.Context, model string) (*Caps, error) {
	if c, ok := s.data[model]; ok {
		return c, nil
	}
	return nil, nil
}

func TestResolve_DBTierWins(t *testing.T) {
	db := &stubDB{data: map[string]*Caps{
		"custom-model": {Supported: true, Dialect: DialectOpenAI, Efforts: []string{"low", "high"}},
	}}
	caps := Resolve(context.Background(), "custom-model", db)
	if caps.Source != "db" {
		t.Errorf("source = %q, want db", caps.Source)
	}
	if caps.Dialect != DialectOpenAI {
		t.Errorf("dialect = %q, want openai", caps.Dialect)
	}
}

func TestResolve_PatternTierFallback(t *testing.T) {
	caps := Resolve(context.Background(), "claude-sonnet-4-20250514", nil)
	if !caps.Supported {
		t.Error("claude-sonnet-4 should be supported")
	}
	if caps.Dialect != DialectAnthropic {
		t.Errorf("dialect = %q, want anthropic", caps.Dialect)
	}
	if caps.Source != "pattern" {
		t.Errorf("source = %q, want pattern", caps.Source)
	}
}

func TestResolve_UnknownModel(t *testing.T) {
	caps := Resolve(context.Background(), "unknown-llm-v99", nil)
	if caps.Supported {
		t.Error("unknown model should not be supported")
	}
	if caps.Source != "none" {
		t.Errorf("source = %q, want none", caps.Source)
	}
}

func TestResolve_EmptyModel(t *testing.T) {
	caps := Resolve(context.Background(), "", nil)
	if caps.Supported {
		t.Error("empty model should not be supported")
	}
}

// TestPatterns is the comprehensive pattern-table test.
// Each entry documents the exact expectation and its source.
func TestPatterns(t *testing.T) {
	tests := []struct {
		name    string // raw model name (PRE-normalisation)
		dialect Dialect
		want    bool
	}{
		// ─── OpenAI ───────────────────────────────────────────────────────
		{"o1", DialectOpenAI, true},
		{"o3", DialectOpenAI, true},
		{"o4", DialectOpenAI, true},
		{"o1-mini", DialectOpenAI, true},          // prefix o1-
		{"o3-mini", DialectOpenAI, true},          // prefix o3-
		{"o4-mini-2025-04-16", DialectOpenAI, true}, // prefix o4-
		{"gpt-5", DialectOpenAI, true},
		{"gpt-5-turbo", DialectOpenAI, true},
		{"gpt-4o", DialectNone, false}, // standard gpt-4o has no reasoning
		// ─── Anthropic ────────────────────────────────────────────────────
		{"claude-3-7-sonnet-20250219", DialectAnthropic, true},
		{"claude-3-7-sonnet-latest", DialectAnthropic, true},
		{"claude-sonnet-4-20250514", DialectAnthropic, true},
		{"claude-sonnet-4-5", DialectAnthropic, true},
		{"claude-opus-4-20250514", DialectAnthropic, true},
		{"claude-opus-5-20260101", DialectAnthropic, true},
		{"claude-haiku-4-20250514", DialectAnthropic, true},
		// "-thinking" suffix — must be caught BEFORE alias normalisation strips the token
		{"claude-sonnet-4-5-thinking", DialectAnthropic, true},
		{"claude-3-7-sonnet-thinking", DialectAnthropic, true},
		// ─── Gemini ───────────────────────────────────────────────────────
		{"gemini-2.5-flash", DialectGemini25, true},
		{"gemini-2.5-flash-lite", DialectGemini25, true},
		{"gemini-2.5-flash-lite-preview", DialectGemini25, true},
		{"gemini-2.5-pro", DialectGemini25, true},
		{"gemini-2.5-pro-exp-03-25", DialectGemini25, true},
		{"gemini-2.5-0528", DialectGemini25, true}, // other 2.5 variants
		{"gemini-3-pro", DialectGemini3, true},
		{"gemini-3-flash", DialectGemini3, true},
		{"gemini-pro", DialectNone, false}, // legacy gemini-pro has no thinking
		// ─── DeepSeek ─────────────────────────────────────────────────────
		{"deepseek-reasoner", DialectDeepSeek, true},
		{"deepseek-r1-distill-qwen-7b", DialectDeepSeek, true},
		{"deepseek-r1-distill-llama-70b", DialectDeepSeek, true},
		{"deepseek-v4-pro", DialectDeepSeek, true},
		{"deepseek-v4-flash", DialectDeepSeek, true},
		{"deepseek-chat", DialectNone, false}, // standard deepseek-chat, no thinking
		// "-reasoner" suffix catches OSS variants
		{"deepseek-v3-reasoner", DialectDeepSeek, true},
		// ─── GLM ──────────────────────────────────────────────────────────
		{"glm-4.5-air", DialectGLM, true},
		{"glm-4.5-flash", DialectGLM, true},
		{"glm-5-plus", DialectGLM, true},
		{"glm-z1-flash", DialectGLM, true},
		{"glm-z1-rumination-online", DialectGLM, true},
		{"glm-4", DialectNone, false}, // older GLM without thinking
		// ─── Qwen ─────────────────────────────────────────────────────────
		{"qwq-32b", DialectQwen, true},
		{"qwq-32b-preview", DialectQwen, true},
		{"qwen3-235b-a22b", DialectQwen, true},
		{"qwen3-7b", DialectQwen, true},
		// ─── Kimi ─────────────────────────────────────────────────────────
		{"kimi-k2", DialectKimiThink, true},
		{"kimi-k2-0528", DialectKimiThink, true},
		{"kimi-k3", DialectKimiEffort, true},
		{"kimi-k3-0528", DialectKimiEffort, true},
		// ─── MiniMax ──────────────────────────────────────────────────────
		{"minimax-m3-8k", DialectMiniMax, true},
		{"minimax-m3", DialectMiniMax, true},
		// ─── Ark / Doubao ─────────────────────────────────────────────────
		{"doubao-pro-thinking-32k", DialectArk, true},
		// ─── Mistral ──────────────────────────────────────────────────────
		{"magistral-8b", DialectMistral, true},
		{"magistral-medium-latest", DialectMistral, true},
		{"mistral-medium-3-latest", DialectMistral, true},
		// ─── Grok ─────────────────────────────────────────────────────────
		{"grok-3-mini", DialectGrok, true},
		{"grok-3-mini-fast", DialectGrok, true},
		{"grok-4", DialectGrok, true},
		{"grok-4.5", DialectGrok, true},
		{"grok-4-latest", DialectGrok, true},
		{"grok-2", DialectNone, false}, // grok-2 has no reasoning
		// ─── vLLM / open-weight "-r1" suffix ──────────────────────────────
		{"llama-3-70b-r1", DialectVLLM, true},
		{"qwen2.5-72b-r1", DialectVLLM, true},
		// ─── Non-reasoning models (must not false-positive) ────────────────
		{"gpt-4o", DialectNone, false},
		{"gpt-4-turbo", DialectNone, false},
		{"gpt-3.5-turbo", DialectNone, false},
		{"claude-2", DialectNone, false},
		{"claude-instant-1", DialectNone, false},
		{"gemini-1.5-pro", DialectNone, false},
		{"text-embedding-ada-002", DialectNone, false},
		{"mistral-7b", DialectNone, false},
		{"mixtral-8x7b", DialectNone, false},
		{"deepseek-coder", DialectNone, false},
	}

	for _, tt := range tests {
		caps, _ := inferFromName(tt.name)
		if caps.Supported != tt.want {
			t.Errorf("inferFromName(%q): supported=%v, want %v (dialect=%q)",
				tt.name, caps.Supported, tt.want, caps.Dialect)
			continue
		}
		if tt.want && caps.Dialect != tt.dialect {
			t.Errorf("inferFromName(%q): dialect=%q, want %q",
				tt.name, caps.Dialect, tt.dialect)
		}
	}
}

// TestAdaptiveModels verifies models known to support adaptive thinking.
func TestAdaptiveModels(t *testing.T) {
	for _, model := range []string{
		"claude-3-7-sonnet-20250219",
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-sonnet-4-5-thinking",
	} {
		caps, ok := inferFromName(model)
		if !ok || !caps.Adaptive {
			t.Errorf("%q should support adaptive thinking", model)
		}
	}
}

// TestGrok45CannotDisable verifies grok-4.5's cannot-disable constraint.
func TestGrok45CannotDisable(t *testing.T) {
	caps, ok := inferFromName("grok-4.5")
	if !ok {
		t.Fatal("grok-4.5 should match")
	}
	if caps.CanDisable {
		t.Error("grok-4.5 should not be disableable")
	}
	// Other grok-4 variants can disable
	caps2, _ := inferFromName("grok-4")
	if !caps2.CanDisable {
		t.Error("grok-4 should be disableable")
	}
}

// TestGemini25BudgetMinima verifies the per-model minimum budget differences.
func TestGemini25BudgetMinima(t *testing.T) {
	tests := []struct {
		model  string
		minBud int
	}{
		{"gemini-2.5-flash", 1},
		{"gemini-2.5-pro", 128},
		{"gemini-2.5-flash-lite", 512},
	}
	for _, tt := range tests {
		caps, ok := inferFromName(tt.model)
		if !ok {
			t.Fatalf("%q should match", tt.model)
		}
		if caps.BudgetMin != tt.minBud {
			t.Errorf("%q BudgetMin=%d, want %d", tt.model, caps.BudgetMin, tt.minBud)
		}
	}
}

// TestRawNameBeforeNormalisation confirms the key design invariant:
// the "-thinking" suffix must be detected before alias normalisation strips it.
func TestRawNameBeforeNormalisation(t *testing.T) {
	raw := "claude-sonnet-4-5-thinking"
	caps, ok := inferFromName(raw)
	if !ok || !caps.Supported {
		t.Errorf("raw name %q should be detected as thinking-capable", raw)
	}
	// Simulate what modelname.GenerateAliasVariants would produce:
	// "claude-sonnet-4-5" (no "thinking" token).
	// If you call inferFromName on the normalised name, it must STILL work
	// via the prefix rule on "claude-sonnet-4".
	normalised := "claude-sonnet-4-5"
	caps2, ok2 := inferFromName(normalised)
	if !ok2 || !caps2.Supported {
		t.Errorf("normalised name %q should also match via prefix rule", normalised)
	}
}

// TestHistoryFields verifies vendors that need history-preservation controls.
func TestHistoryFields(t *testing.T) {
	tests := []struct {
		model string
		field string
	}{
		{"glm-4.5-flash", "clear_thinking"},
		{"qwq-32b", "preserve_thinking"},
		{"kimi-k2-0528", "keep"},
	}
	for _, tt := range tests {
		caps, ok := inferFromName(tt.model)
		if !ok {
			t.Fatalf("%q should match", tt.model)
		}
		if caps.HistoryField != tt.field {
			t.Errorf("%q HistoryField=%q, want %q", tt.model, caps.HistoryField, tt.field)
		}
	}
}
