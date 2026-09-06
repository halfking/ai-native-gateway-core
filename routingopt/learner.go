package routingopt

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// AdaptiveLearner: GetStats 性能统计与在线参数学习
// =============================================================================

// AdaptiveLearner computes optimizer statistics and performs online parameter
// optimization (adaptive threshold tuning based on rolling accuracy).
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.4
type AdaptiveLearner struct {
	stateDAO    *OptimizationStateDAO
	metricsDAO  *OptimizationMetricsDAO
	feedbackDAO *FeedbackLogDAO
	integrator  *FeedbackIntegrator
}

// NewAdaptiveLearner constructs a learner instance.
func NewAdaptiveLearner(pool *pgxpool.Pool, integrator *FeedbackIntegrator) *AdaptiveLearner {
	return &AdaptiveLearner{
		stateDAO:    NewOptimizationStateDAO(pool),
		metricsDAO:  NewOptimizationMetricsDAO(pool),
		feedbackDAO: NewFeedbackLogDAO(pool),
		integrator:  integrator,
	}
}

// WeightedAccuracy computes the human-annotation-weighted accuracy:
//
//	(autoCorrect + 2×humanCorrect) / (autoTotal + 2×humanTotal)
//
// Each human annotation counts double (P2.1 ground truth). Returns 0 when
// there is no data yet.
func WeightedAccuracy(autoCorrect, autoTotal, humanCorrect, humanTotal int) float64 {
	total := autoTotal + 2*humanTotal
	if total <= 0 {
		return 0
	}
	correct := autoCorrect + 2*humanCorrect
	if correct > total {
		correct = total
	}
	return float64(correct) / float64(total)
}

// GetStats implements the GetStats hook.
// Returns optimizer performance statistics for admin API.
//
// Accuracy blends auto feedback (success column) with human corrections
// (P2.1 annotations, weight ×2) via WeightedAccuracy. When no feedback data
// exists yet, falls back to the state's persisted overall_accuracy.
func (l *AdaptiveLearner) GetStats(ctx context.Context) (*OptimizerStats, error) {
	// 1. Get active optimization state
	state, err := l.stateDAO.GetActive(ctx)
	if err != nil {
		// No active state: return empty stats
		return &OptimizerStats{
			OverallAccuracy:      0.0,
			ParameterVersion:     0,
			LastUpdated:          time.Time{},
			HumanAnnotationsUsed: 0,
		}, nil
	}

	// 2. Auto feedback counts (last 24 hours)
	since := time.Now().Add(-24 * time.Hour)
	autoCorrect, autoTotal, err := l.feedbackDAO.GetAutoAccuracyCounts(ctx, since)
	overallAccuracy := 0.0
	if err == nil && autoTotal > 0 {
		// 3. Human correction counts (weight ×2, P2.1 ground truth)
		humanAgreeing, humanTotal, herr := l.feedbackDAO.GetHumanCorrectionCounts(ctx, since)
		if herr == nil {
			overallAccuracy = WeightedAccuracy(autoCorrect, autoTotal, humanAgreeing, humanTotal)
		} else {
			overallAccuracy = float64(autoCorrect) / float64(autoTotal)
		}
	} else if state.OverallAccuracy != nil {
		// No recent feedback: fall back to the persisted aggregate
		overallAccuracy = *state.OverallAccuracy
	}

	// 4. Get human annotation count (last 24 hours)
	humanAnnotationsUsed, err := l.integrator.GetHumanAnnotationStats(ctx, since)
	if err != nil {
		humanAnnotationsUsed = 0 // Ignore error, return 0
	}

	// 5. Construct stats
	stats := &OptimizerStats{
		OverallAccuracy:      overallAccuracy,
		ParameterVersion:     state.Version,
		LastUpdated:          state.UpdatedAt,
		HumanAnnotationsUsed: humanAnnotationsUsed,
	}

	return stats, nil
}

// Adaptation tuning constants (vars so tests can adjust without fixtures).
var (
	// adaptMinImprovement: a new parameter version is only created when the
	// sliding-window weighted accuracy improves by more than this (2pp).
	adaptMinImprovement = 0.02
	// adaptDropThreshold: accuracy falling more than this below the active
	// state's persisted accuracy is an anomaly — hold parameters, alert.
	adaptDropThreshold = 0.05
	// adaptExplorationDecay / Floor: when accuracy improves, taper ε-greedy
	// exploration (we explore less when the current policy performs well).
	adaptExplorationDecay = 0.8
	adaptExplorationFloor = 0.01
)

// adaptDecision is the outcome of the adaptation policy for one evaluation.
type adaptDecision int

const (
	adaptHold    adaptDecision = iota // insufficient evidence or no significant change
	adaptUpdate                       // accuracy improved ≥ threshold → new parameter version
	adaptAnomaly                      // accuracy dropped ≥ threshold → hold + alert
)

