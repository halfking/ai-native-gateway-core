package credentialfpslot

import (
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	slotAcquireTotal     *prometheus.CounterVec
	slotAcquireFailures  *prometheus.CounterVec
	slotReleaseTotal     prometheus.Counter
	slotReleaseFailures  prometheus.Counter
	slotUtilization      *prometheus.GaugeVec
	slotSaturationEvents prometheus.Counter
	slotPreemptEvents    prometheus.Counter
	slotReclaimEvents    prometheus.Counter

	registerMetricsOnce sync.Once
)

func registerMetrics() {
	registerMetricsOnce.Do(func() {
		slotAcquireTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_acquire_total",
				Help: "Total number of fingerprint slot acquire attempts",
			},
			[]string{"outcome"}, // success, saturated, redis_error
		)

		slotAcquireFailures = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_acquire_failures_total",
				Help: "Total number of fingerprint slot acquire failures by reason",
			},
			[]string{"reason"}, // saturated, redis_error, script_error
		)

		slotReleaseTotal = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_release_total",
				Help: "Total number of fingerprint slot release calls",
			},
		)

		slotReleaseFailures = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_release_failures_total",
				Help: "Total number of fingerprint slot release failures",
			},
		)

		slotUtilization = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llmgw_fpslot_utilization_ratio",
				Help: "Fingerprint slot utilization ratio (used/limit) per credential",
			},
			[]string{"credential_id"},
		)

		slotSaturationEvents = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_saturation_events_total",
				Help: "Total number of slot saturation events (all slots occupied)",
			},
		)

		slotPreemptEvents = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_preempt_events_total",
				Help: "Total number of slot preemption events (LRU eviction)",
			},
		)

		slotReclaimEvents = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_reclaim_events_total",
				Help: "Total number of background reclaim events",
			},
		)

		prometheus.MustRegister(
			slotAcquireTotal,
			slotAcquireFailures,
			slotReleaseTotal,
			slotReleaseFailures,
			slotUtilization,
			slotSaturationEvents,
			slotPreemptEvents,
			slotReclaimEvents,
		)
	})
}

func init() {
	registerMetrics()
}

// recordAcquireSuccess records a successful slot acquisition
func recordAcquireSuccess() {
	if slotAcquireTotal != nil {
		slotAcquireTotal.WithLabelValues("success").Inc()
	}
}

// recordAcquireSaturated records a slot acquisition failure due to saturation
func recordAcquireSaturated() {
	if slotAcquireTotal != nil {
		slotAcquireTotal.WithLabelValues("saturated").Inc()
	}
	if slotAcquireFailures != nil {
		slotAcquireFailures.WithLabelValues("saturated").Inc()
	}
	if slotSaturationEvents != nil {
		slotSaturationEvents.Inc()
	}
}

// recordAcquireRedisError records a slot acquisition failure due to Redis error
func recordAcquireRedisError() {
	if slotAcquireTotal != nil {
		slotAcquireTotal.WithLabelValues("redis_error").Inc()
	}
	if slotAcquireFailures != nil {
		slotAcquireFailures.WithLabelValues("redis_error").Inc()
	}
}

// recordReleaseSuccess records a successful slot release
func recordReleaseSuccess() {
	if slotReleaseTotal != nil {
		slotReleaseTotal.Inc()
	}
}

// recordReleaseFailure records a slot release failure
func recordReleaseFailure() {
	if slotReleaseFailures != nil {
		slotReleaseFailures.Inc()
	}
}

// recordPreempt records a slot preemption event
func recordPreempt() {
	if slotPreemptEvents != nil {
		slotPreemptEvents.Inc()
	}
}

// recordReclaim records a background reclaim event
func recordReclaim() {
	if slotReclaimEvents != nil {
		slotReclaimEvents.Inc()
	}
}

// updateUtilization updates the slot utilization gauge for a credential
func updateUtilization(credentialID int, used, limit int) {
	if slotUtilization != nil && limit > 0 {
		ratio := float64(used) / float64(limit)
		slotUtilization.WithLabelValues(strconv.Itoa(credentialID)).Set(ratio)
	}
}
