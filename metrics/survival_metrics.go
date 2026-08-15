package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Request-survival observability skeleton (docs/修订0811/18 §15.1,
// docs/修订0811/19 CO-1, 2026-08-15 M0).
//
// Phase 0/Wave 0 ships the metric declarations BEFORE the SurvivalCoordinator
// exists so that Wave 1/2 producers have a stable contract to increment and
// dashboards/alerts can be authored against the series names ahead of the
// canary rollout. Until a producer is wired, all series stay at zero — safe
// to export unconditionally.
//
// Label cardinality follows the GW-00 rule (see omnifree_metrics.go): tenant
// labels are NOT raw tenant IDs on gauges that could fan out; the durable
// active-task gauge uses the tenant dimension only because task volume is
// bounded by max_active_tasks_per_tenant (default 100) and per-tenant series
// are the operational unit for capacity alerts.

var (
	// SurvivalRequestsTotal counts every request that entered the survival
	// coordinator, by client protocol, durability and terminal outcome.
	// outcome is the closed enum: completed / permanent_failed / expired /
	// cancelled / resume_safety_blocked / error.
	SurvivalRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_survival_requests_total",
		Help: "Total requests handled by the survival coordinator by protocol, durability and outcome.",
	}, []string{"protocol", "durable", "outcome"})

	// SurvivalStateTransitionsTotal counts state-machine transitions
	// (from → to) with the triggering reason code.
	SurvivalStateTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_survival_state_transitions_total",
		Help: "Survival task state transitions by from-state, to-state and reason.",
	}, []string{"from", "to", "reason"})

	// SurvivalWaitSeconds records how long a request waited in
	// waiting_recovery before its next attempt or terminal state, by the
	// dominant recovery reason (rate_limit / quota_periodic / concurrent /
	// upstream_overloaded / transient / network / no_candidates / other).
	SurvivalWaitSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_survival_wait_seconds",
		Help:    "Wait duration in waiting_recovery by recovery reason.",
		Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 14400, 86400},
	}, []string{"reason"})

	// SurvivalAttemptsTotal counts provider attempts executed under the
	// survival coordinator by upstream error kind ("" on success) and
	// provider code.
	SurvivalAttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_survival_attempts_total",
		Help: "Provider attempts under survival by error kind and provider.",
	}, []string{"kind", "provider"})

	// SurvivalRecoveryLatencySeconds measures outage-to-success: from the
	// first recoverable failure to eventual completion of the same request.
	SurvivalRecoveryLatencySeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_survival_recovery_latency_seconds",
		Help:    "First recoverable failure to request completion, by protocol.",
		Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 14400, 86400},
	}, []string{"protocol"})

	// SurvivalActiveTasks gauges runnable/active survival tasks per tenant
	// (durable worker scope, doc 18 §12; the series name follows §15.1
	// verbatim). Alert when this approaches max_active_tasks_per_tenant.
	SurvivalActiveTasks = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_survival_active_tasks",
		Help: "Active survival tasks per tenant (bounded by per-tenant cap).",
	}, []string{"tenant"})

	// SurvivalLeaseConflictsTotal counts fenced-off submissions: a worker or
	// foreground holder whose (lease_owner, fencing_token) no longer matched
	// at commit time. Non-zero sustained = lease churn or a fencing bug.
	SurvivalLeaseConflictsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_survival_lease_conflicts_total",
		Help: "Durable task updates rejected by the fencing check.",
	})

	// SurvivalResumeSafetyBlockedTotal counts requests stopped because the
	// stream had already committed semantic content (text mid-stream, tool
	// call, structured output) and a safe resume was impossible.
	SurvivalResumeSafetyBlockedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_survival_resume_safety_blocked_total",
		Help: "Requests terminated with resume_safety_blocked by response type.",
	}, []string{"response_type"})

	// SurvivalKeepaliveWriteErrorsTotal counts failed keepalive/status
	// writes to the client connection by protocol — the early signal that a
	// client disconnected while the gateway was still holding the request.
	SurvivalKeepaliveWriteErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_survival_keepalive_write_errors_total",
		Help: "Failed keepalive/status SSE writes by protocol.",
	}, []string{"protocol"})

	// SanitizePlaceholderTamperingTotal counts placeholders observed in LLM
	// responses that the gateway never issued (unknown index/type) — the
	// signal for placeholder forgery attempts or sanitizer drift (SC-2,
	// docs/修订0811/19; was a TODO in smart_sani_guard since Phase 1 Task 1.1).
	// source: llm_generated (model fabricated/echoed an unknown placeholder).
	SanitizePlaceholderTamperingTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sanitize_placeholder_tampering_total",
		Help: "Invalid (never-issued) placeholders found in LLM responses.",
	}, []string{"source"})
)

var (
	// SurvivalAttemptGateMetadataOverflowTotal counts attempts whose buffered
	// attempt metadata exceeded the commit-gate cap (doc 18 §5.1): the attempt
	// is blocked/skipped rather than force-committed. Sustained non-zero means
	// a vendor started emitting unbounded pre-content metadata.
	SurvivalAttemptGateMetadataOverflowTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_survival_attempt_gate_metadata_overflow_total",
		Help: "Attempt commit-gate metadata buffer overflows by client protocol.",
	}, []string{"protocol"})
)
