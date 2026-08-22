// Package reasonnorm implements the six-dialect reasoning normalisation layer.
//
// Problem: clients send reasoning intent in six mutually incompatible shapes:
//
//	OpenAI Chat    reasoning_effort: "medium"
//	OpenAI Resp    reasoning: {effort:"high", summary:"auto", context:"all_turns"}
//	Anthropic      thinking: {type:"enabled", budget_tokens:8192}
//	Gemini 2.5     generationConfig.thinkingConfig: {thinkingBudget:8192, includeThoughts:true}
//	Gemini 3       generationConfig.thinkingConfig: {thinkingLevel:"medium"}
//	Qwen           enable_thinking:true + thinking_budget:8192
//	DeepSeek/GLM   thinking: {type:"enabled"}
//	Ark/Kimi       same or with "auto"
//	vLLM           thinking_token_budget:8192 | chat_template_kwargs.enable_thinking
//
// Solution: parse to a canonical ReasoningIntent, then render to each target
// dialect using the model's Caps (from reasoncap.Resolve).
//
// Design doc: docs/参数全量兼容/02-目标架构.md §3.3
package reasonnorm

import (
	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
)

// Mode is the canonical reasoning activation state.
type Mode int8

const (
	ModeUnset    Mode = 0  // client did not specify; use upstream default
	ModeEnabled  Mode = 1  // explicitly enabled
	ModeDisabled Mode = -1 // explicitly disabled
	ModeAdaptive Mode = 2  // model decides (Anthropic "adaptive")
)

// Intent is the normalised, dialect-agnostic reasoning specification.
// It is derived from whatever shape the client sent and is then rendered
// into the target dialect by Render.
type Intent struct {
	Mode Mode

	// Effort is the client's requested effort level (raw, not yet clamped to
	// the target model's supported set). Empty means unset.
	Effort string

	// BudgetTokens is the client's requested token budget. 0 means unset.
	// -1 means unlimited (vLLM convention).
	BudgetTokens int

	// IncludeThoughts requests that thinking content be returned in the
	// response. Nil means unset (let the upstream decide).
	IncludeThoughts *bool

	// Summary controls how reasoning summaries are delivered (OpenAI Responses
	// API: "auto" | "concise" | "detailed"). Empty means unset.
	Summary string

	// Context controls which reasoning items are replayed (OpenAI Responses:
	// "auto" | "current_turn" | "all_turns"). Empty means unset.
	Context string

	// KeepHistory controls whether prior reasoning turns are preserved.
	// Used by GLM (clear_thinking), Kimi (keep), Qwen (preserve_thinking).
	// Nil means unset.
	KeepHistory *bool
}

// ─── effort → budget mapping ──────────────────────────────────────────────────
//
// Adopted from LiteLLM production-validated constants
// (litellm/constants.py:83-192 + transformation.py:1174-1229).

var effortToBudget = map[string]int{
	"none":    0, // 0 = disable thinking entirely
	"minimal": 1024,
	"low":     1024,
	"medium":  2048,
	"high":    4096,
	"xhigh":   8192,
	"max":     16384,
}

// AnthropicMinBudget is the hard floor for Anthropic thinking budgets.
// The API rejects requests with budget_tokens < 1024.
const AnthropicMinBudget = 1024

// BudgetToEffort converts a numeric budget to the nearest effort level.
// Used when serialising Anthropic → OpenAI (currently produces nothing).
// Adopted from LiteLLM's reverse-mapping approach.
func BudgetToEffort(budget int) string {
	switch {
	case budget <= 0:
		return "none"
	case budget <= 1024:
		return "low"
	case budget <= 2048:
		return "medium"
	case budget <= 4096:
		return "high"
	case budget <= 8192:
		return "xhigh"
	default:
		return "max"
	}
}

// EffortToBudget converts an effort string to a token budget.
// Returns (budget, true) for known efforts; (0, false) for unknown strings.
func EffortToBudget(effort string) (int, bool) {
	v, ok := effortToBudget[effort]
	return v, ok
}

// ─── clamp effort to model's supported set ───────────────────────────────────

