package analysis

import "time"

// PromptQuality 提示词质量评分。
//
// 由 PromptQualityScorer 在请求完成时产出；4 维分项各 0-100，Overall 为加权。
type PromptQuality struct {
	RequestID   string
	TenantID    string
	SessionID   string
	Overall     int
	Clarity     int
	Specificity int
	Context     int
	Safety      int
	Suggestions []string
	EvaluatedAt time.Time
	Evaluator   string
}

// PromptTemplate 高质量提示词模板。
//
// 由 OptimizationAdviser / 人工管理员沉淀；同步治理层在 mutate 决策时优先
// 从 PromptTemplateBank 中匹配以提升后续提示词质量。
type PromptTemplate struct {
	ID           int64
	TenantID     string
	Name         string
	TaskType     string
	Template     string
	QualityScore int
	UsageCount   int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
