package routingopt

import (
	"context"
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

// AdaptParameters performs online parameter optimization.
// Called by a background worker every 5 minutes.
//
// Week 1: 禁用（ROUTING_OPT_ADAPTIVE_LEARNING=false）
// Week 2: 启用自适应学习
//   - Bayesian Optimization for hyperparameters
//   - Sliding window accuracy (最近1000次)
//   - 异常检测: 准确率连续3个窗口下降>5%
//   - 自动回滚: 新参数准确率<旧参数
func (l *AdaptiveLearner) AdaptParameters(ctx context.Context) error {
	// Week 1: no-op (adaptive learning disabled)
	// Week 2: implement Bayesian Optimization
	return nil
}

// DetectAnomalies detects accuracy drops and triggers alerts.
//
// Week 1: 禁用
// Week 2: 实现异常检测
//   - Accuracy drop > 5% for 3 consecutive 5-minute windows
//   - Latency spike > 2x baseline
//   - Provider failure rate > 20%
func (l *AdaptiveLearner) DetectAnomalies(ctx context.Context) ([]Anomaly, error) {
	// Week 1: no anomalies
	return nil, nil
}

// Anomaly represents a detected routing anomaly.
type Anomaly struct {
	Type        string // "accuracy_drop", "latency_spike", "provider_failure"
	Severity    string // "warning", "critical"
	Description string // human-readable description
	DetectedAt  time.Time
	Metrics     map[string]float64 // relevant metrics
}