// decideAdaptation is the pure adaptation policy. prevAccuracy nil means the
// active state has no persisted accuracy yet (first data lands → checkpoint).
// samples is the sliding-window feedback count; below confidenceMinSamples
// the policy always holds (same evidence bar as confidence adjustment).
func decideAdaptation(prevAccuracy *float64, currAccuracy float64, samples int) (adaptDecision, string) {
	if samples < confidenceMinSamples {
		return adaptHold, "insufficient samples"
	}
	if prevAccuracy == nil {
		return adaptUpdate, "first accuracy checkpoint"
	}
	delta := currAccuracy - *prevAccuracy
	switch {
	case delta >= adaptMinImprovement:
		return adaptUpdate, "accuracy improved"
	case delta <= -adaptDropThreshold:
		return adaptAnomaly, "accuracy drop"
	default:
		return adaptHold, "within tolerance"
	}
}

// currentWeightedAccuracy computes the 24h sliding-window accuracy with the
// human-annotation ×2 weighting, plus the effective auto sample count.
func (l *AdaptiveLearner) currentWeightedAccuracy(ctx context.Context) (accuracy float64, samples int, err error) {
	since := time.Now().Add(-24 * time.Hour)
	autoCorrect, autoTotal, err := l.feedbackDAO.GetAutoAccuracyCounts(ctx, since)
	if err != nil {
		return 0, 0, err
	}
	humanAgreeing, humanTotal, err := l.feedbackDAO.GetHumanCorrectionCounts(ctx, since)
	if err != nil {
		// Human counts are best-effort: fall back to auto-only accuracy.
		return autoCorrectRatio(autoCorrect, autoTotal), autoTotal, nil
	}
	return WeightedAccuracy(autoCorrect, autoTotal, humanAgreeing, humanTotal), autoTotal + 2*humanTotal, nil
}

func autoCorrectRatio(correct, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(correct) / float64(total)
}

// AdaptParameters performs online parameter optimization over the 24h
// sliding window. Called by a background worker every 5 minutes; gated by
// ROUTING_OPT_ADAPTIVE_LEARNING at the call site.
//
// Policy (conservative, fully auditable via optimization_state versions):
//   - accuracy improved ≥2pp vs the active state → checkpoint a new version,
//     tapering ε-greedy exploration by ×0.8 (floor 1%)
//   - accuracy dropped ≥5pp → hold parameters and surface an anomaly
//   - otherwise hold (no churn)
//
// Version creation is transactional in OptimizationStateDAO.Create (old
// active row deactivated in the same tx), so a failed write leaves the
// previous parameters untouched.
func (l *AdaptiveLearner) AdaptParameters(ctx context.Context) error {
	state, err := l.stateDAO.GetActive(ctx)
	if err != nil {
		return err // no active state yet: nothing to adapt
	}

	accuracy, samples, err := l.currentWeightedAccuracy(ctx)
	if err != nil {
		return err
	}

	decision, reason := decideAdaptation(state.OverallAccuracy, accuracy, samples)
	switch decision {
	case adaptHold:
		return nil
	case adaptAnomaly:
		slog.WarnContext(ctx, "routingopt: accuracy drop detected, holding parameters",
			"current", accuracy, "persisted", state.OverallAccuracy, "samples", samples)
		return nil
	case adaptUpdate:
	}

	newState := *state
	newState.ID = 0
	newState.Version = state.Version + 1
	newState.OverallAccuracy = &accuracy
	newState.ExplorationRate = maxFloat(adaptExplorationFloor, state.ExplorationRate*adaptExplorationDecay)
	newState.ActivatedAt = time.Now()
	newState.DeactivatedAt = nil
	newState.Notes = strPtr("adaptive checkpoint: " + reason)

	if _, err := l.stateDAO.Create(ctx, &newState); err != nil {
		return err
	}
	slog.InfoContext(ctx, "routingopt: parameter version created",
		"version", newState.Version, "accuracy", accuracy,
		"exploration_rate", newState.ExplorationRate, "reason", reason)
	return nil
}

// DetectAnomalies inspects the same sliding window as AdaptParameters and
// reports an accuracy_drop anomaly when the weighted accuracy sits ≥5pp below
// the active state's persisted accuracy. Other anomaly classes (latency spike,
// provider failure) remain Week 3 scope.
func (l *AdaptiveLearner) DetectAnomalies(ctx context.Context) ([]Anomaly, error) {
	state, err := l.stateDAO.GetActive(ctx)
	if err != nil || state.OverallAccuracy == nil {
		return nil, nil // nothing to compare against
	}
	accuracy, samples, err := l.currentWeightedAccuracy(ctx)
	if err != nil {
		return nil, err
	}
	if samples < confidenceMinSamples {
		return nil, nil
	}
	if *state.OverallAccuracy-accuracy < adaptDropThreshold {
		return nil, nil
	}
	return []Anomaly{{
		Type:        "accuracy_drop",
		Severity:    "warning",
		Description: "weighted routing accuracy dropped ≥5pp below the active parameter checkpoint",
		DetectedAt:  time.Now(),
		Metrics: map[string]float64{
			"current_accuracy":   accuracy,
			"persisted_accuracy": *state.OverallAccuracy,
			"samples":            float64(samples),
		},
	}}, nil
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func strPtr(s string) *string {
	return &s
}

// Anomaly represents a detected routing anomaly.
type Anomaly struct {
	Type        string // "accuracy_drop", "latency_spike", "provider_failure"
	Severity    string // "warning", "critical"
	Description string // human-readable description
	DetectedAt  time.Time
	Metrics     map[string]float64 // relevant metrics
}
