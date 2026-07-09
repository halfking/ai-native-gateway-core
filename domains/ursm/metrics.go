package ursm

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	stateWriteFailures  *prometheus.CounterVec
	registerMetricsOnce sync.Once
)

func registerMetrics() {
	registerMetricsOnce.Do(func() {
		stateWriteFailures = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_ursm_state_write_failures_total",
				Help: "Total number of URSM state write failures by layer",
			},
			[]string{"layer"}, // provider, credential, model, node
		)
		prometheus.MustRegister(stateWriteFailures)
	})
}

func init() {
	registerMetrics()
}

func recordStateWriteFailure(layer string) {
	if stateWriteFailures != nil {
		stateWriteFailures.WithLabelValues(layer).Inc()
	}
}
