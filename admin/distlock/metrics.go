package distlock

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	lockScopeAuto    = "auto"
	lockScopeManual  = "manual"
	lockScopeUnknown = "unknown"
)

var (
	distlockAcquireTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "distlock_acquire_total",
		Help: "Distributed lock acquisition attempts by result.",
	}, []string{"result"})

	distlockWaitSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "distlock_wait_seconds",
		Help:    "Time followers spent waiting for a distributed lock leader.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"scope"})
)

func normalizeLockScope(scope string) string {
	switch scope {
	case lockScopeAuto, lockScopeManual:
		return scope
	default:
		return lockScopeUnknown
	}
}
