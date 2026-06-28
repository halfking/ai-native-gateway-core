package analysis

import "time"

// SuggestionCategory 优化建议分类。
type SuggestionCategory string

const (
	SuggestPrompt  SuggestionCategory = "prompt"
	SuggestTool    SuggestionCategory = "tool"
	SuggestModel   SuggestionCategory = "model"
	SuggestPolicy  SuggestionCategory = "policy"
	SuggestSession SuggestionCategory = "session"
)

// SuggestionSeverity 建议严重度。
type SuggestionSeverity string

const (
	SeverityInfo           SuggestionSeverity = "info"
	SeverityWarn           SuggestionSeverity = "warn"
	SeverityActionRequired SuggestionSeverity = "action_required"
)

// OptimizationSuggestion 优化建议。
//
// 由 OptimizationAdviser 在会话关闭或定期巡检时产出；默认 Applied=false，
// 仅当管理员在 UI 显式采纳后才会影响 governance 决策权重。
type OptimizationSuggestion struct {
	ID              int64
	TenantID        string
	TargetSessionID string
	Category        SuggestionCategory
	Severity        SuggestionSeverity
	Content         string
	Evidence        map[string]any
	Applied         bool
	CreatedAt       time.Time
	AppliedAt       *time.Time
}
