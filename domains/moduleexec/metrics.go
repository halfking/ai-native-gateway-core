// Package moduleexec - metrics.go
// Prometheus metrics for module executor
//
// Metrics exposed:
//   - llmgw_module_execution_total         (Counter)   module execution count by module/status/cache_hit
//   - llmgw_module_execution_duration_seconds (Histogram) module execution latency
//   - llmgw_module_cache_hit_total         (Counter)   cache hits by level (L0/L1/L2)
//   - llmgw_module_cache_miss_total        (Counter)   cache misses (no valid cache found)
package moduleexec

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// moduleExecutionTotal counts module executions by module name, status and cache source.
	moduleExecutionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_module_execution_total",
			Help: "Total number of module executions.",
		},
		[]string{"module", "status", "from_cache"},
	)

	// moduleExecutionDuration observes the duration of module execution in seconds.
	moduleExecutionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_module_execution_duration_seconds",
			Help:    "Module execution duration in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10},
		},
		[]string{"module"},
	)

	// cacheHitTotal counts cache hits by cache level.
	//   level: "L0" = in-memory, "L1" = Redis, "L2" = database
	cacheHitTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_module_cache_hit_total",
			Help: "Total number of cache hits by cache level (L0/L1/L2).",
		},
		[]string{"module", "level"},
	)

	// cacheMissTotal counts cache misses (all cache levels checked, none valid).
	cacheMissTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_module_cache_miss_total",
			Help: "Total number of cache misses (no valid cache found).",
		},
		[]string{"module"},
	)
)

// recordExecution records metrics for a completed module execution.
func recordExecution(module string, status string, fromCache bool, duration time.Duration) {
	fromCacheStr := "false"
	if fromCache {
		fromCacheStr = "true"
	}
	moduleExecutionTotal.WithLabelValues(module, status, fromCacheStr).Inc()
	moduleExecutionDuration.WithLabelValues(module).Observe(duration.Seconds())
}

// recordCacheHit records a cache hit at the specified level.
func recordCacheHit(module string, level string) {
	cacheHitTotal.WithLabelValues(module, level).Inc()
}

// recordCacheMiss records a cache miss.
func recordCacheMiss(module string) {
	cacheMissTotal.WithLabelValues(module).Inc()
}
