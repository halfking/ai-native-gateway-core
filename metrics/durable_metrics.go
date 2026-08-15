package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Durable-task worker observability (SR-W3, docs/修订0811/18 §12/§15).
//
// The durable_* series are the worker/projection view of the survival
// observability contract: gateway_survival_* (survival_metrics.go) covers the
// per-request coordinator path, durable_* covers the PostgreSQL recovery
// worker and the PendingStore projection outbox. Producers live in
// domains/streaming/durable_recovery_worker.go (runs gauge/lease) and
// durable/pending_outbox.go (projection counters).
//
// Cardinality note (GW-00): durable_tasks_active carries a raw tenant label
// deliberately — task volume per tenant is bounded by the per-tenant active
// task cap, so the series count is bounded by tenants × 1.

var (
	// DurableTasksActive gauges non-terminal durable tasks per tenant,
	// refreshed by the recovery worker from the authoritative
	// ActiveTaskCounts query. Alert when it approaches the tenant cap.
	DurableTasksActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "durable_tasks_active",
		Help: "Active (non-terminal) durable tasks per tenant, from the authoritative PostgreSQL count.",
	}, []string{"tenant"})

	// DurableRecoveryRunsTotal counts recovery worker claim cycles by
	// outcome: claimed (one or more tasks executed), empty (nothing due),
	// error (the claim query itself failed). Sustained empty+claimed
	// imbalance or rising error is the runnable-backlog signal.
	DurableRecoveryRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "durable_recovery_runs_total",
		Help: "Recovery worker claim cycles by outcome (claimed/empty/error).",
	}, []string{"outcome"})

	// DurablePendingProjectionsTotal counts terminal task projections
	// successfully written into the PendingStore by the outbox deliverer,
	// by terminal status. doc 18 §15.3: "worker succeeded but the final
	// result write-back failed" — compare against durable_tasks terminal
	// transitions to detect projection loss.
	DurablePendingProjectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "durable_pending_projections_total",
		Help: "Terminal durable task projections delivered to the PendingStore by terminal status.",
	}, []string{"status"})

	// DurablePendingProjectionErrorsTotal counts failed outbox deliveries
	// by reason: decrypt (result ciphertext undecryptable/tampered),
	// project_cas (PendingStore write rejected/failed), mark_failed
	// (bookkeeping update lost the row lease). Retries continue with
	// backoff; sustained growth means the projection is stuck.
	DurablePendingProjectionErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "durable_pending_projection_errors_total",
		Help: "Failed PendingStore outbox deliveries by reason.",
	}, []string{"reason"})

	// DurableLeaseLostTotal counts fenced-off durable task writes — a
	// worker or foreground holder whose (lease_owner, fencing_token) no
	// longer matched at commit time (worker-path counterpart of
	// gateway_survival_lease_conflicts_total).
	DurableLeaseLostTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "durable_lease_lost_total",
		Help: "Durable task writes rejected by the fencing check (ErrLeaseLost).",
	})

	// DurableRecoveryStopGraceExceededTotal counts Stop calls that hit the
	// StopGrace timeout: the bounded wait gave up before the in-flight
	// attempt goroutine exited on its own. A rising rate means the streaming
	// upstream (which uses WithoutCancel and may ignore cancellation) is
	// keeping the worker goroutine alive past the grace, so Stop returns
	// while a background attempt is still running — visibility into a
	// potential goroutine leak / slow foreground detach.
	DurableRecoveryStopGraceExceededTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "durable_recovery_stop_grace_exceeded_total",
		Help: "Recovery worker Stop() calls that timed out waiting for the in-flight attempt to exit.",
	})
)

// durableActiveTenants remembers every tenant series ever published so a
// tenant that drained to zero (absent from the latest GROUP BY) gets its
// gauge reset instead of freezing at the last non-zero value.
var durableActiveTenants sync.Map

// SetDurableTasksActive publishes one authoritative per-tenant snapshot.
// Tenants absent from counts (no active tasks left) are reset to zero.
func SetDurableTasksActive(counts map[string]int64) {
	seen := make(map[string]struct{}, len(counts))
	for tenant, n := range counts {
		DurableTasksActive.WithLabelValues(tenant).Set(float64(n))
		seen[tenant] = struct{}{}
		durableActiveTenants.Store(tenant, struct{}{})
	}
	durableActiveTenants.Range(func(key, _ any) bool {
		tenant, _ := key.(string)
		if _, ok := seen[tenant]; !ok {
			DurableTasksActive.WithLabelValues(tenant).Set(0)
		}
		return true
	})
}
