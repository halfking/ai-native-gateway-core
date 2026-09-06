// Package catalog resolves tenant-facing vendor labels and modality for standard models.
package catalog

import "strings"

var familyVendor = map[string]string{
	"openai-gpt": "OpenAI", "gpt": "OpenAI", "o3": "OpenAI", "o4": "OpenAI", "sora": "OpenAI",
	"anthropic-claude": "Anthropic", "anthropic": "Anthropic", "claude": "Anthropic",
	"google-gemini": "Google", "gemini": "Google", "gemma": "Google",
	"deepseek": "DeepSeek",
	"qwen":     "Alibaba", "qwen2": "Alibaba", "qwen3": "Alibaba", "qwen3.5": "Alibaba", "qwen3.6": "Alibaba", "qwen3.8": "Alibaba", "qwq": "Alibaba", "wan2": "Alibaba", "wan2.6": "Alibaba",
	"doubao":    "ByteDance",
	"zhipu-glm": "Zhipu AI", "glm": "Zhipu AI",
	"meta-llama": "Meta", "llama": "Meta", "llama2": "Meta", "llama3": "Meta", "codellama": "Meta",
	"minimax": "MiniMax", "abab5.5": "MiniMax", "abab6.5s": "MiniMax",
	"mimo": "小米", "xiaomi-mimo": "小米",
	"mistral": "Mistral AI", "ministral": "Mistral AI", "mixtral": "Mistral AI", "codestral": "Mistral AI",
	"moonshot": "Moonshot AI", "kimi": "Moonshot AI",
	"xai": "xAI", "grok": "xAI",
	"stepfun": "StepFun", "step": "StepFun",
	"baichuan":   "Baichuan",
	"yi":         "01.AI",
	"perplexity": "Perplexity", "sonar": "Perplexity",
	"sensenova": "商汤",
	// 历史 family 字符串兼容：612 之前的历史 seed 行 family='sensetime'
	"sensetime": "商汤",
	"cohere":    "Cohere",
	"nvidia":    "NVIDIA", "nemotron": "NVIDIA", "nv": "NVIDIA",
	"phi":    "Microsoft",
	"cursor": "Cursor",

	// 2026-09-07 (676): 默认厂商识别扩展。models_canonical 允许出现
	// model_families 没有行的孤儿 family（如 qwen3.8），InferVendor 先查
	// 本表（family id 精确命中），查不到再按名称前缀推断。
	// Alibaba 通义系（qwq/wan2 已在上面）：
	"qwen2.5": "Alibaba", "qwen3.7": "Alibaba",
	// OpenAI 全家桶：
	"openai-embedding": "OpenAI", "openai-image": "OpenAI", "openai-audio": "OpenAI", "codex": "OpenAI",
	// Google：
	"codegemma": "Google", "recurrentgemma": "Google", "diffusiongemma": "Google", "google-palm": "Google", "deplot": "Google", "lyria": "Google",
	// 智谱：
	"glm5.2": "Zhipu AI", "chatglm": "Zhipu AI",
	// NVIDIA（nv/nemotron 已在上面）：
	"nvidia-nemotron": "NVIDIA", "nvclip": "NVIDIA", "neva": "NVIDIA", "riva": "NVIDIA", "nemoretriever": "NVIDIA", "cosmos": "NVIDIA", "vila": "NVIDIA",
	// 字节（doubao 已在上面）：
	"seed": "ByteDance",
	// 国内其他：
	"hunyuan": "Tencent", "ernie": "Baidu", "pangu": "Huawei", "spark": "iFlytek", "longcat": "Meituan", "ling": "InclusionAI", "bge": "BAAI", "youdao": "Youdao", "dots": "rednote", "kat": "Kwaipilot",
	// Mistral 系：
	"devstral": "Mistral AI", "voxtral": "Mistral AI",
	// 微软（phi 已在上面）：
	"wizardlm": "Microsoft", "kosmos": "Microsoft",
	// Meta：codellama 已在上面
	// 海外长尾（family id 与模型名同源）：
	"naver-hyperclova": "Naver", "starcoder2": "BigCode", "bigscience-bloom": "BigScience", "olmo": "AI2", "jamba": "AI21", "falcon": "TII", "dbrx": "Databricks", "arctic": "Snowflake", "palmyra": "Writer", "stability": "Stability AI", "hermes": "Nous Research", "solar": "Upstage", "sakana": "Sakana AI", "sarvam": "Sarvam AI", "stockmark": "Stockmark", "eleutherai": "EleutherAI", "lfm": "Liquid AI", "mercury": "Inception", "fuyu": "Adept", "zamba2": "Zyphra", "sea": "AI Singapore", "morph": "Morph", "relace": "Relace", "reka": "Reka", "rinna": "rinna",
	// 社区微调/合并模型（TheDrummer/Gryphe/Doktor 等无名分发）：
	"l3": "Community", "l3.1": "Community", "l3.3": "Community", "dolphin": "Community", "mythomax": "Community", "magnum": "Community", "rocinante": "Community", "remm": "Community", "cydonia": "Community", "unslopnemo": "Community", "allamoe": "Community", "dracarys": "Community", "skyfall": "Community",
}

type namePrefix struct {
	prefix string
	vendor string
}

