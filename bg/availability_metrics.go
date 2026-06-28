package bg

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const availabilityMetricPrefix = "llmgw_availability_"

var (
	availabilityMetricOnce sync.Once

	availabilityCacheWrites        *prometheus.CounterVec
	availabilityCacheReads         *prometheus.CounterVec
	availabilityBackfillRuns       *prometheus.CounterVec
	availabilityBackfillRows       *prometheus.CounterVec
	availabilityReadDuration       prometheus.Histogram
	availabilityWriteDuration      prometheus.Histogram
	availabilityKeys               prometheus.Gauge
	availabilityBackfillRowsPerRun prometheus.Gauge
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
//   - "how many cache entries are live right now?" → keys_count
//   - "is each backfill pass productive?" → backfill_rows_per_run
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
		// availability_keys_count is the live Redis cardinality for the
		// llmgw:avail:* keyspace. Updated from two sources:
		//   - the cache writer increments it after a successful Set so
		//     gauge tracks writes within seconds
		//   - the optional AvailabilityKeyCounter can periodically SCAN
		//     and correct drift (e.g. after Redis FLUSHDB, or after a
		//     writer path that bypassed the cache package)
		availabilityKeys = prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: availabilityMetricPrefix + "keys_count",
				Help: "Live count of keys under the llmgw:avail:* namespace.",
			},
		)
		// availability_backfill_rows_per_run is the average row yield
		// per backfill invocation. Operators can pair this with the
		// BackfillEmpty alert to spot "running but producing nothing"
		// without having to compute rate(rows) / rate(runs) in
		// PromQL.
		availabilityBackfillRowsPerRun = prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: availabilityMetricPrefix + "backfill_rows_per_run",
				Help: "Average rows written per availability backfill pass.",
			},
		)
		prometheus.MustRegister(availabilityCacheWrites, availabilityCacheReads,
			availabilityBackfillRuns, availabilityBackfillRows,
			availabilityReadDuration, availabilityWriteDuration,
			availabilityKeys, availabilityBackfillRowsPerRun)
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

// recordAvailabilityKeysAbsolute lets periodic sweepers (e.g. an SCAN
// based reconciler) replace the gauge with a fresh, authoritative
// count. The cache writer path does NOT update this gauge because
// HSET is upsert and we cannot distinguish create from update without
// an extra Redis call; the periodic SCAN is the only correct source
// of truth. See AvailabilityKeyCounter for the worker.
func recordAvailabilityKeysAbsolute(count int) {
	if availabilityKeys == nil {
		return
	}
	availabilityKeys.Set(float64(count))
}

// recordAvailabilityBackfillRowsPerRun updates the running average of
// rows written per backfill pass. The caller passes the most recent
// run's row count; we apply a simple incremental average so the
// gauge does not snap to a new value on every tick.
func recordAvailabilityBackfillRowsPerRun(rows int) {
	if availabilityBackfillRowsPerRun == nil {
		return
	}
	// Read current gauge, blend with new sample, set. The "0.7 prior +
	// 0.3 new" weighting gives a slow-moving but eventually-tracking
	// view that smooths over the periodic pass cadence.
	prior := 0.0
	if existing, err := readBackfillRowsPerRun(); err == nil {
		prior = existing
	}
	blended := 0.7*prior + 0.3*float64(rows)
	availabilityBackfillRowsPerRun.Set(blended)
}

// readBackfillRowsPerRun is a small helper that reads the current
// gauge value without going through the dto pipeline. Returns 0 if the
// gauge has not been observed yet.
func readBackfillRowsPerRun() (float64, error) {
	if availabilityBackfillRowsPerRun == nil {
		return 0, nil
	}
	m := &dto.Metric{}
	if err := availabilityBackfillRowsPerRun.Write(m); err != nil {
		return 0, err
	}
	return m.Gauge.GetValue(), nil
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
