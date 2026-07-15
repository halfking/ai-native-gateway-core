package routingstate

import (
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricsOnce sync.Once

	evidenceTotal       *prometheus.CounterVec
	probeDecisionsTotal *prometheus.CounterVec
)

func registerMetrics() {
	metricsOnce.Do(func() {
		evidenceTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_routingstate_evidence_total",
				Help: "Shadow routing-state evidence decisions by source, scope, accepted flag, and bounded reason.",
			},
			[]string{"source", "scope", "accepted", "reason"},
		)
		probeDecisionsTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_routingstate_probe_decisions_total",
				Help: "Shadow probe coordinator decisions by trigger, scope, accepted flag, and bounded reason.",
			},
			[]string{"trigger", "scope", "accepted", "reason"},
		)
		prometheus.MustRegister(evidenceTotal, probeDecisionsTotal)
	})
}

func recordEvidence(source Source, scope Scope, accepted bool, reason string) {
	if evidenceTotal == nil {
		return
	}
	evidenceTotal.WithLabelValues(string(source), string(scope), strconv.FormatBool(accepted), reason).Inc()
}

func recordProbeDecision(trigger ProbeTrigger, scope Scope, accepted bool, reason string) {
	if probeDecisionsTotal == nil {
		return
	}
	probeDecisionsTotal.WithLabelValues(string(trigger), string(scope), strconv.FormatBool(accepted), reason).Inc()
}

func init() {
	registerMetrics()
}
