// families.go — 内置种子方言。实现体从 internal/ir 各 serializer **逐字
// 搬移**（行为保持；差异只在上报从内联调用改为 Loss 载体返回）。每个
// 方言的注释保留原现场的教训编号（R23-A / N21-2 / R25-U4 / 2026-10-01
// 审计 / R45），那些是语义的一部分，不是装饰。
package reasoningdialect

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// 家族注册键：与 reasoncap.Dialect 常量同值（字符串字面量对齐，避免
// 循环依赖；reasoningdialect 只依赖 reasonnorm/reasoncap）。任何一侧
// 改名，契约门会抓住（见 reasoningdialect_test.go 的键对齐断言）。
const (
	FamilyAnthropic = "anthropic" // reasoncap.DialectAnthropic
	FamilyGemini25  = "gemini25"  // reasoncap.DialectGemini25
	FamilyOpenAI    = "openai"    // reasoncap.DialectOpenAI（reasoning_effort 原生）
	FamilyOllama    = "ollama"    // reasoncap.DialectOllama（请求侧无表达，按设计丢弃）
	// openai-wire thinking 家族（经 reasonnorm.Render 出向）：
	FamilyMiniMax  = "minimax"  // reasoncap.DialectMiniMax
	FamilyDeepSeek = "deepseek" // reasoncap.DialectDeepSeek
	FamilyGLM      = "glm"      // reasoncap.DialectGLM
	FamilyArk      = "ark"      // reasoncap.DialectArk
	FamilyQwen     = "qwen"     // reasoncap.DialectQwen
)

// 目标协议常量：与 internal/ir 的 Protocol* 同值（Loss.TargetProtocol 用）。
const (
	ProtocolAnthropicMessages = "anthropic-messages"
	ProtocolGeminiGenerate    = "gemini-generate"
	ProtocolOllamaChat        = "ollama-chat"
	ProtocolOpenAIChat        = "openai-chat"
)

// seedFamilies 返回内置种子（未装配即正确）。
func seedFamilies() map[string]ReasoningDialect {
	owf := func(d reasoncap.Dialect) *openAIWireFamily {
		return &openAIWireFamily{dialect: d}
	}
	return map[string]ReasoningDialect{
		FamilyAnthropic: &AnthropicFamily{},
		FamilyGemini25:  &Gemini25Family{},
		FamilyOpenAI:    &OpenAIEffortFamily{},
		FamilyOllama:    &OllamaFamily{},
		FamilyMiniMax:   owf(reasoncap.DialectMiniMax),
		FamilyDeepSeek:  owf(reasoncap.DialectDeepSeek),
		FamilyGLM:       owf(reasoncap.DialectGLM),
		FamilyArk:       owf(reasoncap.DialectArk),
		FamilyQwen:      owf(reasoncap.DialectQwen),
	}
}

// ── 模型族门（从 internal/ir/reasoning_dialect.go 逐字搬移）──────────────
// 注释原文保留：模型能力需要 provider-aware registry 而非猜测的全局规则；
// 这里刻意只点名已确证的家族。

// modelHasFamilyPrefix matches a provider model family and its dated/preview
// variants. The route may retain a provider-qualified name in IR.Model.
func modelHasFamilyPrefix(model, family string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return model == family || strings.HasPrefix(model, family+"-")
}

// Gemini 2.5 Flash and Flash Lite are the confirmed thinkingBudget=0 families.
// Pro, Gemini 3, and unrecognised aliases must not receive that sentinel.
func geminiSupportsDisabledThinking(model string) bool {
	return modelHasFamilyPrefix(model, "gemini-2.5-flash")
}

// Claude Opus 5.5 always thinks and rejects thinking.type=disabled.
func anthropicRejectsDisabledThinking(model string) bool {
	return modelHasFamilyPrefix(model, "claude-opus-5-5")
}

// ── anthropic 家族 ──────────────────────────────────────────────────────
// 从 serialize_anthropic.go 逐字搬移（含 2026-10-01 审计的 max_tokens
// 一致性钳制与 R23-A / N21-2 教训）。

// AnthropicFamily 投影 anthropic-messages wire 的 thinking 对象。
const anthropicMinBudget = 1024

type AnthropicFamily struct{}

func (*AnthropicFamily) Name() string { return FamilyAnthropic }

