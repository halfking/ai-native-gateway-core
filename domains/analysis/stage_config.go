// Package analysis — LLM stage configuration & model resolution.
//
// 会话分析插件为不同阶段（标题/总结/标签/逐步摘要/聚类）调用 LLM。
// 每个阶段的模型可独立配置（settings: session_analytics.model.<stage>）：
//   - 指定模型名（如 "gpt-4o-mini"）→ 固定使用
//   - "auto" → 由 ModelResolver 根据阶段特性自动选择
//   - 空 → 使用该阶段内置默认模型
package analysis

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

var errEmpty = errors.New("empty value")

// Stage 标识一个分析阶段（每个阶段可独立配置模型）。
type Stage string

const (
	StageTitle          Stage = "title"
	StageSummary        Stage = "summary"
	StageTags           Stage = "tags"
	StageRequestSummary Stage = "request_summary"
	StageEmbedding      Stage = "embedding"
	StageClusterLabel   Stage = "cluster_label"
)

// settingKeyOf 将 Stage 映射到 settings key。
func (s Stage) settingKey() string {
	return "session_analytics.model." + string(s)
}

// defaultModelOf 返回每个阶段的内置默认模型（当配置为空时）。
// 选择轻量、快速、低成本的模型作为分析默认。
func (s Stage) defaultModel() string {
	switch s {
	case StageTitle, StageTags, StageClusterLabel:
		// 短文本生成，用轻量模型
		return "gpt-4o-mini"
	case StageSummary, StageRequestSummary:
		// 中等理解任务
		return "gpt-4o-mini"
	case StageEmbedding:
		// 需 embedding 能力
		return "text-embedding-3-small"
	default:
		return "gpt-4o-mini"
	}
}

// ModelResolver 在 "auto" 模式下为一个 Stage 选择具体模型。
// 实现可对接 autoroute 或基于历史成本/质量的简单选择器。
type ModelResolver interface {
	// ResolveModel 返回该阶段在 auto 模式下应使用的模型名。
	ResolveModel(stage Stage) string
}

// DefaultModelResolver 是基于规则的简单 auto 解析器。
// 当没有接入 autoroute 时使用：根据阶段返回默认轻量模型。
type DefaultModelResolver struct{}

// ResolveModel 实现 ModelResolver。
func (DefaultModelResolver) ResolveModel(stage Stage) string {
	return stage.defaultModel()
}

// LLMStageConfig 读取 settings 并为每个阶段解析最终模型。
// 是 settings 层与具体分析引擎之间的解耦层。
type LLMStageConfig struct {
	resolver ModelResolver
}

// NewLLMStageConfig 构造配置读取器。resolver 为 nil 时用 DefaultModelResolver。
func NewLLMStageConfig(resolver ModelResolver) *LLMStageConfig {
	if resolver == nil {
		resolver = DefaultModelResolver{}
	}
	return &LLMStageConfig{resolver: resolver}
}

// ModelFor 返回某阶段最终使用的模型名。
// 解析顺序：settings 值 → "auto" → resolver → 默认。
func (c *LLMStageConfig) ModelFor(stage Stage) string {
	key := stage.settingKey()
	if settings.Global != nil {
		sp := settings.Global.Spec(key)
		if sp != nil {
			raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
			if err == nil && raw != nil {
				var s string
				// settings 值可能是 JSON 字符串或纯文本
				if uErr := unmarshalString(raw, &s); uErr == nil {
					s = strings.TrimSpace(s)
					if s != "" && s != "auto" {
						return s
					}
					if s == "auto" {
						return c.resolver.ResolveModel(stage)
					}
				}
			}
		}
	}
	// settings 未配置或读取失败 → 默认
	return stage.defaultModel()
}

// Enabled 读取 session_analytics.enabled。
func (c *LLMStageConfig) Enabled() bool {
	return settings.IsSessionAnalyticsEnabled()
}

// RequestSummaryMode 读取逐步摘要模式（rule/llm/hybrid）。
func (c *LLMStageConfig) RequestSummaryMode() string {
	return c.enumSetting("session_analytics.request_summary_mode", "rule")
}

// SummaryStrategy 读取总结策略（full/rolling/map_reduce）。
func (c *LLMStageConfig) SummaryStrategy() string {
	return c.enumSetting("session_analytics.summary_strategy", "rolling")
}

// ClusterMode 读取聚类模式（off/rule/vector/hybrid）。
func (c *LLMStageConfig) ClusterMode() string {
	return c.enumSetting("session_analytics.cluster_mode", "hybrid")
}

// OptimizationEnabled 读取优化建议开关。
func (c *LLMStageConfig) OptimizationEnabled() bool {
	return c.boolSetting("session_analytics.optimization_enabled", true)
}

// TitleOnFirstRequest 读取首请求即生成标题开关。
func (c *LLMStageConfig) TitleOnFirstRequest() bool {
	return c.boolSetting("session_analytics.title_on_first_request", true)
}

// enumSetting 读取一个枚举型 setting，失败时返回 def。
func (c *LLMStageConfig) enumSetting(key, def string) string {
	if settings.Global == nil {
		return def
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return def
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || raw == nil {
		return def
	}
	var s string
	if err := unmarshalString(raw, &s); err != nil {
		return def
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

// boolSetting 读取一个布尔型 setting，失败时返回 def。
func (c *LLMStageConfig) boolSetting(key string, def bool) bool {
	if settings.Global == nil {
		return def
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return def
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || raw == nil {
		return def
	}
	var v bool
	if err := unmarshalBool(raw, &v); err != nil {
		return def
	}
	return v
}

// unmarshalString 容错解析 settings 值（可能是 "gpt-4o-mini" 或带引号 JSON）。
func unmarshalString(raw []byte, s *string) error {
	if len(raw) == 0 {
		return errEmpty
	}
	// 尝试 JSON 字符串
	if raw[0] == '"' {
		return json.Unmarshal(raw, s)
	}
	// 纯文本
	*s = string(raw)
	return nil
}

func unmarshalBool(raw []byte, v *bool) error {
	return json.Unmarshal(raw, v)
}