// ClampEffort maps an effort value to the nearest value supported by the model.
// If the model supports no effort enum (empty Efforts), it returns "".
// If effort is already in the supported set, it is returned unchanged.
// On a tie (equidistant up/down), prefers rounding UP (higher effort) to avoid
// under-serving the user's intent. This matches LiteLLM behaviour.
func ClampEffort(effort string, supported []string) string {
	if len(supported) == 0 {
		return ""
	}
	for _, s := range supported {
		if s == effort {
			return effort
		}
	}
	// Map both sides to a canonical numeric tier so we can find the closest.
	requested := effortIndex(effort)
	best := supported[0]
	bestIdx := effortIndex(best)
	for _, s := range supported[1:] {
		idx := effortIndex(s)
		dNew := abs(requested - idx)
		dBest := abs(requested - bestIdx)
		if dNew < dBest || (dNew == dBest && idx > bestIdx) {
			// Prefer closer; on ties prefer higher effort (round up).
			best = s
			bestIdx = idx
		}
	}
	return best
}

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func effortIndex(e string) int {
	for i, v := range effortOrder {
		if v == e {
			return i
		}
	}
	return 3 // default: mid-tier (medium)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ─── Render: Intent → per-dialect output ──────────────────────────────────────

// Result is the rendered reasoning configuration ready to be merged into the
// outgoing request body. Each field is only populated if the target dialect
// uses it; callers must check IsEmpty() before serialising.
type Result struct {
	// OpenAI Chat (reasoning_effort)
	ReasoningEffort string

	// OpenAI Responses (reasoning object)
	ReasoningObject *ReasoningObject

	// Anthropic (thinking object)
	Thinking *ThinkingConfig

	// Gemini 2.5 (thinkingConfig)
	ThinkingConfig25 *GeminiThinkingConfig25

	// Gemini 3 (thinkingConfig with level)
	ThinkingConfig3 *GeminiThinkingConfig3

	// Qwen (enable_thinking + thinking_budget)
	EnableThinking *bool
	ThinkingBudget *int

	// DeepSeek / GLM / Kimi / Ark / MiniMax (thinking object)
	ThinkingObject *GenericThinkingObject
}

func (r *Result) IsEmpty() bool {
	return r == nil ||
		(r.ReasoningEffort == "" &&
			r.ReasoningObject == nil &&
			r.Thinking == nil &&
			r.ThinkingConfig25 == nil &&
			r.ThinkingConfig3 == nil &&
			r.EnableThinking == nil &&
			r.ThinkingObject == nil)
}

// OpenAI Responses reasoning object.
type ReasoningObject struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
	Context string `json:"context,omitempty"`
}

