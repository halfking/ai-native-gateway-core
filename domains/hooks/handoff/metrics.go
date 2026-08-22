// Package handoff metrics — Prometheus instrumentation for the GoalHandoff
// contract (12-GoalHandoff契约.md §8.2).
//
// Six bare-named metric families are registered against
// prometheus.DefaultRegisterer at package init, matching the sibling
// domains/hooks/compression/metrics.go pattern:
//
//	handoff_proposals_total{trigger_kind,result}
//	handoff_confirmation_total{result}
//	handoff_restore_total{result}
//	handoff_restore_duration_seconds{result}
//	handoff_payload_bytes{kind}
//	handoff_restore_failure_total{reason}
//
// Cardinality discipline (§8.2 / §9.2): only low-cardinality enum-like labels
// (trigger kind, result code, failure reason, payload kind) are used. No
// request/session/tenant/goal_run identifiers ever appear in labels.
//
// Bare metric names (no namespace prefix) are intentional: the contract
// quotes these exact names. They match the compression package's
// "compression_*" convention rather than the "orchestration_" namespaced
// collectors in domains/orchestration/observ (which remains an unused,
// unwired façade).
package handoff

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// metrics holds the handoff Prometheus collectors. A single package-level
// instance is registered in init(); tests reset values via ResetMetrics.
type metrics struct {
	proposals        *prometheus.CounterVec // labels: trigger_kind, result
	confirmations    *prometheus.CounterVec // labels: result
	restores         *prometheus.CounterVec // labels: result
	restoreDuration  *prometheus.HistogramVec // labels: result
	payloadBytes     *prometheus.HistogramVec // labels: kind
	restoreFailures  *prometheus.CounterVec // labels: reason

	mu sync.Mutex
}

var defaultMetrics = newMetrics()

func newMetrics() *metrics {
	return &metrics{
		proposals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "handoff_proposals_total",
			Help: "Handoff proposals by trigger kind and result (triggered|suppressed|prepared|prepare_failed|save_failed).",
		}, []string{"trigger_kind", "result"}),
		confirmations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "handoff_confirmation_total",
			Help: "Explicit handoff confirmations by result (first|idempotent|expired|replay|invalid).",
		}, []string{"result"}),
		restores: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "handoff_restore_total",
			Help: "Goal-state restores after confirmation by result (restored|retryable|manual_required|no_snapshot).",
		}, []string{"result"}),
		restoreDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "handoff_restore_duration_seconds",
			Help:    "Wall-clock duration of a goal-state restore by result.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms → 16s
		}, []string{"result"}),
		payloadBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "handoff_payload_bytes",
			Help:    "Serialized handoff payload size in bytes by kind (message|snapshot).",
			Buckets: prometheus.ExponentialBuckets(64, 2, 14), // 64B → 1MB
		}, []string{"kind"}),
		restoreFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "handoff_restore_failure_total",
			Help: "Goal-state restore failures by reason (conflict|version_mismatch|tenant_mismatch|retryable|manual_required).",
		}, []string{"reason"}),
	}
}

func init() {
	// Tolerate AlreadyRegisteredError: if the handoff package is imported by
	// more than one compiled unit in the same process (or a test double
	// re-registers), registration must not panic. Genuine errors still panic.
	collectors := []prometheus.Collector{
		defaultMetrics.proposals,
		defaultMetrics.confirmations,
		defaultMetrics.restores,
		defaultMetrics.restoreDuration,
		defaultMetrics.payloadBytes,
		defaultMetrics.restoreFailures,
	}
	for _, c := range collectors {
		if err := prometheus.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if !errors.As(err, &are) {
				panic(err)
			}
		}
	}
}

// RecordProposal emits one handoff_proposals_total{trigger_kind,result} sample.
// triggerKind is a low-cardinality enum (e.g. the trigger reason code); result
// is one of triggered|suppressed|prepared|prepare_failed|save_failed.
func RecordProposal(triggerKind, result string) {
	defaultMetrics.proposals.WithLabelValues(triggerKind, result).Inc()
}

