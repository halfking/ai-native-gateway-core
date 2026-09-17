// Package taskprofile implements the task-type profile + model-tier
// suggestion module (2026-09-18).
//
// The module consolidates, in one plugin-style package:
//   - the task taxonomy with its default model-tier data (V3 task types,
//     preferred tier / fallback chain / minimum confidence per type),
//   - per-request human corrections of the AUTO task-type assignment,
//   - the tier-suggestion engine driven by those corrections.
//
// Design: docs/planning/TASKPROFILE_MODULE_DESIGN.md
//
// Boundaries:
//   - The request hot path does NOT call into this package. Runtime effects
//     reach routing only through the optional routingopt CorrectionSource
//     (wired in cmd/gateway when ROUTING_OPT_ENABLED=true) and the admin API.
//   - taskprofile imports neither autoroute nor routingopt. Interfaces are
//     defined locally and satisfied structurally (FeedbackRecorder is
//     implemented by *autoroute.ClassificationFeedbackAggregator; the
//     routingopt adapter lives in cmd/gateway).
package taskprofile

// Tier names (same vocabulary as autoroute tier_selector / task_type_tier_config).
const (
	TierA = "tier-a" // high-performance: architecture / audit / debugging
	TierB = "tier-b" // standard: coding / refactoring / testing
	TierC = "tier-c" // economy: devops / documentation / summary / dependency
)

// TaskProfile is the consolidated per-task-type record: taxonomy entry +
// default model-tier data. This is the "task type identification + suggested
// model tier data" asset the module owns; it is versioned and overlayable so
// the profile can be upgraded independently of the binary.
type TaskProfile struct {
	TaskType      string   `json:"task_type"`
	Description   string   `json:"description"`
	PreferredTier string   `json:"preferred_tier"`
	FallbackTiers []string `json:"fallback_tiers"`
	MinConfidence float64  `json:"min_confidence"`
}

// CorrectionStat aggregates human corrections for one auto task type.
type CorrectionStat struct {
	TaskType  string `json:"task_type"`
	Total     int    `json:"total"`
	Agrees    int    `json:"agrees"`
	Corrected int    `json:"corrected"`

	// CorrectionRate = corrected/total; 0 when Total == 0.
	CorrectionRate float64 `json:"correction_rate"`
}

// FeedbackRecorder receives one verdict per recorded correction. It exists so
// cmd/gateway can hand the store the in-process classification feedback
// aggregator without taskprofile importing autoroute:
// *autoroute.ClassificationFeedbackAggregator satisfies it structurally.
type FeedbackRecorder interface {
	RecordFeedback(taskType string, correct bool)
}
