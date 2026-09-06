package routingopt

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// confidence.go — P2.2 PostClassify: adjust classification confidence using
// historical per-task-type routing accuracy.
//
// Rationale: the heuristic classifier emits the same confidence for prompts it
// repeatedly misclassifies. When feedback shows a task type routing poorly
// (low success rate), damping its confidence makes the LLM-fallback threshold
// (Decider.LLMConfidenceThreshold) trigger more often exactly where the
// classifier is weakest — the "learning from runtime behaviour" loop for
// task-type assignment. High-accuracy task types get a small boost so the
// fast path stays on the heuristic classifier.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.3

// Confidence tuning constants. Kept as vars (not consts) so tests can tighten
// them without DB fixtures.
var (
	// confidenceBaseline is the accuracy level that produces zero adjustment.
	confidenceBaseline = 0.75
	// confidenceStrength scales the delta: adjusted = conf + (acc-baseline)*strength.
	confidenceStrength = 0.2
	// confidenceMinSamples below which no adjustment is applied (avoid noise
	// from a handful of early feedback rows).
	confidenceMinSamples = 20
	// confidenceMaxDelta bounds |adjustment| per request.
	confidenceMaxDelta = 0.1
	// confidenceTTL is how long the per-task accuracy snapshot is cached.
	confidenceTTL = 60 * time.Second
)

// ConfidenceAdjuster implements the PostClassify hook logic on top of
// routing_feedback_log history. DB reads are cached for confidenceTTL so the
// Decide hot path pays at most one small aggregate query per minute.
type ConfidenceAdjuster struct {
	feedbackDAO *FeedbackLogDAO

	mu          sync.Mutex
	taskStats   map[string]TaskAccuracyStat
	refreshedAt time.Time
}

// NewConfidenceAdjuster constructs an adjuster.
func NewConfidenceAdjuster(pool *pgxpool.Pool) *ConfidenceAdjuster {
	return &ConfidenceAdjuster{feedbackDAO: NewFeedbackLogDAO(pool)}
}

// taskStatsSnapshot returns the cached per-task accuracy map, refreshing from
// the DAO when the cache is older than confidenceTTL. On DAO error the last
// known snapshot is kept (possibly empty → no adjustment, baseline behaviour).
func (c *ConfidenceAdjuster) taskStatsSnapshot(ctx context.Context) map[string]TaskAccuracyStat {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.taskStats != nil && time.Since(c.refreshedAt) < confidenceTTL {
		return c.taskStats
	}
	stats, err := c.feedbackDAO.GetTaskTypeAccuracy(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		// Keep stale data (or nothing) — never fail routing for tuning.
		if c.taskStats == nil {
			c.taskStats = map[string]TaskAccuracyStat{}
		}
		c.refreshedAt = time.Now() // back off for one TTL before retrying
		return c.taskStats
	}
	c.taskStats = stats
	c.refreshedAt = time.Now()
	return c.taskStats
}

// AdjustClassConfidence returns the confidence adjusted by the historical
// accuracy of taskType. Pure function — see package constants for the formula.
func AdjustClassConfidence(taskType string, confidence float64, stats map[string]TaskAccuracyStat) float64 {
	stat, ok := stats[taskType]
	if !ok || stat.Samples < confidenceMinSamples {
		return confidence // not enough evidence → baseline
	}
	delta := (stat.Accuracy - confidenceBaseline) * confidenceStrength
	if delta > confidenceMaxDelta {
		delta = confidenceMaxDelta
	}
	if delta < -confidenceMaxDelta {
		delta = -confidenceMaxDelta
	}
	adjusted := confidence + delta
	if adjusted < 0 {
		return 0
	}
	if adjusted > 1 {
		return 1
	}
	return adjusted
}

// PostClassify is the hook entry used by RealOptimizer. Errors never escape:
// a failed snapshot read degrades to the original confidence.
func (c *ConfidenceAdjuster) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	return AdjustClassConfidence(taskType, confidence, c.taskStatsSnapshot(ctx)), nil
}
