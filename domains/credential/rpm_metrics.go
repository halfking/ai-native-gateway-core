package credential

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	rpmLimiterMode = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_rpm_limiter_mode",
			Help: "RPM limiter mode (0=memory, 1=redis)",
		},
		[]string{"mode"},
	)
	rpmRedisDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_rpm_redis_duration_seconds",
			Help:    "Redis RPM check latency",
			Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05},
		},
		[]string{"result"},
	)
	rpmRedisFallback = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_rpm_redis_fallback_total",
			Help: "Redis RPM fallback to memory count",
		},
		[]string{"reason"},
	)
	rpmMetricsOnce sync.Once
)

func registerRPMMetrics() {
	rpmMetricsOnce.Do(func() {
		prometheus.MustRegister(rpmLimiterMode, rpmRedisDuration, rpmRedisFallback)
	})
}

func init() {
	registerRPMMetrics()
}

func recordRPMMode(redisMode bool) {
	if redisMode {
		rpmLimiterMode.WithLabelValues("redis").Set(1)
		rpmLimiterMode.WithLabelValues("memory").Set(0)
		return
	}
	rpmLimiterMode.WithLabelValues("redis").Set(0)
	rpmLimiterMode.WithLabelValues("memory").Set(1)
}

func observeRedisRPM(start time.Time, result string) {
	rpmRedisDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
}

func recordRPMFallback(reason string) {
	rpmRedisFallback.WithLabelValues(reason).Inc()
}
