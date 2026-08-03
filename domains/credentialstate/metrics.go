// Package credentialstate — metrics.go Prometheus collectors.
//
// Deprecated: credentialstate is superseded by URSM v2 (domains/ursm/v2).
// It remains for legacy/off/canary modes only. Do not add new callers.
// In URSM_V2_MODE=authoritative, this package must have zero live reads/writes (spec §10 Step 5 C-1).
package credentialstate

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	redisWriteFailures  prometheus.Counter
	registerMetricsOnce sync.Once
)

func registerMetrics() {
	registerMetricsOnce.Do(func() {
		redisWriteFailures = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_credstate_redis_write_failures_total",
				Help: "Total number of credentialstate Redis cache write failures",
			},
		)
		prometheus.MustRegister(redisWriteFailures)
	})
}

func init() {
	registerMetrics()
}

func recordRedisWriteFailure() {
	if redisWriteFailures != nil {
		redisWriteFailures.Inc()
	}
}
