package config

import "strings"

const SummaryModelAuto = "auto"

// SummaryDimension identifies a structured conversation-summary view. The
// values are deliberately stable because they are used as suffixes in runtime
// settings keys and environment-variable names.
type SummaryDimension string

const (
	SummaryDimensionProject   SummaryDimension = "project"
	SummaryDimensionKeywords  SummaryDimension = "keywords"
	SummaryDimensionTasks     SummaryDimension = "tasks"
	SummaryDimensionDecisions SummaryDimension = "decisions"
	SummaryDimensionProblems  SummaryDimension = "problems"
	SummaryDimensionTechnical SummaryDimension = "technical"
)

// AllSummaryDimensions returns the canonical dimension order. The order is
// deterministic so callers that generate several summaries can produce stable
// output and telemetry.
func AllSummaryDimensions() []SummaryDimension {
	return []SummaryDimension{
		SummaryDimensionProject,
		SummaryDimensionKeywords,
		SummaryDimensionTasks,
		SummaryDimensionDecisions,
		SummaryDimensionProblems,
		SummaryDimensionTechnical,
	}
}

// ParseSummaryDimension normalizes a raw dimension string into a supported
// SummaryDimension value.
func ParseSummaryDimension(raw string) (SummaryDimension, bool) {
	d := SummaryDimension(strings.TrimSpace(strings.ToLower(raw)))
	return d, d.Valid()
}

// Valid reports whether d is a supported summary dimension.
func (d SummaryDimension) Valid() bool {
	for _, candidate := range AllSummaryDimensions() {
		if d == candidate {
			return true
		}
	}
	return false
}

// SummaryModelKey returns the primary settings key for one summary dimension.
func (d SummaryDimension) SummaryModelKey() string {
	switch d {
	case SummaryDimensionProject:
		return "summary_models.project_context"
	case SummaryDimensionKeywords:
		return "summary_models.keywords"
	case SummaryDimensionTasks:
		return "summary_models.tasks"
	case SummaryDimensionDecisions:
		return "summary_models.decisions"
	case SummaryDimensionProblems:
		return "summary_models.problems"
	case SummaryDimensionTechnical:
		return "summary_models.technical_details"
	default:
		return ""
	}
}

// SummaryModelAliases returns backward-compatible alternate keys for a
// dimension. The canonical keys follow docs/session-compression-step2-audit-and-plan.md.
func (d SummaryDimension) SummaryModelAliases() []string {
	switch d {
	case SummaryDimensionProject:
		return []string{"compression.summary_model.project"}
	case SummaryDimensionKeywords:
		return []string{"compression.summary_model.keywords"}
	case SummaryDimensionTasks:
		return []string{"compression.summary_model.tasks"}
	case SummaryDimensionDecisions:
		return []string{"compression.summary_model.decisions"}
	case SummaryDimensionProblems:
		return []string{"compression.summary_model.problems"}
	case SummaryDimensionTechnical:
		return []string{"compression.summary_model.technical"}
	default:
		return nil
	}
}

// SummaryConfig is the normalized, ordered model configuration for one
// dimension. Models must be tried from left to right.
type SummaryConfig struct {
	Dimension SummaryDimension
	Models    []string
}

// ParseSummaryModelList parses a comma-separated ordered model list. Empty
// entries and the "auto" sentinel mean "use the next fallback level". It does
// not deduplicate models because repeated entries can intentionally represent
// distinct provider routing policies behind the same model alias.
func ParseSummaryModelList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, SummaryModelAuto) {
		return nil
	}

	parts := strings.Split(raw, ",")
	models := make([]string, 0, len(parts))
	for _, part := range parts {
		model := strings.TrimSpace(part)
		if model == "" {
			continue
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil
	}
	return models
}
