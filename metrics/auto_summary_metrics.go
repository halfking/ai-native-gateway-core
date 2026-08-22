package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 2026-08-06: observability for the auto-title / auto-summary loopback
// pipeline. All metric names use the `auto_summary_` / `auto_title_`
// prefixes so they group cleanly on /metrics and don't collide with
// existing `circuit_*`, `adapter_*`, `sessions_v2_*` etc. registered
// in prometheus.go / sessions_v2_metrics.go.
//
// Label vocabulary kept low-cardinality by design:
//   - result ∈ {ok, error, rate_limited, saturated, transient_retry,
//     map_reduce_partial, invalid_response}
//     (closed enum, safe for Prometheus per-label growth)
//   - mode ∈ {summary, map, reduce} — distinguishes the LLM call shape
//     inside auto_summary
//
// Cardinality is bounded by the small enum above; the auto-summary
// emit site uses NewAutoSummaryMetricsHistogram to avoid double-register
// in unit tests (which instantiate multiple AutoSummaryGenerators).
var (
	// Counter — every trigger decision (success or skip)
	AutoSummaryTrigger = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auto_summary_trigger_total",
		Help: "auto-summary trigger decisions (ok/error/rate_limited/saturated/etc.)",
	}, []string{"result"})

	// Counter — every LLM call (ok, transient_retry, invalid_response, etc.)
	AutoSummaryLLMCall = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auto_summary_llm_call_total",
		Help: "auto-summary LLM calls",
	}, []string{"mode", "result"})

	// Histogram — LLM call latency
	AutoSummaryLLMLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auto_summary_llm_latency_seconds",
		Help:    "auto-summary LLM call latency (per mode)",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 12), // 50ms .. ~200s
	}, []string{"mode"})

	// Counter — rolling-gate skip reason
	AutoSummaryGateSkip = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auto_summary_gate_skip_total",
		Help: "auto-summary rolling-gate skips by reason",
	}, []string{"reason"})

	// Counter — map-reduce partial failure (one chunk failed)
	AutoSummaryMapReducePartialFail = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auto_summary_map_reduce_partial_fail_total",
		Help: "map-reduce calls where at least one chunk LLM failed",
	})

	// Counter — chunk count distribution summary
	AutoSummaryChunks = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "auto_summary_chunks",
		Help:    "number of chunks a long corpus was split into (map-reduce only)",
		Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 34},
	})

	// Title-side metrics (mirrors for the older auto_title pipeline)
	AutoTitleTrigger = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auto_title_trigger_total",
		Help: "auto-title trigger decisions",
	}, []string{"result"})
	AutoTitleLLMCall = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auto_title_llm_call_total",
		Help: "auto-title LLM calls",
	}, []string{"result"})
	AutoTitleLLMLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "auto_title_llm_latency_seconds",
		Help:    "auto-title LLM call latency",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 12),
	})
)