// Anthropic thinking config.
type ThinkingConfig struct {
	Type         string `json:"type"` // "enabled" | "disabled" | "adaptive"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// Gemini 2.5 thinkingConfig.
type GeminiThinkingConfig25 struct {
	ThinkingBudget  int  `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

// Gemini 3 thinkingConfig.
type GeminiThinkingConfig3 struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"` // minimal|low|medium|high
}

// GenericThinkingObject covers DeepSeek / GLM / Kimi / Ark / MiniMax.
type GenericThinkingObject struct {
	Type string `json:"type"` // "enabled" | "disabled" | "auto" | "adaptive"
}

// Render converts a normalised Intent into the per-dialect output for the
// given target model's Caps.
//
// If intent is empty (ModeUnset + zero budget + empty effort), it returns
// an empty Result — no thinking fields are injected.
//
// maxTokens is the request's max_tokens / max_completion_tokens, used to cap
// the Anthropic budget (must be < max_tokens per Anthropic spec).
func Render(intent Intent, caps reasoncap.Caps, maxTokens int) Result {
	if intent.Mode == ModeUnset && intent.Effort == "" && intent.BudgetTokens == 0 {
		return Result{}
	}

	switch caps.Dialect {
	case reasoncap.DialectOpenAI:
		return renderOpenAIChat(intent, caps)
	case reasoncap.DialectAnthropic:
		return renderAnthropic(intent, caps, maxTokens)
	case reasoncap.DialectGemini25:
		return renderGemini25(intent, caps)
	case reasoncap.DialectGemini3:
		return renderGemini3(intent, caps)
	case reasoncap.DialectQwen:
		return renderQwen(intent, caps)
	case reasoncap.DialectDeepSeek, reasoncap.DialectGLM,
		reasoncap.DialectKimiThink, reasoncap.DialectArk:
		return renderGenericThinking(intent, caps)
	case reasoncap.DialectMiniMax:
		return renderMiniMax(intent)
	case reasoncap.DialectGrok, reasoncap.DialectMistral, reasoncap.DialectKimiEffort:
		return renderEffortOnly(intent, caps)
	case reasoncap.DialectVLLM:
		return renderVLLM(intent)
	default:
		return Result{}
	}
}

// ─── dialect renderers ────────────────────────────────────────────────────────

func renderOpenAIChat(intent Intent, caps reasoncap.Caps) Result {
	var effort string
	switch intent.Mode {
	case ModeDisabled:
		if caps.CanDisable {
			effort = "none"
		}
	case ModeEnabled, ModeAdaptive, ModeUnset:
		if intent.Effort != "" {
			effort = ClampEffort(intent.Effort, caps.Efforts)
		} else if intent.BudgetTokens > 0 {
			// budget → effort conversion (fills the Anthropic→OpenAI gap)
			effort = ClampEffort(BudgetToEffort(intent.BudgetTokens), caps.Efforts)
		} else if len(caps.Efforts) > 0 {
			effort = ClampEffort("medium", caps.Efforts) // safe default
		}
	}
	if effort == "" {
		return Result{}
	}
	return Result{ReasoningEffort: effort}
}

func renderAnthropic(intent Intent, caps reasoncap.Caps, maxTokens int) Result {
	switch intent.Mode {
	case ModeDisabled:
		return Result{Thinking: &ThinkingConfig{Type: "disabled"}}
	case ModeAdaptive:
		if caps.Adaptive {
			return Result{Thinking: &ThinkingConfig{Type: "adaptive"}}
		}
		// Fall through to budget-based enabled
	}

	if intent.Mode == ModeEnabled || intent.Mode == ModeAdaptive || intent.Mode == ModeUnset {
		budget := intent.BudgetTokens
		if budget == 0 && intent.Effort != "" {
			// effort → budget
			b, _ := EffortToBudget(intent.Effort)
			budget = b
		}
		if budget == 0 {
			budget = 8192 // LiteLLM default (medium effort)
		}

		// Hard floor
		if budget < AnthropicMinBudget {
			budget = AnthropicMinBudget
		}
		// Cap to max_tokens - 1 (Anthropic spec; LiteLLM transformation.py:1246)
		if maxTokens > 0 && budget >= maxTokens {
			budget = maxTokens - 1
		}
		if budget < AnthropicMinBudget {
			// Can't fit thinking budget; omit entirely.
			return Result{}
		}

		if intent.Mode == ModeAdaptive && caps.Adaptive {
			return Result{Thinking: &ThinkingConfig{Type: "adaptive"}}
		}
		return Result{Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: budget}}
	}

	return Result{}
}

func renderGemini25(intent Intent, caps reasoncap.Caps) Result {
	switch intent.Mode {
	case ModeDisabled:
		// Gemini sentinel: thinkingBudget=0 means disabled
		if caps.CanDisable {
			return Result{ThinkingConfig25: &GeminiThinkingConfig25{ThinkingBudget: 0}}
		}
	}

	budget := intent.BudgetTokens
	if budget == 0 && intent.Effort != "" {
		b, _ := EffortToBudget(intent.Effort)
		budget = b
	}

	// Automatic (no client budget) → Gemini sentinel -1
	if budget == 0 && intent.Mode == ModeUnset {
		return Result{}
	}
	if budget <= 0 {
		budget = -1 // AUTOMATIC
	}

	// Apply model min/max
	if budget > 0 && caps.BudgetMin > 0 && budget < caps.BudgetMin {
		budget = caps.BudgetMin
	}
	if caps.BudgetMax > 0 && budget > caps.BudgetMax {
		budget = caps.BudgetMax
	}

	includeThoughts := true
	if intent.IncludeThoughts != nil {
		includeThoughts = *intent.IncludeThoughts
	}
	return Result{ThinkingConfig25: &GeminiThinkingConfig25{
		ThinkingBudget:  budget,
		IncludeThoughts: includeThoughts,
	}}
}

