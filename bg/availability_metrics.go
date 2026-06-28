package bg

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const availabilityMetricPrefix = "llmgw_availability_"

var (
	availabilityMetricOnce sync.Once

	availabilityCacheWrites  *prometheus.CounterVec
	availabilityCacheReads   *prometheus.CounterVec
	availabilityBackfillRuns *prometheus.CounterVec
	availabilityBackfillRows *prometheus.CounterVec
)

// registerAvailabilityMetrics registers all llmgw_availability_* collectors
// with the default Prometheus registry. Idempotent thanks to sync.Once;
// the gateway's promhttp handler at /metrics will surface these without
// any further wiring.
//
// Counters chosen so operators can answer the two questions we care about:
//   - "are probe writers still writing to Redis?" → writes_total{state,source}
//   - "is anything reading the cache?" → reads_total{source}
//   - "is the periodic backfill doing anything?" → backfill_runs_total{trigger}
//   - backfill_rows_total{trigger}
func registerAvailabilityMetrics() {
	availabilityMetricOnce.Do(func() {
		availabilityCacheWrites = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: availabilityMetricPrefix + "cache_writes_total",
				Help: "Total Redis HSET calls into llmgw:avail:* by source and target state.",
			},
			[]string{"source", "state"},
		)
		availabilityCacheReads = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: availabilityMetricPrefix + "cache_reads_total",
				Help: "Total Redis HGETALL calls against llmgw:avail:* by source (hit/miss).",
			},
			[]string{"source", "result"},
		)
		availabilityBackfillRuns = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: availabilityMetricPrefix + "backfill_runs_total",
				Help: "Total availability cache backfill invocations.",
			},
			[]string{"trigger"},
		)
		availabilityBackfillRows = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: availabilityMetricPrefix + "backfill_rows_total",
				Help: "Total rows written by availability cache backfill.",
			},
			[]string{"trigger"},
		)
		prometheus.MustRegister(availabilityCacheWrites, availabilityCacheReads,
			availabilityBackfillRuns, availabilityBackfillRows)
	})
}

func init() {
	// Register eagerly so the metrics show up on /metrics even before the
	// first write — counters default to 0 and the labels appear when the
	// first event lands.
	registerAvailabilityMetrics()
}

// recordAvailabilityCacheWrite is invoked from the cache writer path.
// source is one of "model_probe", "credential_probe", "passive_probe",
// "backfill", or "call_exit".
func recordAvailabilityCacheWrite(source, state string) {
	if availabilityCacheWrites == nil {
		return
	}
	availabilityCacheWrites.WithLabelValues(source, state).Inc()
}

// recordAvailabilityCacheRead is invoked from the reader path.
// result is "hit" or "miss".
func recordAvailabilityCacheRead(source, result string) {
	if availabilityCacheReads == nil {
		return
	}
	availabilityCacheReads.WithLabelValues(source, result).Inc()
}

// recordAvailabilityBackfillRun is invoked once per backfill pass.
// trigger is "periodic" or "manual".
func recordAvailabilityBackfillRun(trigger string) {
	if availabilityBackfillRuns == nil {
		return
	}
	availabilityBackfillRuns.WithLabelValues(trigger).Inc()
}

// recordAvailabilityBackfillRows is invoked per row written by a
// backfill pass (count = rows).
func recordAvailabilityBackfillRows(trigger string, rows int) {
	if availabilityBackfillRows == nil || rows <= 0 {
		return
	}
	availabilityBackfillRows.WithLabelValues(trigger).Add(float64(rows))
}
