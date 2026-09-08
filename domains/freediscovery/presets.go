package freediscovery

import (
	"sort"
	"strings"
)

// ProviderPreset 内置供应商预设 (源自 Orbi templates/pi-providers/*.json 的多租户化).
// SeedTemplate 用预设一键建模板, 减少手工录入.
type ProviderPreset struct {
	ProviderCode string
	DisplayName  string
	BaseURL      string
	APIType      APIType
	APIKeyEnv    string // 空 = keyless
	TosURL       string
	TosVerdict   string
	TosNotes     string
	// FreeOf 判定上游模型条目是否免费 (nil = 默认 :free 后缀规则)
	FreeOf func(m modelEntry) bool
	// PoolKeyOf 共享配额池推断 (nil = 无共享池)
	PoolKeyOf func(modelID string) string
	// QuotaEstimator 估算免费配额 (nil = 不估算, 0)
	QuotaEstimator func(modelID string) (monthly, daily int64)
}

// builtinPresets 内置预设注册表. 新增提供商只需在此追加.
var builtinPresets = map[string]*ProviderPreset{
	"groq": {
		ProviderCode: "groq",
		DisplayName:  "Groq Cloud (Free Tier)",
		BaseURL:      "https://api.groq.com/openai/v1",
		APIType:      APITypeOpenAICompletions,
		APIKeyEnv:    "$GROQ_API_KEY",
		TosURL:       "https://groq.com/terms-of-use/",
		TosVerdict:   "caution",
		TosNotes:     "Free tier documented (RPM/RPD/TPD limits); production proxy of free tier is a gray area — review before scale",
		// Groq 免费层按账户+模型计: /models 返回的全部模型在免费账户内均可用
		FreeOf: func(modelEntry) bool { return true },
		// 配额按模型独立计 (无跨模型共享池)
		QuotaEstimator: func(string) (int64, int64) { return 0, 14400 },
	},
	"openrouter": {
		ProviderCode: "openrouter",
		DisplayName:  "OpenRouter (Free Models)",
		BaseURL:      "https://openrouter.ai/api/v1",
		APIType:      APITypeOpenAICompletions,
		APIKeyEnv:    "$OPENROUTER_API_KEY",
		TosURL:       "https://openrouter.ai/docs/api-reference/limits",
		TosVerdict:   "ok",
		TosNotes:     "':'-suffixed models are officially free; all :free models share one daily request pool per account",
		// 默认规则已覆盖 (:free 后缀 / 零定价), 显式留 nil
		PoolKeyOf: func(modelID string) string {
			if strings.HasSuffix(modelID, ":free") {
				return "openrouter-free-pool"
			}
			return ""
		},
		QuotaEstimator: func(modelID string) (int64, int64) {
			if strings.HasSuffix(modelID, ":free") {
				return 0, 50
			}
			return 0, 0
		},
	},
	"google-ai-studio": {
		ProviderCode: "google-ai-studio",
		DisplayName:  "Google AI Studio (Free Tier)",
		BaseURL:      "https://generativelanguage.googleapis.com/v1beta",
		APIType:      APITypeGoogleGenerativeAI,
		APIKeyEnv:    "$GOOGLE_API_KEY",
		TosURL:       "https://ai.google.dev/gemini-api/terms",
		TosVerdict:   "caution",
		TosNotes:     "Free tier granted via AI Studio; Google may use prompts to improve products — not for sensitive workloads",
	},
	"siliconflow": {
		ProviderCode: "siliconflow",
		DisplayName:  "SiliconFlow (Free Models)",
		BaseURL:      "https://api.siliconflow.cn/v1",
		APIType:      APITypeOpenAICompletions,
		APIKeyEnv:    "$SILICONFLOW_API_KEY",
		TosURL:       "https://www.siliconflow.cn/agreement",
		TosVerdict:   "unknown",
		TosNotes:     "Free model list changes frequently; verify per-model pricing before import",
	},
	"zhipu": {
		ProviderCode: "zhipu",
		DisplayName:  "Zhipu BigModel (Free Models)",
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		APIType:      APITypeOpenAICompletions,
		APIKeyEnv:    "$ZHIPU_API_KEY",
		TosURL:       "https://open.bigmodel.cn/dev/api/free",
		TosVerdict:   "unknown",
		TosNotes:     "glm-*-8b/9b flash models documented as free with RPM limits",
	},
}

// GetPreset 返回内置预设 (找不到返回 nil).
func GetPreset(providerCode string) *ProviderPreset {
	return builtinPresets[providerCode]
}

// ListPresetCodes 返回全部内置预设 code (字母序稳定输出).
func ListPresetCodes() []string {
	codes := make([]string, 0, len(builtinPresets))
	for code := range builtinPresets {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// PresetToCreateRequest 把预设转换为创建请求 (displayName 可覆盖).
func PresetToCreateRequest(p *ProviderPreset) *CreateTemplateRequest {
	verdict := p.TosVerdict
	if verdict == "" {
		verdict = "unknown"
	}
	return &CreateTemplateRequest{
		ProviderCode:   p.ProviderCode,
		DisplayName:    p.DisplayName,
		BaseURL:        p.BaseURL,
		APIType:        p.APIType,
		APIKeyEnv:      p.APIKeyEnv,
		ModelsEndpoint: "/models",
		TosURL:         p.TosURL,
		TosVerdict:     verdict,
		TosNotes:       p.TosNotes,
	}
}
