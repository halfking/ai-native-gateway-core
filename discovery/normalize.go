package discovery

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// NormalizeModelName is a thin wrapper around modelname.NormalizeRouteKey.
// The discovery package used to maintain a separate normalization with its
// own regex set; that implementation has been retired in favour of the
// modelname package's single source of truth (P1 of
// 2026-06-18-model-match-and-404-plan.md). Behaviour is identical for the
// case-insensitive vendor-prefix-stripping + date-stripping + dash-collapse
// pipeline; see modelname.NormalizeRouteKey for details.
func NormalizeModelName(raw string) string {
	return modelname.NormalizeRouteKey(raw)
}

// vendorCanonicalFamilies maps the leading-token of a normalized model
// name to the canonical family id that should be used in
// models_canonical.family / model_families.id. The collapse eliminates
// historical family-id splits that arose because (a) the legacy Python
// llm-gateway + admin UI used vendor-prefixed ids like
// "anthropic-claude" / "openai-gpt" / "meta-llama" / "google-gemini" /
// "zhipu-glm", while (b) the Go rewrite's InferFamily (introduced
// during the 2026-06-18 P1 normalize refactor) returned the bare
// token ("claude" / "gpt" / "llama" / "gemini" / "glm").  The two
// naming schemes co-existed in the DB, so a family-chip filter in
// /models page returned 1 model for "Anthropic" instead of the 21
// claude models that should match (2026-06-20 user report).
//
// We do NOT collapse token differences that have *intra-vendor*
// meaning: qwen / qwen2 / qwen3 / qwen3.5 / qwen3.6 stay separate
// (different model generations), gemma stays separate from gemini
// (Google's gemma line is a distinct family), and unknown tokens fall
// through to the bare token so a brand-new vendor still works without
// a code change.
//
// 2026-06-20: re-introduced after the 2026-06-18 refactor removed the
// previous hard-coded map. The previous version was an if/else
// cascade; this is a single map for the same effect.
var vendorCanonicalFamilies = map[string]string{
	// ========== 国际厂商 ==========

	// Anthropic (美国) — all "claude-*" collapse to "anthropic-claude"
	"claude": "anthropic-claude",

	// OpenAI (美国) — gpt-*, o1/o3/o4/o5, dall-e, whisper, tts
	"gpt":  "openai-gpt",
	"o1":   "openai-gpt",
	"o3":   "openai-gpt",
	"o4":   "openai-gpt",
	"o5":   "openai-gpt",
	"dall": "openai-image", // dall-e-3 → openai-image

	// Meta (美国) — llama/llama2/llama3/llama4, codellama
	"llama":     "meta-llama",
	"llama2":    "meta-llama",
	"llama3":    "meta-llama",
	"llama4":    "meta-llama",
	"codellama": "meta-llama",

	// Google (美国) — gemini vs gemma (独立family), palm, bard
	"gemini": "google-gemini",
	"gemma":  "gemma",
	"palm":   "google-palm",
	"bard":   "google-gemini",

	// Mistral AI (法国) — mistral / mixtral / ministral / codestral
	"mistral":   "mistral",
	"ministral": "mistral",
	"mixtral":   "mistral",
	"codestral": "mistral",

	// Cohere (加拿大) — command, embed, rerank
	"command": "cohere",
	"embed":   "cohere",
	"rerank":  "cohere",

	// xAI (美国, Elon Musk) — grok
	"grok": "xai-grok",

	// Microsoft (美国) — phi
	"phi": "microsoft-phi",

	// NVIDIA (美国) — nemotron
	"nemotron": "nvidia-nemotron",

	// Perplexity (美国) — sonar
	"sonar": "perplexity-sonar",

	// Stability AI (英国) — stable-diffusion, stable-lm
	"stable": "stability",

	// ========== 中国厂商 - 互联网巨头 ==========

	// 阿里云 / Alibaba — qwen 各版本独立, qwq
	"qwen":  "qwen",
	"qwen2": "qwen2",
	"qwen3": "qwen3",
	"qwq":   "qwq",

	// 腾讯 / Tencent — hunyuan (混元)
	"hunyuan": "hunyuan",

	// 字节跳动 / ByteDance — doubao (豆包)
	"doubao": "doubao",

	// 百度 / Baidu — ernie (文心), wenxin
	"ernie":  "ernie",
	"wenxin": "ernie",

	// ========== 中国厂商 - AI 独角兽 ==========

	// 智谱 AI / Zhipu AI — glm, chatglm, codegeex
	"glm":      "zhipu-glm",
	"chatglm":  "zhipu-glm",
	"codegeex": "zhipu-glm",

	// 月之暗面 / Moonshot AI — moonshot, kimi
	"kimi":     "moonshot",
	"moonshot": "moonshot",

	// 零一万物 / 01.AI (李开复) — yi
	"yi": "yi",

	// 稀宇科技 / MiniMax — minimax, abab (旧系列)
	// Note: "abab5.5" / "abab6.5s" 无"-"分隔符，InferFamily返回原值(向后兼容)
	"minimax": "minimax",
	"abab":    "minimax",

	// 深度求索 / DeepSeek — deepseek
	"deepseek": "deepseek",

	// 阶跃星辰 / StepFun — step, stepfun
	"step":    "stepfun",
	"stepfun": "stepfun",

	// 百川智能 / Baichuan — baichuan
	"baichuan": "baichuan",

	// 光年之外 / LightYear (王慧文) — kuae, skywork
	"kuae":    "kuae",
	"skywork": "kuae",

	// 商汤科技 / SenseTime — sensechat, sensenova
	"sensechat": "sensetime",
	"sensenova": "sensetime",

	// ========== 中国厂商 - 传统科技公司 ==========

	// 科大讯飞 / iFlytek — spark (星火), xinghuo
	"spark":   "spark",
	"xinghuo": "spark",

	// 小米 / Xiaomi — mimo
	"mimo": "mimo",

	// 华为 / Huawei — pangu (盘古)
	"pangu": "pangu",

	// 网易 / NetEase — youdao (有道)
	"youdao": "youdao",

	// ========== 其他地区厂商 ==========

	// NAVER (韩国) — hyperclova
	"hyperclova": "naver-hyperclova",

	// Rinna (日本) — rinna, japanese-gpt
	"rinna": "rinna",

	// Inception / TII (阿联酋) — falcon
	"falcon": "falcon",

	// SDAIA (沙特) — allamoe
	"allamoe": "allamoe",

	// ========== 开源社区 ==========

	// EleutherAI — gpt-neo, gpt-j, pythia
	"pythia": "eleutherai",

	// BigScience — bloom
	"bloom": "bigscience-bloom",

	// Together AI — together
	"together": "together",

	// Cursor (IDE) — cursor
	"cursor": "cursor",
}

