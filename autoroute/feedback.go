package autoroute

// feedback.go — implicit feedback signal computation for auto-route tuning.
//
// This module computes a per-request quality score from implicit signals
// (success, latency, cost, session drift) WITHOUT requiring explicit user
// feedback (thumbs up/down). The quality score is the input to the Phase 4
// feedback analyzer, which aggregates it into keyword and weight proposals.
//
// Design:
//   - Pure functions (no I/O) for testability
//   - All scores normalised to [0, 1] where 1 = best
//   - The composite quality_score uses fixed weights (0.4/0.3/0.2/0.1)
//     chosen to prioritise correctness (success) over speed/cost
//   - Session drift detection is session-local (no cross-request DB lookup
//     in the hot path; the relay layer passes the previous task_type)

// FeedbackWeights controls how each signal contributes to the composite
// quality score. The defaults prioritise success (correctness) above all.
type FeedbackWeights struct {
	Success float64 // default 0.4
	Latency float64 // default 0.3
	Cost    float64 // default 0.2
	Drift   float64 // default 0.1 (penalty for task-type instability)
}

// DefaultFeedbackWeights returns the standard weighting.
func DefaultFeedbackWeights() FeedbackWeights {
	return FeedbackWeights{
		Success: 0.4,
		Latency: 0.3,
		Cost:    0.2,
		Drift:   0.1,
	}
}

// FeedbackInput is the raw data needed to compute a feedback signal.
// All fields are sourced from request_logs at request completion time.
type FeedbackInput struct {
	// Success is 1.0 for full success, 0.0 for failure, 0.5 for partial
	// (e.g. stream interrupted before completion).
	Success float64

	// LatencyMs is the total request latency in milliseconds.
	LatencyMs int

	// CostUSD is the total cost for this request.
	CostUSD float64

	// P95BaselineMs is the 75th-percentile latency for this task_type
	// (from model_task_index or a rolling cache). 0 = unknown.
	P95BaselineMs int

	// P75BaselineCost is the 75th-percentile cost for this task_type.
	// 0 = unknown.
	P75BaselineCost float64

	// DriftFlag is true when this request's task_type differs from the
	// previous request in the same session AND the change was NOT from
	// a hard override (vision/long_context/agent).
	DriftFlag bool
}

// FeedbackSignal is the computed quality assessment for one request.
type FeedbackSignal struct {
	SuccessScore  float64 // 0-1
	LatencyScore  float64 // 0-1
	CostScore     float64 // 0-1
	QualityScore  float64 // 0-1 composite
	DriftFlag     bool
}

// ComputeFeedback calculates the per-request quality signal from raw metrics.
//
// Normalisation formulas:
//
//	latency_score = 1 - clamp(latency_ms / p95_baseline, 0, 1)
//	  When p95_baseline = 0 (unknown), defaults to 0.5 (neutral).
//	cost_score    = 1 - clamp(cost / p75_baseline, 0, 1)
//	  When p75_baseline = 0 (unknown), defaults to 0.5 (neutral).
//	quality       = w.Success*success + w.Latency*latency_score +
//	                w.Cost*cost_score + w.Drift*(1 - drift_flag)
func ComputeFeedback(input FeedbackInput) FeedbackSignal {
	w := DefaultFeedbackWeights()
	return ComputeFeedbackWithWeights(input, w)
}

// ComputeFeedbackWithWeights allows custom weighting (used in tests and
// future admin-tunable configurations).
func ComputeFeedbackWithWeights(input FeedbackInput, w FeedbackWeights) FeedbackSignal {
	success := clamp01(input.Success)

	// Latency normalisation
	latencyScore := 0.5 // neutral when unknown
	if input.P95BaselineMs > 0 {
		ratio := float64(input.LatencyMs) / float64(input.P95BaselineMs)
		latencyScore = 1.0 - clamp01(ratio)
	}

	// Cost normalisation
	costScore := 0.5 // neutral when unknown
	if input.P75BaselineCost > 0 {
		ratio := input.CostUSD / input.P75BaselineCost
		costScore = 1.0 - clamp01(ratio)
	}

	// Drift penalty: drift_flag=true → 0 contribution; false → full
	driftContribution := 0.0
	if !input.DriftFlag {
		driftContribution = 1.0
	}

	quality := w.Success*success +
		w.Latency*latencyScore +
		w.Cost*costScore +
		w.Drift*driftContribution

	return FeedbackSignal{
		SuccessScore: success,
		LatencyScore: latencyScore,
		CostScore:    costScore,
		QualityScore: clamp01(quality),
		DriftFlag:    input.DriftFlag,
	}
}

// DetectSessionDrift determines whether a task-type change between two
// consecutive requests in the same session is a "drift" (potential
// misclassification) or an expected hard-override transition.
//
// A drift is flagged when:
//   - prevTask != currTask AND
//   - currTask is NOT a hard-override type (vision/long_context/agent)
//
// Hard-override transitions are expected (e.g. user adds an image →
// vision) and should not be penalised.
func DetectSessionDrift(prevTask, currTask TaskType) bool {
	if prevTask == "" || prevTask == currTask {
		return false
	}
	// Hard overrides are expected transitions, not drift
	switch currTask {
	case TaskVision, TaskLongContext, TaskAgent:
		return false
	}
	return true
}

// clamp01 restricts a float64 to the [0, 1] range.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