func renderGemini3(intent Intent, caps reasoncap.Caps) Result {
	switch intent.Mode {
	case ModeDisabled:
		if caps.CanDisable {
			return Result{ThinkingConfig3: &GeminiThinkingConfig3{ThinkingLevel: "minimal"}}
		}
	}

	level := intent.Effort
	if level == "" {
		// budget → level
		if intent.BudgetTokens > 0 {
			level = BudgetToEffort(intent.BudgetTokens)
		}
	}
	if level == "" || level == "none" {
		return Result{}
	}
	// Gemini 3 only supports minimal/low/medium/high
	level = ClampEffort(level, caps.Efforts)
	return Result{ThinkingConfig3: &GeminiThinkingConfig3{ThinkingLevel: level}}
}

func renderQwen(intent Intent, caps reasoncap.Caps) Result {
	switch intent.Mode {
	case ModeDisabled:
		f := false
		return Result{EnableThinking: &f}
	}

	t := true
	budget := intent.BudgetTokens
	if budget == 0 && intent.Effort != "" {
		b, _ := EffortToBudget(intent.Effort)
		budget = b
	}
	if caps.BudgetMax > 0 && budget > caps.BudgetMax {
		budget = caps.BudgetMax
	}
	r := Result{EnableThinking: &t}
	if budget > 0 {
		r.ThinkingBudget = &budget
	}
	return r
}

func renderGenericThinking(intent Intent, caps reasoncap.Caps) Result {
	switch intent.Mode {
	case ModeDisabled:
		if caps.CanDisable {
			return Result{ThinkingObject: &GenericThinkingObject{Type: "disabled"}}
		}
	case ModeAdaptive:
		// Ark supports "auto"
		if caps.Dialect == reasoncap.DialectArk {
			return Result{ThinkingObject: &GenericThinkingObject{Type: "auto"}}
		}
		return Result{ThinkingObject: &GenericThinkingObject{Type: "enabled"}}
	}

	return Result{ThinkingObject: &GenericThinkingObject{Type: "enabled"}}
}

func renderMiniMax(intent Intent) Result {
	// MiniMax uses "disabled" | "adaptive" — NO "enabled"
	switch intent.Mode {
	case ModeDisabled:
		return Result{ThinkingObject: &GenericThinkingObject{Type: "disabled"}}
	}
	return Result{ThinkingObject: &GenericThinkingObject{Type: "adaptive"}}
}

func renderEffortOnly(intent Intent, caps reasoncap.Caps) Result {
	switch intent.Mode {
	case ModeDisabled:
		if caps.CanDisable {
			return Result{ReasoningEffort: "none"}
		}
	}

	effort := intent.Effort
	if effort == "" && intent.BudgetTokens > 0 {
		effort = BudgetToEffort(intent.BudgetTokens)
	}
	if effort == "" {
		return Result{}
	}
	effort = ClampEffort(effort, caps.Efforts)
	if effort == "" || effort == "none" && !caps.CanDisable {
		return Result{}
	}
	return Result{ReasoningEffort: effort}
}

func renderVLLM(intent Intent) Result {
	switch intent.Mode {
	case ModeDisabled:
		budget := 0
		return Result{ThinkingBudget: &budget}
	}

	budget := intent.BudgetTokens
	if budget == 0 && intent.Effort != "" {
		b, _ := EffortToBudget(intent.Effort)
		budget = b
	}
	if budget < 0 {
		budget = -1 // unlimited
	}
	if budget == 0 {
		return Result{}
	}
	return Result{ThinkingBudget: &budget}
}