// ProjectNative implements NativeProjector：原生 req.Thinking → 顶层
// thinking 对象。budget 只在显式 "enabled" 时发射（adaptive/disabled
// 省略，mirrors TestAnthropicNativeThinkingPrecedesReasoning）；
// opus-5-5 拒 disabled ⇒ 照发 + unsupported loss（原文：record loss so
// the request still goes upstream but the loss is observable downstream）。
func (f *AnthropicFamily) ProjectNative(n NativeRequest) Projection {
	p := Projection{}
	t := map[string]any{
		"type": n.Type,
	}
	if n.Type == "enabled" {
		t["budget_tokens"] = n.Budget
	}
	p.Wire = map[string]any{"thinking": t}
	// claude-opus-5-5 always thinks; record loss when native disabled intent
	// is requested (TestNativeDisabledModelLossesRemainVisible/Anthropic_native_thinking).
	if n.Type == "disabled" && anthropicRejectsDisabledThinking(n.Model) {
		fieldPath := "reasoning_effort"
		if n.SourceProtocol == ProtocolAnthropicMessages {
			// 原生路径恒有 Thinking != nil（helper 原条件在 native 路径恒真）。
			fieldPath = "thinking.type"
		}
		p.Losses = append(p.Losses, Loss{
			FieldPath:      fieldPath,
			Reason:         "unsupported",
			Message:        "Anthropic model rejected thinking.type=disabled; omitted thinking block to keep request valid",
			SourceProtocol: n.SourceProtocol,
			TargetProtocol: ProtocolAnthropicMessages,
			Metadata: map[string]any{
				"model":       n.Model,
				"family":      f.Name(),
				"disposition": "by_design_model_gate",
			},
		})
	}
	return p
}

// ProjectDerived implements DerivedProjector：跨协议 ReasoningConfig →
// thinking 对象。调用方门（原生优先）：仅在 req.Thinking == nil 时调用
// ——因此 loss 的 fieldPath 原条件
// `SourceProtocol == anthropic-messages && req.Thinking != nil` 在本路径
// **恒假** ⇒ fieldPath 恒为 "reasoning_effort"（与历史行为逐字一致）。
func (f *AnthropicFamily) ProjectDerived(d DerivedRequest) Projection {
	p := Projection{}
	a := Arbitrate(d.Type, d.BudgetTokens, d.Effort)
	thinkingType := "enabled"
	thinkingBudget := 0
	hasBudget := false
	disableUnsupported := false
	switch a.Kind {
	case KindDirective:
		thinkingType = a.Directive
		if thinkingType == DirectiveEnabled {
			// Explicit Type wins, but must not drop an explicit positive
			// budget (R23-A): parse_gemini builds {Type:"enabled",
			// BudgetTokens} for any positive thinkingBudget, and Anthropic
			// requires budget_tokens whenever thinking is enabled.
			hasBudget = true
			if a.ExplicitBudget {
				thinkingBudget = a.Budget
			} else {
				thinkingBudget = 8192 // safe default (xhigh); enabled without budget is an API error
			}
		}
		// disabled / adaptive carry no budget: the adaptive canonical form
		// is budgetless (reasonnorm renders native adaptive without one),
		// mirroring the native Thinking path.
	case KindBudget:
		if a.Budget == 0 {
			thinkingType = DirectiveDisabled
		} else {
			thinkingBudget = a.Budget
			hasBudget = true
		}
	case KindEffort:
		if a.DisableEffort {
			thinkingType = DirectiveDisabled
		} else if a.BudgetFromEffort {
			thinkingBudget = a.Budget
			hasBudget = true
		} else {
			thinkingBudget = 8192 // safe default (xhigh)
			hasBudget = true
		}
	default:
		// No recognizable intent (unknown Type vocabulary only, no budget,
		// no effort): omit the thinking block entirely — a budgetless
		// {"type":"enabled"} is an Anthropic 400.
		thinkingType = ""
	}

	// Model-specific gate: claude-opus-5-5 always thinks; the API
	// rejects thinking.type=disabled. Per TestAnthropicOpus55Disabled
	// OmittedAndReported, omit the entire thinking block on this model
	// (the client intent to disable is recorded as a loss instead).
	if thinkingType == DirectiveDisabled && anthropicRejectsDisabledThinking(d.Model) {
		disableUnsupported = true
		thinkingType = "" // omit the entire block
	}

	// 2026-10-01 审计：thinking budget × 最终 max_tokens 的最终一致性钳制
	// （**仅客户端省略 max_tokens 的形态参与**；显式选值逐字透传，两路
	// 对等金标准 chat_to_anthropic_parity 钉住）。钳制语义与
	// renderAnthropic / ClampRestoredReasoningForAnthropic 同款：
	// cap 到 max-1；cap 后低于 1024 硬下限则整体省略 thinking。
	if !d.ClientSetMaxTokens && thinkingType != "" && thinkingType != DirectiveDisabled && hasBudget {
		if thinkingBudget >= d.FinalMaxTokens {
			thinkingBudget = d.FinalMaxTokens - 1
		}
		if thinkingBudget < anthropicMinBudget {
			thinkingType = "" // 放不下 thinking：整体省略
		}
	}

	if thinkingType != "" {
		thinking := map[string]any{}
		thinking["type"] = thinkingType
		// Disabled thinking cannot carry a budget_tokens field
		// (TestReasoningDisabledConflictPrecedence/type_disabled_beats_budget);
		// hasBudget is never set for disabled in the switch above.
		if hasBudget && thinkingType != DirectiveDisabled {
			thinking["budget_tokens"] = thinkingBudget
		}
		if len(thinking) > 0 {
			p.Wire = map[string]any{"thinking": thinking}
		}
	}
	if disableUnsupported {
		p.Losses = append(p.Losses, Loss{
			FieldPath:      "reasoning_effort",
			Reason:         "unsupported",
			Message:        "Anthropic model rejected thinking.type=disabled; omitted thinking block to keep request valid",
			SourceProtocol: d.SourceProtocol,
			TargetProtocol: ProtocolAnthropicMessages,
			Metadata: map[string]any{
				"model":       d.Model,
				"family":      f.Name(),
				"disposition": "by_design_model_gate",
			},
		})
	}
	return p
}

