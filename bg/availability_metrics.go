package bg

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const availabilityMetricPrefix = "llmgw_availability_"

var (
	availabilityMetricOnce sync.Once

	availabilityCacheWrites   *prometheus.CounterVec
	availabilityCacheReads    *prometheus.CounterVec
	availabilityBackfillRuns  *prometheus.CounterVec
	availabilityBackfillRows  *prometheus.CounterVec
	availabilityReadDuration  prometheus.Histogram
	availabilityWriteDuration prometheus.Histogram
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
//   - "is Redis serving the cache fast enough?" → read_duration_seconds
//   - "is Redis accepting writes fast enough?" → write_duration_seconds
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
		// availability_read_duration_seconds measures the wall-clock cost
		// of the HGETALL round-trip on the hot read path (admin probe
		// endpoints + routing MaybeExitSuspicious). The bucket layout is
		// tuned for a Redis with sub-millisecond LAN latency: a healthy
		// p99 sits under 5ms; sustained values above 50ms indicate
		// Redis is under pressure or the cache layer is not getting the
		// hit rate it should.
		availabilityReadDuration = prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: availabilityMetricPrefix + "read_duration_seconds",
				Help: "Wall-clock duration of Redis HGETALL against llmgw:avail:*.",
				Buckets: []float64{
					0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1,
				},
			},
		)
		// availability_write_duration_seconds measures the HSET + EXPIRE
		// pipeline round-trip. Probe workers call this on every state
		// transition, so a p99 above 50ms here directly slows the probe
		// loop and can back-pressure SuspiciousProbe / PassiveProbe
		// workers.
		availabilityWriteDuration = prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: availabilityMetricPrefix + "write_duration_seconds",
				Help: "Wall-clock duration of Redis HSET+EXPIRE pipeline against llmgw:avail:*.",
				Buckets: []float64{
					0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1,
				},
			},
		)
		prometheus.MustRegister(availabilityCacheWrites, availabilityCacheReads,
			availabilityBackfillRuns, availabilityBackfillRows,
			availabilityReadDuration, availabilityWriteDuration)
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

// recordAvailabilityReadDuration is invoked from the reader path. It
// captures the wall-clock cost of the underlying HGETALL round-trip
// regardless of hit/miss outcome so operators can correlate the
// cache_writes_total/cache_reads_total counters against raw latency.
func recordAvailabilityReadDuration(seconds float64) {
	if availabilityReadDuration == nil {
		return
	}
	availabilityReadDuration.Observe(seconds)
}

// recordAvailabilityWriteDuration is invoked from the cache writer
// path. It captures the wall-clock cost of the HSET+EXPIRE pipeline
// round-trip so operators can spot a slow write side that wouldn't
// show up in the read latency histogram.
func recordAvailabilityWriteDuration(seconds float64) {
	if availabilityWriteDuration == nil {
		return
	}
	availabilityWriteDuration.Observe(seconds)
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
