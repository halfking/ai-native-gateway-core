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
	}, []string{"kind"}) // kind: cred_retry|cred_switch|model_change

	metricStatsDrop = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dispatch_stats_drop_total",
		Help: "Tier-3 stats events dropped because the event bus was full.",
	})

	// ===== V3.1: 9-stage lifecycle histograms (T0–T9) =====
	// Observed once per completed request in Pipeline.complete().
	// Label "result" is a closed enum: success|fail_prefirstbyte|fail_postfirstbyte|shutdown|error.
	// Buckets: short stages use 5ms..~20s; end-to-end / streaming use 50ms..~14min.

	stageBucketsShort = prometheus.ExponentialBuckets(0.005, 2, 12) // 5ms .. ~20s
	stageBucketsLong  = prometheus.ExponentialBuckets(0.05, 2, 14)  // 50ms .. ~14min

	// T0→T6: total time spent waiting in all queues before forward.
	metricStageQueueWaitT0T6 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_queue_wait_seconds",
		Help:    "T0→T6: time from request arrival to credential-queue dequeue (all queue wait).",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T1→T2: wait inside the total/model admission queue.
	metricStageTotalQueueT1T2 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_total_queue_seconds",
		Help:    "T1→T2: time spent in the total (admission) queue.",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T3→T4: wait inside the per-model queue before credential selection.
	metricStageModelQueueT3T4 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_model_queue_seconds",
		Help:    "T3→T4: time spent in the Tier-1 model queue.",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T5→T6: wait inside the per-credential queue (governor pacing).
	metricStageCredQueueT5T6 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_cred_queue_seconds",
		Help:    "T5→T6: time spent in the Tier-2 credential queue before governor admission.",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T2→T5: routing + credential selection overhead (between total-dequeue and cred-enqueue).
	metricStageRoutingT2T5 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_routing_seconds",
		Help:    "T2→T5: model routing and credential selection overhead.",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T6→T7: gap from governor acquire to upstream forward start.
	metricStageAcquireT6T7 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_acquire_seconds",
		Help:    "T6→T7: time from credential dequeue to forward start.",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T7→T8: upstream TTFB (time to first byte).
	metricStageUpstreamT7T8 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_upstream_seconds",
		Help:    "T7→T8: upstream latency from forward start to first response byte.",
		Buckets: stageBucketsShort,
	}, []string{"result"})

	// T8→T9: streaming / response body transfer duration.
	metricStageStreamingT8T9 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_streaming_seconds",
		Help:    "T8→T9: response streaming duration from first byte to completion.",
		Buckets: stageBucketsLong,
	}, []string{"result"})

	// T0→T9: end-to-end request lifetime inside dispatch.
	metricStageTotalT0T9 = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dispatch_stage_total_seconds",
		Help:    "T0→T9: end-to-end dispatch lifetime from arrival to response end.",
		Buckets: stageBucketsLong,
	}, []string{"result"})
)
