package autoroute

import "time"

// TreatmentGateConfig defines the minimum evidence and maximum regression
// tolerated before a rollout can advance. Zero thresholds are intentionally
// conservative: only MinSamples is required for a useful decision.
type TreatmentGateConfig struct {
	MinSamples               uint64
	MaxCostRegression        float64
	MaxLatencyRegression     float64
	MaxFailureRateRegression float64
	MinFeedbackAccuracy      float64
}

// GateDecision is the evaluator result. RollbackSuggested is advisory unless
// a caller explicitly enables and executes an external rollback action.
type GateDecision struct {
	Experiment          string
	Version             string
	Treatment           Treatment
	Allowed             bool
	RollbackSuggested   bool
	Reasons             []string
	Observed            TreatmentGateObserved
	Thresholds          TreatmentGateConfig
	EvaluationStartedAt time.Time
	EvaluationEndedAt   time.Time
}

type TreatmentGateObserved struct {
	Samples               uint64
	CostRegression        float64
	LatencyRegression     float64
	FailureRateRegression float64
	FeedbackAccuracy      float64
}

// EvaluateTreatmentGate compares a treatment cohort with its control cohort.
// Missing cohorts, insufficient samples, or unavailable required dimensions
// fail closed. The two outcomes must belong to the same experiment/version.
func EvaluateTreatmentGate(control, treatment TreatmentOutcome, cfg TreatmentGateConfig, now time.Time) GateDecision {
	result := GateDecision{
		Experiment:          treatment.Key.Experiment,
		Version:             treatment.Key.Version,
		Treatment:           treatment.Key.Treatment,
		Allowed:             false,
		Thresholds:          cfg,
		EvaluationStartedAt: now,
		EvaluationEndedAt:   now,
	}
	if control.Key.Experiment == "" || treatment.Key.Experiment == "" ||
		control.Key.Experiment != treatment.Key.Experiment ||
		control.Key.Version == "" || treatment.Key.Version == "" ||
		control.Key.Version != treatment.Key.Version {
		result.Reasons = append(result.Reasons, "experiment_or_version_mismatch")
		return result
	}
	if treatment.Key.Treatment == TreatmentControl {
		result.Reasons = append(result.Reasons, "treatment_must_not_be_control")
		return result
	}
	if control.Samples < cfg.MinSamples || treatment.Samples < cfg.MinSamples {
		result.Reasons = append(result.Reasons, "insufficient_samples")
		result.Observed.Samples = minUint64(control.Samples, treatment.Samples)
		return result
	}
	result.Observed.Samples = minUint64(control.Samples, treatment.Samples)
	result.Observed.CostRegression = relativeRegression(control.ActualCost, treatment.ActualCost)
	result.Observed.LatencyRegression = relativeRegression(control.P95Latency(), treatment.P95Latency())
	result.Observed.FailureRateRegression = treatment.FailureRate() - control.FailureRate()
	result.Observed.FeedbackAccuracy = treatment.FeedbackAccuracy()

	if cfg.MaxCostRegression >= 0 && result.Observed.CostRegression > cfg.MaxCostRegression {
		result.Reasons = append(result.Reasons, "cost_regression_exceeded")
	}
	if cfg.MaxLatencyRegression >= 0 && result.Observed.LatencyRegression > cfg.MaxLatencyRegression {
		result.Reasons = append(result.Reasons, "latency_regression_exceeded")
	}
	if cfg.MaxFailureRateRegression >= 0 && result.Observed.FailureRateRegression > cfg.MaxFailureRateRegression {
		result.Reasons = append(result.Reasons, "failure_rate_regression_exceeded")
	}
	feedbackTotal := treatment.FeedbackCorrect + treatment.FeedbackWrong
	if cfg.MinFeedbackAccuracy > 0 && feedbackTotal == 0 {
		result.Reasons = append(result.Reasons, "feedback_unavailable")
	} else if cfg.MinFeedbackAccuracy > 0 && result.Observed.FeedbackAccuracy < cfg.MinFeedbackAccuracy {
		result.Reasons = append(result.Reasons, "feedback_accuracy_below_threshold")
	}
	result.Allowed = len(result.Reasons) == 0
	result.RollbackSuggested = !result.Allowed
	return result
}

func relativeRegression(control, treatment float64) float64 {
	if control <= 0 {
		if treatment <= 0 {
			return 0
		}
		return 1
	}
	return (treatment - control) / control
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
