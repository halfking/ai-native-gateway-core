// Package reasoncap resolves a model's reasoning/thinking capability from three
// tiers, highest priority first:
//
//  1. models_canonical.reasoning_caps (JSONB column — operator hot-override)
//  2. Name-pattern matching (reasoning_defaults.go — built-in coverage)
//  3. Provider protocol default (fallback — conservative "not supported")
//
// Important: callers must pass the RAW (pre-normalisation) model name.
// modelname.GenerateAliasVariants strips "thinking"/"reasoning" tokens, so
// calling Resolve after normalisation would destroy the signal.
//
// Design rationale: docs/参数全量兼容/02-目标架构.md §3.2.
package reasoncap

import (
	"context"
	"strings"
)

// Dialect describes which wire-format the model uses for reasoning.
// This mirrors paramreg.Dialect but avoids an import cycle:
// reasoncap → paramreg is fine; paramreg must not depend on reasoncap.
type Dialect string

const (
	DialectNone       Dialect = ""            // no reasoning capability
	DialectOpenAI     Dialect = "openai"      // reasoning_effort enum
	DialectAnthropic  Dialect = "anthropic"   // thinking{type,budget_tokens}
	DialectGemini25   Dialect = "gemini25"    // thinkingConfig{thinkingBudget,includeThoughts}
	DialectGemini3    Dialect = "gemini3"     // thinkingConfig{thinkingLevel}
	DialectDeepSeek   Dialect = "deepseek"    // thinking{type:enabled|disabled}
	DialectGLM        Dialect = "glm"         // thinking{type} + reasoning_effort 7-tier
	DialectQwen       Dialect = "qwen"        // enable_thinking + thinking_budget
	DialectKimiThink  Dialect = "kimi_think"  // thinking{keep}
	DialectKimiEffort Dialect = "kimi_effort" // reasoning_effort low|high|max
	DialectMiniMax    Dialect = "minimax"     // thinking{type:disabled|adaptive}
	DialectArk        Dialect = "ark"         // thinking{type:enabled|disabled|auto}
	DialectGrok       Dialect = "grok"        // reasoning_effort none|low|medium|high
	DialectMistral    Dialect = "mistral"     // reasoning_effort + prompt_mode:reasoning
	DialectVLLM       Dialect = "vllm"        // thinking_token_budget or chat_template_kwargs
	DialectOllama     Dialect = "ollama"      // think: bool
)

// Caps describes a model's thinking/reasoning capability.
type Caps struct {
	// Supported reports whether the model has any reasoning capability at all.
	Supported bool

	// Dialect is the wire-format used to activate/configure reasoning.
	Dialect Dialect

	// Efforts is the set of accepted reasoning_effort values, ordered low→high.
	// Empty means the model does not accept an effort enum (uses budget instead).
	Efforts []string

	// BudgetMin / BudgetMax are the allowed thinking-token-budget range.
	// Both 0 means the model does not accept a numeric budget.
	BudgetMin int
	BudgetMax int

	// CanDisable reports whether reasoning can be explicitly turned off.
	// Example: grok-4.5 cannot disable reasoning (always on).
	CanDisable bool

	// Adaptive reports whether the model supports {"type":"adaptive"} (no
	// fixed budget; model decides). Anthropic claude-3-7+ supports this.
	Adaptive bool

	// HistoryField is the field name used to control whether prior reasoning
	// turns are preserved in context. Empty = no such control.
	// Examples: "clear_thinking" (GLM), "keep" (Kimi), "exclude" (OpenRouter).
	HistoryField string

	// Source records which tier produced these Caps, for debugging and audit.
	// Values: "db", "pattern", "provider_default", "none"
	Source string
}

// Unsupported is the zero value for a model with no reasoning capability.
var Unsupported = Caps{Supported: false, Source: "none"}

// DBSource is the interface for tier-1 DB lookup. Implementations should
// return (nil, nil) when the model has no DB entry (falling through to tier 2).
//
// The JSONB schema stored in models_canonical.reasoning_caps is intentionally
// identical to Caps minus the Source field, for simple unmarshalling.
type DBSource interface {
	LookupReasoningCaps(ctx context.Context, canonicalModel string) (*Caps, error)
}

// Resolve returns the reasoning Caps for the given model name.
//
// modelName must be the raw, pre-normalisation name (e.g. "claude-sonnet-4-5-thinking").
// After modelname.Normalize the "thinking" token would be stripped and the
// capability inference would fail silently.
//
// db may be nil — callers that don't have a DB connection fall through to tier 2.
func Resolve(ctx context.Context, modelName string, db DBSource) Caps {
	if modelName == "" {
		return Unsupported
	}

	// Tier 1: DB lookup (operator hot-override, highest priority).
	if db != nil {
		caps, err := db.LookupReasoningCaps(ctx, modelName)
		if err == nil && caps != nil {
			caps.Source = "db"
			return *caps
		}
	}

	// Tier 2: Name-pattern table (built-in, zero-config coverage).
	if caps, ok := inferFromName(modelName); ok {
		caps.Source = "pattern"
		return caps
	}

	// Tier 3: No capability.
	return Unsupported
}

// inferFromName applies the reasoning_defaults.go pattern table to modelName.
// The name is lowercased before matching (same convention as InferModality).
func inferFromName(modelName string) (Caps, bool) {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	if normalized == "" {
		return Caps{}, false
	}

	// Walk priority tiers exactly like InferModality.
	for _, tier := range matchTiers {
		best := ""
		var bestCaps Caps
		found := false

		for i := range reasoningRules {
			r := &reasoningRules[i]
			if r.priority != tier || !ruleMatches(r, normalized) {
				continue
			}
			if len(r.pattern) > len(best) || !found {
				best = r.pattern
				bestCaps = r.caps
				found = true
			}
		}

		if found {
			return bestCaps, true
		}
	}

	return Caps{}, false
}

// ruleMatches checks whether normalized (already lowercased) matches rule.
func ruleMatches(r *reasoningRule, normalized string) bool {
	switch r.priority {
	case 0: // exact
		return normalized == r.pattern
	case 1: // prefix
		return strings.HasPrefix(normalized, r.pattern)
	case 2: // suffix
		return strings.HasSuffix(normalized, r.pattern)
	case 3: // contains
		return strings.Contains(normalized, r.pattern)
	}
	return false
}

// matchTiers iterates priorities 0→3 (exact before prefix before suffix before contains).
var matchTiers = []int{0, 1, 2, 3}
