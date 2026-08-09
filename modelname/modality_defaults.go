package modelname

import "strings"

// modalityRule represents a pattern-matching rule for model modality inference.
type modalityRule struct {
	pattern  string
	modality string
	priority int // 0=exact, 1=prefix, 2=suffix, 3=contains
}

// modalityRules is an ordered list of rules, evaluated from top to bottom.
// Within each priority tier, longer patterns are checked first.
var modalityRules = []modalityRule{
	// ========== Priority 0: Exact matches (no wildcards) ==========
	// OpenAI
	{"gpt-4o", "vision", 0},
	{"gpt-4-turbo", "vision", 0},
	{"gpt-4v", "vision", 0},
	{"chatgpt-4o-latest", "vision", 0},
	{"o1", "vision", 0},
	{"o3-mini", "vision", 0},
	{"gpt-3.5-turbo", "text", 0},

	// Anthropic
	{"claude-2", "text", 0},

	// Google Gemini
	{"gemini-pro-vision", "vision", 0},
	{"gemini-ultra", "multimodal", 0},
	{"gemini-pro", "text", 0},

	// Mistral
	{"mistral-embed", "embedding", 0},

	// Chinese models
	{"glm-4v", "vision", 0},
	{"yi-vision", "vision", 0},
	{"hunyuan-vision", "vision", 0},

	// ========== Priority 1: Prefix matches (pattern ends with *) ==========
	// Order by pattern length (longest first for specificity)

	// OpenAI - Audio (specific, must come before gpt-4o-*)
	{"gpt-4o-audio-", "audio", 1},    // gpt-4o-audio-preview (10 chars before *)
	{"gpt-4o-realtime-", "audio", 1}, // gpt-4o-realtime-* (14 chars)

	// OpenAI - Vision (after audio to avoid conflict)
	{"gpt-4o-", "vision", 1}, // gpt-4o-2024-08-06 (6 chars)
	{"gpt-4-turbo-", "vision", 1},
	{"gpt-4-vision-", "vision", 1},
	{"o1-", "vision", 1},
	{"o3-", "vision", 1}, // o3, o3-mini, o3-pro — same convention as the exact o3-mini rule
	{"o4-", "vision", 1}, // o4-mini (2026-08-09: present in this deployment)
	// GPT-5 family: natively multimodal (text+image), same convention as
	// gpt-4o. Covers gpt-5, gpt-5.4, gpt-5.6 and dated variants seen in the
	// model_offers inventory. 2026-08-09.
	{"gpt-5", "multimodal", 1},

	// OpenAI - Audio
	{"whisper-", "audio", 1},
	{"tts-", "audio", 1},

	// OpenAI - Text
	{"gpt-3.5-turbo-", "text", 1},
	{"text-davinci-", "text", 1},
	{"text-curie-", "text", 1},
	{"text-babbage-", "text", 1},
	{"text-ada-", "text", 1},

	// OpenAI - Embedding
	{"text-embedding-", "embedding", 1},
	{"text-similarity-", "embedding", 1},
	{"text-search-", "embedding", 1},
	{"embedding-", "embedding", 1},

	// Anthropic - Vision
	{"claude-3-", "vision", 1},
	{"claude-sonnet-", "vision", 1},
	{"claude-opus-", "vision", 1},
	{"claude-haiku-", "vision", 1},

	// Anthropic - Text
	{"claude-instant-", "text", 1},
	{"claude-2.", "text", 1},
	{"claude-v1", "text", 1},

	// Google Gemini - Embedding
	{"textembedding-", "embedding", 1},

	// Google Gemini - Specific multimodal patterns (add for test coverage)
	// Note: Gemini 1.5/2.0 Pro/Flash are TRUE multimodal (text+image+audio+video).
	// They are categorized as "multimodal" so the SQL filter accepts them for
	// ANY modality request. Dedicated video-only models should use "video".
	{"gemini-1.5-pro", "multimodal", 0},
	{"gemini-1.5-flash", "multimodal", 0},
	{"gemini-2.0-flash-exp", "multimodal", 0},
	{"gemini-2.0-flash", "multimodal", 0},
	{"gemini-2.0-pro", "multimodal", 0},
	{"gemini-2.5-flash", "multimodal", 0},
	{"gemini-2.5-flash-image", "multimodal", 0}, // image generation/editing variant, accepts image input
	{"gemini-2.5-pro", "multimodal", 0},
	{"gemini-3-flash", "multimodal", 0},

	// 2026-08-09: the exact rules above only cover bare names, so dated and
	// preview variants (gemini-3-flash-preview, gemini-3.5-flash,
	// gemini-2.5-pro-preview-03-25) fell through to "text". Every Gemini
	// generation from 1.5 onward is natively multimodal, so cover the
	// families by prefix. Keep these AFTER the exact rules; the tier-based
	// matcher evaluates exact before prefix regardless of ordering.
	{"gemini-1.5-", "multimodal", 1},
	{"gemini-2.0-", "multimodal", 1},
	{"gemini-2.5-", "multimodal", 1},
	{"gemini-3-", "multimodal", 1},
	{"gemini-3.5-", "multimodal", 1},

	// Meta Llama - Vision (specific patterns first)
	{"llama-3.2-11b-vision", "vision", 1},
	{"llama-3.2-90b-vision", "vision", 1},

	// Meta Llama - Text (after vision)
	{"llama-3.2-", "text", 1},
	{"llama-3.1-", "text", 1},
	{"llama-3-", "text", 1},
	{"llama-2-", "text", 1},
	{"llama-guard-", "text", 1},

	// Mistral
	{"pixtral-", "vision", 1},
	{"mistral-large-", "text", 1},
	{"mistral-medium-", "text", 1},
	{"mistral-small-", "text", 1},
	{"mixtral-", "text", 1},
	{"ministral-", "text", 1},
	{"codestral-", "text", 1},

	// Zhipu GLM
	//
	// 2026-08-09: verified against docs.bigmodel.cn/cn/guide/start/model-overview.
	// Zhipu marks vision models with a `v` immediately after the version
	// number (glm-4v, glm-4.1v, glm-4.6v, glm-5v), so the text-family
	// prefixes below (glm-4-, glm-) must not swallow them. The chat families
	// GLM-4.5/4.6/4.7/5/5.1/5.2 are text-only per the same page — the
	// existing `glm-` → text fallback is correct for those and is unchanged.
	{"glm-4v-", "vision", 1},
	{"glm-4.1v", "vision", 1}, // glm-4.1v-thinking-flash / -flashx
	{"glm-4.5v", "vision", 1},
	{"glm-4.6v", "vision", 1}, // glm-4.6v, glm-4.6v-flash
	{"glm-5v", "vision", 1},   // glm-5v-turbo (multimodal coding base)
	{"glm-ocr", "vision", 1},  // document/image parsing
	{"glm-realtime", "multimodal", 1},
	{"glm-asr", "audio", 1},
	{"glm-tts", "audio", 1},
	{"glm-4-voice", "audio", 1},
	{"cogview-", "vision", 1},
	{"glm-4-", "text", 1},
	{"glm-3-", "text", 1},
	{"chatglm", "text", 1},
	{"codegeex-", "text", 1},
	{"glm-", "text", 1},

	// Moonshot
	{"moonshot-", "text", 1},
	{"kimi-k3", "vision", 1}, // 2026-08-09: Aliyun Model Studio lists kimi-k3 under 图像与视频理解
	{"kimi-", "text", 1},

	// ByteDance Doubao - Text specific patterns first (longer wins)
	{"doubao-pro-", "text", 1},           // doubao-pro-32k (10 chars, before doubao-)
	{"doubao-lite-", "text", 1},          // doubao-lite-* (11 chars)
	{"doubao-vision-", "vision", 1},      // doubao-vision-* (14 chars)
	{"doubao-1.5-vision", "vision", 1},   // doubao-1.5-vision-pro
	{"doubao-1-5-vision", "vision", 1},   // dashed release form
	{"doubao-embedding", "embedding", 1}, // incl. doubao-embedding-vision (multimodal embedding)
	{"doubao-", "text", 1},               // fallback (7 chars)
	{"seed-", "text", 1},

	// Alibaba Qwen - Vision / omni before the text families.
	// 2026-08-09: verified against Aliyun Model Studio 模型大全. The `-vl-`
	// contains rule cannot win against a `qwen2.5-` style prefix, so each
	// vision/omni family needs an explicit prefix rule.
	{"qwen-vl-", "vision", 1},
	{"qwen2-vl-", "vision", 1},
	{"qwen2.5-vl", "vision", 1},
	{"qwen3-vl", "vision", 1},
	{"qwen3.5-vl", "vision", 1},
	{"qwen-audio-", "audio", 1},
	{"qwen-omni", "multimodal", 1}, // text+image+audio+video (全模态)
	{"qwen2.5-omni", "multimodal", 1},
	{"qwen3-omni", "multimodal", 1},
	{"qwen3.5-omni", "multimodal", 1}, // qwen3.5-omni-plus / -realtime
	{"qwen2.5-", "text", 1},
	{"qwen2-", "text", 1},
	{"qwen-", "text", 1},

	// DeepSeek — 2026-08-09: deepseek-v3/v3.1/v4-pro/v4-flash appear only
	// under Aliyun's 文本生成 section (no image/video understanding), so the
	// text fallback is correct and deliberately left as-is.
	{"deepseek-vl", "vision", 1}, // research VL line, if ever registered
	{"deepseek-", "text", 1},

	// MiniMax
	// 2026-08-09: minimax-m3 accepts image content blocks — confirmed by the
	// in-repo investigation (MINIMAX_MULTIMODAL_INVESTIGATION.md) and already
	// special-cased in catalog.inferModalityFromName. Aligning the inference
	// table so routing and display agree.
	{"minimax-m3", "multimodal", 1},
	{"minimax-", "text", 1},
	{"abab", "text", 1},

	// Baichuan
	{"baichuan", "text", 1},

	// 01.AI Yi - Vision first
	{"yi-vl-", "vision", 1},
	{"yi-", "text", 1},

	// iFlytek Spark
	{"spark", "text", 1},

	// Huawei Pangu
	{"pangu-", "text", 1},

	// Baidu Ernie - Only specific text patterns, vision caught by suffix/contains
	{"ernie-bot-", "text", 1}, // ernie-bot-4, ernie-bot-turbo (text models)
	// Note: removed generic "ernie-" prefix to allow "-vision" suffix/contains to work

	// Tencent Hunyuan
	{"hunyuan-", "text", 1}, // hunyuan-vision handled by exact match

	// Cohere
	{"command-r-", "text", 1},
	{"command-", "text", 1},
	{"embed-", "embedding", 1},
	{"rerank-", "text", 1},

	// xAI Grok
	{"grok-beta", "text", 0},      // exact match first
	{"grok-vision-", "vision", 1}, // grok-vision-beta (12 chars)
	{"grok-", "text", 1},          // fallback to text (5 chars)

	// StepFun - Vision first
	{"step-1v-", "vision", 1},
	{"step-", "text", 1},

	// NVIDIA
	{"nemotron-", "text", 1},

	// ========== Priority 2: Suffix matches (pattern starts with *) ==========
	{"-vision-instruct", "vision", 2},
	{"-vision", "vision", 2}, // catches ernie-4.0-vision, grok-vision, etc.
	{"-audio-preview", "audio", 2},
	{"-audio", "audio", 2},
	{"-nemo", "text", 2},

	// ========== Priority 3: Contains matches (pattern has * on both sides) ==========
	{"-vision-", "vision", 3}, // matches any model with "-vision-" in the middle
	{"-vl-", "vision", 3},     // visual-language models
	{"-audio-", "audio", 3},
	{"whisper", "audio", 3},
	{"-tts-", "audio", 3},
	{"-stt-", "audio", 3},
	{"embedding", "embedding", 3},
	{"-embed-", "embedding", 3},
	{"-multimodal-", "multimodal", 3},
	{"-neva-", "vision", 3}, // NVIDIA vision models
}

