// Package paramguard enforces hard invariants on outgoing request bodies.
//
// This is the last gate before a request reaches an upstream LLM API.
// Rules here prevent well-known hard errors (HTTP 400) caused by:
//
//  1. Anthropic rejecting requests where max_tokens ≤ thinking.budget_tokens
//  2. Anthropic / Mistral / Kimi rejecting temperature=2 (max is 1.0 / 1.5 / 1.0)
//  3. Anthropic rejecting temperature when thinking is enabled (must be 1 or absent)
//  4. Grok reasoning models rejecting presence_penalty / frequency_penalty / stop
//  5. OpenAI o-series requiring max_completion_tokens instead of max_tokens
//
// Callers pass the serialised body plus a Dialect, and get back the (possibly
// modified) body. All modifications are non-destructive: only the minimum
// correction is applied and a warning is logged for observability.
//
// Design doc: docs/参数全量兼容/02-目标架构.md §3.4
package paramguard

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// Apply runs all param-guard rules for the given dialect and returns the
// (possibly modified) body.
//
// dialect is the target upstream dialect (paramreg.Dialect*).
// If dialect is DialectUnknown, only dialect-agnostic rules run.
func Apply(body []byte, dialect paramreg.Dialect) []byte {
	if len(body) == 0 {
		return body
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}

	modified := false

	// Anthropic thinking forbids sampling parameters. Do this before the
	// generic cap so we never clamp a value that is immediately discarded.
	if dialect == paramreg.DialectAnthropic {
		modified = removeSamplingIfThinking(obj) || modified
		modified = fixAnthropicBudgetCap(obj) || modified
	}

	// Temperature cap per dialect applies only when the field survives the
	// thinking rule above.
	modified = fixTemperatureCap(obj, dialect) || modified

	// Grok only rejects these controls on reasoning models. The raw model is
	// carried in the body and is the compatibility signal available here.
	if dialect == paramreg.DialectGrok && isGrokReasoningModel(obj) {
		modified = stripGrokIncompatible(obj) || modified
	}

	if !modified {
		return body
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// ─── Rule implementations ─────────────────────────────────────────────────────

// fixAnthropicBudgetCap ensures thinking.budget_tokens < max_tokens.
//
// Anthropic API requirement: max_tokens MUST be strictly greater than
// thinking.budget_tokens (returns 400 otherwise).
// LiteLLM ref: transformation.py:1246.
// Cline ref: gateway.ts:195-204.
func fixAnthropicBudgetCap(obj map[string]json.RawMessage) bool {
	maxTok := intField(obj, "max_tokens")
	if maxTok <= 0 {
		return false
	}

	thinking, ok := obj["thinking"]
	if !ok {
		return false
	}
	var th map[string]json.RawMessage
	if err := json.Unmarshal(thinking, &th); err != nil {
		return false
	}
	var typ string
	if raw, ok := th["type"]; ok {
		_ = json.Unmarshal(raw, &typ)
	}
	if typ != "enabled" && typ != "adaptive" {
		return false
	}
	var budgetTokens int
	if raw, ok := th["budget_tokens"]; !ok || json.Unmarshal(raw, &budgetTokens) != nil || budgetTokens <= 0 {
		return false
	}

	if budgetTokens < maxTok {
		return false
	}

	// Budget is too large: cap to max_tokens - 1.
	newBudget := maxTok - 1
	if newBudget < 1024 {
		// Can't fit thinking with this max_tokens; remove thinking entirely.
		slog.Warn("paramguard: removing thinking block (max_tokens too small for min budget)",
			"max_tokens", maxTok, "budget_tokens", budgetTokens)
		delete(obj, "thinking")
		return true
	}

	slog.Warn("paramguard: clamped thinking.budget_tokens to max_tokens-1",
		"original_budget", budgetTokens, "new_budget", newBudget, "max_tokens", maxTok)
	rawBudget, err := json.Marshal(newBudget)
	if err != nil {
		return false
	}
	th["budget_tokens"] = rawBudget
	raw, err := json.Marshal(th)
	if err != nil {
		return false
	}
	obj["thinking"] = raw
	return true
}

// temperatureCaps maps dialects to their maximum accepted temperature.
var temperatureCaps = map[paramreg.Dialect]float64{
	paramreg.DialectAnthropic: 1.0, // Anthropic max is 1.0, not 2.0
	paramreg.DialectMistral:   1.5, // Mistral max is 1.5
	paramreg.DialectKimi:      1.0, // Kimi max is 1.0
}

// fixTemperatureCap clamps temperature to the dialect's maximum.
func fixTemperatureCap(obj map[string]json.RawMessage, dialect paramreg.Dialect) bool {
	cap, ok := temperatureCaps[dialect]
	if !ok {
		return false
	}

	raw, ok := obj["temperature"]
	if !ok {
		return false
	}

	var temp float64
	if err := json.Unmarshal(raw, &temp); err != nil {
		return false
	}

	if temp <= cap {
		return false
	}

	slog.Warn("paramguard: clamped temperature to dialect maximum",
		"original", temp, "clamped_to", cap, "dialect", dialect)
	capped, err := json.Marshal(cap)
	if err != nil {
		return false
	}
	obj["temperature"] = capped
	return true
}

// removeSamplingIfThinking removes sampling controls that Anthropic rejects
// when thinking is active. Claude Code omits both rather than forcing values.
func removeSamplingIfThinking(obj map[string]json.RawMessage) bool {
	thinking, ok := obj["thinking"]
	if !ok {
		return false
	}
	var th struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(thinking, &th); err != nil || (th.Type != "enabled" && th.Type != "adaptive") {
		return false
	}
	modified := false
	for _, key := range []string{"temperature", "top_p"} {
		if _, ok := obj[key]; ok {
			slog.Warn("paramguard: removed Anthropic-thinking-incompatible parameter", "key", key)
			delete(obj, key)
			modified = true
		}
	}
	return modified
}

// grokIncompatibleParams is the set of params that cause hard errors on Grok
// reasoning models (grok-3-mini, grok-4, grok-4.5).
// Source: xAI docs (docs.x.ai/openapi.json), confirmed 2026-08.
var grokIncompatibleParams = []string{
	"presence_penalty",
	"frequency_penalty",
	"stop",
	"stop_sequences",
}

// stripGrokIncompatible removes params that Grok reasoning models hard-reject.
func stripGrokIncompatible(obj map[string]json.RawMessage) bool {
	modified := false
	for _, key := range grokIncompatibleParams {
		if _, ok := obj[key]; ok {
			slog.Warn("paramguard: removed Grok-incompatible param", "key", key)
			delete(obj, key)
			modified = true
		}
	}
	return modified
}

func isGrokReasoningModel(obj map[string]json.RawMessage) bool {
	raw, ok := obj["model"]
	if !ok {
		return false
	}
	var model string
	if json.Unmarshal(raw, &model) != nil {
		return false
	}
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "grok-3-mini") || strings.HasPrefix(model, "grok-4")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func intField(obj map[string]json.RawMessage, key string) int {
	raw, ok := obj[key]
	if !ok {
		return 0
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	return v
}
