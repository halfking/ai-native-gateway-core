package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// V2-P7: observation-window metrics for the Sessions V2 storage path.
//
// All metric names use the `sessions_v2_` prefix so they group cleanly on
// the Prometheus /metrics endpoint and don't collide with the existing
// `circuit_*`, `adapter_*`, `scheduler_*`, `safety_*`, `pool_*` metrics
// already registered in prometheus.go.
var (
	SessionsV2WriteSuccess = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_write_success_total",
		Help: "Total successful V2 writes",
	})
	SessionsV2WriteFailed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_write_failure_total",
		Help: "Total failed V2 writes",
	})
	SessionsV2WriteLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sessions_v2_write_latency_seconds",
		Help:    "V2 write latency",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
	})
	// SessionsV2SessionLockWait measures how long pg_advisory_xact_lock
	// (the per tenant/session key) blocked before being granted.
	//
	// ★ Why it had to be added (runbook §10.106.16): SessionsV2WriteLatency
	//   starts *after* the session lock is already held, so it excludes all
	//   queueing. Measured on 2026-10-07: that one statement
	//   (SELECT pg_advisory_xact_lock(session_turns_advisory_lock_key(...)))
	//   is 83.9 hours of the gateway's 643.6 hours of database time — 13.0% —
	//   at a mean of 99.7 ms per call, which for an advisory lock (microseconds
	//   when uncontended) is almost entirely WAIT. None of it was visible in
	//   any metric, which is why it survived 26 days of statistics.
	//   ⇒ This histogram is the missing dial, not an optimisation.
	SessionsV2SessionLockWait = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sessions_v2_session_lock_wait_seconds",
		Help:    "Blocking wait for the per tenant/session advisory lock (queueing, not work)",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 14),
	})
	// SessionsV2SessionLockAcquisitions counts lock acquisitions so the wait
	// histogram can be read as "per acquisition" rather than in the void.
	SessionsV2SessionLockAcquisitions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_session_lock_acquisitions_total",
		Help: "Total acquisitions of the per tenant/session advisory lock",
	})
	SessionsV2CacheHit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_cache_hit_total",
		Help: "L0/L1/L2 cache hits",
	})
	SessionsV2CacheMiss = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_cache_miss_total",
		Help: "L0/L1/L2 cache misses",
	})
	SessionsV2DualReadDiff = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sessions_v2_dual_read_diff_total",
		Help: "Number of turns with V1/V2 diffs",
	})
)
