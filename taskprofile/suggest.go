package taskprofile

// suggest.go — tier-suggestion engine. Pure functions over the registry
// snapshot + correction stats: no I/O, fully table-testable.
//
// Semantics (design doc §5.2):
//  1. base = registry profile default (unknown type → tier-b safe default,
//     mirroring autoroute.TierSelector's unknown-type fallback);
//  2. a high human-correction rate means the classifier is weakest on this
//     task type (OmniRoute "taskFit", inverted) → escalate one tier and
//     tighten min_confidence;
//  3. low request confidence escalates straight to tier-a (same semantics as
//     the existing TierSelector confidence escalation).

// Tuning knobs. vars (not consts) so tests can tighten them without
// fixtures; production values are the documented defaults.
var (
	// correctionEscalationRate: at/above this correction rate the suggestion
	// escalates the tier.
	correctionEscalationRate = 0.30
	// correctionMinSamples: below this many corrections the rate is noise.
	correctionMinSamples = 5
	// escalationMinConfidenceBump added to MinConfidence when escalated.
	escalationMinConfidenceBump = 0.05
)

// Suggestion is the merged tier advice for one request shape.
type Suggestion struct {
	TaskType      string   `json:"task_type"`
	Tier          string   `json:"tier"`
	FallbackTiers []string `json:"fallback_tiers"`
	MinConfidence float64  `json:"min_confidence"`

	// TierSource: "registry" (profile default) | "correction_escalation"
	// (correction density pushed the tier up) | "confidence_escalation"
	// (low request confidence escalated to tier-a).
	TierSource string `json:"tier_source"`

	// CorrectionStats is nil when no human corrections exist for the type.
	CorrectionStats *CorrectionStat `json:"correction_stats,omitempty"`
}

// Suggest merges the registry profile with correction stats for taskType at
// the given request confidence. stats may be nil / missing the type.
func Suggest(taskType string, confidence float64, stats map[string]CorrectionStat) Suggestion {
	base, ok := Profile(taskType)
	tierSource := "registry"
	if !ok {
		// Unknown task type: conservative middle ground (same default the
		// existing TierSelector uses).
		base = TaskProfile{
			TaskType:      taskType,
			PreferredTier: TierB,
			FallbackTiers: []string{TierA, TierC},
			MinConfidence: 0.70,
		}
	}

	tier := base.PreferredTier
	fallbacks := append([]string(nil), base.FallbackTiers...)
	minConf := base.MinConfidence

	var cs *CorrectionStat
	if stat, ok := stats[taskType]; ok && stat.Total > 0 {
		s := stat
		cs = &s
	}

	if cs != nil && cs.Total >= correctionMinSamples && cs.CorrectionRate >= correctionEscalationRate {
		tier = escalateTier(tier)
		tierSource = "correction_escalation"
		if minConf += escalationMinConfidenceBump; minConf > 1 {
			minConf = 1
		}
	}

	if confidence < minConf {
		tier = TierA
		tierSource = "confidence_escalation"
		if len(fallbacks) == 0 {
			fallbacks = []string{TierB}
		}
	}

	return Suggestion{
		TaskType:        taskType,
		Tier:            tier,
		FallbackTiers:   fallbacks,
		MinConfidence:   minConf,
		TierSource:      tierSource,
		CorrectionStats: cs,
	}
}

// escalateTier moves c→b→a; tier-a stays (no luxury tier above it).
func escalateTier(tier string) string {
	switch tier {
	case TierC:
		return TierB
	case TierB:
		return TierA
	default:
		return TierA
	}
}
