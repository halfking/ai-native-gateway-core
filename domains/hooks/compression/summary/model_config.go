package summary

import (
	"encoding/json"
	"os"
	"strings"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/settings"
)

var defaultSummaryModels = []string{"minimax-text-01", "gemini-2.5-flash"}

const fallbackSummaryModelsKey = "summary_models.fallback"

// ModelConfig captures the ordered model list for one summary dimension.
type ModelConfig struct {
	Dimension appconfig.SummaryDimension
	Key       string
	Source    string
	Models    []string
}

// AsSummaryConfig converts the resolved model config into the normalized
// config package type used by callers.
func (m ModelConfig) AsSummaryConfig() appconfig.SummaryConfig {
	return appconfig.SummaryConfig{
		Dimension: m.Dimension,
		Models:    append([]string(nil), m.Models...),
	}
}

// ResolveModelConfig returns the hot-reloadable model chain for one summary
// dimension. Resolution order:
//  1. dimension-specific key (e.g. summary_models.project_context)
//  2. shared summary_models.fallback
//  3. compression.llm_model
//  4. legacy LLM_GATEWAY_COMPACTION_MODELS env var
//  5. built-in default chain
//
// The function reads settings.Global on every call, so it always sees the
// latest committed settings_kv/env value without extra caching.
func ResolveModelConfig(dim appconfig.SummaryDimension) ModelConfig {
	cfg := ModelConfig{Dimension: dim, Key: dim.SummaryModelKey()}
	if models, source := resolveSettingModels(cfg.Key); len(models) > 0 {
		cfg.Models = models
		cfg.Source = source
		return cfg
	}
	for _, alias := range dim.SummaryModelAliases() {
		if models, source := resolveSettingModels(alias); len(models) > 0 {
			cfg.Models = models
			cfg.Source = source
			return cfg
		}
	}
	if models, source := resolveSettingModels(fallbackSummaryModelsKey); len(models) > 0 {
		cfg.Models = models
		cfg.Source = source
		return cfg
	}
	if models, source := resolveSettingModels("compression.llm_model"); len(models) > 0 {
		cfg.Models = models
		cfg.Source = source
		return cfg
	}
	if models := appconfig.ParseSummaryModelList(os.Getenv("LLM_GATEWAY_COMPACTION_MODELS")); len(models) > 0 {
		cfg.Models = models
		cfg.Source = "env:LLM_GATEWAY_COMPACTION_MODELS"
		return cfg
	}
	cfg.Models = append([]string(nil), defaultSummaryModels...)
	cfg.Source = "default"
	return cfg
}

func resolveSettingModels(key string) ([]string, string) {
	if settings.Global == nil || key == "" {
		return nil, ""
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return nil, ""
	}
	raw, src, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || len(raw) == 0 || src == "default" {
		return nil, ""
	}
	value := decodeSettingString(raw)
	models := appconfig.ParseSummaryModelList(value)
	if len(models) == 0 {
		return nil, ""
	}
	return models, "settings:" + key
}

func decodeSettingString(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return strings.TrimSpace(s)
		}
	}
	return strings.TrimSpace(string(raw))
}