// ── gemini25 家族 ───────────────────────────────────────────────────────
// 从 serialize_gemini.go 逐字搬移（原生 switch 944-966 + 派生块
// 968-1051 + R45/R23-A/N21-2 教训）。

// Gemini25Family 投影 gemini generationConfig.thinkingConfig
// （2.5 形态：thinkingBudget [+includeThoughts]）。
type Gemini25Family struct{}

func (*Gemini25Family) Name() string { return FamilyGemini25 }

// ProjectNative implements NativeProjector：原生 req.Thinking →
// thinkingConfig。原生优先（mirror of serialize_anthropic 的
// `req.Thinking == nil` 门）——此前被忽略导致并发 Reasoning 覆盖原生
// 意图（PoC: Thinking budget=2048 + Effort=high 曾发射 4096）。
func (f *Gemini25Family) ProjectNative(n NativeRequest) Projection {
	p := Projection{}
	switch n.Type {
	case "disabled":
		if geminiSupportsDisabledThinking(n.Model) {
			p.Wire = map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 0}}
		} else {
			// 记 loss 后省略 thinkingConfig（hasAny 不变）。
			// 原 native 块传字面量 "thinking.type"（与派生块按 SourceProtocol
			// 推导 fieldPath 的行为不同——行为保持，两处各自逐字）。
			p.Losses = append(p.Losses, Loss{
				FieldPath:      "thinking.type",
				Reason:         "unsupported",
				Message:        "Gemini model rejected thinkingBudget=0; omitted thinkingConfig to keep request valid",
				SourceProtocol: n.SourceProtocol,
				TargetProtocol: ProtocolGeminiGenerate,
				Metadata: map[string]any{
					"model":       n.Model,
					"family":      f.Name(),
					"disposition": "by_design_model_gate",
				},
			})
		}
	case "adaptive":
		// Model decides: dynamic budget with thoughts surfaced.
		p.Wire = map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": -1, "includeThoughts": true}}
	default:
		// "enabled" and unrecognized types: "think, roughly this much";
		// without a positive budget fall back to the Gemini dynamic
		// default (-1) rather than dropping the intent.
		budget := -1
		if n.Budget > 0 {
			budget = n.Budget
		}
		p.Wire = map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": budget, "includeThoughts": true}}
	}
	return p
}

