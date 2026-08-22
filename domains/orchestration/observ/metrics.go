// Unified Auto-Orchestration Plugin — Workflow C (Observability & Audit Log)
//
// metrics.go implements the orchestrator metrics primitive from design §9.2:
//
//	"Recommended metrics: active runs, terminal reason, retry, budget stop,
//	 lease conflict, resume safety block, action duplicate, handoff restore
//	 failure, audit degraded, fallback rejection, pending projection error."
//
// Cardinality discipline (§9.2):
//   - All IDs (goal_run / session / request / task / trace / correlation /
//     causation) MUST stay out of labels. Only low-cardinality enum-like
//     labels (reason codes, failure kinds) are allowed.
//   - The Prometheus implementation is wired against a caller-provided
//     prometheus.Registerer so tests can use prometheus.NewRegistry() and
//     never touch the global default registry.
package observ

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the orchestration-side façade for emitting the §9.2 metric
// surface. Production wires PrometheusMetrics against the gateway's main
// registry; tests use NoopMetrics or PrometheusMetrics with a private
// registry.
type Metrics interface {
	SetActiveRuns(n float64)
	IncTerminalReason(reason string)
	IncRetry()
	IncBudgetStop()
	IncLeaseConflict()
	IncResumeSafetyBlock()
	IncActionDuplicate()
	IncAuditDegraded()
	IncFallbackRejection(reason string)
	IncPendingProjectionError(reason string)
}

// NoopMetrics is the zero-cost implementation used in tests and when metrics
// emission is explicitly disabled. Every method is a no-op.
type NoopMetrics struct{}

// NewNoopMetrics returns a Metrics that drops every call.
func NewNoopMetrics() *NoopMetrics { return &NoopMetrics{} }

func (*NoopMetrics) SetActiveRuns(_ float64)            {}
func (*NoopMetrics) IncTerminalReason(_ string)         {}
func (*NoopMetrics) IncRetry()                          {}
func (*NoopMetrics) IncBudgetStop()                     {}
func (*NoopMetrics) IncLeaseConflict()                  {}
func (*NoopMetrics) IncResumeSafetyBlock()              {}
func (*NoopMetrics) IncActionDuplicate()                {}
func (*NoopMetrics) IncAuditDegraded()                  {}
func (*NoopMetrics) IncFallbackRejection(_ string)      {}
func (*NoopMetrics) IncPendingProjectionError(_ string) {}

// PrometheusMetrics is the production Prometheus-backed implementation. All
// counters/gauges are created against a caller-provided prometheus.Registerer
// (typically the gateway's main registry) so the test suite never collides
// with the global default registry.
//
// Field names are lowercase and exported-package-private: they are read by
// tests in the same package via testutil.ToFloat64, and must not be touched
// from outside this package.
type PrometheusMetrics struct {
	activeRuns             prometheus.Gauge
	terminalReason         *prometheus.CounterVec // labels: reason
	retry                  prometheus.Counter
	budgetStop             prometheus.Counter
	leaseConflict          prometheus.Counter
	resumeSafetyBlock      prometheus.Counter
	actionDuplicate        prometheus.Counter
	auditDegraded          prometheus.Counter
	fallbackRejection      *prometheus.CounterVec // labels: reason
	pendingProjectionError *prometheus.CounterVec // labels: reason
}

// NewPrometheusMetrics creates and registers the §9.2 orchestrator metrics
// against reg. All collectors share the "orchestration_" namespace prefix so
// they do not collide with the gateway's existing metric families (which
// live under names like "durable_", "gateway_", "ursm_").
func NewPrometheusMetrics(reg prometheus.Registerer) *PrometheusMetrics {
	m := &PrometheusMetrics{
		activeRuns: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "orchestration",
			Name:      "active_runs",
			Help:      "Active (non-terminal) GoalRun instances across all tenants.",
		}),
		terminalReason: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "terminal_reason_total",
			Help:      "GoalRun terminal transitions by reason code (manual_required, budget_exhausted, …).",
		}, []string{"reason"}),
		retry: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "retry_total",
			Help:      "Total orchestrator retries (lease re-claim, recovery, etc.).",
		}),
		budgetStop: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "budget_stop_total",
			Help:      "Times the orchestrator stopped due to budget exhaustion.",
		}),
		leaseConflict: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "lease_conflict_total",
			Help:      "CAS / lease conflicts detected during action claim.",
		}),
		resumeSafetyBlock: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "resume_safety_block_total",
			Help:      "Times resume was blocked after a content/tool/side-effect checkpoint.",
		}),
		actionDuplicate: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "action_duplicate_total",
			Help:      "Duplicate action detections (idempotency key reused or sequence out-of-order).",
		}),
		auditDegraded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "audit_degraded_total",
			Help:      "Times an audit write failed and the run was marked audit_degraded.",
		}),
		fallbackRejection: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "fallback_rejection_total",
			Help:      "Times a model / credential fallback was rejected by policy.",
		}, []string{"reason"}),
		pendingProjectionError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orchestration",
			Name:      "pending_projection_error_total",
			Help:      "PendingStore projection errors by reason (decrypt, project_cas, mark_failed).",
		}, []string{"reason"}),
	}
	reg.MustRegister(
		m.activeRuns,
		m.terminalReason,
		m.retry,
		m.budgetStop,
		m.leaseConflict,
		m.resumeSafetyBlock,
		m.actionDuplicate,
		m.auditDegraded,
		m.fallbackRejection,
		m.pendingProjectionError,
	)
	return m
}

// SetActiveRuns records the current non-terminal GoalRun count. Callers
// refresh this gauge after each durable claim / completion cycle.
func (m *PrometheusMetrics) SetActiveRuns(n float64) {
	m.activeRuns.Set(n)
}

// IncTerminalReason increments the terminal_reason counter for the given
// reason code. Reason codes are a low-cardinality enum (manual_required,
// budget_exhausted, terminal, …) — never pass an ID.
func (m *PrometheusMetrics) IncTerminalReason(reason string) {
	m.terminalReason.WithLabelValues(reason).Inc()
}

// IncRetry increments the retry counter.
func (m *PrometheusMetrics) IncRetry() {
	m.retry.Inc()
}

// IncBudgetStop increments the budget_stop counter.
func (m *PrometheusMetrics) IncBudgetStop() {
	m.budgetStop.Inc()
}

// IncLeaseConflict increments the lease_conflict counter.
func (m *PrometheusMetrics) IncLeaseConflict() {
	m.leaseConflict.Inc()
}

// IncResumeSafetyBlock increments the resume_safety_block counter.
func (m *PrometheusMetrics) IncResumeSafetyBlock() {
	m.resumeSafetyBlock.Inc()
}

// IncActionDuplicate increments the action_duplicate counter.
func (m *PrometheusMetrics) IncActionDuplicate() {
	m.actionDuplicate.Inc()
}

// IncAuditDegraded increments the audit_degraded counter — called when an
// audit sink returns an error and the run is downgraded.
func (m *PrometheusMetrics) IncAuditDegraded() {
	m.auditDegraded.Inc()
}

// IncFallbackRejection increments the fallback_rejection counter for the
// given reason (model_unavailable, cost_cap, …).
func (m *PrometheusMetrics) IncFallbackRejection(reason string) {
	m.fallbackRejection.WithLabelValues(reason).Inc()
}

// IncPendingProjectionError increments the pending_projection_error counter
// for the given reason (decrypt, project_cas, mark_failed).
func (m *PrometheusMetrics) IncPendingProjectionError(reason string) {
	m.pendingProjectionError.WithLabelValues(reason).Inc()
}
