package autoroute

// outcome_metrics.go — 2026-09-09 24h audit round 3 (#7 follow-up): the
// RoutingOutcomeStats five counters (stashed / matched / orphan / expired /
// dropped, autoroute/outcome_feedback.go) previously lived only in memory.
// Without a Prometheus export an orphan surge (every completion failing to
// match its stashed decision — the signature of a decider/reporter version
// skew or a lost X-Request-Id) was completely invisible.
//
// Export style: a custom Collector that reads the atomics at scrape time —
// same pattern as routingopt/metrics.go feedbackWritesCollector. No double
// bookkeeping (the atomic counters stay the single source of truth, so the
// in-process janitor log and the metric can never drift), no locks, no
// allocation on the request hot path.
//
// Cardinality: one metric, one fixed label `result` with exactly five values.
// No request-level dimensions (GW-00: no request_id / model / tenant).
// Registration follows the package convention (init + sync.Once, see
// metrics.go); /metrics surfaces it via promhttp automatically.

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const outcomeMetricName = routingMetricPrefix + "outcome_total"

// outcomeResultLabels are the five fixed label values, one per registry
// counter. Order is irrelevant to Prometheus but keeps tests readable.
var outcomeResultLabels = []string{"stashed", "matched", "orphan", "expired", "dropped"}

// outcomeOutcomeDesc is shared by Describe and Collect. Help text documents
// what each label value means so an operator can alert on orphan growth
// without reading the source.
var outcomeOutcomeDesc = prometheus.NewDesc(
	outcomeMetricName,
	"Auto-route outcome registry counters (autoroute.ReportRoutingOutcome loop), "+
		"read from atomic counters at scrape time. "+
		"result=stashed: decisions parked awaiting an outcome; "+
		"matched: outcomes that found their stashed decision (feedback written); "+
		"orphan: outcomes with NO stashed decision (non-auto request, cache hit, "+
		"already settled, or decider/reporter skew — alert on sustained growth); "+
		"expired: stashed decisions dropped on TTL/capacity eviction (outcome never "+
		"arrived, no feedback row written); "+
		"dropped: feedback writes shed because the bounded write semaphore was full.",
	[]string{"result"}, nil,
)

// outcomeStatsCollector projects the package-level atomics into
// llmgw_autoroute_outcome_total{result=...} at scrape time.
type outcomeStatsCollector struct{}

func (outcomeStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- outcomeOutcomeDesc
}

func (outcomeStatsCollector) Collect(ch chan<- prometheus.Metric) {
	stashed, matched, orphan, expired, dropped := RoutingOutcomeStats()
	values := []int64{stashed, matched, orphan, expired, dropped}
	for i, label := range outcomeResultLabels {
		ch <- prometheus.MustNewConstMetric(outcomeOutcomeDesc, prometheus.CounterValue,
			float64(values[i]), label)
	}
}

var registerOutcomeMetricsOnce sync.Once

// registerOutcomeMetrics registers the collector with the default registry.
// sync.Once idempotent (init and tests may both trigger it).
func registerOutcomeMetrics() {
	registerOutcomeMetricsOnce.Do(func() {
		prometheus.MustRegister(outcomeStatsCollector{})
	})
}

func init() {
	registerOutcomeMetrics()
}