// ProjectDerived implements DerivedProjector：跨协议 ReasoningConfig →
// thinkingConfig。Precedence mirrors the Anthropic branch（单源 =
// Arbitrate）；Type 只在指令词汇内是指令（N21-2）。
func (f *Gemini25Family) ProjectDerived(d DerivedRequest) Projection {
	p := Projection{}
	a := Arbitrate(d.Type, d.BudgetTokens, d.Effort)
	thinkingType := "enabled"
	thinkingBudget := -1 // Gemini default: -1 = dynamic budget
	emitBlock := false
	disableUnsupported := false
	includeThoughts := true

	switch a.Kind {
	case KindDirective:
		thinkingType = a.Directive
		if thinkingType == DirectiveDisabled {
			thinkingBudget = 0
		} else if a.ExplicitBudget {
			// Explicit Type wins, but must not drop an explicit positive
			// budget (R23-A): parse_gemini builds {Type:"enabled",
			// BudgetTokens} for positive thinkingBudget; falling back to
			// the -1 dynamic default silently discards the caller's cap.
			thinkingBudget = a.Budget
		}
		emitBlock = true
	case KindBudget:
		if a.Budget == 0 {
			thinkingType = DirectiveDisabled
			thinkingBudget = 0
		} else {
			thinkingBudget = a.Budget
		}
		emitBlock = true
	case KindEffort:
		if a.DisableEffort {
			thinkingType = DirectiveDisabled
			thinkingBudget = 0
			emitBlock = true
		} else if a.BudgetFromEffort {
			thinkingBudget = a.Budget
			emitBlock = true
		}
	}

	// Model-specific gate: Gemini Pro / Gemini 3 / unknown aliases do
	// not accept thinkingBudget=0; fall through to model-default
	// (dynamic) thinking and record an unsupported loss.
	if emitBlock && thinkingType == DirectiveDisabled && !geminiSupportsDisabledThinking(d.Model) {
		disableUnsupported = true
		emitBlock = false
	}

	if !emitBlock {
		if disableUnsupported {
			fieldPath := "reasoning_effort"
			if d.SourceProtocol == ProtocolGeminiGenerate {
				fieldPath = "reasoning"
			}
			p.Losses = append(p.Losses, Loss{
				FieldPath:      fieldPath,
				Reason:         "unsupported",
				Message:        "Gemini model rejected thinkingBudget=0; omitted thinkingConfig to keep request valid",
				SourceProtocol: d.SourceProtocol,
				TargetProtocol: ProtocolGeminiGenerate,
				Metadata: map[string]any{
					"model":       d.Model,
					"family":      f.Name(),
					"disposition": "by_design_model_gate",
				},
			})
		}
		// Skip the emission block; hasAny remains unchanged (调用方语义).
	} else if thinkingType == DirectiveDisabled {
		// Gemini sentinel: thinkingBudget=0 means disabled; do not
		// attach a positive budget to the disabled block.
		p.Wire = map[string]any{"thinkingConfig": map[string]any{
			"thinkingBudget": thinkingBudget,
		}}
	} else {
		tc := map[string]any{
			"thinkingBudget": thinkingBudget,
		}
		tc["includeThoughts"] = includeThoughts
		p.Wire = map[string]any{"thinkingConfig": tc}
	}
	return p
}

// ── openai（reasoning_effort 原生）家族 ─────────────────────────────────
// 从 serialize_openai.go:78-84 逐字搬移（R25-U4）。

// OpenAIEffortFamily 投影 openai-chat 的 reasoning_effort 原生字段。
type OpenAIEffortFamily struct{}

func (*OpenAIEffortFamily) Name() string { return FamilyOpenAI }

// ProjectDerived implements DerivedProjector：effort 原生发射。
// R25-U4: an explicit disabled Type outranks Effort (same priority as the
// Anthropic/Gemini serializers) — emitting reasoning_effort alongside
// would silently re-enable what the caller disabled.
func (*OpenAIEffortFamily) ProjectDerived(d DerivedRequest) Projection {
	p := Projection{}
	if d.Effort != "" && d.Type != DirectiveDisabled {
		p.Wire = map[string]any{"reasoning_effort": d.Effort}
	}
	return p
}

// ── ollama 家族 ─────────────────────────────────────────────────────────
// 从 serialize_ollama.go reportOllamaReasoningLoss 逐字搬移。

// OllamaFamily：Ollama 在 serve 期配置 reasoning，请求侧无表达 ⇒
// 全族按设计丢弃并逐字段上报（budgets/enums only：不携带自由文本）。
type OllamaFamily struct{}

func (*OllamaFamily) Name() string { return FamilyOllama }

// ProjectNative implements NativeProjector。
func (f *OllamaFamily) ProjectNative(n NativeRequest) Projection {
	p := Projection{}
	p.Losses = append(p.Losses, Loss{
		FieldPath:      "thinking",
		Reason:         "ollama_reasoning_drop",
		Message:        "thinking config has no Ollama request-side equivalent and was dropped",
		SourceProtocol: n.SourceProtocol,
		TargetProtocol: ProtocolOllamaChat,
		Metadata: map[string]any{
			"thinking_type": n.Type,
			"family":        f.Name(),
			"disposition":   "by_design_no_request_side_surface",
		},
	})
	return p
}

