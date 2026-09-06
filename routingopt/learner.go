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
	integrator  *FeedbackIntegrator
}

// NewAdaptiveLearner constructs a learner instance.
func NewAdaptiveLearner(pool *pgxpool.Pool, integrator *FeedbackIntegrator) *AdaptiveLearner {
	return &AdaptiveLearner{
		stateDAO:   NewOptimizationStateDAO(pool),
		metricsDAO: NewOptimizationMetricsDAO(pool),
		integrator: integrator,
	}
}

// GetStats implements the GetStats hook.
// Returns optimizer performance statistics for admin API.
//
// Week 1 骨架版本：
//   - Overall accuracy: 从 routing_optimization_metrics 聚合（最近24小时）
//   - Parameter version: 从 routing_optimization_state 读取
//   - Human annotations used: 从 FeedbackIntegrator 查询
//
// Week 2 完整版本：
//   - Weighted accuracy: human annotations × 2
//   - Accuracy by task type / provider
//   - Confidence distribution histogram
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
	
	// 2. Get aggregated metrics (last 24 hours)
	since := time.Now().Add(-24 * time.Hour)
	overallAccuracy, _, _, err := l.metricsDAO.GetAggregatedMetrics(ctx, since)
	if err != nil {
		// Metrics not available: use state's overall_accuracy
		if state.OverallAccuracy != nil {
			overallAccuracy = *state.OverallAccuracy
		} else {
			overallAccuracy = 0.0
		}
	}
	
	// 3. Get human annotation count (last 24 hours)
	humanAnnotationsUsed, err := l.integrator.GetHumanAnnotationStats(ctx, since)
	if err != nil {
		humanAnnotationsUsed = 0 // Ignore error, return 0
	}
	
	// 4. Construct stats
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
	Type        string    // "accuracy_drop", "latency_spike", "provider_failure"
	Severity    string    // "warning", "critical"
	Description string    // human-readable description
	DetectedAt  time.Time
	Metrics     map[string]float64 // relevant metrics
}
