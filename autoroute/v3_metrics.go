package autoroute

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// v3Metrics bundles the V3 observability collectors so tests can register
// them in an isolated registry instead of the process-global default one.
type v3Metrics struct {
	classificationDuration *prometheus.HistogramVec
	classificationTotal    *prometheus.CounterVec
	classificationCache    *prometheus.CounterVec
	costTotal              *prometheus.CounterVec
	costSavedTotal         prometheus.Counter
	feedbackTotal          *prometheus.CounterVec
}

// newV3Metrics creates the V3 collectors and registers them with reg when it
// is non-nil. Callers own the returned instance; there is no package-level
// deduplication, so each registry should get exactly one instance.
func newV3Metrics(reg prometheus.Registerer) *v3Metrics {
	m := &v3Metrics{
		classificationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    routingMetricPrefix + "classification_duration_seconds",
			Help:    "Time spent classifying requests.",
			Buckets: prometheus.DefBuckets,
		}, []string{"classifier", "path"}),
		classificationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: routingMetricPrefix + "classification_total",
			Help: "Classification results by task type and classifier.",
		}, []string{"task_type", "classifier"}),
		classificationCache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: routingMetricPrefix + "classification_cache_total",
			Help: "Classification cache accesses by outcome.",
		}, []string{"outcome"}),
		costTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: routingMetricPrefix + "cost_dollars_total",
			Help: "Estimated routing cost in USD by tier.",
		}, []string{"tier"}),
		costSavedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: routingMetricPrefix + "cost_saved_dollars_total",
			Help: "Estimated routing cost saved in USD.",
		}),
		feedbackTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: routingMetricPrefix + "classification_feedback_total",
			Help: "Classification feedback outcomes.",
		}, []string{"task_type", "correct"}),
	}
	if reg != nil {
		reg.MustRegister(
			m.classificationDuration, m.classificationTotal, m.classificationCache,
			m.costTotal, m.costSavedTotal, m.feedbackTotal,
		)
	}
	return m
}

// defaultV3Metrics is registered with the default registry at package init so
// the collectors are visible on /metrics from startup. Labels appear lazily
// on first event.
var defaultV3Metrics = newV3Metrics(prometheus.DefaultRegisterer)

func (m *v3Metrics) recordClassification(result *Classification, classifier, path string, duration time.Duration) {
	if m == nil || result == nil {
		return
	}
	m.classificationDuration.WithLabelValues(classifier, path).Observe(duration.Seconds())
	m.classificationTotal.WithLabelValues(string(result.Primary), classifier).Inc()
}

func (m *v3Metrics) recordCache(hit bool) {
	if m == nil {
		return
	}
	outcome := "miss"
	if hit {
		outcome = "hit"
	}
	m.classificationCache.WithLabelValues(outcome).Inc()
}

func (m *v3Metrics) recordCost(tier string, actualCost, baselineCost float64) {
	// Negative actual means "unknown cost": skip entirely, otherwise the
	// baseline-minus-actual saving would be inflated by |actual|.
	if m == nil || actualCost < 0 {
		return
	}
	m.costTotal.WithLabelValues(tier).Add(actualCost)
	if saved := baselineCost - actualCost; saved > 0 {
		m.costSavedTotal.Add(saved)
	}
}

func (m *v3Metrics) recordFeedback(taskType string, correct bool) {
	if m == nil {
		return
	}
	label := "false"
	if correct {
		label = "true"
	}
	m.feedbackTotal.WithLabelValues(taskType, label).Inc()
}

// Package-level record functions keep the original signatures so existing
// call sites (classification_cache.go) are unaffected. They all delegate to
// the default-registry instance and are nil-safe.

func recordClassificationMetrics(result *Classification, classifier, path string, duration time.Duration) {
	defaultV3Metrics.recordClassification(result, classifier, path, duration)
}

func recordClassificationCacheMetric(hit bool) {
	defaultV3Metrics.recordCache(hit)
}

func recordCostMetrics(tier string, actualCost, baselineCost float64) {
	defaultV3Metrics.recordCost(tier, actualCost, baselineCost)
}

func recordClassificationFeedback(taskType string, correct bool) {
	defaultV3Metrics.recordFeedback(taskType, correct)
}
