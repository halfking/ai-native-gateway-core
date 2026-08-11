package outbox

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for outbox pattern observability.
// Tracks event delivery lifecycle: sent, failed, retried, and DLQ accumulation.
var (
	// outboxEventsSentTotal counts successful event deliveries to ASM.
	// Labels: status=delivered
	outboxEventsSentTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_events_sent_total",
			Help: "Total number of outbox events successfully delivered to ASM",
		},
		[]string{"status"},
	)

	// outboxEventsFailedTotal counts event delivery failures.
	// Labels: reason=http_error|network|timeout|validation
	outboxEventsFailedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_events_failed_total",
			Help: "Total number of outbox event delivery failures",
		},
		[]string{"reason"},
	)

	// outboxEventsRetriedTotal counts retry attempts (not final failure).
	// A single event can increment this multiple times before success or DLQ.
	outboxEventsRetriedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "outbox_events_retried_total",
			Help: "Total number of outbox event retry attempts",
		},
	)

	// outboxDLQCount tracks current number of events in dead letter queue (failed >= max_attempts).
	// This is a gauge that can go up (new DLQ) or down (manual replay).
	outboxDLQCount = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "outbox_dlq_count",
			Help: "Current number of events in outbox dead letter queue (status=failed, attempt_count >= max_attempts)",
		},
	)

	// outboxDeliveryDurationSeconds measures event delivery latency (HTTP roundtrip).
	// Labels: status=success|failure
	outboxDeliveryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "outbox_delivery_duration_seconds",
			Help:    "Histogram of outbox event delivery duration in seconds",
			Buckets: prometheus.DefBuckets, // [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
		},
		[]string{"status"},
	)

	// outboxPendingCount tracks current number of pending events (status=pending).
	// High value indicates backlog or Dispatcher downtime.
	outboxPendingCount = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "outbox_pending_count",
			Help: "Current number of pending outbox events awaiting delivery",
		},
	)
)

// RecordEventSent increments the sent counter after successful ASM delivery.
func RecordEventSent() {
	outboxEventsSentTotal.WithLabelValues("delivered").Inc()
}

// RecordEventFailed increments the failed counter with a reason label.
// Reasons: "http_error", "network", "timeout", "validation", "hmac_mismatch", "tenant_mismatch"
func RecordEventFailed(reason string) {
	outboxEventsFailedTotal.WithLabelValues(reason).Inc()
}

// RecordEventRetried increments the retry counter.
func RecordEventRetried() {
	outboxEventsRetriedTotal.Inc()
}

// SetDLQCount updates the DLQ gauge to the current count of failed events.
// Should be called periodically (e.g., every poll cycle) to track DLQ accumulation.
func SetDLQCount(count int) {
	outboxDLQCount.Set(float64(count))
}

// SetPendingCount updates the pending gauge to the current count of pending events.
// Should be called periodically to track backlog.
func SetPendingCount(count int) {
	outboxPendingCount.Set(float64(count))
}

// ObserveDeliveryDuration records the HTTP roundtrip time for an event delivery.
// status: "success" or "failure"
func ObserveDeliveryDuration(durationSeconds float64, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}
	outboxDeliveryDurationSeconds.WithLabelValues(status).Observe(durationSeconds)
}
