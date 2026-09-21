package freediscovery

import (
	"sort"
	"strings"
)

// ProviderPreset built-in provider preset (sourced from Orbi
// templates/pi-providers/*.json, multi-tenantified).
// SeedTemplate uses the preset to create a template in one step, reducing
// manual entry.
type ProviderPreset struct {
	ProviderCode string
	DisplayName  string
	BaseURL      string
	APIType      APIType
	APIKeyEnv    string // empty = keyless
	TosURL       string
	TosVerdict   string
	TosNotes     string
	// FreeOf judges whether an upstream model entry is free
	// (nil = default :free suffix rule).
	FreeOf func(m modelEntry) bool
	// PoolKeyOf infers the shared quota pool (nil = no shared pool).
	PoolKeyOf func(modelID string) string
	// QuotaEstimator estimates the free quota (nil = no estimate, 0).
	QuotaEstimator func(modelID string) (monthly, daily int64)
}

// builtinPresets is the built-in preset registry. To add a new provider, just
// append here.
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
		// Groq free tier is metered per account+model: every model returned by
		// /models is usable on a free account.
		FreeOf: func(modelEntry) bool { return true },
		// Quota is counted per model (no cross-model shared pool).
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
		// The default rule already covers this (:free suffix / zero pricing);
		// explicitly left as nil.
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

// GetPreset returns the built-in preset (nil if not found).
func GetPreset(providerCode string) *ProviderPreset {
	return builtinPresets[providerCode]
}

// ListPresetCodes returns all built-in preset codes (stable alphabetical output).
func ListPresetCodes() []string {
	codes := make([]string, 0, len(builtinPresets))
	for code := range builtinPresets {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// PresetToCreateRequest converts the preset into a create request (displayName
// can be overridden).
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
