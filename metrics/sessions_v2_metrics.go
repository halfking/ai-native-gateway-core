package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// V2-P7: observation-window metrics for the Sessions V2 storage path.
//
// All metric names use the `sessions_v2_` prefix so they group cleanly on
// the Prometheus /metrics endpoint and don't collide with the existing
// `circuit_*`, `adapter_*`, `scheduler_*`, `safety_*`, `pool_*` metrics
// already registered in prometheus.go.
var (
	SessionsV2WriteSuccess = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_write_success_total",
		Help: "Total successful V2 writes",
	})
	SessionsV2WriteFailed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_write_failure_total",
		Help: "Total failed V2 writes",
	})
	SessionsV2WriteLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sessions_v2_write_latency_seconds",
		Help:    "V2 write latency",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
	})
	SessionsV2CacheHit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_cache_hit_total",
		Help: "L0/L1/L2 cache hits",
	})
	SessionsV2CacheMiss = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_cache_miss_total",
		Help: "L0/L1/L2 cache misses",
	})
	SessionsV2DualReadDiff = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_dual_read_diff_total",
		Help: "Number of turns with V1/V2 diffs",
	})
)