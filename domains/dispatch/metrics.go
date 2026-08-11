package dispatch

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics are flat, domain-prefixed promauto gauges/counters/histograms
// (matches the per-package style in alerting/metrics.go and
// metrics/auto_summary_metrics.go). Label cardinality is closed-enum per the
// guard in metrics/label_cardinality_guard_test.go.

var (
	metricModelQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dispatch_model_queue_depth",
		Help: "Current depth of the Tier-1 model queue.",
	}, []string{"model"})

	metricModelQueueWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_model_queue_wait_seconds",
		Help:    "Time a request spent in the Tier-1 model queue before dispatch.",
		Buckets: prometheus.ExponentialBuckets(0.005, 2, 12), // 5ms .. ~20s
	}, []string{"model"})

	metricCredQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dispatch_cred_queue_depth",
		Help: "Current depth of the Tier-2 credential queue.",
	}, []string{"credential", "mode"})

	metricCredQueueWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_cred_queue_wait_seconds",
		Help:    "Time a request spent in the Tier-2 credential queue before forwarding.",
		Buckets: prometheus.ExponentialBuckets(0.005, 2, 12),
	}, []string{"credential"})

	metricInFlight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dispatch_in_flight",
		Help: "Requests currently being forwarded (past the governor, before completion).",
	}, []string{"credential", "mode"})

	metricDequeued = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dispatch_dequeued_total",
		Help: "Requests dequeued from a credential queue for forwarding.",
	}, []string{"credential", "mode"})

	metricForwarded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dispatch_forwarded_total",
		Help: "Forward attempts by outcome.",
	}, []string{"credential", "result"}) // result: success|fail_prefirstbyte|fail_postfirstbyte

	metricOverflow = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dispatch_overflow_total",
		Help: "Requests that could not be enqueued and were redirected/rejected.",
	}, []string{"reason"}) // reason: cred_queue_full|model_queue_full|pace_timeout|no_route

	metricFailover = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dispatch_failover_total",
		Help: "Failover transitions by kind.",
	}, []string{"kind"}) // kind: cred_retry|cred_switch|model_switch

	metricStatsDrop = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dispatch_stats_drop_total",
		Help: "Tier-3 stats events dropped because the event bus was full.",
	})
)
