// Package intentconfig 提供意图分类器的配置管理功能。
//
// 核心能力：
//   - 租户级配置热加载（30秒轮询，继承平台默认）
//   - 支持多语言关键词、正则模式、阈值配置
//   - 配置版本管理和变更追踪
//   - 与 hotconfig 和 autoroute 集成
//
// 设计原则：
//   - 平台级配置（tenant_id=NULL）作为默认值
//   - 租户级配置覆盖平台配置
//   - 配置变更立即生效，无需重启
package intentconfig

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation
)

// IntentKind 意图类型（复用 analysis.IntentKind）
type IntentKind = analysis.IntentKind

const (
	IntentChat         IntentKind = analysis.IntentChat
	IntentCode         IntentKind = analysis.IntentCode
	IntentReasoning    IntentKind = analysis.IntentReasoning
	IntentSummary      IntentKind = analysis.IntentSummary
	IntentTranslation  IntentKind = analysis.IntentTranslation
	IntentExtraction   IntentKind = analysis.IntentExtraction
	IntentToolUse      IntentKind = analysis.IntentToolUse
	IntentUnclassified IntentKind = analysis.IntentUnclassified
)

// Strategy 分类策略
type Strategy string

const (
	StrategyBaselineHeuristic Strategy = "baseline_heuristic" // 仅关键词匹配
	StrategyPatternLayered    Strategy = "pattern_layered"    // 硬规则→模式→关键词（默认）
	StrategyLLMFallback       Strategy = "llm_fallback"       // 低置信度LLM兜底
)