// CanonicalizeFamilyID takes a raw family id (as it might be stored in
// the DB from either the legacy Python admin UI or the Go
// InferFamily) and returns the vendor-prefixed canonical form. If the
// input is not a known split, the input is returned unchanged.
//
// Examples:
//
//	"claude"           → "anthropic-claude"
//	"anthropic-claude" → "anthropic-claude"  (idempotent)
//	"minimax"          → "minimax"           (no split, passthrough)
func CanonicalizeFamilyID(family string) string {
	if f, ok := vendorCanonicalFamilies[family]; ok {
		return f
	}
	return family
}

// InferFamily returns the canonical family id for a normalized model
// name. The previous generic form (just the leading token) was
// correct for families with a 1:1 leading-token mapping (mimo, glm,
// minimax, qwen) but produced the wrong id for vendors whose
// canonical name prefix differs from their family id (claude-*
// should map to "anthropic-claude" not "claude", gpt-* to
// "openai-gpt" not "gpt", etc.).  This now consults
// vendorCanonicalFamilies first and falls back to the bare token.
//
// Examples:
//
//	"mimo-v2.5-pro"     → "mimo"
//	"glm-5.1"           → "zhipu-glm"
//	"claude-opus-4-6"   → "anthropic-claude"
//	"gpt-5.4"           → "openai-gpt"
//	"o3-mini"           → "openai-gpt"
//	"minimax-m3"        → "minimax"
//	"qwen-max"          → "qwen"
//
// If the name has no "-" the whole name is returned.  An empty /
// whitespace input returns "unknown".
// canonicalFamilyIDs is the inverse of vendorCanonicalFamilies: the
// set of vendor-prefixed canonical ids (values of the map).  Used to
// recognise an already-canonical name passed to InferFamily (e.g.
// "anthropic-claude" / "openai-gpt" / "meta-llama"), which the admin
// UI may have stored verbatim.  Built once at package init.
var canonicalFamilyIDs = func() map[string]bool {
	set := make(map[string]bool, len(vendorCanonicalFamilies))
	for _, v := range vendorCanonicalFamilies {
		set[v] = true
	}
	return set
}()

func InferFamily(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "unknown"
	}
	// Already canonical? (e.g. "anthropic-claude" passed in — the
	// legacy admin UI / Python path may have already stored it in
	// the vendor-prefixed form).
	if canonicalFamilyIDs[name] {
		return name
	}
	if idx := strings.Index(name, "-"); idx > 0 {
		first := name[:idx]
		if canonical, ok := vendorCanonicalFamilies[first]; ok {
			return canonical
		}
		return first
	}
	return name
}

// GenerateAliases produces the set of alternative names a (canonical_id,
// raw_model_name) pair should be reachable by. New aliases are written to
// model_aliases at sync time, and the SQL resolver in
// provider/client.go:loadCandidatesDB joins on them when the client's
// requested model name doesn't exactly match the offer's raw_model_name.
//
// The previous implementation had a GLM-specific block (4 extra aliases
// for cross-form names like "glm-4.7" / "glm-4-7" / "glm47"). That
// special case is gone — every model family now gets the same 5 aliases
// below. If a future family needs additional cross-form coverage, add
// the variants explicitly to model_aliases after the sync (e.g. via a
// dedicated "alias: model:foo -> model:bar" admin endpoint) rather than
// re-introducing family-specific code here.
func GenerateAliases(rawName, canonicalName string) []string {
	return modelname.GenerateAliasVariants(rawName, canonicalName)
}
