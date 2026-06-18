// Package catalog resolves tenant-facing vendor labels and modality for standard models.
package catalog

import "strings"

var familyVendor = map[string]string{
	"openai-gpt": "OpenAI", "gpt": "OpenAI", "o3": "OpenAI", "o4": "OpenAI", "sora": "OpenAI",
	"anthropic-claude": "Anthropic", "anthropic": "Anthropic", "claude": "Anthropic",
	"google-gemini": "Google", "gemini": "Google", "gemma": "Google",
	"deepseek": "DeepSeek",
	"qwen": "Alibaba", "qwen2": "Alibaba", "qwen3": "Alibaba", "qwen3.5": "Alibaba", "qwen3.6": "Alibaba", "qwq": "Alibaba", "wan2": "Alibaba", "wan2.6": "Alibaba",
	"doubao": "ByteDance",
	"zhipu-glm": "Zhipu AI", "glm": "Zhipu AI",
	"meta-llama": "Meta", "llama": "Meta", "llama2": "Meta", "llama3": "Meta", "codellama": "Meta",
	"minimax": "MiniMax", "abab5.5": "MiniMax", "abab6.5s": "MiniMax",
	"mimo": "小米", "xiaomi-mimo": "小米",
	"mistral": "Mistral AI", "ministral": "Mistral AI", "mixtral": "Mistral AI", "codestral": "Mistral AI",
	"moonshot": "Moonshot AI", "kimi": "Moonshot AI",
	"xai": "xAI", "grok": "xAI",
	"stepfun": "StepFun", "step": "StepFun",
	"baichuan": "Baichuan",
	"yi": "01.AI",
	"perplexity": "Perplexity", "sonar": "Perplexity",
	"sensenova": "商汤",
	"cohere": "Cohere",
	"nvidia": "NVIDIA", "nemotron": "NVIDIA", "nv": "NVIDIA",
	"phi": "Microsoft",
	"cursor": "Cursor",
}

type namePrefix struct {
	prefix string
	vendor string
}

var nameVendorPrefixes = []namePrefix{
	{"minimax-m3", "MiniMax"}, {"minimax-m2", "MiniMax"}, {"minimax", "MiniMax"}, {"abab", "MiniMax"},
	{"gpt-", "OpenAI"}, {"gpt-image", "OpenAI"}, {"text-embedding-", "OpenAI"}, {"sora-", "OpenAI"},
	{"claude-", "Anthropic"},
	{"gemini-", "Google"}, {"gemma-", "Google"},
	{"qwen", "Alibaba"}, {"wan2", "Alibaba"},
	{"doubao", "ByteDance"},
	{"deepseek", "DeepSeek"},
	{"glm-", "Zhipu AI"},
	{"llama-", "Meta"}, {"llama2-", "Meta"}, {"llama3-", "Meta"},
	{"mimo-", "小米"},
	{"kimi-", "Moonshot AI"}, {"moonshot-", "Moonshot AI"},
	{"grok-", "xAI"},
	{"mistral", "Mistral AI"}, {"mixtral", "Mistral AI"}, {"codestral", "Mistral AI"}, {"ministral", "Mistral AI"}, {"open-mistral", "Mistral AI"},
	{"step-", "StepFun"},
	{"baichuan", "Baichuan"},
	{"yi-", "01.AI"},
	{"sonar", "Perplexity"},
	{"sensechat", "商汤"},
	{"command-", "Cohere"},
	{"nemotron", "NVIDIA"}, {"phi-", "Microsoft"},
	{"o1", "OpenAI"}, {"o3", "OpenAI"}, {"o4", "OpenAI"},
}

// ResolveVendor picks the OEM brand for grouping model catalogs.
func ResolveVendor(canonicalName, family, dbVendor string) string {
	if v := strings.TrimSpace(dbVendor); v != "" {
		return v
	}
	family = strings.TrimSpace(family)
	if v, ok := familyVendor[family]; ok {
		return v
	}
	if v := inferVendorFromName(canonicalName); v != "" {
		return v
	}
	if family != "" {
		return HumanizeFamilyID(family)
	}
	return "其他"
}

func inferVendorFromName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	for _, p := range nameVendorPrefixes {
		if strings.HasPrefix(n, p.prefix) {
			return p.vendor
		}
	}
	return ""
}

// HumanizeFamilyID turns an internal family id into a display label when vendor is unknown.
func HumanizeFamilyID(id string) string {
	if v, ok := familyVendor[id]; ok {
		return v
	}
	parts := strings.Split(id, "-")
	if len(parts) >= 2 && len(parts[0]) > 0 && len(parts[1]) > 0 {
		return strings.ToUpper(parts[0][:1]) + parts[0][1:] + " " + strings.ToUpper(parts[1][:1]) + parts[1][1:]
	}
	if id == "" {
		return "其他"
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// EffectiveModality returns the modality shown to tenants.
func EffectiveModality(canonicalName, stored string) string {
	s := strings.TrimSpace(strings.ToLower(stored))
	name := strings.ToLower(strings.TrimSpace(canonicalName))
	switch s {
	case "multimodal", "vision", "audio", "embedding":
		return s
	}
	if inferred := inferModalityFromName(name); inferred != "" {
		return inferred
	}
	if s == "" {
		return "text"
	}
	return s
}

func inferModalityFromName(name string) string {
	if name == "" {
		return ""
	}
	if strings.Contains(name, "embedding") || strings.Contains(name, "embed-") ||
		strings.HasPrefix(name, "bge-") || strings.HasPrefix(name, "text-embedding") {
		return "embedding"
	}
	if strings.Contains(name, "audio") || strings.HasSuffix(name, "-tts") {
		return "audio"
	}
	if strings.HasPrefix(name, "minimax-m3") {
		return "multimodal"
	}
	if strings.Contains(name, "-vl") || strings.Contains(name, "vl-") ||
		strings.Contains(name, "vision") || strings.HasPrefix(name, "gpt-4o") ||
		strings.HasPrefix(name, "gpt-5") || strings.HasPrefix(name, "claude-") ||
		strings.HasPrefix(name, "gemini-") || strings.Contains(name, "qwen3-vl") {
		return "multimodal"
	}
	return ""
}

// FamilyDisplayAndVendor returns display name and vendor for a family id.
func FamilyDisplayAndVendor(familyID string) (displayName, vendor string) {
	if familyID == "" {
		return "", ""
	}
	legacy := map[string]string{
		"openai-gpt": "OpenAI GPT", "anthropic-claude": "Anthropic Claude", "google-gemini": "Google Gemini",
		"deepseek": "DeepSeek", "qwen": "Qwen (通义千问)", "doubao": "Doubao (豆包)",
		"zhipu-glm": "Zhipu GLM", "meta-llama": "Meta Llama", "minimax": "MiniMax", "xiaomi-mimo": "Xiaomi MiMo",
	}
	if dn, ok := legacy[familyID]; ok {
		return dn, familyVendor[familyID]
	}
	return HumanizeFamilyID(familyID), ResolveVendor("", familyID, "")
}
