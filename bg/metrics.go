// Package bg — metrics.go
//
// Prometheus counters and histograms for the synchronous no-candidate
// probe path added in 2026-07-17. The metrics are intentionally distinct
// from the background NodeProbeWorker counters so dashboards can
// separately attribute "probe runs triggered by an inbound request that
// hit no_candidates" vs "probe runs scheduled by the 30s tick /
// request_failure threshold".
//
// Counters
//
//	llmgw_node_probe_sync_total{outcome="recovered"|"exhausted"|"timeout"|"client_cancel"|"skipped"}
//
//	  recovered  — at least one (cred,model) pair passed both rounds,
//	               the executor retried Execute() and the upstream call
//	               returned 200. The user-facing request succeeded.
//	  exhausted  — every (cred,model) probe failed; the executor returned
//	               503 model_not_found.
//	  timeout    — ProbeSync ctx fired before any candidate recovered.
//	  client_cancel — the client's r.Context() was cancelled (client
//	               disconnected) while we were holding.
//	  skipped    — ProbeSync was a no-op (kill-switch off, candidates
//	               empty, etc.) — kept for trace completeness.
//
// Histogram
//
//	llmgw_node_probe_sync_duration_seconds
//
//	  Wall-clock time spent inside ProbeSync, including the parallel
//	  fan-out + gateway round. Observed at every outcome.
//
// Counter
//
//	llmgw_node_probe_sync_inflight_waiters
//
//	  Gauge of how many request goroutines are currently waiting for an
//	  in-flight background probe to finish (dedup reuse path).
package bg

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	nodeProbeSyncTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_node_probe_sync_total",
			Help: "Outcomes of synchronous no-candidate probes triggered from the request hot path.",
		},
		[]string{"outcome"},
	)

	nodeProbeSyncDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "llmgw_node_probe_sync_duration_seconds",
		Help:    "Wall-clock duration of ProbeSync invocations from the request hot path.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 15},
	})

	nodeProbeSyncInflightWaiters = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llmgw_node_probe_sync_inflight_waiters",
		Help: "Number of request goroutines currently blocked on an in-flight background probe (dedup reuse).",
	})
)