// ClassifierConfig 分类器配置（租户级或平台级）
type ClassifierConfig struct {
	ID       int
	TenantID string // 空字符串表示平台级

	// 策略
	Strategy      Strategy
	EnabledLayers EnabledLayers

	// 规则配置
	KeywordsConfig map[IntentKind]KeywordSet
	PatternsConfig map[IntentKind][]Pattern

	// 阈值
	ConfidenceThresholds  ThresholdConfig
	DriftThreshold        float64
	MultiTurnMemory       int
	LLMFallbackEnabled    bool
	LLMModel              string
	LLMConfidenceThreshold float64

	// 元数据
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EnabledLayers 启用的检测层
type EnabledLayers struct {
	HardRules    bool `json:"hard_rules"`
	PatternMatch bool `json:"pattern_match"`
	KeywordScore bool `json:"keyword_score"`
	LLMFallback  bool `json:"llm_fallback"`
}

// KeywordSet 关键词集合（多语言）
type KeywordSet struct {
	EN []string `json:"en"` // 英文关键词
	ZH []string `json:"zh"` // 中文关键词
}

// Pattern 正则模式
type Pattern struct {
	Pattern     string  `json:"pattern"`     // 正则表达式
	Weight      float64 `json:"weight"`      // 权重（0-1）
	Description string  `json:"description"` // 描述
}

// ThresholdConfig 置信度阈值配置
type ThresholdConfig struct {
	High   float64 `json:"high"`   // >= 0.80
	Medium float64 `json:"medium"` // >= 0.60
	Low    float64 `json:"low"`    // >= 0.40
}

// IntentCandidate 意图候选（包含多方向判断）
type IntentCandidate struct {
	Kind       IntentKind         `json:"kind"`
	Confidence float64            `json:"confidence"`
	Signals    map[string]float64 `json:"signals"` // 触发的信号及其权重
}

// IntentEvolution 意图演化记录
type IntentEvolution struct {
	ID        int64
	SessionID string
	TenantID  string
	RequestID string

	TurnNumber           int
	IntentCandidates     []IntentCandidate
	PrimaryIntent        string
	PrimaryConfidence    float64
	PreviousPrimaryIntent *string
	IntentDriftScore     *float64
	IsIntentChanged      bool

	ClassifierVersion      string
	ClassificationLatencyMs int

	UserContent     *string
	UserContentHash *string
	ContextLength   int
	HasImages       bool
	ToolCount       int

	ClassifiedAt time.Time
}

// AdjustmentType 调整类型
type AdjustmentType string

const (
	AdjustmentKeywordAdd          AdjustmentType = "keyword_add"
	AdjustmentKeywordRemove       AdjustmentType = "keyword_remove"
	AdjustmentPatternAdd          AdjustmentType = "pattern_add"
	AdjustmentPatternRemove       AdjustmentType = "pattern_remove"
	AdjustmentThresholdChange     AdjustmentType = "threshold_change"
	AdjustmentStrategyChange      AdjustmentType = "strategy_change"
	AdjustmentDriftThresholdChange AdjustmentType = "drift_threshold_change"
	AdjustmentMemoryWindowChange  AdjustmentType = "memory_window_change"
)

// AdjustmentStatus 调整状态
type AdjustmentStatus string

const (
	AdjustmentActive     AdjustmentStatus = "active"
	AdjustmentRolledBack AdjustmentStatus = "rolled_back"
	AdjustmentSuperseded AdjustmentStatus = "superseded"
)

// Adjustment 配置调整记录
type Adjustment struct {
	ID         int64
	TenantID   string
	Type       AdjustmentType
	TargetIntent *string // 影响的意图类型（NULL=全局）
	Detail     map[string]interface{}

	Reason      *string
	TriggeredBy string
	OperatorID  *string

	EffectivenessScore  *float64
	EvaluationSampleSize *int
	BeforeAccuracy      *float64
	AfterAccuracy       *float64

	Status         AdjustmentStatus
	RollbackReason *string
	SupersededBy   *int64

	CreatedAt     time.Time
	EvaluatedAt   *time.Time
	RolledBackAt  *time.Time
}

// Feedback 意图分类反馈
type Feedback struct {
	ID        int64
	SessionID string
	RequestID string
	TenantID  string

	PredictedIntent    string
	PredictedConfidence float64

	ActualIntent    *string
	IsCorrect       *bool
	AnnotatorID     *string
	AnnotatedAt     *time.Time
	AnnotationNotes *string

	UserAcceptedModel    *bool
	UserSwitchedToModel  *string
	UserRetryCount       int
	SessionDurationSec   *int
	UserSatisfactionScore *int

	UserContentHash       *string
	ClassificationContext map[string]interface{}
	EvolutionID           *int64

	CreatedAt time.Time
}

// DefaultClassifierConfig 返回平台级默认配置
func DefaultClassifierConfig() *ClassifierConfig {
	return &ClassifierConfig{
		TenantID: "", // 平台级
		Strategy: StrategyPatternLayered,
		EnabledLayers: EnabledLayers{
			HardRules:    true,
			PatternMatch: true,
			KeywordScore: true,
			LLMFallback:  false,
		},
		KeywordsConfig: map[IntentKind]KeywordSet{
			IntentCode: {
				EN: []string{"function", "algorithm", "implement", "code", "debug", "refactor", "class", "method"},
				ZH: []string{"函数", "算法", "实现", "代码", "调试", "重构", "类", "方法"},
			},
			IntentReasoning: {
				EN: []string{"solve", "prove", "calculate", "reason", "derive", "logic", "theorem"},
				ZH: []string{"证明", "推导", "计算", "推理", "求解", "逻辑", "定理"},
			},
			IntentSummary: {
				EN: []string{"summarize", "abstract", "brief", "outline", "overview"},
				ZH: []string{"总结", "摘要", "概要", "简述", "概览"},
			},
			IntentTranslation: {
				EN: []string{"translate", "convert", "interpret"},
				ZH: []string{"翻译", "转换", "释义"},
			},
			IntentChat: {
				EN: []string{"hello", "hi", "how are you", "thank you", "bye"},
				ZH: []string{"你好", "您好", "谢谢", "再见", "问候"},
			},
		},
		PatternsConfig: map[IntentKind][]Pattern{
			IntentCode: {
				{Pattern: "```", Weight: 0.95, Description: "代码块标记"},
				{Pattern: `(?i)(def|function|class)\s+\w+`, Weight: 0.85, Description: "函数/类定义"},
			},
			IntentReasoning: {
				{Pattern: `(?i)(solve|prove|calculate|证明|推导|计算)`, Weight: 0.80, Description: "推理动词"},
			},
		},
		ConfidenceThresholds: ThresholdConfig{
			High:   0.80,
			Medium: 0.60,
			Low:    0.40,
		},
		DriftThreshold:        0.3,
		MultiTurnMemory:       5,
		LLMFallbackEnabled:    false,
		LLMModel:              "gpt-4o-mini",
		LLMConfidenceThreshold: 0.50,
		Version:               1,
		UpdatedAt:             time.Now(),
	}
}
