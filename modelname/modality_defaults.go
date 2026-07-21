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
	{"o3-mini-", "vision", 1},

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
	{"glm-4v-", "vision", 1},
	{"cogview-", "vision", 1},
	{"glm-4-", "text", 1},
	{"glm-3-", "text", 1},
	{"chatglm", "text", 1},
	{"codegeex-", "text", 1},
	{"glm-", "text", 1},

	// Moonshot
	{"moonshot-", "text", 1},
	{"kimi-", "text", 1},

	// ByteDance Doubao - Text specific patterns first (longer wins)
	{"doubao-pro-", "text", 1},      // doubao-pro-32k (10 chars, before doubao-)
	{"doubao-lite-", "text", 1},     // doubao-lite-* (11 chars)
	{"doubao-vision-", "vision", 1}, // doubao-vision-* (14 chars)
	{"doubao-", "text", 1},          // fallback (7 chars)
	{"seed-", "text", 1},

	// Alibaba Qwen - Vision first
	{"qwen-vl-", "vision", 1},
	{"qwen2-vl-", "vision", 1},
	{"qwen-audio-", "audio", 1},
	{"qwen2.5-", "text", 1},
	{"qwen2-", "text", 1},
	{"qwen-", "text", 1},

	// DeepSeek
	{"deepseek-", "text", 1},

	// MiniMax
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
// Used by:
//   - discovery.Service.registerModel (seed models_canonical.modality)
//   - bg model probe (verify modality on first probe)
func InferModality(modelName string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	if normalized == "" {
		return "text"
	}

	// Evaluate rules in order (already sorted by priority and specificity)
	for _, rule := range modalityRules {
		matched := false

		switch rule.priority {
		case 0: // Exact match
			matched = (rule.pattern == normalized)

		case 1: // Prefix match (pattern ends with *, but we store it without *)
			matched = strings.HasPrefix(normalized, rule.pattern)

		case 2: // Suffix match (pattern starts with *, but we store it without *)
			matched = strings.HasSuffix(normalized, rule.pattern)

		case 3: // Contains match (pattern has content between *)
			matched = strings.Contains(normalized, rule.pattern)
		}

		if matched {
			return rule.modality
		}
	}

	// Default: text
	return "text"
}