// RecordConfirmation emits one handoff_confirmation_total{result} sample.
// result is one of first|idempotent|expired|replay|invalid.
func RecordConfirmation(result string) {
	defaultMetrics.confirmations.WithLabelValues(result).Inc()
}

// RecordRestore emits one handoff_restore_total{result} sample and observes
// the restore latency on handoff_restore_duration_seconds{result}. result is
// one of restored|retryable|manual_required|no_snapshot.
func RecordRestore(result string, durationSec float64) {
	defaultMetrics.restores.WithLabelValues(result).Inc()
	defaultMetrics.restoreDuration.WithLabelValues(result).Observe(durationSec)
}

// RecordPayload observes the serialized handoff payload size on
// handoff_payload_bytes{kind}. kind is one of message|snapshot.
func RecordPayload(kind string, bytes int) {
	if bytes < 0 {
		return
	}
	defaultMetrics.payloadBytes.WithLabelValues(kind).Observe(float64(bytes))
}

// IncRestoreFailure emits one handoff_restore_failure_total{reason} sample.
// reason is one of conflict|version_mismatch|tenant_mismatch|retryable|manual_required.
func IncRestoreFailure(reason string) {
	defaultMetrics.restoreFailures.WithLabelValues(reason).Inc()
}

// ResetMetrics is a test helper that wipes all handoff metric values so
// per-test assertions run against a clean slate. Exported so cross-package
// tests (e.g. domains/streaming) can reset the shared registry.
func ResetMetrics() {
	defaultMetrics.mu.Lock()
	defer defaultMetrics.mu.Unlock()
	defaultMetrics.proposals.Reset()
	defaultMetrics.confirmations.Reset()
	defaultMetrics.restores.Reset()
	defaultMetrics.restoreDuration.Reset()
	defaultMetrics.payloadBytes.Reset()
	defaultMetrics.restoreFailures.Reset()
}

// ProposalCount is a test helper returning (exported for cross-package tests) the current value of
// handoff_proposals_total for a label pair. Exported for cross-package tests.
func ProposalCount(triggerKind, result string) float64 {
	return readCounterVec(defaultMetrics.proposals, triggerKind, result)
}

// ConfirmationCount is a test helper returning (exported for cross-package tests) the current value of
// handoff_confirmation_total for a result.
func ConfirmationCount(result string) float64 {
	return readCounterVec(defaultMetrics.confirmations, result)
}

// RestoreCount is a test helper returning (exported for cross-package tests) the current value of
// handoff_restore_total for a result.
func RestoreCount(result string) float64 {
	return readCounterVec(defaultMetrics.restores, result)
}

// RestoreFailureCount is a test helper returning (exported for cross-package tests) the current value of
// handoff_restore_failure_total for a reason.
func RestoreFailureCount(reason string) float64 {
	return readCounterVec(defaultMetrics.restoreFailures, reason)
}

// PayloadCount is a test helper returning (exported for cross-package tests) the cumulative observation count of
// handoff_payload_bytes for a kind (number of samples, not the summed bytes).
func PayloadCount(kind string) float64 {
	cv, err := defaultMetrics.payloadBytes.GetMetricWithLabelValues(kind)
	if err != nil || cv == nil {
		return 0
	}
	pb := &dto.Metric{}
	if err := cv.(prometheus.Metric).Write(pb); err != nil {
		return 0
	}
	if pb.Histogram != nil && pb.Histogram.SampleCount != nil {
		return float64(*pb.Histogram.SampleCount)
	}
	return 0
}

// readCounterVec returns the current counter value for the given label values.
func readCounterVec(cv *prometheus.CounterVec, labels ...string) float64 {
	m, err := cv.GetMetricWithLabelValues(labels...)
	if err != nil || m == nil {
		return 0
	}
	pb := &dto.Metric{}
	if err := m.(prometheus.Metric).Write(pb); err != nil {
		return 0
	}
	if pb.Counter != nil && pb.Counter.Value != nil {
		return *pb.Counter.Value
	}
	return 0
}
