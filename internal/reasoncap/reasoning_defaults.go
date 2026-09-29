package reasoncap

// reasoningRule maps a name pattern to a Caps value.
//
// Priority semantics (mirrors modelname/modality_defaults.go):
//
//	0 = exact match
//	1 = prefix match (normalized name starts with pattern)
//	2 = suffix match (normalized name ends with pattern)
//	3 = contains match
//
// Within each priority tier, longer patterns win (more specific).
//
// IMPORTANT: patterns are matched against the RAW, pre-normalisation model
// name (lowercased). modelname.GenerateAliasVariants strips "thinking" and
// "reasoning" tokens, so this table must be consulted BEFORE normalisation.
//
// Data sources: docs/参数全量兼容/01-审计基线与研究结论.md 研究结论 2.1–2.3.
type reasoningRule struct {
	pattern  string
	priority int // 0=exact, 1=prefix, 2=suffix, 3=contains
	caps     Caps
}

var reasoningRules = []reasoningRule{

	// ══════════════════════════════════════════════════════════════════════
	// Priority 0: Exact matches
	// ══════════════════════════════════════════════════════════════════════

	// ─── OpenAI o-series (effort enum, no numeric budget) ─────────────────
	{"o1", 0, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},
	{"o3", 0, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},
	{"o4", 0, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},

	// ─── DeepSeek reasoner (exact, before prefix rule) ────────────────────
	{"deepseek-reasoner", 0, Caps{
		Supported: true, Dialect: DialectDeepSeek,
		Efforts:    []string{"low", "high", "max"},
		CanDisable: true,
	}},

	// ─── Kimi models ──────────────────────────────────────────────────────
	{"kimi-k2", 0, Caps{
		Supported: true, Dialect: DialectKimiThink,
		CanDisable: true, HistoryField: "keep",
	}},
	{"kimi-k2.6", 0, Caps{
		Supported: true, Dialect: DialectKimiThink,
		CanDisable: true, HistoryField: "keep",
	}},
	{"kimi-k2.7-code", 0, Caps{
		Supported: true, Dialect: DialectKimiThink,
		CanDisable: true, HistoryField: "keep",
	}},
	{"kimi-k2.7-code-highspeed", 0, Caps{
		Supported: true, Dialect: DialectKimiThink,
		CanDisable: true, HistoryField: "keep",
	}},
	{"kimi-k3", 0, Caps{
		Supported: true, Dialect: DialectKimiEffort,
		Efforts: []string{"low", "high", "max"}, CanDisable: true,
	}},

	// ─── Grok exact variants ──────────────────────────────────────────────
	{"grok-4", 0, Caps{
		Supported: true, Dialect: DialectGrok,
		Efforts:    []string{"none", "low", "medium", "high"},
		CanDisable: true,
	}},
	{"grok-4.5", 0, Caps{
		// grok-4.5 cannot disable reasoning
		Supported: true, Dialect: DialectGrok,
		Efforts:    []string{"low", "medium", "high"},
		CanDisable: false,
	}},
	{"grok-4.6", 0, Caps{
		// grok-4.6 supports low, medium, high, xhigh reasoning levels
		Supported: true, Dialect: DialectGrok,
		Efforts:    []string{"low", "medium", "high", "xhigh"},
		CanDisable: true,
	}},

	// ══════════════════════════════════════════════════════════════════════
	// Priority 1: Prefix matches (longer patterns first for specificity)
	// ══════════════════════════════════════════════════════════════════════

	// ─── OpenAI gpt-5 family (reasoning built in) ─────────────────────────
	{"gpt-5", 1, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},

	// ─── OpenAI o-series prefixes (o1-, o3-, o4-) ─────────────────────────
	{"o1-", 1, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},
	{"o3-", 1, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},
	{"o4-", 1, Caps{
		Supported: true, Dialect: DialectOpenAI,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		CanDisable: true,
	}},

	// ─── Anthropic Claude claude-3-7 and claude-4+ ────────────────────────
	// claude-3-7-sonnet supports thinking with budget + adaptive.
	{"claude-3-7-", 1, Caps{
		Supported: true, Dialect: DialectAnthropic,
		BudgetMin: 1024, BudgetMax: 32000,
		CanDisable: true, Adaptive: true,
	}},
	// claude-4+ (sonnet/opus/haiku variants with thinking)
	{"claude-sonnet-4", 1, Caps{
		Supported: true, Dialect: DialectAnthropic,
		BudgetMin: 1024, BudgetMax: 32000,
		CanDisable: true, Adaptive: true,
	}},
	{"claude-opus-4", 1, Caps{
		Supported: true, Dialect: DialectAnthropic,
		BudgetMin: 1024, BudgetMax: 32000,
		CanDisable: true, Adaptive: true,
	}},
	{"claude-opus-5", 1, Caps{
		Supported: true, Dialect: DialectAnthropic,
		BudgetMin: 1024, BudgetMax: 32000,
		CanDisable: true, Adaptive: true,
	}},
	{"claude-haiku-4", 1, Caps{
		Supported: true, Dialect: DialectAnthropic,
		BudgetMin: 1024, BudgetMax: 16000,
		CanDisable: true,
	}},

	// ─── Gemini 2.5 family — numeric budget + includeThoughts ─────────────
	{"gemini-2.5-flash-lite", 1, Caps{
		Supported: true, Dialect: DialectGemini25,
		BudgetMin: 512, BudgetMax: 24576,
		CanDisable: true,
	}},
	{"gemini-2.5-flash", 1, Caps{
		Supported: true, Dialect: DialectGemini25,
		BudgetMin: 1, BudgetMax: 24576,
		CanDisable: true,
	}},
	{"gemini-2.5-pro", 1, Caps{
		Supported: true, Dialect: DialectGemini25,
		BudgetMin: 128, BudgetMax: 32768,
		CanDisable: true,
	}},
	{"gemini-2.5-", 1, Caps{ // catch other 2.5 variants
		Supported: true, Dialect: DialectGemini25,
		BudgetMin: 128, BudgetMax: 24576,
		CanDisable: true,
	}},

	// ─── Gemini 3+ family — thinkingLevel enum ────────────────────────────
	{"gemini-3", 1, Caps{
		Supported: true, Dialect: DialectGemini3,
		Efforts:    []string{"minimal", "low", "medium", "high"},
		CanDisable: true,
	}},

	// ─── DeepSeek R1 prefixes ─────────────────────────────────────────────
	{"deepseek-r1-", 1, Caps{
		Supported: true, Dialect: DialectDeepSeek,
		Efforts:    []string{"low", "high", "max"},
		CanDisable: true,
	}},
	{"deepseek-v4-", 1, Caps{
		// deepseek-v4 series (pro/flash) supports thinking
		Supported: true, Dialect: DialectDeepSeek,
		Efforts:    []string{"low", "high", "max"},
		CanDisable: true,
	}},

	// ─── GLM-4.5 / GLM-5 / GLM-5.3 / GLM-Z1 ───────────────────────────────
	{"glm-5.3", 1, Caps{
		Supported: true, Dialect: DialectGLM,
		Efforts:    []string{"low", "high", "max"},
		CanDisable: false, HistoryField: "clear_thinking",
	}},
	// 2026-09-30 真机（方舟 ark.cn-beijing.volces.com/api/coding/v3/chat/completions，
	// model=glm-5-2-260617，即网关 `glm-5.2` 的实际出站名；间隔 7s，none/low 各
	// n=3 复验，其余档 n=1 全扫）推翻本表原先的 7 档声明：
	//
	//   none     → 400 `reasoning_effort 'none' is not supported by this model`（3/3）
	//   minimal  → 400 同上（方舟把 minimal 归一化成 none 后按 none 拒）
	//   low      → 200，reasoning_content 长度 0（3/3，即"关闭思考"的真实档）
	//   medium   → 200，reasoning_content 长度 0
	//   high     → 200，reasoning_content 长度 0
	//   xhigh    → 200，reasoning_content 759 字符（真开启）
	//   max      → 200，reasoning_content 937 字符（真开启）
	//
	// 即：关闭档是 low 而非 none/minimal；且 low/medium/high 三档在方舟侧
	// 都不产生思考。此前声明 none/minimal 会让 ClampEffort 把客户端的关闭意图
	// 改写成上游明确拒绝的值（400），把"原样透传被上游拒"变成"被网关改写成
	// 上游拒绝的值"——错误归属从上游转移到网关，且对调用方不可见。
	//
	// 只对 glm-5 家族落此结论：真机可验的只有 glm-5-2-260617。glm-4.5 / glm-z1
	// 未取得真机证据，保留原 7 档声明（宁可多声明也不误删，见下方 KEEP 说明）。
	// 智谱原生端点（api/paas/v4）在方舟凭据下 404，无法交叉验证。
	{"glm-5", 1, Caps{
		Supported: true, Dialect: DialectGLM,
		// 真机七档全扫（见上）：none/minimal 被拒，low 才是关闭档。
		Efforts:    []string{"max", "xhigh", "high", "medium", "low"},
		CanDisable: true, HistoryField: "clear_thinking",
	}},
	{"glm-4.5", 1, Caps{
		Supported: true, Dialect: DialectGLM,
		// KEEP: 无真机证据，维持原声明。移除 none/minimal 是有实测支撑的改动，
		// 在 glm-4.5 上没有；凭同族推断删档等于把未验证的判断写成事实。
		Efforts:    []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"},
		CanDisable: true, HistoryField: "clear_thinking",
	}},
	{"glm-z1", 1, Caps{
		Supported: true, Dialect: DialectGLM,
		// KEEP: 同 glm-4.5，无真机证据，维持原声明。
		Efforts:    []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"},
		CanDisable: true, HistoryField: "clear_thinking",
	}},

	// ─── Qwen3 / QwQ (enable_thinking + thinking_budget) ─────────────────
	{"qwen3-", 1, Caps{
		Supported: true, Dialect: DialectQwen,
		BudgetMin: 0, BudgetMax: 38912,
		CanDisable: true, HistoryField: "preserve_thinking",
	}},
	{"qwq-", 1, Caps{
		Supported: true, Dialect: DialectQwen,
		BudgetMin: 0, BudgetMax: 38912,
		CanDisable: true, HistoryField: "preserve_thinking",
	}},

	// ─── Kimi k-series prefixes ───────────────────────────────────────────
	{"kimi-k2-", 1, Caps{
		Supported: true, Dialect: DialectKimiThink,
		CanDisable: true, HistoryField: "keep",
	}},
	{"kimi-k3-", 1, Caps{
		Supported: true, Dialect: DialectKimiEffort,
		Efforts: []string{"low", "high", "max"}, CanDisable: true,
	}},

	// ─── MiniMax M3+ ──────────────────────────────────────────────────────
	{"minimax-m3", 1, Caps{
		// MiniMax uses disabled|adaptive — NO "enabled" value
		Supported: true, Dialect: DialectMiniMax,
		CanDisable: true, Adaptive: true,
	}},

	// ─── Volcengine Ark doubao-thinking / kimi-thinking ───────────────────
	{"doubao-pro-thinking", 1, Caps{
		Supported: true, Dialect: DialectArk,
		Efforts:    []string{"minimal", "low", "medium", "high"},
		CanDisable: true,
	}},

	// ─── Mistral reasoning ────────────────────────────────────────────────
	{"magistral", 1, Caps{
		Supported: true, Dialect: DialectMistral,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh"},
		CanDisable: true,
	}},
	{"mistral-medium-3", 1, Caps{
		Supported: true, Dialect: DialectMistral,
		Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh"},
		CanDisable: true,
	}},

	// ─── Grok 3-mini ──────────────────────────────────────────────────────
	{"grok-3-mini", 1, Caps{
		Supported: true, Dialect: DialectGrok,
		Efforts:    []string{"none", "low", "medium", "high"},
		CanDisable: true,
	}},
	{"grok-4-", 1, Caps{
		Supported: true, Dialect: DialectGrok,
		Efforts:    []string{"none", "low", "medium", "high"},
		CanDisable: true,
	}},

	// ══════════════════════════════════════════════════════════════════════
	// Priority 2: Suffix matches
	// ══════════════════════════════════════════════════════════════════════

	// ─── Generic "-thinking" suffix (Anthropic style) ─────────────────────
	// Catches e.g. "claude-sonnet-4-5-thinking" BEFORE alias normalisation.
	{"-thinking", 2, Caps{
		Supported: true, Dialect: DialectAnthropic,
		BudgetMin: 1024, BudgetMax: 32000,
		CanDisable: true, Adaptive: true,
	}},

	// ─── Generic "-reasoner" suffix (DeepSeek style) ──────────────────────
	{"-reasoner", 2, Caps{
		Supported: true, Dialect: DialectDeepSeek,
		Efforts:    []string{"low", "high", "max"},
		CanDisable: true,
	}},

	// ─── vLLM open-weight: any "-r1" suffix ───────────────────────────────
	{"-r1", 2, Caps{
		Supported: true, Dialect: DialectVLLM,
		BudgetMin: -1, BudgetMax: -1, // -1 = unlimited (thinking_token_budget = -1)
		CanDisable: true,
	}},

	// ══════════════════════════════════════════════════════════════════════
	// Priority 3: Contains matches (weakest, last resort)
	// ══════════════════════════════════════════════════════════════════════

	// ─── Any model name containing "qwq" ──────────────────────────────────
	{"qwq", 3, Caps{
		Supported: true, Dialect: DialectQwen,
		BudgetMin: 0, BudgetMax: 38912,
		CanDisable: true, HistoryField: "preserve_thinking",
	}},

	// ─── Doubao / Ark models with "thinking" in name ──────────────────────
	{"doubao", 3, Caps{
		// Generic doubao — may or may not support thinking; defer to DB/conservative
		Supported: false, Dialect: DialectNone,
	}},
}
