package statesource

import (
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/prometheus/client_golang/prometheus"
)

// routingStateSourceTotal is the Prometheus-facing counter that mirrors
// the in-process atomic counters. It is incremented in
// RecordRoutingStateSource so every PlanCandidates call that records a
// source also bumps the Prometheus metric — making the fail-open ratio
// observable via /metrics without a separate collector loop.
//
// 2026-08-03 (spec §13 #10 DEFERRED → resolved): the Snapshot() API
// remains for in-process/tests; this counter is the production
// observability channel.
var routingStateSourceTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "routing_state_source_total",
	Help: "Routing state source decisions labelled by source (node_mirror_hit, node_mirror_miss, node_mirror_stale, fallback, off, canary, authoritative, skipped).",
},
	[]string{"source"},
)

func init() {
	// Pre-initialise all labels so the metric surface is stable from
	// boot (dashboards never see a missing label before traffic).
	for _, s := range allSources {
		routingStateSourceTotal.WithLabelValues(string(s)).Add(0)
	}
}