// ProjectDerived implements DerivedProjector：逐字段 loss（原 5 个 if）。
func (f *OllamaFamily) ProjectDerived(d DerivedRequest) Projection {
	p := Projection{}
	if d.Type != "" {
		p.Losses = append(p.Losses, Loss{
			FieldPath:      "reasoning.type",
			Reason:         "ollama_reasoning_drop",
			Message:        "reasoning toggle has no Ollama request-side equivalent and was dropped",
			SourceProtocol: d.SourceProtocol,
			TargetProtocol: ProtocolOllamaChat,
			Metadata: map[string]any{
				"reasoning_type": d.Type,
				"family":         f.Name(),
				"disposition":    "by_design_no_request_side_surface",
			},
		})
	}
	if d.Effort != "" {
		p.Losses = append(p.Losses, Loss{
			FieldPath:      "reasoning.effort",
			Reason:         "ollama_reasoning_drop",
			Message:        "reasoning effort has no Ollama request-side equivalent and was dropped",
			SourceProtocol: d.SourceProtocol,
			TargetProtocol: ProtocolOllamaChat,
			Metadata: map[string]any{
				"reasoning_effort": d.Effort,
				"family":           f.Name(),
				"disposition":      "by_design_no_request_side_surface",
			},
		})
	}
	if d.BudgetTokens != nil {
		p.Losses = append(p.Losses, Loss{
			FieldPath:      "reasoning.budget_tokens",
			Reason:         "ollama_reasoning_drop",
			Message:        "reasoning token budget has no Ollama request-side equivalent and was dropped",
			SourceProtocol: d.SourceProtocol,
			TargetProtocol: ProtocolOllamaChat,
			Metadata: map[string]any{
				"budget_tokens": *d.BudgetTokens,
				"family":        f.Name(),
				"disposition":   "by_design_no_request_side_surface",
			},
		})
	}
	if d.MaxReasoningTokens != nil {
		p.Losses = append(p.Losses, Loss{
			FieldPath:      "reasoning.max_reasoning_tokens",
			Reason:         "ollama_reasoning_drop",
			Message:        "max reasoning token cap has no Ollama request-side equivalent and was dropped",
			SourceProtocol: d.SourceProtocol,
			TargetProtocol: ProtocolOllamaChat,
			Metadata: map[string]any{
				"max_reasoning_tokens": *d.MaxReasoningTokens,
				"family":               f.Name(),
				"disposition":          "by_design_no_request_side_surface",
			},
		})
	}
	return p
}

// ── openai-wire thinking 家族（minimax/deepseek/glm/ark/qwen）───────────
// 从 serialize_openai.go applyThinkingToOpenAIChat 的写回段逐字搬移；
// caps 构造从 syntheticReasonCaps 搬移（每族自持，新增家族不再改核心
// switch）。

// openAIWireFamily 经 reasonnorm.Render（单一语义源）出向。Dialect keying
// follows the 2026-09-18 incident lesson: the dialect comes from the target
// provider, never from the model name. kimi / grok / mistral / vllm / ollama
// and unknown families stay on the loss-report path（不注册 ⇒ ForVendorFamily
// 回落 noop ⇒ IntentProjector 断言失败 ⇒ 调用方保持 loss 行为）。
type openAIWireFamily struct {
	dialect reasoncap.Dialect
}

func (f *openAIWireFamily) Name() string { return string(f.dialect) }

// RenderIntent implements IntentProjector。Keys already present in out are
// never overwritten (IR/serializer output wins, mirroring restoreExtensions)。
func (f *openAIWireFamily) RenderIntent(intent reasonnorm.Intent, maxTokens int, out map[string]any) bool {
	caps := reasoncap.Caps{
		Supported:  true,
		Dialect:    f.dialect,
		CanDisable: true, // family members all accept an explicit off toggle
	}
	res := reasonnorm.Render(intent, caps, maxTokens)
	if res.IsEmpty() {
		return false
	}
	rendered := false
	if res.ThinkingObject != nil {
		if _, exists := out["thinking"]; !exists {
			out["thinking"] = res.ThinkingObject
			rendered = true
		}
	}
	if res.EnableThinking != nil {
		if _, exists := out["enable_thinking"]; !exists {
			out["enable_thinking"] = *res.EnableThinking
			rendered = true
		}
	}
	if res.ThinkingBudget != nil {
		if _, exists := out["thinking_budget"]; !exists {
			out["thinking_budget"] = *res.ThinkingBudget
			rendered = true
		}
	}
	if res.ReasoningEffort != "" {
		if _, exists := out["reasoning_effort"]; !exists {
			out["reasoning_effort"] = res.ReasoningEffort
			rendered = true
		}
	}
	return rendered
}
