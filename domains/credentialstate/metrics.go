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