var nameVendorPrefixes = []namePrefix{
	{"minimax-m3", "MiniMax"}, {"minimax-m2", "MiniMax"}, {"minimax", "MiniMax"}, {"abab", "MiniMax"},
	{"gpt-", "OpenAI"}, {"gpt-image", "OpenAI"}, {"text-embedding-", "OpenAI"}, {"sora-", "OpenAI"},
	// 2026-09-07 (676): OpenAI 全家桶默认识别。
	{"chatgpt", "OpenAI"}, {"dall-e", "OpenAI"}, {"tts-", "OpenAI"}, {"whisper", "OpenAI"}, {"davinci", "OpenAI"}, {"codex", "OpenAI"},
	{"claude-", "Anthropic"},
	{"gemini-", "Google"}, {"gemma-", "Google"},
	// 676: Google 长尾（gemma 变体 / palm / 研究模型）。
	{"codegemma", "Google"}, {"recurrentgemma", "Google"}, {"diffusiongemma", "Google"}, {"deplot", "Google"}, {"lyria", "Google"},
	{"qwen", "Alibaba"}, {"wan2", "Alibaba"}, {"qwq", "Alibaba"},
	{"doubao", "ByteDance"}, {"skylark", "ByteDance"}, {"seed", "ByteDance"}, {"ui-tars", "ByteDance"}, {"longcat", "Meituan"},
	{"deepseek", "DeepSeek"},
	// 676: glm 去掉连字符限制，覆盖 glm5.2-ultraspeed 等无连字符命名。
	{"glm", "Zhipu AI"}, {"chatglm", "Zhipu AI"},
	{"llama-", "Meta"}, {"llama2-", "Meta"}, {"llama3-", "Meta"}, {"codellama", "Meta"}, {"llama", "Meta"},
	{"mimo-", "小米"},
	{"kimi-", "Moonshot AI"}, {"moonshot-", "Moonshot AI"},
	{"grok-", "xAI"},
	{"mistral", "Mistral AI"}, {"mixtral", "Mistral AI"}, {"codestral", "Mistral AI"}, {"ministral", "Mistral AI"}, {"open-mistral", "Mistral AI"},
	{"devstral", "Mistral AI"}, {"voxtral", "Mistral AI"}, {"pixtral", "Mistral AI"}, {"mathstral", "Mistral AI"},
	{"step-", "StepFun"},
	{"baichuan", "Baichuan"},
	{"yi-", "01.AI"},
	{"sonar", "Perplexity"},
	{"sensechat", "商汤"},
	// 2026-08-29 (612): 显式覆盖 sensenova-* 新行（含 u1-fast / u1.5-lite /
	// 6.x-flash-lite），使 inferVendorFromName 直接命中。
	{"sensenova", "商汤"},
	{"command-", "Cohere"}, {"aya-", "Cohere"},
	// 676: NVIDIA 长尾（nemo* 统一覆盖 nemotron/nemoretriever/nemoguard）。
	{"nemo", "NVIDIA"}, {"nv-", "NVIDIA"}, {"nvclip", "NVIDIA"}, {"neva", "NVIDIA"}, {"riva", "NVIDIA"}, {"cosmos", "NVIDIA"}, {"vila", "NVIDIA"},
	{"phi-", "Microsoft"}, {"wizardlm", "Microsoft"}, {"kosmos", "Microsoft"},
	{"o1", "OpenAI"}, {"o3", "OpenAI"}, {"o4", "OpenAI"},
	// ── 2026-09-07 (676): 默认厂商识别——名称前缀兜底。family id 命中
	// familyVendor 优先；此处覆盖 family 行缺失但模型名可辨识的场景。
	// 注意顺序敏感：{"palmyra"} 必须在 {"palm"} 之前，否则被并成 Google。
	{"palmyra", "Writer"}, {"palm", "Google"},
	{"hunyuan", "Tencent"}, {"ernie", "Baidu"}, {"pangu", "Huawei"}, {"spark", "iFlytek"}, {"youdao", "Youdao"},
	{"bge-", "BAAI"}, {"ling-", "InclusionAI"}, {"kat-", "Kwaipilot"}, {"dots", "rednote"},
	{"hyperclova", "Naver"}, {"sea-lion", "AI Singapore"}, {"zamba", "Zyphra"},
	{"jamba", "AI21"}, {"falcon", "TII"}, {"dbrx", "Databricks"}, {"granite", "IBM"},
	{"olmo", "AI2"}, {"tulu", "AI2"}, {"starcoder", "BigCode"}, {"bloom", "BigScience"},
	{"solar", "Upstage"}, {"hermes", "Nous Research"}, {"sakana", "Sakana AI"}, {"sarvam", "Sarvam AI"},
	{"stockmark", "Stockmark"}, {"reka", "Reka"}, {"rinna", "rinna"}, {"mercury", "Inception"}, {"lfm", "Liquid AI"}, {"fuyu", "Adept"},
	{"stable-diffusion", "Stability AI"}, {"stability", "Stability AI"}, {"arctic", "Snowflake"},
	{"morph-", "Morph"}, {"relace-", "Relace"},
	// 社区微调/合并模型统一归 Community，不再散落「其他」。
	{"l3", "Community"}, {"dolphin", "Community"}, {"mythomax", "Community"}, {"magnum", "Community"},
	{"rocinante", "Community"}, {"remm-", "Community"}, {"cydonia", "Community"}, {"unslop", "Community"},
	{"allamoe", "Community"}, {"dracarys", "Community"}, {"skyfall", "Community"},
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

// InferVendor resolves the vendor from known family ids and canonical name
// prefixes only.  Unlike ResolveVendor it never falls back to HumanizeFamilyID
// or 「其他」, so callers can keep their own default when inference fails.
func InferVendor(canonicalName, family string) string {
	family = strings.TrimSpace(family)
	if v, ok := familyVendor[family]; ok {
		return v
	}
	return inferVendorFromName(canonicalName)
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
