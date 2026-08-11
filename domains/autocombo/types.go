package autocombo

import (
	"encoding/json"
	"time"
)

// Variant 路由变体
type Variant string

const (
	VariantCheap     Variant = "cheap"     // 最低成本
	VariantFast      Variant = "fast"      // 低延迟
	VariantSmart     Variant = "smart"     // 高质量
	VariantCoding    Variant = "coding"    // 代码任务
	VariantReasoning Variant = "reasoning" // 推理任务
	VariantCreative  Variant = "creative"  // 创作任务
	VariantChaos     Variant = "chaos"     // 随机混沌
)

// AutoComboSpec Auto Combo 规范
type AutoComboSpec struct {
	ID                 int64
	ComboName          string
	Variant            Variant
	TierFilter         []string // 如 ["free"]
	FreeTypeFilter     []string
	ToSFilter          []string // 如 ["ok", "caution"]
	ProviderAllowlist  []string
	ProviderDenylist   []string
	ModelPattern       string
	ScoringWeightsJSON json.RawMessage
	MaxCandidates      int
	ExplorationRate    float64
	Enabled            bool
	TenantID           string

	// HideTrainableModels (round 4 M7): 当 true, 过滤 trains_on_prompts=TRUE
	// 的 catalog 行. 用户隐私偏好 (例如"我的 prompt 不能被用来训练")
	// 通过 spec 字段传给 factory, 与 OmniRoute hidePaidModels (#6512)
	// 设计对齐.
	HideTrainableModels bool
}

// ScoringWeights 评分权重
type ScoringWeights struct {
	HealthScore    float64 `json:"health_score"`    // 健康分数权重
	LatencyP95     float64 `json:"latency_p95"`     // 延迟权重
	QuotaRemaining float64 `json:"quota_remaining"` // 配额剩余权重
	Cost           float64 `json:"cost"`            // 成本权重
	TaskFit        float64 `json:"task_fit"`        // 任务适配度权重
	TierAffinity   float64 `json:"tier_affinity"`   // 层级亲和度权重
	// ResetWindowAffinity (P3 2026-08-11) rewards credentials whose quota has
	// freshly reset (more remaining runway in the current window). Mirrors
	// OmniRoute combo/quotaScoring.ts:304-311, but unlike OmniRoute (which
	// leaves it weight-0) we give it a small real weight so freshly-reset
	// free credentials edge out near-exhausted ones beyond what QuotaRemaining
	// already captures. Optional — defaults to 0 in legacy weight presets, so
	// the validation sum check ignores it when unset.
	ResetWindowAffinity float64 `json:"reset_window_affinity,omitempty"`
}

// Candidate 候选模型
type Candidate struct {
	CredentialID int64
	ProviderCode string
	ModelID      string
	DisplayName  string
	HealthScore  float64 // 0-1
	LatencyP95   int     // 毫秒
	CostPer1M    float64 // 每百万 token 成本
	QuotaRemain  float64 // 配额剩余比例 0-1
	IsKeyless    bool
	IsFree       bool
}

// ScoredCandidate 带评分的候选
type ScoredCandidate struct {
	Candidate Candidate
	Score     float64 // 综合评分 0-1
}

// VirtualCombo 虚拟 Combo
type VirtualCombo struct {
	Name            string
	Variant         Variant
	CandidatePool   []Candidate
	ExplorationRate float64
	CreatedAt       time.Time
}

// Tiers 分层候选
type Tiers struct {
	Top  []ScoredCandidate // score >= 0.8
	Mid  []ScoredCandidate // 0.5 <= score < 0.8
	Rest []ScoredCandidate // score < 0.5
}