// matchTiers is the evaluation order for modality rules: exact match first,
// then prefix, then suffix, then contains. Within a tier the longest pattern
// wins, so a specific rule always beats a generic one from the same tier.
var matchTiers = [4]int{0, 1, 2, 3}

// ruleMatches reports whether a single rule matches the normalized name,
// using the comparison implied by the rule's priority tier.
func ruleMatches(rule modalityRule, normalized string) bool {
	switch rule.priority {
	case 0: // Exact match
		return rule.pattern == normalized
	case 1: // Prefix match (pattern ends with *, but we store it without *)
		return strings.HasPrefix(normalized, rule.pattern)
	case 2: // Suffix match (pattern starts with *, but we store it without *)
		return strings.HasSuffix(normalized, rule.pattern)
	case 3: // Contains match (pattern has content between *)
		return strings.Contains(normalized, rule.pattern)
	}
	return false
}

// InferModality returns the default modality for a given model name.
// Returns "text" if no pattern matches (conservative default).
//
// Matching strategy:
//  1. Exact match (no wildcards)
//  2. Prefix match (longest first)
//  3. Suffix match (longest first)
//  4. Contains match (longest first)
//  5. Default to "text"
//
// 2026-08-09: the tiers above are now enforced by the matcher itself rather
// than relying on the declaration order of modalityRules. The previous
// implementation returned the first rule that matched while walking the slice
// top-to-bottom, so a broad prefix rule declared above a specific rule from a
// stronger tier would win — e.g. the `glm-` prefix (text) shadowed the `-vl-`
// contains rule, labelling glm-4.5v as text, and the `qwen2.5-` prefix
// shadowed `-vl-` for qwen2.5-vl-*. Mislabelling a vision model as text is not
// cosmetic: loadCandidatesByModalityDB only admits modality IN ('vision',
// 'multimodal') for an image request, so a shadowed model is dropped from the
// candidate set and the request 503s instead of routing.
//
// Used by:
//   - discovery.Service.registerModel (seed models_canonical.modality)
//   - bg model probe (verify modality on first probe)
func InferModality(modelName string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	if normalized == "" {
		return "text"
	}

	// Walk tier by tier so a stronger match always beats a weaker one
	// regardless of where each rule sits in modalityRules. Within a tier,
	// prefer the longest pattern for specificity.
	for _, tier := range matchTiers {
		best := ""
		bestModality := ""
		for _, rule := range modalityRules {
			if rule.priority != tier || !ruleMatches(rule, normalized) {
				continue
			}
			if len(rule.pattern) > len(best) || bestModality == "" {
				best = rule.pattern
				bestModality = rule.modality
			}
		}
		if bestModality != "" {
			return bestModality
		}
	}

	// Default: text
	return "text"
}
